package proxy

import (
	"fmt"
	kiroconfig "kiro-go/config"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

func buildKiroUsageURL(account *kiroconfig.Account) string {
	region := kiroRegion(account)
	responses := account != nil && strings.EqualFold(strings.TrimSpace(account.KiroEndpoint), "responses")
	path := "/Get-Usage-Limits"
	if responses {
		path = "/getUsageLimits"
	}
	values := url.Values{}
	values.Add("origin", "AI_EDITOR")
	if account != nil && strings.TrimSpace(account.ProfileArn) != "" {
		values.Add("profileArn", strings.TrimSpace(account.ProfileArn))
	}
	if responses {
		values.Add("resourceType", "AGENTIC_REQUEST")
	}
	return fmt.Sprintf("https://management.%s.kiro.dev%s?%s", region, path, values.Encode())
}

func applyKiroUsageHeaders(req *http.Request, account *kiroconfig.Account) {
	responses := account != nil && strings.EqualFold(strings.TrimSpace(account.KiroEndpoint), "responses")
	service := "kirocontrolplanebearer"
	maxAttempts := "1"
	if responses {
		service = "codewhispererruntime"
		maxAttempts = "3"
	}
	applyKiroManagementHeaders(req, account, service, maxAttempts)
}

func applyKiroManagementHeaders(req *http.Request, account *kiroconfig.Account, service, maxAttempts string) {
	clientCfg := kiroconfig.GetKiroClientConfig()
	version := kiroconfig.KiroVersionForAccount(account)
	node := kiroconfig.KiroNodeVersionForAccount(account)
	machine := kiroconfig.EffectiveMachineId(account)
	identity := "KiroIDE-" + version
	if machine != "" {
		identity += "-" + machine
	}
	req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/1.0.0 "+identity)
	req.Header.Set(
		"User-Agent",
		fmt.Sprintf(
			"aws-sdk-js/1.0.0 ua/2.1 os/%s lang/js md/nodejs#%s api/%s#1.0.0 m/N,E %s",
			clientCfg.SystemVersion,
			node,
			service,
			identity,
		),
	)
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.NewString())
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max="+maxAttempts)
	req.Header.Set("Connection", "close")
	if req.URL != nil {
		req.Host = req.URL.Host
	}
	if token := accountBearerToken(account); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if account != nil {
		if kiroconfig.IsAPIKeyAccount(account) {
			req.Header.Set("TokenType", "API_KEY")
		} else if strings.EqualFold(strings.TrimSpace(account.AuthMethod), "external_idp") {
			req.Header.Set("TokenType", "EXTERNAL_IDP")
		}
		if strings.EqualFold(strings.TrimSpace(account.Provider), "internal") {
			req.Header.Set("redirect-for-internal", "true")
		}
	}
}
