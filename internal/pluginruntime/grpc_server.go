package pluginruntime

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// GRPCServer exposes lifecycle control and capability proxies. The embedded
// unimplemented services keep the runtime forward-compatible when the public
// protocol adds methods.
type GRPCServer struct {
	pluginv1.UnimplementedPluginRuntimeServer
	pluginv1.UnimplementedPluginLifecycleServer
	pluginv1.UnimplementedDataSourcePluginServer
	pluginv1.UnimplementedDocumentParserPluginServer
	pluginv1.UnimplementedWebSearchPluginServer
	pluginv1.UnimplementedModelProviderPluginServer

	manager *Manager
	events  *eventBus
}

func NewGRPCServer(manager *Manager, events *eventBus) *GRPCServer {
	return &GRPCServer{manager: manager, events: events}
}

func (s *GRPCServer) Register(server *grpc.Server) {
	pluginv1.RegisterPluginRuntimeServer(server, s)
	pluginv1.RegisterPluginLifecycleServer(server, s)
	pluginv1.RegisterDataSourcePluginServer(server, s)
	pluginv1.RegisterDocumentParserPluginServer(server, s)
	pluginv1.RegisterWebSearchPluginServer(server, s)
	pluginv1.RegisterModelProviderPluginServer(server, s)
}

func (s *GRPCServer) Install(
	ctx context.Context,
	req *pluginv1.InstallPluginRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	result, err := s.manager.Install(ctx, req)
	return result, mapRuntimeError(err)
}

func (s *GRPCServer) Upgrade(
	ctx context.Context,
	req *pluginv1.UpgradePluginRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	result, err := s.manager.Upgrade(ctx, req)
	return result, mapRuntimeError(err)
}

func (s *GRPCServer) Start(
	ctx context.Context,
	req *pluginv1.PluginTargetRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	result, err := s.manager.Start(ctx, req.GetPluginId())
	return result, mapRuntimeError(err)
}

func (s *GRPCServer) Stop(
	ctx context.Context,
	req *pluginv1.PluginTargetRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	result, err := s.manager.Stop(ctx, req.GetPluginId())
	return result, mapRuntimeError(err)
}

func (s *GRPCServer) Uninstall(
	ctx context.Context,
	req *pluginv1.PluginTargetRequest,
) (*emptypb.Empty, error) {
	if err := s.manager.Uninstall(ctx, req.GetPluginId()); err != nil {
		return nil, mapRuntimeError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *GRPCServer) GetStatus(
	_ context.Context,
	req *pluginv1.PluginTargetRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	result, err := s.manager.Status(req.GetPluginId())
	return result, mapRuntimeError(err)
}

func (s *GRPCServer) ListStatuses(
	context.Context,
	*emptypb.Empty,
) (*pluginv1.ListPluginStatusesResponse, error) {
	return &pluginv1.ListPluginStatusesResponse{Statuses: s.manager.Statuses()}, nil
}

func (s *GRPCServer) ListEvents(
	_ context.Context,
	req *pluginv1.ListRuntimeEventsRequest,
) (*pluginv1.ListRuntimeEventsResponse, error) {
	return &pluginv1.ListRuntimeEventsResponse{
		Events: s.events.list(req.GetPluginId(), req.GetAfterSequence(), req.GetLimit()),
	}, nil
}

func (s *GRPCServer) WatchEvents(
	req *pluginv1.WatchRuntimeEventsRequest,
	stream grpc.ServerStreamingServer[pluginv1.RuntimeEvent],
) error {
	backlog, subscriptionID, events := s.events.subscribe(req.GetAfterSequence())
	defer s.events.unsubscribe(subscriptionID)
	for _, event := range backlog {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(event); err != nil {
				return err
			}
		}
	}
}

func (s *GRPCServer) Handshake(
	ctx context.Context,
	req *pluginv1.HandshakeRequest,
) (*pluginv1.HandshakeResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewPluginLifecycleClient(connection).Handshake(callCtx, req)
	s.publishCall(req.GetContext(), "lifecycle", "Handshake", startedAt, err)
	return response, err
}

func (s *GRPCServer) HealthCheck(
	ctx context.Context,
	req *pluginv1.HealthCheckRequest,
) (*pluginv1.HealthCheckResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewPluginLifecycleClient(connection).HealthCheck(callCtx, req)
	s.publishCall(req.GetContext(), "lifecycle", "HealthCheck", startedAt, err)
	return response, err
}

func (s *GRPCServer) ValidateConfig(
	ctx context.Context,
	req *pluginv1.ValidateConfigRequest,
) (*pluginv1.ValidateConfigResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewPluginLifecycleClient(connection).ValidateConfig(callCtx, req)
	s.publishCall(req.GetContext(), "lifecycle", "ValidateConfig", startedAt, err)
	return response, err
}

func (s *GRPCServer) ListResources(
	ctx context.Context,
	req *pluginv1.ListResourcesRequest,
) (*pluginv1.ListResourcesResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(item, req.GetContext(), "data_source", "ListResources")
	if err != nil {
		return nil, err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewDataSourcePluginClient(connection).ListResources(callCtx, req)
	s.publishCall(req.GetContext(), "data_source", "ListResources", startedAt, err)
	return response, err
}

func (s *GRPCServer) ResolveResourceAncestors(
	ctx context.Context,
	req *pluginv1.ResolveResourceAncestorsRequest,
) (*pluginv1.ResolveResourceAncestorsResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(item, req.GetContext(), "data_source", "ResolveResourceAncestors")
	if err != nil {
		return nil, err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewDataSourcePluginClient(connection).ResolveResourceAncestors(callCtx, req)
	s.publishCall(req.GetContext(), "data_source", "ResolveResourceAncestors", startedAt, err)
	return response, err
}

func (s *GRPCServer) Sync(
	req *pluginv1.DataSourceSyncRequest,
	stream grpc.ServerStreamingServer[pluginv1.DataSourceSyncEvent],
) error {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(item, req.GetContext(), "data_source", "Sync")
	if err != nil {
		return err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(stream.Context(), item)
	defer cancel()
	downstream, err := pluginv1.NewDataSourcePluginClient(connection).Sync(callCtx, req)
	if err != nil {
		s.publishCall(req.GetContext(), "data_source", "Sync", startedAt, err)
		return err
	}
	err = forwardServerStream(downstream.Recv, stream.Send)
	s.publishCall(req.GetContext(), "data_source", "Sync", startedAt, err)
	return err
}

func (s *GRPCServer) Parse(stream grpc.BidiStreamingServer[pluginv1.ParseRequest, pluginv1.ParseEvent]) error {
	startedAt := time.Now()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	connection, item, err := s.target(first.GetContext())
	if err != nil {
		return mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(
		item,
		first.GetContext(),
		"document_parser",
		"Parse",
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT,
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_METADATA,
	)
	if err != nil {
		return err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(stream.Context(), item)
	defer cancel()
	downstream, err := pluginv1.NewDocumentParserPluginClient(connection).Parse(callCtx)
	if err != nil {
		s.publishCall(first.GetContext(), "document_parser", "Parse", startedAt, err)
		return err
	}
	if err := downstream.Send(first); err != nil {
		return err
	}

	requestErrors := make(chan error, 1)
	responseErrors := make(chan error, 1)
	go func() {
		for {
			request, recvErr := stream.Recv()
			if recvErr == io.EOF {
				requestErrors <- downstream.CloseSend()
				return
			}
			if recvErr != nil {
				requestErrors <- recvErr
				return
			}
			if sendErr := downstream.Send(request); sendErr != nil {
				requestErrors <- sendErr
				return
			}
		}
	}()
	go func() {
		responseErrors <- forwardServerStream(downstream.Recv, stream.Send)
	}()

	select {
	case requestErr := <-requestErrors:
		if requestErr != nil {
			err = normalizeStreamError(requestErr)
			s.publishCall(first.GetContext(), "document_parser", "Parse", startedAt, err)
			return err
		}
		err = normalizeStreamError(<-responseErrors)
	case responseErr := <-responseErrors:
		err = normalizeStreamError(responseErr)
	}
	s.publishCall(first.GetContext(), "document_parser", "Parse", startedAt, err)
	return err
}

func (s *GRPCServer) Search(
	ctx context.Context,
	req *pluginv1.WebSearchRequest,
) (*pluginv1.WebSearchResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(
		item,
		req.GetContext(),
		"web_search",
		"Search",
		pluginv1.DataAccess_DATA_ACCESS_QUERY_TEXT,
	)
	if err != nil {
		return nil, err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewWebSearchPluginClient(connection).Search(callCtx, req)
	s.publishCall(req.GetContext(), "web_search", "Search", startedAt, err)
	return response, err
}

func (s *GRPCServer) ListModels(
	ctx context.Context,
	req *pluginv1.ListModelsRequest,
) (*pluginv1.ListModelsResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(item, req.GetContext(), "model_provider", "ListModels")
	if err != nil {
		return nil, err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewModelProviderPluginClient(connection).ListModels(callCtx, req)
	s.publishCall(req.GetContext(), "model_provider", "ListModels", startedAt, err)
	return response, err
}

func (s *GRPCServer) Chat(
	req *pluginv1.ChatRequest,
	stream grpc.ServerStreamingServer[pluginv1.ChatEvent],
) error {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(
		item,
		req.GetContext(),
		"model_provider",
		"Chat",
		pluginv1.DataAccess_DATA_ACCESS_CONVERSATION,
	)
	if err != nil {
		return err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(stream.Context(), item)
	defer cancel()
	downstream, err := pluginv1.NewModelProviderPluginClient(connection).Chat(callCtx, req)
	if err != nil {
		s.publishCall(req.GetContext(), "model_provider", "Chat", startedAt, err)
		return err
	}
	err = forwardServerStream(downstream.Recv, stream.Send)
	s.publishCall(req.GetContext(), "model_provider", "Chat", startedAt, err)
	return err
}

func (s *GRPCServer) Embed(
	ctx context.Context,
	req *pluginv1.EmbedRequest,
) (*pluginv1.EmbedResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(
		item,
		req.GetContext(),
		"model_provider",
		"Embed",
		pluginv1.DataAccess_DATA_ACCESS_QUERY_TEXT,
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT,
	)
	if err != nil {
		return nil, err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewModelProviderPluginClient(connection).Embed(callCtx, req)
	s.publishCall(req.GetContext(), "model_provider", "Embed", startedAt, err)
	return response, err
}

func (s *GRPCServer) Rerank(
	ctx context.Context,
	req *pluginv1.RerankRequest,
) (*pluginv1.RerankResponse, error) {
	startedAt := time.Now()
	connection, item, err := s.target(req.GetContext())
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	release, err := s.authorizeCapability(
		item,
		req.GetContext(),
		"model_provider",
		"Rerank",
		pluginv1.DataAccess_DATA_ACCESS_QUERY_TEXT,
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT,
	)
	if err != nil {
		return nil, err
	}
	defer release()
	callCtx, cancel := withPluginTimeout(ctx, item)
	defer cancel()
	response, err := pluginv1.NewModelProviderPluginClient(connection).Rerank(callCtx, req)
	s.publishCall(req.GetContext(), "model_provider", "Rerank", startedAt, err)
	return response, err
}

func (s *GRPCServer) publishCall(
	invocation *pluginv1.InvocationContext,
	capability string,
	method string,
	startedAt time.Time,
	err error,
) {
	if invocation == nil || invocation.GetPluginId() == "" {
		return
	}
	details := map[string]string{
		"capability":  capability,
		"method":      method,
		"duration_ms": strconv.FormatInt(time.Since(startedAt).Milliseconds(), 10),
	}
	kind := "call_succeeded"
	message := capability + "." + method + " completed"
	if err != nil {
		kind = "call_failed"
		message = capability + "." + method + " failed"
		details["code"] = status.Code(err).String()
	}
	s.events.publish(invocation.GetPluginId(), kind, message, details)
}

func (s *GRPCServer) target(invocation *pluginv1.InvocationContext) (*grpc.ClientConn, *installation, error) {
	if invocation == nil || invocation.GetPluginId() == "" {
		return nil, nil, status.Error(codes.InvalidArgument, "context.plugin_id is required")
	}
	return s.manager.pluginClient(invocation.GetPluginId())
}

func (s *GRPCServer) authorizeCapability(
	item *installation,
	invocation *pluginv1.InvocationContext,
	capability, method string,
	required ...pluginv1.DataAccess,
) (func(), error) {
	if missing, ok := manifestHasDataAccess(item, required...); !ok {
		access := dataAccessName(missing)
		if s.events != nil {
			s.events.publish(item.Manifest.GetId(), "data_access_denied", "plugin data access denied", map[string]string{
				"capability": capability,
				"method":     method,
				"required":   access,
				"request_id": invocation.GetRequestId(),
			})
		}
		return nil, status.Errorf(codes.PermissionDenied, "plugin has not declared data access %s", access)
	}
	return s.manager.beginCapabilityCall(item, capability, method)
}

func dataAccessName(value pluginv1.DataAccess) string {
	switch value {
	case pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT:
		return "document_content"
	case pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_METADATA:
		return "document_metadata"
	case pluginv1.DataAccess_DATA_ACCESS_QUERY_TEXT:
		return "query_text"
	case pluginv1.DataAccess_DATA_ACCESS_CONVERSATION:
		return "conversation"
	default:
		return "unspecified"
	}
}

func withPluginTimeout(ctx context.Context, item *installation) (context.Context, context.CancelFunc) {
	timeout := item.CallTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return context.WithTimeout(ctx, timeout)
}

func forwardServerStream[T any](recv func() (*T, error), send func(*T) error) error {
	for {
		message, err := recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := send(message); err != nil {
			return err
		}
	}
}

func normalizeStreamError(err error) error {
	if err == nil || err == io.EOF || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func mapRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, ErrRuntimePluginNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrRuntimePluginAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrInvalidRuntimePlugin):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
