package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

const (
	contentChunkBytes        = 64 * 1024
	maxParsedMarkdownBytes   = 100 * 1024 * 1024
	maxParsedMetadataBytes   = 1024 * 1024
	maxParsedMetadataFields  = 4096
	maxParsedAttachments     = 256
	maxParsedAttachmentBytes = 100 * 1024 * 1024
)

type Reader struct {
	pluginID     string
	runtime      runtimeclient.Gateway
	overrides    map[string]string
	configSchema map[string]any
}

func NewReader(pluginID string, runtime runtimeclient.Gateway, overrides map[string]string, configSchema map[string]any) *Reader {
	return &Reader{pluginID: pluginID, runtime: runtime, overrides: overrides, configSchema: configSchema}
}

func (r *Reader) Read(ctx context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	if req == nil {
		return nil, errors.New("parser request is required")
	}
	if req.URL != "" {
		return nil, errors.New("external parser plugins currently accept uploaded file content only")
	}
	client, err := r.runtime.Parser()
	if err != nil {
		return nil, err
	}
	stream, err := client.Parse(ctx)
	if err != nil {
		return nil, err
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	configJSON, err := r.configJSON()
	if err != nil {
		return nil, err
	}
	invocation := &pluginv1.InvocationContext{PluginId: r.pluginID, TenantId: tenantID, RequestId: req.RequestID}
	if err := stream.Send(&pluginv1.ParseRequest{Context: invocation, Frame: &pluginv1.ParseRequest_Begin{Begin: &pluginv1.ParseBegin{
		ConfigJson: configJSON, FileName: req.FileName,
		ContentType: req.FileType, ContentSize: int64(len(req.FileContent)),
	}}}); err != nil {
		return nil, err
	}
	for offset := 0; offset < len(req.FileContent); offset += contentChunkBytes {
		end := min(offset+contentChunkBytes, len(req.FileContent))
		if err := stream.Send(&pluginv1.ParseRequest{Frame: &pluginv1.ParseRequest_ContentChunk{ContentChunk: req.FileContent[offset:end]}}); err != nil {
			return nil, err
		}
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	result := &types.ReadResult{Metadata: map[string]string{}}
	var markdown bytes.Buffer
	var budget parserOutputBudget
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, recvErr
		}
		switch value := event.GetEvent().(type) {
		case *pluginv1.ParseEvent_Metadata:
			if err := budget.addMetadata(value.Metadata); err != nil {
				return nil, err
			}
			if title := value.Metadata.GetTitle(); title != "" {
				result.Metadata["title"] = title
			}
			for key, item := range value.Metadata.GetValues() {
				result.Metadata[key] = item
			}
		case *pluginv1.ParseEvent_MarkdownChunk:
			if markdown.Len()+len(value.MarkdownChunk) > maxParsedMarkdownBytes {
				return nil, errors.New("parser plugin markdown output exceeds 100 MiB")
			}
			markdown.WriteString(value.MarkdownChunk)
		case *pluginv1.ParseEvent_Attachment:
			attachment := value.Attachment
			if err := budget.addAttachment(attachment); err != nil {
				return nil, err
			}
			result.ImageRefs = append(result.ImageRefs, types.ImageRef{
				Filename: attachment.GetFileName(), MimeType: attachment.GetContentType(),
				ImageData: append([]byte(nil), attachment.GetContent()...),
			})
		default:
			return nil, errors.New("parser plugin emitted an unknown event")
		}
	}
	result.MarkdownContent = markdown.String()
	return result, nil
}

type parserOutputBudget struct {
	metadataBytes   int
	metadataFields  int
	attachmentBytes int
	attachments     int
}

func (b *parserOutputBudget) addMetadata(metadata *pluginv1.ParseMetadata) error {
	if metadata == nil {
		return errors.New("parser plugin emitted empty metadata")
	}
	b.metadataBytes += len(metadata.GetTitle())
	b.metadataFields += len(metadata.GetValues())
	for key, value := range metadata.GetValues() {
		b.metadataBytes += len(key) + len(value)
	}
	if b.metadataFields > maxParsedMetadataFields || b.metadataBytes > maxParsedMetadataBytes {
		return errors.New("parser plugin metadata output exceeds limit")
	}
	return nil
}

func (b *parserOutputBudget) addAttachment(attachment *pluginv1.ParsedAttachment) error {
	if attachment == nil || strings.TrimSpace(attachment.GetFileName()) == "" {
		return errors.New("parser plugin attachment requires a file name")
	}
	b.attachments++
	b.attachmentBytes += len(attachment.GetContent())
	if b.attachments > maxParsedAttachments || b.attachmentBytes > maxParsedAttachmentBytes {
		return errors.New("parser plugin attachment output exceeds limit")
	}
	return nil
}

func (r *Reader) configJSON() ([]byte, error) {
	prefix := "plugin." + r.pluginID + "."
	values := map[string]any{}
	for key, value := range r.overrides {
		if strings.HasPrefix(key, prefix) {
			values[strings.TrimPrefix(key, prefix)] = value
		}
	}
	values = pluginsdk.NormalizeConfigValues(r.configSchema, values)
	if err := pluginsdk.ValidateConfigValues(r.configSchema, values); err != nil {
		return nil, fmt.Errorf("invalid parser plugin config: %w", err)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode parser plugin config: %w", err)
	}
	return encoded, nil
}
