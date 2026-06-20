package auth

import (
	"io"
	"kiro-go/config"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestExternalIdpCompleteDescriptorReturnsAuthorizeURL(t *testing.T) {
	defer resetExternalIdpSessionsForTest()

	oldClient := SetGlobalAuthClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://login.microsoftonline.com/example/v2.0/.well-known/openid-configuration" {
				t.Fatalf("unexpected discovery request: %s", req.URL.String())
			}
			return jsonResponse(`{
				"authorization_endpoint":"https://login.microsoftonline.com/example/oauth2/v2.0/authorize",
				"token_endpoint":"https://login.microsoftonline.com/example/oauth2/v2.0/token"
			}`), nil
		}),
	})
	defer SetGlobalAuthClientForTest(oldClient)

	session, err := StartExternalIdpLogin("us-east-1")
	if err != nil {
		t.Fatalf("StartExternalIdpLogin: %v", err)
	}
	if !strings.HasPrefix(session.SigninURL, "https://app.kiro.dev/signin?") {
		t.Fatalf("expected Kiro sign-in URL, got %q", session.SigninURL)
	}

	callbackURL := "http://localhost:3128/signin/callback?" + url.Values{
		"login_option": {"external_idp"},
		"issuer_url":   {"https://login.microsoftonline.com/example/v2.0"},
		"client_id":    {"client-123"},
		"scopes":       {"api://client-123/codewhisperer:conversations offline_access"},
		"login_hint":   {"user@example.com"},
	}.Encode()

	result, err := CompleteExternalIdpLogin(session.ID, callbackURL)
	if err != nil {
		t.Fatalf("CompleteExternalIdpLogin descriptor: %v", err)
	}
	if result.Status != ExternalIdpStatusAuthorizationRequired {
		t.Fatalf("expected authorization_required, got %q", result.Status)
	}

	authorizeURL, err := url.Parse(result.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	q := authorizeURL.Query()
	if authorizeURL.Host != "login.microsoftonline.com" {
		t.Fatalf("expected Microsoft authorize host, got %q", authorizeURL.Host)
	}
	if q.Get("client_id") != "client-123" {
		t.Fatalf("expected client_id propagated, got %q", q.Get("client_id"))
	}
	if q.Get("scope") != "api://client-123/codewhisperer:conversations offline_access" {
		t.Fatalf("expected scope propagated, got %q", q.Get("scope"))
	}
	if q.Get("state") == "" || q.Get("state") != session.Leg2State {
		t.Fatalf("expected generated leg2 state in authorize URL, got %q want %q", q.Get("state"), session.Leg2State)
	}
	if q.Get("code_challenge") == "" {
		t.Fatalf("expected PKCE code challenge")
	}
	if q.Get("login_hint") != "user@example.com" {
		t.Fatalf("expected login_hint propagated, got %q", q.Get("login_hint"))
	}
}

func TestExternalIdpCompleteCodeExchangesAndResolvesProfile(t *testing.T) {
	defer resetExternalIdpSessionsForTest()

	var sawTokenExchange bool
	var sawProfileLookup bool
	oldClient := SetGlobalAuthClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration"):
				return jsonResponse(`{
					"authorization_endpoint":"https://login.microsoftonline.com/example/oauth2/v2.0/authorize",
					"token_endpoint":"https://login.microsoftonline.com/example/oauth2/v2.0/token"
				}`), nil
			case req.URL.String() == "https://login.microsoftonline.com/example/oauth2/v2.0/token":
				sawTokenExchange = true
				body, _ := io.ReadAll(req.Body)
				form, err := url.ParseQuery(string(body))
				if err != nil {
					t.Fatalf("parse token form: %v", err)
				}
				if form.Get("grant_type") != "authorization_code" {
					t.Fatalf("expected authorization_code grant, got %q", form.Get("grant_type"))
				}
				if form.Get("client_id") != "client-123" {
					t.Fatalf("expected client_id, got %q", form.Get("client_id"))
				}
				if form.Get("code") != "enterprise-code" {
					t.Fatalf("expected authorization code, got %q", form.Get("code"))
				}
				if form.Get("code_verifier") == "" {
					t.Fatalf("expected code verifier")
				}
				return jsonResponse(`{"access_token":"access-external","refresh_token":"refresh-external","expires_in":3600}`), nil
			case req.URL.Host == "codewhisperer.us-east-1.amazonaws.com":
				sawProfileLookup = true
				if req.Header.Get("TokenType") != "EXTERNAL_IDP" {
					t.Fatalf("expected TokenType EXTERNAL_IDP, got %q", req.Header.Get("TokenType"))
				}
				if req.Header.Get("Authorization") != "Bearer access-external" {
					t.Fatalf("expected bearer token, got %q", req.Header.Get("Authorization"))
				}
				return jsonResponse(`{"profiles":[{"arn":"arn:aws:codewhisperer:eu-west-1:123456789012:profile/profile-1"}]}`), nil
			default:
				t.Fatalf("unexpected request: %s", req.URL.String())
				return nil, nil
			}
		}),
	})
	defer SetGlobalAuthClientForTest(oldClient)

	session, err := StartExternalIdpLogin("us-east-1")
	if err != nil {
		t.Fatalf("StartExternalIdpLogin: %v", err)
	}
	descriptorURL := "http://localhost:3128/signin/callback?" + url.Values{
		"login_option": {"external_idp"},
		"issuer_url":   {"https://login.microsoftonline.com/example/v2.0"},
		"client_id":    {"client-123"},
		"scopes":       {"scope.one offline_access"},
	}.Encode()
	first, err := CompleteExternalIdpLogin(session.ID, descriptorURL)
	if err != nil {
		t.Fatalf("descriptor callback: %v", err)
	}
	authorizeURL, _ := url.Parse(first.AuthorizeURL)
	secondURL := "http://localhost:3128/oauth/callback?" + url.Values{
		"code":  {"enterprise-code"},
		"state": {authorizeURL.Query().Get("state")},
	}.Encode()

	result, err := CompleteExternalIdpLogin(session.ID, secondURL)
	if err != nil {
		t.Fatalf("code callback: %v", err)
	}
	if result.Status != ExternalIdpStatusCompleted {
		t.Fatalf("expected completed, got %q", result.Status)
	}
	if result.AccessToken != "access-external" || result.RefreshToken != "refresh-external" {
		t.Fatalf("unexpected tokens: access=%q refresh=%q", result.AccessToken, result.RefreshToken)
	}
	if result.ProfileArn != "arn:aws:codewhisperer:eu-west-1:123456789012:profile/profile-1" {
		t.Fatalf("unexpected profile ARN: %q", result.ProfileArn)
	}
	if result.Region != "eu-west-1" {
		t.Fatalf("expected region from profile ARN, got %q", result.Region)
	}
	if result.ClientID != "client-123" || result.IssuerURL == "" || result.TokenEndpoint == "" || result.Scopes != "scope.one offline_access" {
		t.Fatalf("missing external IdP metadata: %+v", result)
	}
	if !sawTokenExchange {
		t.Fatalf("expected token exchange request")
	}
	if !sawProfileLookup {
		t.Fatalf("expected profile lookup request")
	}
}

func TestExternalIdpRejectsUntrustedIssuerHost(t *testing.T) {
	defer resetExternalIdpSessionsForTest()

	oldClient := SetGlobalAuthClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("unexpected request for rejected issuer: %s", req.URL.String())
			return nil, nil
		}),
	})
	defer SetGlobalAuthClientForTest(oldClient)

	session, err := StartExternalIdpLogin("us-east-1")
	if err != nil {
		t.Fatalf("StartExternalIdpLogin: %v", err)
	}
	callbackURL := "http://localhost:3128/signin/callback?" + url.Values{
		"login_option": {"external_idp"},
		"issuer_url":   {"https://evil.example.com/tenant/v2.0"},
		"client_id":    {"client-123"},
	}.Encode()

	_, err = CompleteExternalIdpLogin(session.ID, callbackURL)
	if err == nil {
		t.Fatalf("expected untrusted issuer error")
	}
	if !strings.Contains(err.Error(), "allow-listed") {
		t.Fatalf("expected allow-list error, got %v", err)
	}
}

func TestRefreshTokenUsesExternalIdpTokenEndpoint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(configPath); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	var sawRefresh bool
	oldClient := SetGlobalAuthClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			sawRefresh = true
			if req.URL.String() != "https://login.microsoftonline.com/example/oauth2/v2.0/token" {
				t.Fatalf("unexpected refresh URL: %s", req.URL.String())
			}
			if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Fatalf("expected form content type, got %q", got)
			}
			body, _ := io.ReadAll(req.Body)
			form, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatalf("parse refresh form: %v", err)
			}
			if form.Get("grant_type") != "refresh_token" {
				t.Fatalf("expected refresh_token grant, got %q", form.Get("grant_type"))
			}
			if form.Get("refresh_token") != "refresh-external" {
				t.Fatalf("expected refresh token, got %q", form.Get("refresh_token"))
			}
			if form.Get("client_id") != "client-123" {
				t.Fatalf("expected client id, got %q", form.Get("client_id"))
			}
			if form.Get("scope") != "scope.one offline_access" {
				t.Fatalf("expected scope, got %q", form.Get("scope"))
			}
			if form.Get("clientSecret") != "" || form.Get("client_secret") != "" {
				t.Fatalf("external IdP refresh must not send a client secret: %s", string(body))
			}
			return jsonResponse(`{"access_token":"access-refreshed","refresh_token":"refresh-rotated","expires_in":1800}`), nil
		}),
	})
	defer SetGlobalAuthClientForTest(oldClient)

	before := time.Now().Unix()
	accessToken, refreshToken, expiresAt, profileArn, err := RefreshToken(&config.Account{
		AuthMethod:    "external_idp",
		RefreshToken:  "refresh-external",
		ClientID:      "client-123",
		TokenEndpoint: "https://login.microsoftonline.com/example/oauth2/v2.0/token",
		Scopes:        "scope.one offline_access",
	})
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if !sawRefresh {
		t.Fatalf("expected refresh request")
	}
	if accessToken != "access-refreshed" || refreshToken != "refresh-rotated" {
		t.Fatalf("unexpected refreshed tokens: access=%q refresh=%q", accessToken, refreshToken)
	}
	if profileArn != "" {
		t.Fatalf("external IdP refresh should not invent profile ARN, got %q", profileArn)
	}
	if expiresAt < before+1795 || expiresAt > after+1805 {
		t.Fatalf("expected expiresAt from upstream expires_in, got %d", expiresAt)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
