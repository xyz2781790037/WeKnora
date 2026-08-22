// Package runtimeclient provides the only WeKnora-side transport to the
// isolated plugin-runtime service.
package runtimeclient

import (
	"context"
	"errors"
	"strings"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const maxPluginControlMessageSize = 8 * 1024 * 1024

var ErrDisabled = errors.New("external plugin runtime is disabled")

// Gateway is the host-side runtime boundary used by application services and
// capability adapters. Client is its production gRPC implementation.
type Gateway interface {
	Enabled() bool
	Runtime() (pluginv1.PluginRuntimeClient, error)
	Lifecycle() (pluginv1.PluginLifecycleClient, error)
	DataSource() (pluginv1.DataSourcePluginClient, error)
	Parser() (pluginv1.DocumentParserPluginClient, error)
	Search() (pluginv1.WebSearchPluginClient, error)
	Model() (pluginv1.ModelProviderPluginClient, error)
}

// Client exposes generated capability clients while keeping connection and
// authentication details in one place.
type Client struct {
	enabled    bool
	conn       *grpc.ClientConn
	runtime    pluginv1.PluginRuntimeClient
	lifecycle  pluginv1.PluginLifecycleClient
	datasource pluginv1.DataSourcePluginClient
	parser     pluginv1.DocumentParserPluginClient
	search     pluginv1.WebSearchPluginClient
	model      pluginv1.ModelProviderPluginClient
}

// New creates a lazy gRPC channel. It does not require plugin-runtime to be
// reachable during WeKnora startup.
func New(cfg *config.Config) (*Client, error) {
	if cfg == nil || cfg.PluginRuntime == nil || !cfg.PluginRuntime.Enabled {
		return &Client{}, nil
	}
	address := strings.TrimSpace(cfg.PluginRuntime.Addr)
	if address == "" {
		return nil, errors.New("plugin runtime address is required when enabled")
	}
	if strings.TrimSpace(cfg.PluginRuntime.AuthToken) == "" {
		return nil, errors.New("plugin runtime auth token is required when enabled")
	}

	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerToken(cfg.PluginRuntime.AuthToken)),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxPluginControlMessageSize),
			grpc.MaxCallSendMsgSize(maxPluginControlMessageSize),
		),
	}
	conn, err := grpc.NewClient(address, options...)
	if err != nil {
		return nil, err
	}
	return &Client{
		enabled:    true,
		conn:       conn,
		runtime:    pluginv1.NewPluginRuntimeClient(conn),
		lifecycle:  pluginv1.NewPluginLifecycleClient(conn),
		datasource: pluginv1.NewDataSourcePluginClient(conn),
		parser:     pluginv1.NewDocumentParserPluginClient(conn),
		search:     pluginv1.NewWebSearchPluginClient(conn),
		model:      pluginv1.NewModelProviderPluginClient(conn),
	}, nil
}

func (c *Client) Enabled() bool { return c != nil && c.enabled }

func (c *Client) Runtime() (pluginv1.PluginRuntimeClient, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.runtime, nil
}

func (c *Client) Lifecycle() (pluginv1.PluginLifecycleClient, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.lifecycle, nil
}

func (c *Client) DataSource() (pluginv1.DataSourcePluginClient, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.datasource, nil
}

func (c *Client) Parser() (pluginv1.DocumentParserPluginClient, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.parser, nil
}

func (c *Client) Search() (pluginv1.WebSearchPluginClient, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.search, nil
}

func (c *Client) Model() (pluginv1.ModelProviderPluginClient, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	return c.model, nil
}

func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

type bearerToken string

func (t bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(t)}, nil
}

func (bearerToken) RequireTransportSecurity() bool { return false }
