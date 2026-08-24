package pluginruntime

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

type staticProxyPolicies struct {
	pluginID string
	token    string
	hosts    []string
}

type staticHostResolver []netip.Addr

func (r staticHostResolver) LookupNetIP(context.Context, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r...), nil
}

func (p staticProxyPolicies) proxyPolicy(pluginID, token string) ([]string, bool) {
	if pluginID != p.pluginID || token != p.token {
		return nil, false
	}
	return append([]string(nil), p.hosts...), true
}

func TestAllowedByManifestUsesExactAndSafeWildcardMatches(t *testing.T) {
	tests := []struct {
		host    string
		allowed []string
		want    bool
	}{
		{host: "api.github.com", allowed: []string{"api.github.com"}, want: true},
		{host: "API.GITHUB.COM.", allowed: []string{"api.github.com"}, want: true},
		{host: "uploads.github.com", allowed: []string{"*.github.com"}, want: true},
		{host: "github.com", allowed: []string{"*.github.com"}, want: false},
		{host: "evilgithub.com", allowed: []string{"*.github.com"}, want: false},
	}
	for _, test := range tests {
		if got := allowedByManifest(test.host, test.allowed); got != test.want {
			t.Errorf("allowedByManifest(%q, %v)=%v want %v", test.host, test.allowed, got, test.want)
		}
	}
}

func TestPublicIPRejectsPrivateAndReservedRanges(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1",
		"192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"::1", "fc00::1", "fe80::1", "2001:db8::1",
	} {
		if publicIP(net.ParseIP(value)) {
			t.Errorf("%s must not be considered public", value)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("8.8.8.8 should be considered public")
	}
}

func TestDialPublicRejectsFakeIPResolution(t *testing.T) {
	proxy := &egressProxy{resolver: staticHostResolver{
		netip.MustParseAddr("198.18.0.19"),
	}}
	_, err := proxy.dialPublic(context.Background(), "tcp", "api.github.com:443")
	if err == nil {
		t.Fatal("Fake-IP resolution must not be dialed")
	}
}

func TestProxyDenialPublishesAuditableEvent(t *testing.T) {
	events := newEventBus()
	proxy := newEgressProxy(":0", staticProxyPolicies{
		pluginID: "io.test.plugin",
		token:    "proxy-secret",
		hosts:    []string{"api.github.com"},
	}, events)
	request := httptest.NewRequest(http.MethodGet, "http://example.com/private", nil)
	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("io.test.plugin:proxy-secret")))
	response := httptest.NewRecorder()

	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", response.Code)
	}
	backlog, subscriptionID, _ := events.subscribe(0)
	events.unsubscribe(subscriptionID)
	if len(backlog) != 1 {
		t.Fatalf("events=%d want 1", len(backlog))
	}
	event := backlog[0]
	if event.GetKind() != "network_denied" || event.GetPluginId() != "io.test.plugin" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.GetDetails()["target"] != "example.com:80" || event.GetDetails()["reason"] == "" {
		t.Fatalf("missing safe denial details: %#v", event.GetDetails())
	}
}
