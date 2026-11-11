package proxy

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func parseProxyPoolInput(raw string) ([]string, []string) {
	var proxies []string
	var errors []string
	seen := make(map[string]bool)
	for lineNo, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		proxy, err := normalizeProxyPoolEntry(line)
		if err != nil {
			errors = append(errors, fmt.Sprintf("line %d: %s", lineNo+1, err))
			continue
		}
		if !seen[proxy] {
			proxies = append(proxies, proxy)
			seen[proxy] = true
		}
	}
	return proxies, errors
}

// normalizeProxyPoolEntry accepts common HTTP/SOCKS proxy URIs and the
// host:port:user:password format used by many account suppliers.
func normalizeProxyPoolEntry(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty proxy")
	}
	if !strings.Contains(raw, "://") {
		if strings.Contains(raw, "@") {
			raw = "http://" + raw
		} else if strings.Count(raw, ":") >= 3 {
			parts := strings.SplitN(raw, ":", 4)
			port, err := strconv.Atoi(parts[1])
			if err != nil || port < 1 || port > 65535 || strings.TrimSpace(parts[0]) == "" ||
				strings.TrimSpace(parts[2]) == "" || parts[3] == "" {
				return "", fmt.Errorf("expected host:port:user:password")
			}
			raw = (&url.URL{
				Scheme: "http",
				User:   url.UserPassword(parts[2], parts[3]),
				Host:   net.JoinHostPort(parts[0], parts[1]),
			}).String()
		} else {
			raw = "http://" + raw
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid proxy URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("unsupported proxy scheme")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("proxy host is required")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("proxy port is required")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("proxy path is not supported")
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func maskProxyURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if username != "" {
			return fmt.Sprintf("%s://%s:****@%s", parsed.Scheme, username, parsed.Host)
		}
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
}
