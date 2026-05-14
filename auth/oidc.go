package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kiro-go/config"
	"net/http"
	"strings"
	"time"
)

// ErrInvalidGrant 表示 refreshToken 已永久失效（invalid_grant）。
//
// 调用方应立即禁用对应凭据，不再累计重试。
var ErrInvalidGrant = errors.New("refresh token invalid_grant")

// RefreshResult 包含一次 token 刷新的全部产物。
type RefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	ProfileArn   string // 仅当响应携带时填写；不应清空已有值
}

// RefreshToken 刷新 access token，并返回包含 profileArn 的完整结果。
//
// 对于 authMethod == "api_key" 的 Kiro CLI headless 凭据，直接将已存的 API Key
// 当作 access token 返回，跳过刷新流程。
func RefreshToken(account *config.Account) (RefreshResult, error) {
	if account.AuthMethod == "api_key" {
		return RefreshResult{
			AccessToken: account.AccessToken,
			ExpiresAt:   0, // 永不到期
		}, nil
	}
	if account.AuthMethod == "social" {
		return refreshSocialToken(account.RefreshToken)
	}
	return refreshOIDCToken(account.RefreshToken, account.ClientID, account.ClientSecret, account.Region)
}

// classifyRefreshError 识别 OIDC/Social 刷新返回是否为 invalid_grant 类错误。
func classifyRefreshError(status int, body []byte) error {
	preview := string(body)
	if len(preview) > 500 {
		preview = preview[:500]
	}
	base := fmt.Errorf("refresh failed: %d %s", status, preview)

	if status != 400 && status != 401 {
		return base
	}

	var parsed struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Type             string `json:"__type"`
		Message          string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return base
	}

	combined := strings.ToLower(parsed.Error + " " + parsed.Type)
	if strings.Contains(combined, "invalid_grant") || strings.Contains(combined, "invalidgrant") {
		return fmt.Errorf("%w: %s", ErrInvalidGrant, preview)
	}
	return base
}

// refreshOIDCToken IdC/Builder ID token 刷新
func refreshOIDCToken(refreshToken, clientID, clientSecret, region string) (RefreshResult, error) {
	if clientID == "" || clientSecret == "" {
		return RefreshResult{}, fmt.Errorf("OIDC refresh requires clientId and clientSecret")
	}
	if region == "" {
		region = "us-east-1"
	}

	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient().Do(req)
	if err != nil {
		return RefreshResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return RefreshResult{}, classifyRefreshError(resp.StatusCode, respBody)
	}

	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return RefreshResult{}, err
	}

	return RefreshResult{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().Unix() + int64(result.ExpiresIn),
		ProfileArn:   result.ProfileArn,
	}, nil
}

// refreshSocialToken Social (GitHub/Google) token 刷新
func refreshSocialToken(refreshToken string) (RefreshResult, error) {
	url := "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"

	payload := map[string]string{
		"refreshToken": refreshToken,
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient().Do(req)
	if err != nil {
		return RefreshResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return RefreshResult{}, classifyRefreshError(resp.StatusCode, respBody)
	}

	var result struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int    `json:"expiresIn"`
		ProfileArn   string `json:"profileArn"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return RefreshResult{}, err
	}

	return RefreshResult{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().Unix() + int64(result.ExpiresIn),
		ProfileArn:   result.ProfileArn,
	}, nil
}
