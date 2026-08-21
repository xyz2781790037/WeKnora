package plugin

import (
	"context"
	"encoding/json"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Model struct {
	ID           string
	Name         string
	Capabilities []pluginv1.ModelCapability
	Metadata     map[string]string
}

type ModelUsage struct {
	InputTokens  uint64
	OutputTokens uint64
}

type ChatMessage struct{ Role, Content string }

type ChatInput struct {
	Invocation Invocation
	Config     json.RawMessage
	Model      string
	Messages   []ChatMessage
	Options    map[string]string
}

type ChatEmitter interface {
	Emit(contentDelta string, done bool, usage ModelUsage) error
}

type EmbedInput struct {
	Invocation Invocation
	Config     json.RawMessage
	Model      string
	Inputs     []string
}

type EmbedOutput struct {
	Embeddings [][]float32
	Usage      ModelUsage
}

type RerankInput struct {
	Invocation Invocation
	Config     json.RawMessage
	Model      string
	Query      string
	Documents  []string
	TopN       uint32
}

type RerankResult struct {
	Index uint32
	Score float32
}

type RerankOutput struct {
	Results []RerankResult
	Usage   ModelUsage
}

type ModelProviderHandler interface {
	ListModels(ctx context.Context, invocation Invocation, config json.RawMessage) ([]Model, error)
	Chat(ctx context.Context, input ChatInput, emitter ChatEmitter) error
	Embed(ctx context.Context, input EmbedInput) (EmbedOutput, error)
	Rerank(ctx context.Context, input RerankInput) (RerankOutput, error)
}

func (s *Server) ListModels(ctx context.Context, request *pluginv1.ListModelsRequest) (*pluginv1.ListModelsResponse, error) {
	if s.modelProvider == nil {
		return nil, status.Error(codes.Unimplemented, "model provider capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	models, err := s.modelProvider.ListModels(ctx, invocationFromProto(request.GetContext()), append(json.RawMessage(nil), request.GetConfigJson()...))
	if err != nil {
		return nil, capabilityError(err)
	}
	response := &pluginv1.ListModelsResponse{}
	for _, model := range models {
		response.Models = append(response.Models, &pluginv1.PluginModel{
			Id: model.ID, Name: model.Name, Capabilities: append([]pluginv1.ModelCapability(nil), model.Capabilities...), Metadata: model.Metadata,
		})
	}
	return response, nil
}

func (s *Server) Chat(request *pluginv1.ChatRequest, stream pluginv1.ModelProviderPlugin_ChatServer) error {
	if s.modelProvider == nil {
		return status.Error(codes.Unimplemented, "model provider capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	messages := make([]ChatMessage, 0, len(request.GetMessages()))
	for _, message := range request.GetMessages() {
		messages = append(messages, ChatMessage{Role: message.GetRole(), Content: message.GetContent()})
	}
	return capabilityError(s.modelProvider.Chat(stream.Context(), ChatInput{
		Invocation: invocationFromProto(request.GetContext()), Config: append(json.RawMessage(nil), request.GetConfigJson()...),
		Model: request.GetModel(), Messages: messages, Options: request.GetOptions(),
	}, &chatEmitter{stream: stream}))
}

func (s *Server) Embed(ctx context.Context, request *pluginv1.EmbedRequest) (*pluginv1.EmbedResponse, error) {
	if s.modelProvider == nil {
		return nil, status.Error(codes.Unimplemented, "model provider capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	output, err := s.modelProvider.Embed(ctx, EmbedInput{
		Invocation: invocationFromProto(request.GetContext()), Config: append(json.RawMessage(nil), request.GetConfigJson()...),
		Model: request.GetModel(), Inputs: append([]string(nil), request.GetInputs()...),
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	response := &pluginv1.EmbedResponse{Usage: usageToProto(output.Usage)}
	for _, embedding := range output.Embeddings {
		response.Embeddings = append(response.Embeddings, &pluginv1.Embedding{Values: append([]float32(nil), embedding...)})
	}
	return response, nil
}

func (s *Server) Rerank(ctx context.Context, request *pluginv1.RerankRequest) (*pluginv1.RerankResponse, error) {
	if s.modelProvider == nil {
		return nil, status.Error(codes.Unimplemented, "model provider capability is not enabled")
	}
	if err := validateJSONConfig(request.GetConfigJson()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	output, err := s.modelProvider.Rerank(ctx, RerankInput{
		Invocation: invocationFromProto(request.GetContext()), Config: append(json.RawMessage(nil), request.GetConfigJson()...),
		Model: request.GetModel(), Query: request.GetQuery(), Documents: append([]string(nil), request.GetDocuments()...), TopN: request.GetTopN(),
	})
	if err != nil {
		return nil, capabilityError(err)
	}
	response := &pluginv1.RerankResponse{Usage: usageToProto(output.Usage)}
	for _, result := range output.Results {
		response.Results = append(response.Results, &pluginv1.RerankResult{Index: result.Index, Score: result.Score})
	}
	return response, nil
}

type chatEmitter struct {
	stream pluginv1.ModelProviderPlugin_ChatServer
}

func (e *chatEmitter) Emit(contentDelta string, done bool, usage ModelUsage) error {
	return e.stream.Send(&pluginv1.ChatEvent{ContentDelta: contentDelta, Done: done, Usage: usageToProto(usage)})
}

func usageToProto(usage ModelUsage) *pluginv1.ModelUsage {
	return &pluginv1.ModelUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
}

var _ ChatEmitter = (*chatEmitter)(nil)
