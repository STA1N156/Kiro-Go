package auth

import (
	"net/http"
	"net/url"
	"time"
)

// httpClient is shared by auth requests and can be rebuilt when proxy settings change.
var httpClient *http.Client

func init() {
	InitHttpClient("")
}

func buildAuthTransport(proxyURL string) *http.Transport {
	t := &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
		ForceAttemptHTTP2:   true,
	}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			t.Proxy = http.ProxyURL(u)
			t.ForceAttemptHTTP2 = false
		}
	} else {
		t.Proxy = http.ProxyFromEnvironment
	}
	return t
}

// InitHttpClient rebuilds the auth HTTP client with the current outbound proxy.
func InitHttpClient(proxyURL string) {
	httpClient = &http.Client{
		Timeout:   30 * time.Second,
		Transport: buildAuthTransport(proxyURL),
	}
}
