package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	externalIdpSigninBaseURL      = "https://app.kiro.dev/signin"
	externalIdpRedirectURI        = "http://localhost:3128"
	externalIdpOAuthCallbackPath  = "/oauth/callback"
	externalIdpRedirectFrom       = "KiroIDE"
	externalIdpDefaultRegion      = "us-east-1"
	externalIdpListProfilesTarget = "AmazonCodeWhispererService.ListAvailableProfiles"

	ExternalIdpStatusAuthorizationRequired = "authorization_required"
	ExternalIdpStatusCompleted             = "completed"
)

var (
	externalIdpSessions   = make(map[string]*ExternalIdpSession)
	externalIdpSessionsMu sync.RWMutex
)

var externalIdpAllowedHostSuffixes = []string{
	".microsoftonline.com",
	".microsoftonline.us",
	".microsoftonline.cn",
}

var externalIdpProfileURL = func(region string) string {
	if region == "" {
		region = externalIdpDefaultRegion
	}
	return fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/", region)
}

type ExternalIdpSession struct {
	ID             string
	SigninURL      string
	PortalState    string
	PortalVerifier string
	Leg2State      string
	Leg2Verifier   string
	ClientID       string
	IssuerURL      string
	TokenEndpoint  string
	Scopes         string
	RedirectURI    string
	Region         string
	ExpiresAt      time.Time
}

type ExternalIdpCompleteResult struct {
	Status        string
	AuthorizeURL  string
	AccessToken   string
	RefreshToken  string
	ExpiresIn     int
	ProfileArn    string
	ClientID      string
	IssuerURL     string
	TokenEndpoint string
	Scopes        string
	Region        string
}

func StartExternalIdpLogin(region string) (*ExternalIdpSession, error) {
	if strings.TrimSpace(region) == "" {
		region = externalIdpDefaultRegion
	}

	verifier := generateCodeVerifier()
	state := GenerateAccountID()
	params := url.Values{}
	params.Set("state", state)
	params.Set("code_challenge", generateCodeChallenge(verifier))
	params.Set("code_challenge_method", "S256")
	params.Set("redirect_uri", externalIdpRedirectURI)
	params.Set("redirect_from", externalIdpRedirectFrom)

	session := &ExternalIdpSession{
		ID:             GenerateAccountID(),
		SigninURL:      externalIdpSigninBaseURL + "?" + params.Encode(),
		PortalState:    state,
		PortalVerifier: verifier,
		Region:         region,
		ExpiresAt:      time.Now().Add(10 * time.Minute),
	}

	externalIdpSessionsMu.Lock()
	externalIdpSessions[session.ID] = session
	externalIdpSessionsMu.Unlock()

	go cleanupExpiredExternalIdpSessions()
	return session, nil
}

func CompleteExternalIdpLogin(sessionID, callbackURL string) (*ExternalIdpCompleteResult, error) {
	session, err := getExternalIdpSession(sessionID)
	if err != nil {
		return nil, err
	}

	parsed, err := url.Parse(strings.TrimSpace(callbackURL))
	if err != nil {
		return nil, fmt.Errorf("invalid callback URL")
	}
	q := parsed.Query()

	if parsed.Path != externalIdpOAuthCallbackPath && isExternalIdpDescriptor(q) {
		return completeExternalIdpDescriptor(session, q)
	}
	if parsed.Path == externalIdpOAuthCallbackPath {
		return completeExternalIdpCode(session, q)
	}

	return nil, fmt.Errorf("unsupported external IdP callback URL")
}

func getExternalIdpSession(sessionID string) (*ExternalIdpSession, error) {
	externalIdpSessionsMu.RLock()
	session, ok := externalIdpSessions[sessionID]
	externalIdpSessionsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session not found or expired")
	}
	if time.Now().After(session.ExpiresAt) {
		externalIdpSessionsMu.Lock()
		delete(externalIdpSessions, sessionID)
		externalIdpSessionsMu.Unlock()
		return nil, fmt.Errorf("session expired")
	}
	return session, nil
}

func isExternalIdpDescriptor(q url.Values) bool {
	return strings.EqualFold(strings.TrimSpace(q.Get("login_option")), "external_idp") ||
		strings.TrimSpace(q.Get("issuer_url")) != ""
}

func completeExternalIdpDescriptor(session *ExternalIdpSession, q url.Values) (*ExternalIdpCompleteResult, error) {
	issuerURL := strings.TrimSpace(q.Get("issuer_url"))
	clientID := strings.TrimSpace(q.Get("client_id"))
	scopes := strings.TrimSpace(q.Get("scopes"))
	if scopes == "" {
		scopes = strings.TrimSpace(q.Get("scope"))
	}
	loginHint := strings.TrimSpace(q.Get("login_hint"))
	if issuerURL == "" {
		return nil, fmt.Errorf("invalid external IdP descriptor: issuer_url is required")
	}
	if clientID == "" {
		return nil, fmt.Errorf("invalid external IdP descriptor: client_id is required")
	}

	authEndpoint, tokenEndpoint, err := discoverExternalIdp(issuerURL, httpClient())
	if err != nil {
		return nil, err
	}

	leg2Verifier := generateCodeVerifier()
	leg2State := GenerateAccountID()
	redirectURI := externalIdpRedirectURI + externalIdpOAuthCallbackPath

	externalIdpSessionsMu.Lock()
	session.Leg2Verifier = leg2Verifier
	session.Leg2State = leg2State
	session.ClientID = clientID
	session.IssuerURL = issuerURL
	session.TokenEndpoint = tokenEndpoint
	session.Scopes = scopes
	session.RedirectURI = redirectURI
	externalIdpSessionsMu.Unlock()

	return &ExternalIdpCompleteResult{
		Status:        ExternalIdpStatusAuthorizationRequired,
		AuthorizeURL:  buildExternalIdpAuthorizeURL(authEndpoint, clientID, redirectURI, scopes, generateCodeChallenge(leg2Verifier), leg2State, loginHint),
		ClientID:      clientID,
		IssuerURL:     issuerURL,
		TokenEndpoint: tokenEndpoint,
		Scopes:        scopes,
		Region:        session.Region,
	}, nil
}

func completeExternalIdpCode(session *ExternalIdpSession, q url.Values) (*ExternalIdpCompleteResult, error) {
	if session.Leg2State == "" {
		return nil, fmt.Errorf("external IdP authorization has not been started")
	}
	if errParam := strings.TrimSpace(q.Get("error")); errParam != "" {
		desc := strings.TrimSpace(q.Get("error_description"))
		if desc != "" {
			return nil, fmt.Errorf("external IdP authorization error: %s %s", errParam, desc)
		}
		return nil, fmt.Errorf("external IdP authorization error: %s", errParam)
	}
	if gotState := strings.TrimSpace(q.Get("state")); gotState == "" || gotState != session.Leg2State {
		return nil, fmt.Errorf("state mismatch")
	}
	code := strings.TrimSpace(q.Get("code"))
	if code == "" {
		return nil, fmt.Errorf("authorization code is required")
	}

	accessToken, refreshToken, expiresIn, err := exchangeExternalIdpCode(
		session.TokenEndpoint,
		session.ClientID,
		code,
		session.Leg2Verifier,
		session.RedirectURI,
		session.Scopes,
		httpClient(),
	)
	if err != nil {
		return nil, err
	}

	profileArn, err := resolveExternalIdpProfileArn(accessToken, session.Region, httpClient())
	if err != nil {
		return nil, err
	}
	region := session.Region
	if profileRegion := externalIdpRegionFromProfileArn(profileArn); profileRegion != "" {
		region = profileRegion
	}

	externalIdpSessionsMu.Lock()
	delete(externalIdpSessions, session.ID)
	externalIdpSessionsMu.Unlock()

	return &ExternalIdpCompleteResult{
		Status:        ExternalIdpStatusCompleted,
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		ExpiresIn:     expiresIn,
		ProfileArn:    profileArn,
		ClientID:      session.ClientID,
		IssuerURL:     session.IssuerURL,
		TokenEndpoint: session.TokenEndpoint,
		Scopes:        session.Scopes,
		Region:        region,
	}, nil
}

func discoverExternalIdp(issuerURL string, client *http.Client) (string, string, error) {
	if err := validateExternalIdpEndpoint(issuerURL); err != nil {
		return "", "", err
	}
	discoveryURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequest(http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := noRedirectClient(client).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("OIDC discovery failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", "", err
	}
	authEndpoint := strings.TrimSpace(doc.AuthorizationEndpoint)
	tokenEndpoint := strings.TrimSpace(doc.TokenEndpoint)
	if authEndpoint == "" || tokenEndpoint == "" {
		return "", "", fmt.Errorf("OIDC discovery document missing authorization_endpoint or token_endpoint")
	}
	if err := validateExternalIdpEndpoint(authEndpoint); err != nil {
		return "", "", err
	}
	if err := validateExternalIdpEndpoint(tokenEndpoint); err != nil {
		return "", "", err
	}
	return authEndpoint, tokenEndpoint, nil
}

func noRedirectClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

func validateExternalIdpEndpoint(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid external IdP URL: %w", err)
	}
	if strings.ToLower(parsed.Scheme) != "https" {
		return fmt.Errorf("external IdP URL must be https: %q", rawURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("external IdP URL has no host: %q", rawURL)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("external IdP host must not be an IP literal: %q", host)
	}
	for _, suffix := range externalIdpAllowedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("external IdP host %q is not allow-listed", host)
}

func buildExternalIdpAuthorizeURL(authEndpoint, clientID, redirectURI, scopes, challenge, state, loginHint string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("response_mode", "query")
	q.Set("state", state)
	if strings.TrimSpace(loginHint) != "" {
		q.Set("login_hint", strings.TrimSpace(loginHint))
	}
	return authEndpoint + "?" + q.Encode()
}

func exchangeExternalIdpCode(tokenEndpoint, clientID, code, verifier, redirectURI, scopes string, client *http.Client) (string, string, int, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	if strings.TrimSpace(scopes) != "" {
		form.Set("scope", scopes)
	}

	req, err := http.NewRequest(http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()

	var parsed map[string]interface{}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &parsed)
	accessToken, _ := parsed["access_token"].(string)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || accessToken == "" {
		if errCode, _ := parsed["error"].(string); errCode != "" {
			desc, _ := parsed["error_description"].(string)
			return "", "", 0, fmt.Errorf("external IdP token exchange failed: %s: %s", errCode, desc)
		}
		return "", "", 0, fmt.Errorf("external IdP token exchange failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
	refreshToken, _ := parsed["refresh_token"].(string)
	expiresIn := intFromJSONNumber(parsed["expires_in"])
	return accessToken, refreshToken, expiresIn, nil
}

func resolveExternalIdpProfileArn(accessToken, region string, client *http.Client) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("access token is required")
	}
	if strings.TrimSpace(region) == "" {
		region = externalIdpDefaultRegion
	}
	machineID := externalIdpBuildMachineID(accessToken)
	req, err := http.NewRequest(http.MethodPost, externalIdpProfileURL(region), bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("Accept", "application/x-amz-json-1.0")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Amz-Target", externalIdpListProfilesTarget)
	req.Header.Set("amz-sdk-invocation-id", externalIdpBuildMachineID(accessToken, region, "list-profiles"))
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("User-Agent", externalIdpUserAgent(machineID))
	req.Header.Set("x-amz-user-agent", externalIdpAmzUserAgent(machineID))
	req.Header.Set("TokenType", "EXTERNAL_IDP")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("list profiles failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	for _, profile := range result.Profiles {
		if arn := strings.TrimSpace(profile.Arn); arn != "" {
			return arn, nil
		}
	}
	return "", fmt.Errorf("empty profile list")
}

func intFromJSONNumber(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func externalIdpBuildMachineID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return fmt.Sprintf("%x", sum[:])
}

func externalIdpUserAgent(machineID string) string {
	clientCfg := config.GetKiroClientConfig()
	return fmt.Sprintf(
		"aws-sdk-js/1.0.0 ua/2.1 os/%s lang/js md/nodejs#%s api/codewhispererruntime#1.0.0 m/N,E KiroIDE-%s-%s",
		clientCfg.SystemVersion,
		clientCfg.NodeVersion,
		clientCfg.KiroVersion,
		machineID,
	)
}

func externalIdpAmzUserAgent(machineID string) string {
	clientCfg := config.GetKiroClientConfig()
	return fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-%s-%s", clientCfg.KiroVersion, machineID)
}

func externalIdpRegionFromProfileArn(profileArn string) string {
	parts := strings.Split(strings.TrimSpace(profileArn), ":")
	if len(parts) >= 4 && parts[0] == "arn" && parts[2] == "codewhisperer" {
		return strings.TrimSpace(parts[3])
	}
	return ""
}

func cleanupExpiredExternalIdpSessions() {
	externalIdpSessionsMu.Lock()
	defer externalIdpSessionsMu.Unlock()
	now := time.Now()
	for id, session := range externalIdpSessions {
		if now.After(session.ExpiresAt) {
			delete(externalIdpSessions, id)
		}
	}
}
