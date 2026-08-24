package pluginruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	maxDNSMessageBytes = 64 * 1024
	dohRequestTimeout  = 10 * time.Second
)

// hostResolver resolves plugin destinations independently from the host DNS.
// The returned address is still validated and pinned by the egress dialer.
type hostResolver interface {
	LookupNetIP(ctx context.Context, host string) ([]netip.Addr, error)
}

type dohResolver struct {
	providers []dohProvider
}

type dohProvider struct {
	name     string
	endpoint string
	client   *http.Client
}

type dohProviderConfig struct {
	name          string
	endpoint      string
	serverName    string
	bootstrapAddr string
}

// newTrustedResolver uses HTTPS with fixed public bootstrap addresses so
// system-wide Fake-IP DNS cannot affect plugin egress validation.
func newTrustedResolver() hostResolver {
	configs := []dohProviderConfig{
		{
			name:          "alidns",
			endpoint:      "https://dns.alidns.com/dns-query",
			serverName:    "dns.alidns.com",
			bootstrapAddr: "223.5.5.5:443",
		},
		{
			name:          "cloudflare",
			endpoint:      "https://cloudflare-dns.com/dns-query",
			serverName:    "cloudflare-dns.com",
			bootstrapAddr: "1.1.1.1:443",
		},
		{
			name:          "google",
			endpoint:      "https://dns.google/dns-query",
			serverName:    "dns.google",
			bootstrapAddr: "8.8.8.8:443",
		},
	}
	providers := make([]dohProvider, 0, len(configs))
	for _, config := range configs {
		providers = append(providers, newDoHProvider(config))
	}
	return &dohResolver{providers: providers}
}

func newDoHProvider(config dohProviderConfig) dohProvider {
	transport := &http.Transport{
		Proxy:             nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: config.serverName,
		},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", config.bootstrapAddr)
		},
	}
	return dohProvider{
		name:     config.name,
		endpoint: config.endpoint,
		client: &http.Client{
			Timeout:   dohRequestTimeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (r *dohResolver) LookupNetIP(ctx context.Context, host string) ([]netip.Addr, error) {
	host = normalizeHost(host)
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	if host == "" {
		return nil, errors.New("DNS host is empty")
	}

	var lookupErrors []error
	for _, provider := range r.providers {
		addresses, err := provider.lookup(ctx, host)
		if err == nil && len(addresses) > 0 {
			return addresses, nil
		}
		if err == nil {
			err = errors.New("response contained no IP addresses")
		}
		lookupErrors = append(lookupErrors, fmt.Errorf("%s: %w", provider.name, err))
	}
	return nil, fmt.Errorf("trusted DNS lookup for %s failed: %w", host, errors.Join(lookupErrors...))
}

func (p dohProvider) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses, err := p.query(ctx, host, dnsmessage.TypeA)
	if err != nil {
		return nil, err
	}
	if len(addresses) > 0 {
		return addresses, nil
	}
	return p.query(ctx, host, dnsmessage.TypeAAAA)
}

func (p dohProvider) query(ctx context.Context, host string, queryType dnsmessage.Type) ([]netip.Addr, error) {
	payload, queryID, err := buildDNSQuery(host, queryType)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/dns-message")
	request.Header.Set("Content-Type", "application/dns-message")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("DoH returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDNSMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDNSMessageBytes {
		return nil, errors.New("DoH response exceeds size limit")
	}
	return parseDNSResponse(body, queryID)
}

func buildDNSQuery(host string, queryType dnsmessage.Type) ([]byte, uint16, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return nil, 0, err
	}
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, 0, err
	}
	id := binary.BigEndian.Uint16(idBytes[:])
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := builder.StartQuestions(); err != nil {
		return nil, 0, err
	}
	if err := builder.Question(dnsmessage.Question{
		Name: name, Type: queryType, Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, 0, err
	}
	payload, err := builder.Finish()
	return payload, id, err
}

func parseDNSResponse(payload []byte, queryID uint16) ([]netip.Addr, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(payload)
	if err != nil {
		return nil, err
	}
	if !header.Response || header.ID != queryID {
		return nil, errors.New("DoH response does not match query")
	}
	if header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("DoH response code %s", header.RCode)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return nil, err
	}
	answers, err := parser.AllAnswers()
	if err != nil {
		return nil, err
	}
	addresses := make([]netip.Addr, 0, len(answers))
	seen := make(map[netip.Addr]struct{}, len(answers))
	for _, answer := range answers {
		var address netip.Addr
		switch resource := answer.Body.(type) {
		case *dnsmessage.AResource:
			address = netip.AddrFrom4(resource.A)
		case *dnsmessage.AAAAResource:
			address = netip.AddrFrom16(resource.AAAA).Unmap()
		default:
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	return addresses, nil
}
