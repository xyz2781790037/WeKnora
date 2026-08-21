package plugin

import (
	"context"
	"encoding/json"
	"errors"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ParseInput struct {
	Invocation  Invocation
	Config      json.RawMessage
	FileName    string
	ContentType string
	ContentSize int64
}

type ParsedMetadata struct {
	Title  string
	Values map[string]string
}

type ParsedAttachment struct {
	FileName    string
	ContentType string
	Content     []byte
}

// ParseStream keeps parser implementations independent from generated gRPC
// types and allows content to stay streamed instead of buffering whole files.
type ParseStream interface {
	RecvContent() ([]byte, error)
	EmitMetadata(metadata ParsedMetadata) error
	EmitMarkdown(markdown string) error
	EmitAttachment(attachment ParsedAttachment) error
}

type DocumentParserHandler interface {
	Parse(ctx context.Context, input ParseInput, stream ParseStream) error
}

func (s *Server) Parse(stream pluginv1.DocumentParserPlugin_ParseServer) error {
	if s.documentParser == nil {
		return status.Error(codes.Unimplemented, "document parser capability is not enabled")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	begin := first.GetBegin()
	if begin == nil || begin.GetFileName() == "" || begin.GetContentSize() < 0 {
		return status.Error(codes.InvalidArgument, "first parser frame must contain a valid begin")
	}
	if err := validateJSONConfig(begin.GetConfigJson()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	adapter := &parseStream{stream: stream}
	err = s.documentParser.Parse(stream.Context(), ParseInput{
		Invocation:  invocationFromProto(first.GetContext()),
		Config:      append(json.RawMessage(nil), begin.GetConfigJson()...),
		FileName:    begin.GetFileName(),
		ContentType: begin.GetContentType(),
		ContentSize: begin.GetContentSize(),
	}, adapter)
	return capabilityError(err)
}

type parseStream struct {
	stream pluginv1.DocumentParserPlugin_ParseServer
}

func (s *parseStream) RecvContent() ([]byte, error) {
	request, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	if _, duplicateBegin := request.GetFrame().(*pluginv1.ParseRequest_Begin); duplicateBegin {
		return nil, status.Error(codes.InvalidArgument, "parser begin frame may only appear once")
	}
	chunk, ok := request.GetFrame().(*pluginv1.ParseRequest_ContentChunk)
	if !ok {
		return nil, errors.New("parser frame does not contain content")
	}
	return append([]byte(nil), chunk.ContentChunk...), nil
}

func (s *parseStream) EmitMetadata(metadata ParsedMetadata) error {
	return s.stream.Send(&pluginv1.ParseEvent{Event: &pluginv1.ParseEvent_Metadata{
		Metadata: &pluginv1.ParseMetadata{Title: metadata.Title, Values: metadata.Values},
	}})
}

func (s *parseStream) EmitMarkdown(markdown string) error {
	if markdown == "" {
		return nil
	}
	return s.stream.Send(&pluginv1.ParseEvent{Event: &pluginv1.ParseEvent_MarkdownChunk{MarkdownChunk: markdown}})
}

func (s *parseStream) EmitAttachment(attachment ParsedAttachment) error {
	if attachment.FileName == "" {
		return errors.New("parsed attachment file name is required")
	}
	return s.stream.Send(&pluginv1.ParseEvent{Event: &pluginv1.ParseEvent_Attachment{
		Attachment: &pluginv1.ParsedAttachment{
			FileName: attachment.FileName, ContentType: attachment.ContentType,
			Content: append([]byte(nil), attachment.Content...),
		},
	}})
}

var _ ParseStream = (*parseStream)(nil)
