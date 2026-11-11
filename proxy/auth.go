package proxy

import (
	"context"
	"kiro-go/config"
	"net/http"
	"strings"
)

// apiKeyContextKey is an unexported type used as the context key for the matched ApiKeyEntry
// so it cannot collide with keys defined in other packages.
type apiKeyContextKey struct{}

// authError describes why authentication failed. status is the HTTP status code to send.
type authError struct {
	status  int
	code    string
	message string
}

func (e *authError) Error() string { return e.message }

func newAuthError(status int, code, message string) *authError {
	return &authError{status: status, code: code, message: message}
}

// extractProvidedKey reads the API key from Authorization (Bearer ...) or X-Api-Key header.
func extractProvidedKey(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}
	return ""
}

// authenticate validates an incoming request against the configured API keys.
//
// Master switch: config.RequireApiKey. When false, requests pass without checking
// any keys, even if entries exist (so the admin UI can hold draft keys without
// affecting public deployments).
//
// When RequireApiKey is true:
// The provided key MUST match an enabled, in-quota entry. If no keys are
// configured the service fails closed instead of accepting every request.
//
// Returns (entry, nil) on success. entry is nil when the master switch is off.
func (h *Handler) authenticate(r *http.Request) (*config.ApiKeyEntry, error) {
	if !config.IsApiKeyRequired() {
		return nil, nil
	}

	provided := extractProvidedKey(r)
	if provided == "" {
		return nil, newAuthError(http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
	}
	if !config.HasApiKeys() {
		return nil, newAuthError(http.StatusUnauthorized, "authentication_error", "API key authentication is required but no keys are configured")
	}
	entry := config.FindApiKeyByValue(provided)
	if entry == nil {
		return nil, newAuthError(http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
	}
	if !entry.Enabled {
		return nil, newAuthError(http.StatusUnauthorized, "authentication_error", "API key disabled")
	}
	if overToken, overCredit := config.ApiKeyOverLimit(*entry); overToken || overCredit {
		if overToken {
			return nil, newAuthError(http.StatusTooManyRequests, "rate_limit_error", "token limit exceeded")
		}
		return nil, newAuthError(http.StatusTooManyRequests, "rate_limit_error", "credit limit exceeded")
	}
	return entry, nil
}

// withApiKeyContext attaches the matched entry to the request context so downstream
// handlers (recordSuccess, etc.) can credit usage against the correct key.
func withApiKeyContext(r *http.Request, entry *config.ApiKeyEntry) *http.Request {
	if entry == nil {
		return r
	}
	ctx := context.WithValue(r.Context(), apiKeyContextKey{}, entry.ID)
	return r.WithContext(ctx)
}

// apiKeyIDFromContext returns the matched API key ID stored in ctx, or empty string.
func apiKeyIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(apiKeyContextKey{}).(string); ok {
		return v
	}
	return ""
}
