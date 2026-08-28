package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// LifecycleHooks lets a plugin add live config validation and health details.
// All methods are optional; the SDK supplies safe defaults when hooks are nil.
type LifecycleHooks interface {
	ValidateConfig(ctx context.Context, config json.RawMessage) []FieldViolation
	HealthCheck(ctx context.Context) Health
}

// FieldViolation identifies one invalid configuration path.
type FieldViolation struct {
	Field       string
	Description string
}

// Health is the plugin-authored portion of a health response.
type Health struct {
	Status  pluginv1.HealthCheckResponse_Status
	Message string
	Details map[string]string
}

// ServerOptions contains optional capability implementations. Additional
// capability adapters can be added without changing the lifecycle contract.
type ServerOptions struct {
	Lifecycle      LifecycleHooks
	DataSource     DataSourceHandler
	DocumentParser DocumentParserHandler
	WebSearch      WebSearchHandler
	ModelProvider  ModelProviderHandler
	GRPC           []grpc.ServerOption
}

// Server hosts one plugin manifest and its capability implementations.
type Server struct {
	pluginv1.UnimplementedPluginLifecycleServer
	pluginv1.UnimplementedDataSourcePluginServer
	pluginv1.UnimplementedDocumentParserPluginServer
	pluginv1.UnimplementedWebSearchPluginServer
	pluginv1.UnimplementedModelProviderPluginServer

	manifest       *Manifest
	hooks          LifecycleHooks
	dataSource     DataSourceHandler
	documentParser DocumentParserHandler
	webSearch      WebSearchHandler
	modelProvider  ModelProviderHandler
	grpcServer     *grpc.Server
	stopOnce       sync.Once
}

// NewServer validates the manifest before opening any listener.
func NewServer(manifest *Manifest, options ServerOptions) (*Server, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if containsType(manifest.NormalizedTypes(), "data_source") && options.DataSource == nil {
		return nil, errors.New("manifest declares data_source but no DataSource handler was provided")
	}
	if containsType(manifest.NormalizedTypes(), "document_parser") && options.DocumentParser == nil {
		return nil, errors.New("manifest declares document_parser but no DocumentParser handler was provided")
	}
	if containsType(manifest.NormalizedTypes(), "web_search") && options.WebSearch == nil {
		return nil, errors.New("manifest declares web_search but no WebSearch handler was provided")
	}
	if containsType(manifest.NormalizedTypes(), "model_provider") && options.ModelProvider == nil {
		return nil, errors.New("manifest declares model_provider but no ModelProvider handler was provided")
	}
	return &Server{
		manifest:       manifest,
		hooks:          options.Lifecycle,
		dataSource:     options.DataSource,
		documentParser: options.DocumentParser,
		webSearch:      options.WebSearch,
		modelProvider:  options.ModelProvider,
		grpcServer:     grpc.NewServer(options.GRPC...),
	}, nil
}

// Serve listens until the context is canceled or the server fails. Shutdown is
// graceful so an in-flight sync can observe context cancellation and checkpoint.
func (s *Server) Serve(ctx context.Context, address string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("plugin listen address is required")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}

	pluginv1.RegisterPluginLifecycleServer(s.grpcServer, s)
	if s.dataSource != nil {
		pluginv1.RegisterDataSourcePluginServer(s.grpcServer, s)
	}
	if s.documentParser != nil {
		pluginv1.RegisterDocumentParserPluginServer(s.grpcServer, s)
	}
	if s.webSearch != nil {
		pluginv1.RegisterWebSearchPluginServer(s.grpcServer, s)
	}
	if s.modelProvider != nil {
		pluginv1.RegisterModelProviderPluginServer(s.grpcServer, s)
	}
	reflection.Register(s.grpcServer)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.grpcServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		s.Stop()
		return nil
	case err := <-serveErr:
		if err == nil || errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return fmt.Errorf("serve plugin grpc: %w", err)
	}
}

// Stop gracefully stops the plugin server once.
func (s *Server) Stop() {
	s.stopOnce.Do(func() {
		s.grpcServer.GracefulStop()
	})
}

// Handshake verifies protocol compatibility and returns the authoritative
// manifest compiled into the running plugin image.
func (s *Server) Handshake(
	_ context.Context,
	req *pluginv1.HandshakeRequest,
) (*pluginv1.HandshakeResponse, error) {
	if req == nil || strings.TrimSpace(req.HostProtocolVersion) == "" {
		return nil, status.Error(codes.InvalidArgument, "host protocol version is required")
	}
	negotiated, err := NegotiateProtocol(req.HostProtocolVersion, s.manifest.Spec.ProtocolVersion)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	manifest, err := s.manifest.ToProto()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &pluginv1.HandshakeResponse{
		Manifest:                  manifest,
		NegotiatedProtocolVersion: negotiated,
	}, nil
}

// HealthCheck returns serving by default. Plugins should only provide a hook
// when they have a meaningful dependency to probe.
func (s *Server) HealthCheck(
	ctx context.Context,
	_ *pluginv1.HealthCheckRequest,
) (*pluginv1.HealthCheckResponse, error) {
	health := Health{
		Status:  pluginv1.HealthCheckResponse_STATUS_SERVING,
		Message: "plugin is ready",
	}
	if s.hooks != nil {
		health = s.hooks.HealthCheck(ctx)
		if health.Status == pluginv1.HealthCheckResponse_STATUS_UNSPECIFIED {
			health.Status = pluginv1.HealthCheckResponse_STATUS_NOT_SERVING
		}
	}
	return &pluginv1.HealthCheckResponse{
		Status:  health.Status,
		Message: health.Message,
		Details: health.Details,
	}, nil
}

// ValidateConfig always rejects malformed JSON before calling plugin code.
func (s *Server) ValidateConfig(
	ctx context.Context,
	req *pluginv1.ValidateConfigRequest,
) (*pluginv1.ValidateConfigResponse, error) {
	if req == nil || len(req.ConfigJson) == 0 || !json.Valid(req.ConfigJson) {
		return nil, status.Error(codes.InvalidArgument, "config_json must be valid JSON")
	}
	var values map[string]any
	if err := json.Unmarshal(req.ConfigJson, &values); err != nil || values == nil {
		return nil, status.Error(codes.InvalidArgument, "config_json must be a JSON object")
	}
	if err := ValidateConfigValues(s.manifest.Spec.Config.Schema, values); err != nil {
		return &pluginv1.ValidateConfigResponse{
			Valid: false,
			Violations: []*pluginv1.FieldViolation{{
				Field:       "$",
				Description: err.Error(),
			}},
		}, nil
	}
	if s.hooks == nil {
		return &pluginv1.ValidateConfigResponse{Valid: true}, nil
	}
	violations := s.hooks.ValidateConfig(ctx, json.RawMessage(req.ConfigJson))
	result := &pluginv1.ValidateConfigResponse{Valid: len(violations) == 0}
	for _, violation := range violations {
		result.Violations = append(result.Violations, &pluginv1.FieldViolation{
			Field:       violation.Field,
			Description: violation.Description,
		})
	}
	return result, nil
}

func containsType(types []string, expected string) bool {
	for _, value := range types {
		if value == expected {
			return true
		}
	}
	return false
}
