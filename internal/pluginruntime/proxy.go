package pluginruntime

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type proxyPolicySource interface {
	proxyPolicy(pluginID, token string) ([]string, bool)
}

type egressProxy struct {
	address  string
	policies proxyPolicySource
	events   *eventBus
	resolver hostResolver
	server   *http.Server
}

func newEgressProxy(address string, policies proxyPolicySource, events *eventBus) *egressProxy {
	return newEgressProxyWithResolver(address, policies, events, newTrustedResolver())
}

func newEgressProxyWithResolver(
	address string,
	policies proxyPolicySource,
	events *eventBus,
	resolver hostResolver,
) *egressProxy {
	proxy := &egressProxy{address: address, policies: policies, events: events, resolver: resolver}
	proxy.server = &http.Server{
		Addr:              address,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	return proxy
}

func (p *egressProxy) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", p.address)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- p.server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return p.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (p *egressProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	pluginID, token, ok := proxyCredentials(request)
	if !ok {
		p.deny(writer, "", request.Host, "missing proxy credentials")
		return
	}
	allowedHosts, ok := p.policies.proxyPolicy(pluginID, token)
	if !ok {
		p.deny(writer, pluginID, request.Host, "invalid proxy credentials")
		return
	}
	host, port, err := proxyTarget(request)
	if err != nil {
		p.deny(writer, pluginID, request.Host, err.Error())
		return
	}
	if !allowedByManifest(host, allowedHosts) {
		p.deny(writer, pluginID, net.JoinHostPort(host, port), "host is not in plugin allowlist")
		return
	}
	if port != "80" && port != "443" {
		p.deny(writer, pluginID, net.JoinHostPort(host, port), "only ports 80 and 443 are allowed")
		return
	}

	request.Header.Del("Proxy-Authorization")
	if request.Method == http.MethodConnect {
		p.serveTunnel(writer, request, pluginID, host, port)
		return
	}
	p.serveHTTP(writer, request, pluginID, host, port)
}

func (p *egressProxy) serveTunnel(
	writer http.ResponseWriter,
	request *http.Request,
	pluginID, host, port string,
) {
	upstream, err := p.dialPublic(request.Context(), "tcp", net.JoinHostPort(host, port))
	if err != nil {
		p.deny(writer, pluginID, net.JoinHostPort(host, port), err.Error())
		return
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(writer, "proxy tunnel is unavailable", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	copyDone := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		copyDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		copyDone <- struct{}{}
	}()
	<-copyDone
	_ = client.Close()
	_ = upstream.Close()
}

func (p *egressProxy) serveHTTP(
	writer http.ResponseWriter,
	request *http.Request,
	pluginID, host, port string,
) {
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	outbound.URL.Scheme = "http"
	outbound.URL.Host = net.JoinHostPort(host, port)
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: p.dialPublic,
	}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		p.deny(writer, pluginID, net.JoinHostPort(host, port), err.Error())
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = io.Copy(writer, response.Body)
}

func (p *egressProxy) deny(writer http.ResponseWriter, pluginID, target, reason string) {
	p.events.publish(pluginID, "network_denied", "plugin outbound request denied", map[string]string{
		"target": target,
		"reason": reason,
	})
	http.Error(writer, "plugin network access denied", http.StatusForbidden)
}

func proxyCredentials(request *http.Request) (string, string, bool) {
	header := strings.TrimSpace(request.Header.Get("Proxy-Authorization"))
	if !strings.HasPrefix(header, "Basic ") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, "Basic ")))
	if err != nil {
		return "", "", false
	}
	pluginID, token, ok := strings.Cut(string(decoded), ":")
	return pluginID, token, ok && pluginID != "" && token != ""
}

func proxyTarget(request *http.Request) (string, string, error) {
	target := request.Host
	if request.Method != http.MethodConnect && request.URL.Host != "" {
		target = request.URL.Host
	}
	host, port, err := net.SplitHostPort(target)
	if err == nil {
		return normalizeHost(host), port, nil
	}
	if strings.Contains(err.Error(), "missing port") {
		if request.Method == http.MethodConnect || request.URL.Scheme == "https" {
			return normalizeHost(target), "443", nil
		}
		return normalizeHost(target), "80", nil
	}
	return "", "", errors.New("invalid proxy target")
}

func allowedByManifest(host string, allowedHosts []string) bool {
	host = normalizeHost(host)
	for _, allowed := range allowedHosts {
		allowed = normalizeHost(allowed)
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func (p *egressProxy) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := p.resolver.LookupNetIP(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialErrors []error
	for _, address := range addresses {
		if !publicAddr(address) {
			continue
		}
		var dialer net.Dialer
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, dialErr)
	}
	if len(dialErrors) > 0 {
		return nil, fmt.Errorf("connect to public addresses for %s: %w", host, errors.Join(dialErrors...))
	}
	return nil, fmt.Errorf("target %s resolved only to private or reserved addresses", host)
}

func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return publicAddr(address)
}

func publicAddr(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range reservedEgressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// IsPublicAddress reports whether an address is safe for plugin-related
// outbound traffic. It intentionally applies the same policy as the sandbox
// egress proxy, including private, reserved and Fake-IP ranges.
func IsPublicAddress(address netip.Addr) bool {
	return publicAddr(address)
}

var reservedEgressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func secureTokenEqual(left, right string) bool {
	if len(left) != len(right) || left == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
