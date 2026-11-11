package proxy

import (
	"fmt"
	"kiro-go/config"
	"net/http"
	"strings"
)

const (
	kiroCodeWhispererSDKVersion = "1.0.34"
	kiroRuntimeSDKVersion       = "1.0.0"
)

type kiroHeaderValues struct {
	UserAgent    string
	AmzUserAgent string
	Host         string
}

func buildGenerateHeaderValues(account *config.Account, host string) kiroHeaderValues {
	clientCfg := config.GetKiroClientConfig()
	version := config.KiroVersionForAccount(account)
	return buildKiroHeaderValues(
		account,
		host,
		"codewhispererstreaming",
		clientCfg.StreamingSDK,
		"",
		"KiroIDE",
		version,
	)
}

func buildCodeWhispererHeaderValues(account *config.Account, host string) kiroHeaderValues {
	return buildKiroHeaderValues(
		account,
		host,
		"codewhispererstreaming",
		kiroCodeWhispererSDKVersion,
		"m/E",
		"KiroIDE",
		config.KiroVersionForAccount(account),
	)
}

func buildKiroHeaderValues(account *config.Account, host, apiName, sdkVersion, mode, clientName, version string) kiroHeaderValues {
	clientCfg := config.GetKiroClientConfig()
	machineID := ""
	if account != nil {
		machineID = config.EffectiveMachineId(account)
	}

	userAgent := fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s lang/js md/nodejs#%s api/%s#%s",
		sdkVersion,
		clientCfg.SystemVersion,
		config.KiroNodeVersionForAccount(account),
		apiName,
		sdkVersion,
	)
	if mode != "" {
		userAgent += " " + mode
	}
	amzIdentity := clientName
	if strings.HasPrefix(clientName, "KiroIDE") {
		identity := clientName + "-" + version
		if machineID != "" {
			identity += "-" + machineID
		}
		userAgent += " " + identity
		amzIdentity = identity
	} else {
		userAgent += " " + clientName
	}
	amzUserAgent := fmt.Sprintf("aws-sdk-js/%s %s", sdkVersion, amzIdentity)

	return kiroHeaderValues{
		UserAgent:    userAgent,
		AmzUserAgent: amzUserAgent,
		Host:         host,
	}
}

func applyKiroBaseHeaders(req *http.Request, account *config.Account, values kiroHeaderValues) {
	token := accountBearerToken(account)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Kiro requires external identity-provider access tokens and API keys to be
	// identified explicitly. Keep this in the shared header path so streaming
	// and REST requests cannot drift apart.
	req.Header.Del("TokenType")
	req.Header.Del("tokentype")
	if account != nil {
		if config.IsAPIKeyAccount(account) {
			req.Header.Set("TokenType", "API_KEY")
		} else if strings.EqualFold(strings.TrimSpace(account.AuthMethod), "external_idp") {
			req.Header.Set("TokenType", "EXTERNAL_IDP")
		}
		if strings.EqualFold(strings.TrimSpace(account.Provider), "internal") {
			req.Header.Set("redirect-for-internal", "true")
		}
	}
	req.Header.Set("User-Agent", values.UserAgent)
	req.Header.Set("x-amz-user-agent", values.AmzUserAgent)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	if values.Host != "" {
		req.Host = values.Host
	}
}

func applyKiroResponsesHeaders(req *http.Request, account *config.Account, profileArn string) {
	clientCfg := config.GetKiroClientConfig()
	token := accountBearerToken(account)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("x-amzn-kiro-origin", "AI_EDITOR")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	if strings.TrimSpace(profileArn) != "" {
		req.Header.Set("x-amzn-kiro-profile", strings.TrimSpace(profileArn))
	}
	if account != nil {
		if config.IsAPIKeyAccount(account) {
			req.Header.Set("TokenType", "API_KEY")
		} else if strings.EqualFold(strings.TrimSpace(account.AuthMethod), "external_idp") {
			req.Header.Set("TokenType", "EXTERNAL_IDP")
		}
		if strings.EqualFold(strings.TrimSpace(account.Provider), "internal") {
			req.Header.Set("redirect-for-internal", "true")
		}
	}
	machineID := config.EffectiveMachineId(account)
	req.Header.Set(
		"User-Agent",
		fmt.Sprintf("KiroIDE %s %s KAS/0.66.89 acp-client/kiro-ide", clientCfg.ResponsesVersion, machineID),
	)
	req.Header.Set("x-amzn-kiro-attempt", "1;max=3")
	req.Header.Set("x-amzn-kiro-fallback", "0;max=0")
	req.Header.Set("Connection", "close")
	if req.URL != nil {
		req.Host = req.URL.Host
	}
}

// accountBearerToken returns the token used for Authorization: Bearer.
// API Key accounts prefer KiroApiKey; OAuth accounts use AccessToken.
func accountBearerToken(account *config.Account) string {
	if account == nil {
		return ""
	}
	if config.IsAPIKeyAccount(account) {
		if key := strings.TrimSpace(account.KiroApiKey); key != "" {
			return key
		}
	}
	return strings.TrimSpace(account.AccessToken)
}
