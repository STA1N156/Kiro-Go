package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func refreshExternalIdpToken(refreshToken, clientID, tokenEndpoint, scopes string, client *http.Client) (string, string, int64, string, error) {
	if refreshToken == "" {
		return "", "", 0, "", fmt.Errorf("external IdP refresh requires refreshToken")
	}
	if clientID == "" {
		return "", "", 0, "", fmt.Errorf("external IdP refresh requires clientId")
	}
	if tokenEndpoint == "" {
		return "", "", 0, "", fmt.Errorf("external IdP refresh requires tokenEndpoint")
	}
	if err := validateExternalIdpEndpoint(tokenEndpoint); err != nil {
		return "", "", 0, "", err
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if scopes != "" {
		form.Set("scope", scopes)
	}

	req, _ := http.NewRequest("POST", tokenEndpoint, bytes.NewBufferString(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, "", err
	}
	if result.AccessToken == "" {
		return "", "", 0, "", fmt.Errorf("refresh response missing access_token")
	}

	expiresAt := time.Now().Unix() + int64(result.ExpiresIn)
	return result.AccessToken, result.RefreshToken, expiresAt, "", nil
}
