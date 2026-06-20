package proxy

import (
	"encoding/json"
	"io"
	"kiro-go/auth"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestApiExternalIdpStartReturnsSigninURL(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	h := &Handler{pool: accountpool.GetPool()}
	req := httptest.NewRequest(http.MethodPost, "/auth/external-idp/start", strings.NewReader(`{"region":"us-east-1"}`))
	rec := httptest.NewRecorder()

	h.apiStartExternalIdp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success   bool   `json:"success"`
		SessionID string `json:"sessionId"`
		SigninURL string `json:"signinUrl"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || resp.SessionID == "" {
		t.Fatalf("expected success with session ID, got %+v", resp)
	}
	if !strings.HasPrefix(resp.SigninURL, "https://app.kiro.dev/signin?") {
		t.Fatalf("expected Kiro sign-in URL, got %q", resp.SigninURL)
	}
	if resp.ExpiresIn <= 0 {
		t.Fatalf("expected positive expiresIn, got %d", resp.ExpiresIn)
	}
}

func TestApiExternalIdpCompleteDescriptorReturnsAuthorizationURL(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	defer installExternalIdpAuthClient(t)()

	h := &Handler{pool: accountpool.GetPool()}
	sessionID := startExternalIdpSessionForHandlerTest(t, h)
	callbackURL := "http://localhost:3128/signin/callback?" + url.Values{
		"login_option": {"external_idp"},
		"issuer_url":   {"https://login.microsoftonline.com/example/v2.0"},
		"client_id":    {"client-123"},
		"scopes":       {"scope.one offline_access"},
	}.Encode()

	req := httptest.NewRequest(http.MethodPost, "/auth/external-idp/complete", strings.NewReader(`{"sessionId":`+quoteJSON(sessionID)+`,"callbackUrl":`+quoteJSON(callbackURL)+`}`))
	rec := httptest.NewRecorder()
	h.apiCompleteExternalIdp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Success      bool   `json:"success"`
		Status       string `json:"status"`
		AuthorizeURL string `json:"authorizeUrl"`
		SessionID    string `json:"sessionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || resp.Status != auth.ExternalIdpStatusAuthorizationRequired {
		t.Fatalf("expected authorization_required, got %+v", resp)
	}
	if resp.SessionID != sessionID {
		t.Fatalf("expected session ID echoed, got %q", resp.SessionID)
	}
	authorizeURL, err := url.Parse(resp.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if authorizeURL.Host != "login.microsoftonline.com" {
		t.Fatalf("expected Microsoft authorize host, got %q", authorizeURL.Host)
	}
	if authorizeURL.Query().Get("state") == "" {
		t.Fatalf("expected generated state in authorize URL")
	}
}

func TestApiExternalIdpCompleteCodePersistsAccount(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	defer installExternalIdpAuthClient(t)()

	h := &Handler{pool: accountpool.GetPool()}
	sessionID := startExternalIdpSessionForHandlerTest(t, h)
	descriptorURL := "http://localhost:3128/signin/callback?" + url.Values{
		"login_option": {"external_idp"},
		"issuer_url":   {"https://login.microsoftonline.com/example/v2.0"},
		"client_id":    {"client-123"},
		"scopes":       {"scope.one offline_access"},
	}.Encode()
	firstReq := httptest.NewRequest(http.MethodPost, "/auth/external-idp/complete", strings.NewReader(`{"sessionId":`+quoteJSON(sessionID)+`,"callbackUrl":`+quoteJSON(descriptorURL)+`}`))
	firstRec := httptest.NewRecorder()
	h.apiCompleteExternalIdp(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("descriptor complete failed: %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	var first struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(firstRec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode descriptor response: %v", err)
	}
	authorizeURL, _ := url.Parse(first.AuthorizeURL)
	codeURL := "http://localhost:3128/oauth/callback?" + url.Values{
		"code":  {"enterprise-code"},
		"state": {authorizeURL.Query().Get("state")},
	}.Encode()

	secondReq := httptest.NewRequest(http.MethodPost, "/auth/external-idp/complete", strings.NewReader(`{"sessionId":`+quoteJSON(sessionID)+`,"callbackUrl":`+quoteJSON(codeURL)+`}`))
	secondRec := httptest.NewRecorder()
	h.apiCompleteExternalIdp(secondRec, secondReq)

	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	var second struct {
		Success bool   `json:"success"`
		Status  string `json:"status"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	if err := json.Unmarshal(secondRec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode completed response: %v", err)
	}
	if !second.Success || second.Status != auth.ExternalIdpStatusCompleted || second.Account.ID == "" {
		t.Fatalf("expected completed account response, got %+v", second)
	}

	accounts := config.GetAccounts()
	if len(accounts) != 1 {
		t.Fatalf("expected one persisted account, got %d", len(accounts))
	}
	got := accounts[0]
	if got.AuthMethod != "external_idp" {
		t.Fatalf("expected external_idp auth method, got %q", got.AuthMethod)
	}
	if got.Provider != "ExternalIdP" {
		t.Fatalf("expected provider ExternalIdP, got %q", got.Provider)
	}
	if got.MachineId == "" {
		t.Fatalf("expected generated machine ID")
	}
	if got.ProfileArn != "arn:aws:codewhisperer:eu-west-1:123456789012:profile/profile-1" {
		t.Fatalf("unexpected profile ARN: %q", got.ProfileArn)
	}
	if got.IssuerURL != "https://login.microsoftonline.com/example/v2.0" {
		t.Fatalf("unexpected issuer URL: %q", got.IssuerURL)
	}
	if got.TokenEndpoint != "https://login.microsoftonline.com/example/oauth2/v2.0/token" {
		t.Fatalf("unexpected token endpoint: %q", got.TokenEndpoint)
	}
	if got.Scopes != "scope.one offline_access" {
		t.Fatalf("unexpected scopes: %q", got.Scopes)
	}
	if got.AccessToken != "access-external" || got.RefreshToken != "refresh-external" {
		t.Fatalf("unexpected tokens: access=%q refresh=%q", got.AccessToken, got.RefreshToken)
	}
	if !got.Enabled {
		t.Fatalf("expected account enabled")
	}
}

func startExternalIdpSessionForHandlerTest(t *testing.T, h *Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/external-idp/start", strings.NewReader(`{"region":"us-east-1"}`))
	rec := httptest.NewRecorder()
	h.apiStartExternalIdp(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start failed: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatalf("empty session ID in start response")
	}
	return resp.SessionID
}

func installExternalIdpAuthClient(t *testing.T) func() {
	t.Helper()
	oldClient := auth.SetGlobalAuthClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration"):
				return externalIdpJSONResponse(`{
					"authorization_endpoint":"https://login.microsoftonline.com/example/oauth2/v2.0/authorize",
					"token_endpoint":"https://login.microsoftonline.com/example/oauth2/v2.0/token"
				}`), nil
			case req.URL.String() == "https://login.microsoftonline.com/example/oauth2/v2.0/token":
				body, _ := io.ReadAll(req.Body)
				form, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatalf("parse token form: %v", err)
				}
				if form.Get("code") != "enterprise-code" {
					t.Fatalf("expected enterprise code, got %q", form.Get("code"))
				}
				return externalIdpJSONResponse(`{"access_token":"access-external","refresh_token":"refresh-external","expires_in":3600}`), nil
			case req.URL.Host == "codewhisperer.us-east-1.amazonaws.com":
				if req.Header.Get("TokenType") != "EXTERNAL_IDP" {
					t.Fatalf("expected TokenType EXTERNAL_IDP, got %q", req.Header.Get("TokenType"))
				}
				return externalIdpJSONResponse(`{"profiles":[{"arn":"arn:aws:codewhisperer:eu-west-1:123456789012:profile/profile-1"}]}`), nil
			default:
				t.Fatalf("unexpected auth request: %s", req.URL.String())
				return nil, nil
			}
		}),
	})
	return func() { auth.SetGlobalAuthClientForTest(oldClient) }
}

func externalIdpJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
