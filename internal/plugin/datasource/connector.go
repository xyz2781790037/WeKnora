// Package datasource adapts an out-of-process data source plugin to WeKnora's
// existing datasource.Connector pipeline.
package datasource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	core "github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

const (
	opaqueCursorKey         = "external_plugin_cursor_base64"
	defaultMaxDocumentBytes = 50 * 1024 * 1024
	maxCursorBytes          = 1024 * 1024
)

// Connector forwards source calls to one enabled external plugin.
type Connector struct {
	pluginID      string
	connectorType string
	runtime       runtimeclient.Gateway
	configSchema  map[string]any
	maxBytes      int64
}

func NewConnector(pluginID, connectorType string, runtime runtimeclient.Gateway, configSchema map[string]any) (*Connector, error) {
	pluginID = strings.TrimSpace(pluginID)
	connectorType = strings.TrimSpace(connectorType)
	if pluginID == "" || connectorType == "" || runtime == nil {
		return nil, errors.New("plugin id, connector type and runtime are required")
	}
	return &Connector{
		pluginID:      pluginID,
		connectorType: connectorType,
		runtime:       runtime,
		configSchema:  configSchema,
		maxBytes:      configuredMaxDocumentBytes(),
	}, nil
}

func (c *Connector) Type() string { return c.connectorType }

func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	values, err := mergePluginConfig(config)
	if err != nil {
		return err
	}
	if err := pluginsdk.ValidateConfigValues(c.configSchema, values); err != nil {
		return fmt.Errorf("%w: %s", core.ErrInvalidConfig, err)
	}
	client, err := c.runtime.Lifecycle()
	if err != nil {
		return err
	}
	configJSON, err := encodePluginConfig(config)
	if err != nil {
		return err
	}
	response, err := client.ValidateConfig(ctx, &pluginv1.ValidateConfigRequest{
		Context:    c.invocation(config),
		ConfigJson: configJSON,
	})
	if err != nil {
		return err
	}
	if response.GetValid() {
		return nil
	}
	messages := make([]string, 0, len(response.GetViolations()))
	for _, violation := range response.GetViolations() {
		messages = append(messages, fmt.Sprintf("%s: %s", violation.GetField(), violation.GetDescription()))
	}
	if len(messages) == 0 {
		return core.ErrInvalidConfig
	}
	return fmt.Errorf("%w: %s", core.ErrInvalidConfig, strings.Join(messages, "; "))
}

func (c *Connector) ListResources(
	ctx context.Context,
	config *types.DataSourceConfig,
	parentID string,
) ([]types.Resource, error) {
	client, err := c.runtime.DataSource()
	if err != nil {
		return nil, err
	}
	configJSON, err := encodePluginConfig(config)
	if err != nil {
		return nil, err
	}
	response, err := client.ListResources(ctx, &pluginv1.ListResourcesRequest{
		Context:    c.invocation(config),
		ConfigJson: configJSON,
		ParentId:   parentID,
	})
	if err != nil {
		return nil, err
	}
	resources := make([]types.Resource, 0, len(response.GetResources()))
	for _, item := range response.GetResources() {
		resource := types.Resource{
			ExternalID:  item.GetExternalId(),
			Name:        item.GetName(),
			Type:        item.GetType(),
			Description: item.GetDescription(),
			URL:         item.GetUrl(),
			ParentID:    item.GetParentId(),
			HasChildren: item.GetHasChildren(),
			Metadata:    stringMapToAny(item.GetMetadata()),
		}
		if timestamp := item.GetModifiedAt(); timestamp != nil && timestamp.IsValid() {
			resource.ModifiedAt = timestamp.AsTime()
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func (c *Connector) ResolveResourceAncestors(
	ctx context.Context,
	config *types.DataSourceConfig,
	resourceIDs []string,
) ([]string, error) {
	client, err := c.runtime.DataSource()
	if err != nil {
		return nil, err
	}
	configJSON, err := encodePluginConfig(config)
	if err != nil {
		return nil, err
	}
	response, err := client.ResolveResourceAncestors(ctx, &pluginv1.ResolveResourceAncestorsRequest{
		Context:     c.invocation(config),
		ConfigJson:  configJSON,
		ResourceIds: append([]string(nil), resourceIDs...),
	})
	if err != nil {
		return nil, err
	}
	return append([]string(nil), response.GetAncestorIds()...), nil
}

func (c *Connector) FetchAll(
	ctx context.Context,
	config *types.DataSourceConfig,
	resourceIDs []string,
) ([]types.FetchedItem, error) {
	collector := &collectHandler{}
	_, err := c.fetchStream(ctx, config, nil, resourceIDs, collector)
	return collector.items, err
}

func (c *Connector) FetchIncremental(
	ctx context.Context,
	config *types.DataSourceConfig,
	cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	collector := &collectHandler{}
	next, err := c.fetchStream(ctx, config, cursor, config.ResourceIDs, collector)
	return collector.items, next, err
}

func (c *Connector) FetchStream(
	ctx context.Context,
	config *types.DataSourceConfig,
	cursor *types.SyncCursor,
	handler core.StreamHandler,
) (*types.SyncCursor, error) {
	return c.fetchStream(ctx, config, cursor, config.ResourceIDs, handler)
}

func (c *Connector) fetchStream(
	ctx context.Context,
	config *types.DataSourceConfig,
	cursor *types.SyncCursor,
	resourceIDs []string,
	handler core.StreamHandler,
) (*types.SyncCursor, error) {
	if handler == nil {
		return cursor, errors.New("plugin stream handler is required")
	}
	client, err := c.runtime.DataSource()
	if err != nil {
		return nil, err
	}
	configJSON, err := encodePluginConfig(config)
	if err != nil {
		return nil, err
	}
	opaqueCursor, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	stream, err := client.Sync(ctx, &pluginv1.DataSourceSyncRequest{
		Context:     c.invocation(config),
		ConfigJson:  configJSON,
		ResourceIds: append([]string(nil), resourceIDs...),
		CursorJson:  opaqueCursor,
		Full:        cursor == nil,
	})
	if err != nil {
		return nil, err
	}

	assembler := documentAssembler{maxBytes: c.maxBytes}
	nextCursor := cursor
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nextCursor, recvErr
		}
		switch value := event.GetEvent().(type) {
		case *pluginv1.DataSourceSyncEvent_DocumentBegin:
			if err := assembler.begin(value.DocumentBegin); err != nil {
				return nextCursor, err
			}
		case *pluginv1.DataSourceSyncEvent_DocumentChunk:
			if err := assembler.chunk(value.DocumentChunk); err != nil {
				return nextCursor, err
			}
		case *pluginv1.DataSourceSyncEvent_DocumentEnd:
			item, err := assembler.end(value.DocumentEnd)
			if err != nil {
				return nextCursor, err
			}
			if err := handler.Emit(ctx, item); err != nil {
				return nextCursor, err
			}
		case *pluginv1.DataSourceSyncEvent_DeleteDocument:
			deleted := value.DeleteDocument
			if deleted.GetExternalId() == "" {
				return nextCursor, errors.New("plugin emitted deletion without external id")
			}
			if err := handler.Emit(ctx, types.FetchedItem{
				ExternalID:       deleted.GetExternalId(),
				Title:            deleted.GetTitle(),
				SourceResourceID: deleted.GetSourceResourceId(),
				Metadata:         deleted.GetMetadata(),
				IsDeleted:        true,
			}); err != nil {
				return nextCursor, err
			}
		case *pluginv1.DataSourceSyncEvent_Checkpoint:
			raw := value.Checkpoint.GetCursorJson()
			if len(raw) == 0 || len(raw) > maxCursorBytes || !json.Valid(raw) {
				return nextCursor, errors.New("plugin emitted invalid or oversized cursor checkpoint")
			}
			nextCursor = encodeCursor(raw)
			if err := handler.Checkpoint(ctx, nextCursor); err != nil {
				return nextCursor, err
			}
		case *pluginv1.DataSourceSyncEvent_Warning:
			warning := value.Warning
			externalID := warning.GetExternalId()
			if externalID == "" {
				externalID = "plugin-warning:" + warning.GetCode()
			}
			if err := handler.Emit(ctx, types.FetchedItem{
				ExternalID: externalID,
				Title:      externalID,
				Metadata: map[string]string{
					"error": warning.GetMessage(),
				},
			}); err != nil {
				return nextCursor, err
			}
		default:
			return nextCursor, errors.New("plugin emitted unknown sync event")
		}
	}
	if assembler.active() {
		return nextCursor, errors.New("plugin stream ended before document_end")
	}
	return nextCursor, nil
}

func (c *Connector) invocation(config *types.DataSourceConfig) *pluginv1.InvocationContext {
	invocation := &pluginv1.InvocationContext{PluginId: c.pluginID}
	if config != nil {
		invocation.TenantId = config.TenantID
		invocation.InstanceId = config.InstanceID
	}
	return invocation
}

type documentAssembler struct {
	maxBytes     int64
	header       *pluginv1.DocumentBegin
	buffer       bytes.Buffer
	nextSequence uint32
}

func (a *documentAssembler) active() bool { return a.header != nil }

func (a *documentAssembler) begin(header *pluginv1.DocumentBegin) error {
	if a.active() {
		return errors.New("plugin interleaved document_begin events")
	}
	if header == nil || header.GetExternalId() == "" || header.GetFileName() == "" {
		return errors.New("plugin document header requires external id and file name")
	}
	if header.GetContentSize() < 0 || header.GetContentSize() > a.maxBytes {
		return fmt.Errorf("plugin document %s exceeds maximum size", header.GetExternalId())
	}
	a.header = header
	a.buffer.Reset()
	a.nextSequence = 0
	if header.GetContentSize() > 0 {
		a.buffer.Grow(int(header.GetContentSize()))
	}
	return nil
}

func (a *documentAssembler) chunk(chunk *pluginv1.DocumentChunk) error {
	if !a.active() || chunk == nil || chunk.GetExternalId() != a.header.GetExternalId() {
		return errors.New("plugin document chunk has no matching document_begin")
	}
	if chunk.GetSequence() != a.nextSequence {
		return fmt.Errorf("plugin document chunk sequence mismatch: got %d want %d", chunk.GetSequence(), a.nextSequence)
	}
	if int64(a.buffer.Len()+len(chunk.GetData())) > a.maxBytes {
		return fmt.Errorf("plugin document %s exceeds maximum size", a.header.GetExternalId())
	}
	_, _ = a.buffer.Write(chunk.GetData())
	a.nextSequence++
	return nil
}

func (a *documentAssembler) end(end *pluginv1.DocumentEnd) (types.FetchedItem, error) {
	if !a.active() || end == nil || end.GetExternalId() != a.header.GetExternalId() {
		return types.FetchedItem{}, errors.New("plugin document_end has no matching document_begin")
	}
	content := append([]byte(nil), a.buffer.Bytes()...)
	digest := sha256.Sum256(content)
	if end.GetSha256() == "" || !strings.EqualFold(end.GetSha256(), hex.EncodeToString(digest[:])) {
		return types.FetchedItem{}, fmt.Errorf("plugin document %s checksum mismatch", a.header.GetExternalId())
	}
	if expected := a.header.GetContentSize(); expected > 0 && int64(len(content)) != expected {
		return types.FetchedItem{}, fmt.Errorf("plugin document %s size mismatch", a.header.GetExternalId())
	}
	header := a.header
	a.header = nil
	a.buffer.Reset()
	item := types.FetchedItem{
		ExternalID:       header.GetExternalId(),
		Title:            header.GetTitle(),
		Content:          content,
		ContentType:      header.GetContentType(),
		FileName:         header.GetFileName(),
		URL:              header.GetUrl(),
		Metadata:         header.GetMetadata(),
		SourceResourceID: header.GetSourceResourceId(),
	}
	if timestamp := header.GetUpdatedAt(); timestamp != nil && timestamp.IsValid() {
		item.UpdatedAt = timestamp.AsTime()
	}
	return item, nil
}

type collectHandler struct {
	items  []types.FetchedItem
	cursor *types.SyncCursor
}

func (h *collectHandler) Emit(_ context.Context, item types.FetchedItem) error {
	h.items = append(h.items, item)
	return nil
}

func (h *collectHandler) Checkpoint(_ context.Context, cursor *types.SyncCursor) error {
	h.cursor = cursor
	return nil
}

func encodeCursor(raw []byte) *types.SyncCursor {
	return &types.SyncCursor{
		LastSyncTime: time.Now().UTC(),
		ConnectorCursor: map[string]interface{}{
			opaqueCursorKey: base64.RawStdEncoding.EncodeToString(raw),
		},
	}
}

func decodeCursor(cursor *types.SyncCursor) ([]byte, error) {
	if cursor == nil || cursor.ConnectorCursor == nil {
		return nil, nil
	}
	value, ok := cursor.ConnectorCursor[opaqueCursorKey]
	if !ok {
		return nil, nil
	}
	encoded, ok := value.(string)
	if !ok {
		return nil, errors.New("external plugin cursor has invalid storage type")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode external plugin cursor: %w", err)
	}
	if len(decoded) > maxCursorBytes || !json.Valid(decoded) {
		return nil, errors.New("external plugin cursor is invalid or oversized")
	}
	return decoded, nil
}

func stringMapToAny(values map[string]string) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func encodePluginConfig(config *types.DataSourceConfig) ([]byte, error) {
	values, err := mergePluginConfig(config)
	if err != nil {
		return nil, err
	}
	return json.Marshal(values)
}

func mergePluginConfig(config *types.DataSourceConfig) (map[string]any, error) {
	values := make(map[string]interface{})
	if config != nil {
		for key, value := range config.Settings {
			values[key] = value
		}
		for key, value := range config.Credentials {
			if _, exists := values[key]; exists {
				return nil, fmt.Errorf("plugin config field %q appears in settings and credentials", key)
			}
			values[key] = value
		}
	}
	return values, nil
}

func configuredMaxDocumentBytes() int64 {
	value := strings.TrimSpace(os.Getenv("MAX_FILE_SIZE_MB"))
	if value == "" {
		return defaultMaxDocumentBytes
	}
	megabytes, err := strconv.ParseInt(value, 10, 64)
	if err != nil || megabytes <= 0 {
		return defaultMaxDocumentBytes
	}
	if megabytes > (1<<63-1)/(1024*1024) {
		return defaultMaxDocumentBytes
	}
	return megabytes * 1024 * 1024
}

var _ core.StreamingConnector = (*Connector)(nil)
