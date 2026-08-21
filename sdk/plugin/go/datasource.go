package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const defaultDocumentChunkSize = 512 * 1024

// Invocation identifies one host call without exposing host credentials or
// storage services to the plugin.
type Invocation struct {
	PluginID   string
	TenantID   uint64
	InstanceID string
	RequestID  string
}

// Resource is one selectable source object such as a repository or folder.
type Resource struct {
	ExternalID  string
	Name        string
	Type        string
	Description string
	URL         string
	ModifiedAt  time.Time
	ParentID    string
	HasChildren bool
	Metadata    map[string]string
}

// DataSourceSyncInput contains plugin-owned configuration and cursor state.
type DataSourceSyncInput struct {
	Invocation  Invocation
	Config      json.RawMessage
	ResourceIDs []string
	Cursor      json.RawMessage
	Full        bool
}

// Document is a source-neutral raw file. WeKnora remains responsible for
// parsing, chunking and indexing its Content.
type Document struct {
	ExternalID       string
	Title            string
	FileName         string
	ContentType      string
	URL              string
	UpdatedAt        time.Time
	Metadata         map[string]string
	SourceResourceID string
	Content          []byte
}

// DeletedDocument identifies a source object removed since the last cursor.
type DeletedDocument struct {
	ExternalID       string
	Title            string
	SourceResourceID string
	Metadata         map[string]string
}

// DataSourceEmitter is the transport-neutral event sink passed to source
// plugins. Tests can provide an in-memory implementation while production uses
// SyncEmitter backed by a gRPC stream.
type DataSourceEmitter interface {
	EmitDocument(document Document) error
	EmitDelete(document DeletedDocument) error
	Checkpoint(cursor json.RawMessage) error
	Warn(code, message, externalID string) error
}

// DataSourceHandler is the small interface implemented by data source plugins.
// Sync should emit complete checkpoints only after all preceding events are
// safe to replay.
type DataSourceHandler interface {
	ListResources(ctx context.Context, invocation Invocation, config json.RawMessage, parentID string) ([]Resource, error)
	ResolveResourceAncestors(
		ctx context.Context,
		invocation Invocation,
		config json.RawMessage,
		resourceIDs []string,
	) ([]string, error)
	Sync(ctx context.Context, input DataSourceSyncInput, emitter DataSourceEmitter) error
}

// SyncEmitter serializes document frames. It is safe to call from multiple
// goroutines, although plugins should preserve source order when cursor
// checkpoints depend on it.
type SyncEmitter struct {
	stream    pluginv1.DataSourcePlugin_SyncServer
	chunkSize int
	mu        sync.Mutex
}

func newSyncEmitter(stream pluginv1.DataSourcePlugin_SyncServer) *SyncEmitter {
	return &SyncEmitter{stream: stream, chunkSize: defaultDocumentChunkSize}
}

// EmitDocument transfers one document as begin -> chunk* -> end while holding
// the emitter lock, preventing interleaved frames from concurrent producers.
func (e *SyncEmitter) EmitDocument(document Document) error {
	if document.ExternalID == "" {
		return errors.New("document external id is required")
	}
	if document.FileName == "" {
		return errors.New("document file name is required")
	}
	if len(document.Content) == 0 {
		return errors.New("document content is empty")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	begin := &pluginv1.DocumentBegin{
		ExternalId:       document.ExternalID,
		Title:            document.Title,
		FileName:         document.FileName,
		ContentType:      document.ContentType,
		Url:              document.URL,
		Metadata:         document.Metadata,
		SourceResourceId: document.SourceResourceID,
		ContentSize:      int64(len(document.Content)),
	}
	if !document.UpdatedAt.IsZero() {
		begin.UpdatedAt = timestamppb.New(document.UpdatedAt)
	}
	if err := e.send(&pluginv1.DataSourceSyncEvent{
		Event: &pluginv1.DataSourceSyncEvent_DocumentBegin{DocumentBegin: begin},
	}); err != nil {
		return err
	}

	sequence := uint32(0)
	for offset := 0; offset < len(document.Content); offset += e.chunkSize {
		end := offset + e.chunkSize
		if end > len(document.Content) {
			end = len(document.Content)
		}
		chunk := append([]byte(nil), document.Content[offset:end]...)
		if err := e.send(&pluginv1.DataSourceSyncEvent{
			Event: &pluginv1.DataSourceSyncEvent_DocumentChunk{DocumentChunk: &pluginv1.DocumentChunk{
				ExternalId: document.ExternalID,
				Sequence:   sequence,
				Data:       chunk,
			}},
		}); err != nil {
			return err
		}
		sequence++
	}

	digest := sha256.Sum256(document.Content)
	return e.send(&pluginv1.DataSourceSyncEvent{
		Event: &pluginv1.DataSourceSyncEvent_DocumentEnd{DocumentEnd: &pluginv1.DocumentEnd{
			ExternalId: document.ExternalID,
			Sha256:     hex.EncodeToString(digest[:]),
		}},
	})
}

// EmitDelete reports one source deletion.
func (e *SyncEmitter) EmitDelete(document DeletedDocument) error {
	if document.ExternalID == "" {
		return errors.New("deleted document external id is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.send(&pluginv1.DataSourceSyncEvent{
		Event: &pluginv1.DataSourceSyncEvent_DeleteDocument{DeleteDocument: &pluginv1.DeleteDocument{
			ExternalId:       document.ExternalID,
			Title:            document.Title,
			SourceResourceId: document.SourceResourceID,
			Metadata:         document.Metadata,
		}},
	})
}

// Checkpoint emits an opaque, complete, resumable cursor snapshot.
func (e *SyncEmitter) Checkpoint(cursor json.RawMessage) error {
	if len(cursor) == 0 || !json.Valid(cursor) {
		return errors.New("checkpoint cursor must be valid JSON")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.send(&pluginv1.DataSourceSyncEvent{
		Event: &pluginv1.DataSourceSyncEvent_Checkpoint{Checkpoint: &pluginv1.CursorCheckpoint{
			CursorJson: append([]byte(nil), cursor...),
		}},
	})
}

// Warn reports a recoverable source problem without failing the entire sync.
func (e *SyncEmitter) Warn(code, message, externalID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.send(&pluginv1.DataSourceSyncEvent{
		Event: &pluginv1.DataSourceSyncEvent_Warning{Warning: &pluginv1.SyncWarning{
			Code:       code,
			Message:    message,
			ExternalId: externalID,
		}},
	})
}

func (e *SyncEmitter) send(event *pluginv1.DataSourceSyncEvent) error {
	if err := e.stream.Context().Err(); err != nil {
		return err
	}
	return e.stream.Send(event)
}

// ListResources adapts the public SDK handler to the generated gRPC service.
func (s *Server) ListResources(
	ctx context.Context,
	req *pluginv1.ListResourcesRequest,
) (*pluginv1.ListResourcesResponse, error) {
	if s.dataSource == nil {
		return nil, status.Error(codes.Unimplemented, "data source capability is not enabled")
	}
	if err := validateJSONConfig(req.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	resources, err := s.dataSource.ListResources(
		ctx,
		invocationFromProto(req.GetContext()),
		json.RawMessage(req.GetConfigJson()),
		req.GetParentId(),
	)
	if err != nil {
		return nil, capabilityError(err)
	}
	response := &pluginv1.ListResourcesResponse{Resources: make([]*pluginv1.Resource, 0, len(resources))}
	for _, resource := range resources {
		converted := &pluginv1.Resource{
			ExternalId:  resource.ExternalID,
			Name:        resource.Name,
			Type:        resource.Type,
			Description: resource.Description,
			Url:         resource.URL,
			ParentId:    resource.ParentID,
			HasChildren: resource.HasChildren,
			Metadata:    resource.Metadata,
		}
		if !resource.ModifiedAt.IsZero() {
			converted.ModifiedAt = timestamppb.New(resource.ModifiedAt)
		}
		response.Resources = append(response.Resources, converted)
	}
	return response, nil
}

// ResolveResourceAncestors adapts the optional hierarchy lookup.
func (s *Server) ResolveResourceAncestors(
	ctx context.Context,
	req *pluginv1.ResolveResourceAncestorsRequest,
) (*pluginv1.ResolveResourceAncestorsResponse, error) {
	if s.dataSource == nil {
		return nil, status.Error(codes.Unimplemented, "data source capability is not enabled")
	}
	if err := validateJSONConfig(req.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ancestors, err := s.dataSource.ResolveResourceAncestors(
		ctx,
		invocationFromProto(req.GetContext()),
		json.RawMessage(req.GetConfigJson()),
		append([]string(nil), req.GetResourceIds()...),
	)
	if err != nil {
		return nil, capabilityError(err)
	}
	return &pluginv1.ResolveResourceAncestorsResponse{AncestorIds: ancestors}, nil
}

// Sync streams source events to the caller without buffering a whole sync.
func (s *Server) Sync(
	req *pluginv1.DataSourceSyncRequest,
	stream pluginv1.DataSourcePlugin_SyncServer,
) error {
	if s.dataSource == nil {
		return status.Error(codes.Unimplemented, "data source capability is not enabled")
	}
	if err := validateJSONConfig(req.GetConfigJson()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if len(req.GetCursorJson()) > 0 && !json.Valid(req.GetCursorJson()) {
		return status.Error(codes.InvalidArgument, "cursor_json must be valid JSON")
	}
	input := DataSourceSyncInput{
		Invocation:  invocationFromProto(req.GetContext()),
		Config:      append(json.RawMessage(nil), req.GetConfigJson()...),
		ResourceIDs: append([]string(nil), req.GetResourceIds()...),
		Cursor:      append(json.RawMessage(nil), req.GetCursorJson()...),
		Full:        req.GetFull(),
	}
	if err := s.dataSource.Sync(stream.Context(), input, newSyncEmitter(stream)); err != nil {
		return capabilityError(err)
	}
	return nil
}

func invocationFromProto(value *pluginv1.InvocationContext) Invocation {
	if value == nil {
		return Invocation{}
	}
	return Invocation{
		PluginID:   value.GetPluginId(),
		TenantID:   value.GetTenantId(),
		InstanceID: value.GetInstanceId(),
		RequestID:  value.GetRequestId(),
	}
}

func validateJSONConfig(config []byte) error {
	if len(config) == 0 || !json.Valid(config) {
		return errors.New("config_json must be valid JSON")
	}
	return nil
}

func capabilityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, fmt.Sprintf("plugin capability failed: %v", err))
}
