package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/pluginruntime"
	"github.com/Tencent/WeKnora/internal/utils"
)

const (
	githubWebHost = "github.com"
	githubRawHost = "raw.githubusercontent.com"
	githubAPIHost = "api.github.com"
)

func pluginManifestDownload(rawURL string) (string, *http.Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", nil, errors.New("manifest_url must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return "", nil, errors.New("manifest_url must not contain credentials")
	}

	host := strings.ToLower(parsed.Hostname())
	switch host {
	case githubWebHost:
		normalized, normalizeErr := normalizeGitHubManifestURL(parsed)
		if normalizeErr != nil {
			return "", nil, normalizeErr
		}
		return normalized, newGitHubManifestClient(), nil
	case githubRawHost:
		if parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", nil, errors.New("GitHub Raw manifest URL must not contain a port, query or fragment")
		}
		if !strings.HasSuffix(parsed.Path, "/plugin.yaml") {
			return "", nil, errors.New("GitHub Raw manifest URL must point to plugin.yaml")
		}
		return parsed.String(), newGitHubManifestClient(), nil
	default:
		if err := utils.ValidateURLForSSRF(parsed.String()); err != nil {
			return "", nil, fmt.Errorf("manifest_url rejected by SSRF policy: %w", err)
		}
		config := utils.DefaultSSRFSafeHTTPClientConfig()
		config.Timeout = 15 * time.Second
		config.MaxRedirects = 3
		return parsed.String(), utils.NewSSRFSafeHTTPClient(config), nil
	}
}

func normalizeGitHubManifestURL(parsed *url.URL) (string, error) {
	if parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GitHub manifest URL must not contain a port, query or fragment")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 5 || (parts[2] != "blob" && parts[2] != "raw") {
		return "", errors.New("GitHub manifest URL must use /owner/repository/blob/ref/plugin.yaml")
	}
	if parts[0] == "" || parts[1] == "" || parts[len(parts)-1] != "plugin.yaml" {
		return "", errors.New("GitHub manifest URL must point to plugin.yaml")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("GitHub manifest URL contains an invalid path segment")
		}
	}
	parsed.Host = githubRawHost
	rawParts := append([]string(nil), parts[:2]...)
	rawParts = append(rawParts, parts[3:]...)
	parsed.Path = "/" + strings.Join(rawParts, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func newGitHubManifestClient() *http.Client {
	return newTrustedGitHubClient(githubRawHost, "manifest")
}

// newTrustedGitHubClient uses the operator-configured HTTPS proxy when one is
// available. Without a proxy it bypasses local fake-IP DNS through trusted DoH.
// Both paths keep requests pinned to the expected GitHub host.
func newTrustedGitHubClient(expectedHost, purpose string) *http.Client {
	resolver := pluginruntime.NewTrustedHostResolver()
	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	probe := &http.Request{URL: &url.URL{Scheme: "https", Host: expectedHost}}
	proxyURL, proxyErr := http.ProxyFromEnvironment(probe)
	switch {
	case proxyErr != nil:
		transport.Proxy = func(*http.Request) (*url.URL, error) {
			return nil, fmt.Errorf("resolve GitHub %s proxy: %w", purpose, proxyErr)
		}
	case proxyURL != nil:
		transport.Proxy = http.ProxyURL(proxyURL)
	default:
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid GitHub %s address: %w", purpose, err)
			}
			if !strings.EqualFold(host, expectedHost) || port != "443" {
				return nil, fmt.Errorf("GitHub %s request attempted an unexpected destination", purpose)
			}
			addresses, err := resolver.LookupNetIP(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, resolved := range addresses {
				if !pluginruntime.IsPublicAddress(resolved) {
					return nil, fmt.Errorf("GitHub %s host resolved to restricted address %s", purpose, resolved)
				}
			}
			if len(addresses) == 0 {
				return nil, fmt.Errorf("GitHub %s host did not resolve to an address", purpose)
			}
			var dialer net.Dialer
			var lastErr error
			for _, resolved := range addresses {
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			return nil, fmt.Errorf("connect to GitHub %s host: %w", purpose, lastErr)
		}
	}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("GitHub %s request exceeded redirect limit", purpose)
			}
			if request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Hostname(), expectedHost) {
				return fmt.Errorf("GitHub %s request redirected to an unexpected destination", purpose)
			}
			return nil
		},
	}
}
