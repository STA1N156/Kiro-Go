package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// oidcTokenURL 构造 idc/builderId 刷新 endpoint。测试可替换以拦截网络调用。
var oidcTokenURL = func(region string) string {
	return fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)
}

// socialTokenURL 构造 social 刷新 endpoint。测试可替换以拦截网络调用。
var socialTokenURL = func(region string) string {
	if strings.TrimSpace(region) == "" {
		region = "us-east-1"
	}
	return fmt.Sprintf("https://prod.%s.auth.desktop.kiro.dev/refreshToken", region)
}

// RefreshToken 刷新 access token
// Returns: accessToken, refreshToken, expiresAt, profileArn, error
func RefreshToken(account *config.Account) (string, string, int64, string, error) {
	return RefreshTokenContext(context.Background(), account)
}

func RefreshTokenContext(ctx context.Context, account *config.Account) (string, string, int64, string, error) {
	if config.IsAPIKeyAccount(account) {
		return "", "", 0, "", fmt.Errorf("API Key credentials do not support token refresh")
	}
	// Resolve per-account proxy: account.ProxyURL > global config
	proxyURL := account.ProxyURL
	if proxyURL == "" {
		proxyURL = config.GetProxyURL()
	}
	client := GetAuthClientForProxy(proxyURL)

	if strings.EqualFold(strings.TrimSpace(account.AuthMethod), MicrosoftSSOAuthMethod) {
		return refreshExternalIdpTokenContext(ctx,
			account.RefreshToken,
			account.ClientID,
			account.TokenEndpoint,
			account.IssuerURL,
			account.Scopes,
			client,
		)
	}
	if account.AuthMethod == "social" {
		return refreshSocialToken(ctx, account, client)
	}
	return refreshOIDCToken(ctx, account, client)
}

// refreshOIDCToken IdC/Builder ID token 刷新
func refreshOIDCToken(ctx context.Context, account *config.Account, client *http.Client) (string, string, int64, string, error) {
	if account.ClientID == "" || account.ClientSecret == "" {
		return "", "", 0, "", fmt.Errorf("OIDC refresh requires clientId and clientSecret")
	}
	region := account.Region
	if region == "" {
		region = "us-east-1"
	}

	url := oidcTokenURL(region)

	payload := struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		RefreshToken string `json:"refreshToken"`
		GrantType    string `json:"grantType"`
	}{
		ClientID:     account.ClientID,
		ClientSecret: account.ClientSecret,
		RefreshToken: account.RefreshToken,
		GrantType:    "refresh_token",
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	applyKiroIDCRefreshHeaders(req, region, account)

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", "", 0, "", fmt.Errorf("refresh failed: %d %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, "", err
	}

	expiresAt := time.Now().Unix() + int64(result.ExpiresIn)
	return result.AccessToken, result.RefreshToken, expiresAt, result.ProfileArn, nil
}

// refreshSocialToken Social (GitHub/Google) token 刷新
func refreshSocialToken(ctx context.Context, account *config.Account, client *http.Client) (string, string, int64, string, error) {
	url := socialTokenURL(account.Region)

	payload := struct {
		RefreshToken string `json:"refreshToken"`
	}{RefreshToken: account.RefreshToken}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	applyKiroSocialRefreshHeaders(req, config.EffectiveMachineId(account), account)

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", "", 0, "", fmt.Errorf("refresh failed: %d %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, "", err
	}

	expiresAt := time.Now().Unix() + int64(result.ExpiresIn)
	return result.AccessToken, result.RefreshToken, expiresAt, result.ProfileArn, nil
}

func applyKiroIDCRefreshHeaders(req *http.Request, region string, account *config.Account) {
	clientCfg := config.GetKiroClientConfig()
	const identitySDK = "3.980.0"
	version := config.KiroVersionForAccount(account)
	req.Header.Set("Content-Type", "application/json")
	if region != "" {
		req.Host = fmt.Sprintf("oidc.%s.amazonaws.com", region)
	}
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.NewString())
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=4")
	req.Header.Set("Connection", "close")
	req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/"+identitySDK+" KiroIDE-"+version)
	req.Header.Set("Accept", "*/*")
	req.Header.Set(
		"User-Agent",
		fmt.Sprintf(
			"aws-sdk-js/%s ua/2.1 os/%s lang/js md/nodejs#%s m/E",
			identitySDK,
			clientCfg.SystemVersion,
			config.KiroNodeVersionForAccount(account),
		),
	)
}

func applyKiroSocialRefreshHeaders(req *http.Request, machineID string, account *config.Account) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "KiroIDE-"+config.KiroVersionForAccount(account)+"-"+machineID)
	req.Header.Set("Connection", "close")
	if req.URL != nil {
		req.Host = req.URL.Host
	}
}
