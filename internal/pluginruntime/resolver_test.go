package pluginruntime

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDoHResolverReturnsAddressWithoutSystemDNS(t *testing.T) {
	server := newDoHTestServer(t, netip.MustParseAddr("20.205.243.168"))
	defer server.Close()

	resolver := &dohResolver{providers: []dohProvider{{
		name: "test", endpoint: server.URL, client: server.Client(),
	}}}
	addresses, err := resolver.LookupNetIP(context.Background(), "api.github.com")
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("20.205.243.168")}, addresses)
}

func TestDoHResolverFallsBackToNextProvider(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	working := newDoHTestServer(t, netip.MustParseAddr("8.8.8.8"))
	defer working.Close()

	resolver := &dohResolver{providers: []dohProvider{
		{name: "failing", endpoint: failing.URL, client: failing.Client()},
		{name: "working", endpoint: working.URL, client: working.Client()},
	}}
	addresses, err := resolver.LookupNetIP(context.Background(), "api.github.com")
	require.NoError(t, err)
	require.Equal(t, []netip.Addr{netip.MustParseAddr("8.8.8.8")}, addresses)
}

func TestParseDNSResponseRejectsMismatchedQuery(t *testing.T) {
	payload, err := buildDNSResponse(7, netip.MustParseAddr("8.8.8.8"))
	require.NoError(t, err)
	_, err = parseDNSResponse(payload, 8)
	require.ErrorContains(t, err, "does not match")
}

func TestTrustedResolverLive(t *testing.T) {
	if os.Getenv("WEKNORA_PLUGIN_DOH_LIVE_TEST") != "1" {
		t.Skip("set WEKNORA_PLUGIN_DOH_LIVE_TEST=1 to run the external DoH check")
	}
	addresses, err := newTrustedResolver().LookupNetIP(context.Background(), "api.github.com")
	require.NoError(t, err)
	require.NotEmpty(t, addresses)
	for _, address := range addresses {
		require.True(t, publicAddr(address), "resolver returned non-public address %s", address)
	}
}

func newDoHTestServer(t *testing.T, address netip.Addr) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read DoH request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		var parser dnsmessage.Parser
		header, err := parser.Start(body)
		if err != nil {
			t.Errorf("parse DoH request: %v", err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		questions, err := parser.AllQuestions()
		if err != nil || len(questions) != 1 {
			t.Errorf("parse DoH questions: count=%d err=%v", len(questions), err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		payload, err := buildDNSResponse(header.ID, address)
		if err != nil {
			t.Errorf("build DoH response: %v", err)
			http.Error(writer, "response failure", http.StatusInternalServerError)
			return
		}

		writer.Header().Set("Content-Type", "application/dns-message")
		if _, err := io.Copy(writer, bytes.NewReader(payload)); err != nil {
			t.Errorf("write DoH response: %v", err)
		}
	}))
}

func buildDNSResponse(queryID uint16, address netip.Addr) ([]byte, error) {
	name := dnsmessage.MustNewName("api.github.com.")
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: queryID, Response: true, RecursionAvailable: true,
	})
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{
		Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	if err := builder.AResource(dnsmessage.ResourceHeader{
		Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60,
	}, dnsmessage.AResource{A: address.As4()}); err != nil {
		return nil, err
	}
	return builder.Finish()
}
