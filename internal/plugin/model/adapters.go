package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

const maxExternalChatResponseBytes = 64 * 1024 * 1024

type chatAdapter struct {
	pluginID     string
	runtime      runtimeclient.Gateway
	model        *types.Model
	configSchema map[string]any
}

func (a *chatAdapter) GetModelName() string { return a.model.Name }
func (a *chatAdapter) GetModelID() string   { return a.model.ID }

func (a *chatAdapter) Chat(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	stream, err := a.ChatStream(ctx, messages, options)
	if err != nil {
		return nil, err
	}
	response := &types.ChatResponse{}
	for event := range stream {
		if event.ResponseType == types.ResponseTypeError {
			return nil, errors.New(event.Content)
		}
		if len(response.Content)+len(event.Content) > maxExternalChatResponseBytes {
			return nil, errors.New("model plugin chat output exceeds 64 MiB")
		}
		response.Content += event.Content
		if event.Usage != nil {
			response.Usage = *event.Usage
		}
		if event.Done {
			response.FinishReason = event.FinishReason
		}
	}
	return response, nil
}

func (a *chatAdapter) ChatStream(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	if err := validateChatOptions(options); err != nil {
		return nil, err
	}
	converted, err := convertMessages(messages)
	if err != nil {
		return nil, err
	}
	configJSON, err := modelConfigJSON(a.model, a.configSchema)
	if err != nil {
		return nil, err
	}
	client, err := a.runtime.Model()
	if err != nil {
		return nil, err
	}
	request := &pluginv1.ChatRequest{
		Context: invocation(ctx, a.pluginID, a.model.ID), ConfigJson: configJSON,
		Model: a.model.Name, Messages: converted, Options: chatOptions(options),
	}
	stream, err := client.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	result := make(chan types.StreamResponse, 8)
	go func() {
		defer close(result)
		for {
			event, recvErr := stream.Recv()
			if recvErr == io.EOF {
				return
			}
			if recvErr != nil {
				select {
				case result <- types.StreamResponse{ResponseType: types.ResponseTypeError, Content: recvErr.Error(), Done: true}:
				case <-ctx.Done():
				}
				return
			}
			usage := usageFromProto(event.GetUsage())
			response := types.StreamResponse{
				ResponseType: types.ResponseTypeAnswer, Content: event.GetContentDelta(), Done: event.GetDone(), Usage: usage,
			}
			if event.GetDone() {
				response.FinishReason = "stop"
			}
			select {
			case result <- response:
			case <-ctx.Done():
				return
			}
		}
	}()
	return result, nil
}

func convertMessages(messages []chat.Message) ([]*pluginv1.ChatMessage, error) {
	result := make([]*pluginv1.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if len(message.MultiContent) > 0 || len(message.Images) > 0 || len(message.ToolCalls) > 0 ||
			message.ToolCallID != "" || message.Name != "" || message.ReasoningContent != "" {
			return nil, errors.New("external model provider protocol currently supports plain-text chat only")
		}
		result = append(result, &pluginv1.ChatMessage{Role: message.Role, Content: message.Content})
	}
	return result, nil
}

func validateChatOptions(options *chat.ChatOptions) error {
	if options == nil {
		return nil
	}
	if len(options.Tools) > 0 || options.ToolChoice != "" || options.ParallelToolCalls != nil || len(options.Format) > 0 {
		return errors.New("external model provider protocol currently does not support tools or structured output")
	}
	return nil
}

func chatOptions(options *chat.ChatOptions) map[string]string {
	if options == nil {
		return nil
	}
	result := map[string]string{
		"temperature": strconv.FormatFloat(options.Temperature, 'g', -1, 64),
		"top_p":       strconv.FormatFloat(options.TopP, 'g', -1, 64),
		"seed":        strconv.Itoa(options.Seed), "max_tokens": strconv.Itoa(options.MaxTokens),
		"max_completion_tokens": strconv.Itoa(options.MaxCompletionTokens),
		"frequency_penalty":     strconv.FormatFloat(options.FrequencyPenalty, 'g', -1, 64),
		"presence_penalty":      strconv.FormatFloat(options.PresencePenalty, 'g', -1, 64),
	}
	if options.Thinking != nil {
		result["thinking"] = strconv.FormatBool(*options.Thinking)
	}
	return result
}

type embeddingAdapter struct {
	pluginID     string
	runtime      runtimeclient.Gateway
	model        *types.Model
	configSchema map[string]any
}

func (a *embeddingAdapter) GetModelName() string { return a.model.Name }
func (a *embeddingAdapter) GetModelID() string   { return a.model.ID }
func (a *embeddingAdapter) GetDimensions() int {
	return a.model.Parameters.EmbeddingParameters.Dimension
}
func (a *embeddingAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	result, err := a.BatchEmbed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(result) != 1 {
		return nil, errors.New("model plugin returned an invalid embedding count")
	}
	return result[0], nil
}
func (a *embeddingAdapter) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	configJSON, err := modelConfigJSON(a.model, a.configSchema)
	if err != nil {
		return nil, err
	}
	client, err := a.runtime.Model()
	if err != nil {
		return nil, err
	}
	response, err := client.Embed(ctx, &pluginv1.EmbedRequest{
		Context: invocation(ctx, a.pluginID, a.model.ID), ConfigJson: configJSON,
		Model: a.model.Name, Inputs: append([]string(nil), texts...),
	})
	if err != nil {
		return nil, err
	}
	if len(response.GetEmbeddings()) != len(texts) {
		return nil, fmt.Errorf("model plugin returned %d embeddings for %d inputs", len(response.GetEmbeddings()), len(texts))
	}
	result := make([][]float32, 0, len(texts))
	for _, item := range response.GetEmbeddings() {
		values := append([]float32(nil), item.GetValues()...)
		if err := validateEmbedding(values, a.GetDimensions()); err != nil {
			return nil, err
		}
		result = append(result, values)
	}
	return result, nil
}
func (a *embeddingAdapter) BatchEmbedWithPool(ctx context.Context, _ embedding.Embedder, texts []string) ([][]float32, error) {
	return a.BatchEmbed(ctx, texts)
}

type rerankAdapter struct {
	pluginID     string
	runtime      runtimeclient.Gateway
	model        *types.Model
	configSchema map[string]any
}

func (a *rerankAdapter) GetModelName() string { return a.model.Name }
func (a *rerankAdapter) GetModelID() string   { return a.model.ID }
func (a *rerankAdapter) Rerank(ctx context.Context, query string, documents []string) ([]rerank.RankResult, error) {
	configJSON, err := modelConfigJSON(a.model, a.configSchema)
	if err != nil {
		return nil, err
	}
	client, err := a.runtime.Model()
	if err != nil {
		return nil, err
	}
	response, err := client.Rerank(ctx, &pluginv1.RerankRequest{
		Context: invocation(ctx, a.pluginID, a.model.ID), ConfigJson: configJSON,
		Model: a.model.Name, Query: query, Documents: append([]string(nil), documents...), TopN: uint32(len(documents)),
	})
	if err != nil {
		return nil, err
	}
	return convertRerankResults(response.GetResults(), documents)
}

func validateEmbedding(values []float32, dimensions int) error {
	if dimensions > 0 && len(values) != dimensions {
		return fmt.Errorf("model plugin returned embedding dimension %d, expected %d", len(values), dimensions)
	}
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("model plugin returned a non-finite embedding value")
		}
	}
	return nil
}

func convertRerankResults(items []*pluginv1.RerankResult, documents []string) ([]rerank.RankResult, error) {
	if len(items) > len(documents) {
		return nil, errors.New("model plugin returned too many rerank results")
	}
	seen := make(map[int]struct{}, len(items))
	result := make([]rerank.RankResult, 0, len(items))
	for _, item := range items {
		if item == nil {
			return nil, errors.New("model plugin returned an empty rerank result")
		}
		index := int(item.GetIndex())
		if index < 0 || index >= len(documents) {
			return nil, fmt.Errorf("model plugin returned invalid rerank index %d", index)
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, fmt.Errorf("model plugin returned duplicate rerank index %d", index)
		}
		seen[index] = struct{}{}
		if score := float64(item.GetScore()); math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("model plugin returned non-finite rerank score for index %d", index)
		}
		result = append(result, rerank.RankResult{Index: index, Document: rerank.DocumentInfo{Text: documents[index]}, RelevanceScore: float64(item.GetScore())})
	}
	return result, nil
}

func modelConfigJSON(model *types.Model, configSchema map[string]any) ([]byte, error) {
	values := make(map[string]any, len(model.Parameters.ExtraConfig)+4)
	for key, value := range model.Parameters.ExtraConfig {
		values[key] = value
	}
	for key, value := range map[string]string{
		"api_key": model.Parameters.APIKey, "app_secret": model.Parameters.AppSecret,
		"base_url": model.Parameters.BaseURL, "interface_type": model.Parameters.InterfaceType,
	} {
		if value == "" {
			continue
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("model plugin config key %s conflicts with a protected field", key)
		}
		values[key] = value
	}
	values = pluginsdk.NormalizeConfigValues(configSchema, values)
	if err := pluginsdk.ValidateConfigValues(configSchema, values); err != nil {
		return nil, fmt.Errorf("invalid model plugin config: %w", err)
	}
	return json.Marshal(values)
}

func invocation(ctx context.Context, pluginID, instanceID string) *pluginv1.InvocationContext {
	tenantID, _ := types.TenantIDFromContext(ctx)
	return &pluginv1.InvocationContext{PluginId: pluginID, TenantId: tenantID, InstanceId: instanceID}
}

func usageFromProto(value *pluginv1.ModelUsage) *types.TokenUsage {
	if value == nil || (value.GetInputTokens() == 0 && value.GetOutputTokens() == 0) {
		return nil
	}
	input, output := saturatedTokenCount(value.GetInputTokens()), saturatedTokenCount(value.GetOutputTokens())
	total := input + output
	if input > maxInt()-output {
		total = maxInt()
	}
	return &types.TokenUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: total}
}

func saturatedTokenCount(value uint64) int {
	if value > uint64(maxInt()) {
		return maxInt()
	}
	return int(value)
}

func maxInt() int { return int(^uint(0) >> 1) }
