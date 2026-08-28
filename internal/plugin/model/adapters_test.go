package model

import (
	"math"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertMessagesRejectsUnsupportedFields(t *testing.T) {
	_, err := convertMessages([]chat.Message{{Role: "assistant", Content: "answer", ReasoningContent: "private reasoning"}})
	require.ErrorContains(t, err, "plain-text chat only")
}

func TestValidateEmbeddingRejectsNonFiniteValues(t *testing.T) {
	require.ErrorContains(t, validateEmbedding([]float32{1, float32(math.NaN())}, 2), "non-finite")
	require.ErrorContains(t, validateEmbedding([]float32{1}, 2), "dimension")
	require.NoError(t, validateEmbedding([]float32{1, 2}, 2))
}

func TestConvertRerankResultsRejectsDuplicateAndNonFiniteValues(t *testing.T) {
	documents := []string{"a", "b"}
	_, err := convertRerankResults([]*pluginv1.RerankResult{{Index: 0, Score: 1}, {Index: 0, Score: 0.5}}, documents)
	require.ErrorContains(t, err, "duplicate")

	_, err = convertRerankResults([]*pluginv1.RerankResult{{Index: 1, Score: float32(math.Inf(1))}}, documents)
	require.ErrorContains(t, err, "non-finite")
}

func TestUsageFromProtoSaturatesIntegerOverflow(t *testing.T) {
	usage := usageFromProto(&pluginv1.ModelUsage{InputTokens: ^uint64(0), OutputTokens: ^uint64(0)})
	require.NotNil(t, usage)
	assert.Equal(t, maxInt(), usage.PromptTokens)
	assert.Equal(t, maxInt(), usage.CompletionTokens)
	assert.Equal(t, maxInt(), usage.TotalTokens)
}

func TestValidateChatOptionsRejectsToolsAndStructuredOutput(t *testing.T) {
	require.Error(t, validateChatOptions(&chat.ChatOptions{Tools: []chat.Tool{{Type: "function"}}}))
	require.Error(t, validateChatOptions(&chat.ChatOptions{Format: []byte(`{"type":"json_object"}`)}))
	require.NoError(t, validateChatOptions(&chat.ChatOptions{Temperature: 0.2}))
}

func TestExternalModelConstructorsRejectNilModel(t *testing.T) {
	_, _, err := NewChat(nil)
	require.ErrorContains(t, err, "model is required")
	_, _, err = NewEmbedder(nil)
	require.ErrorContains(t, err, "model is required")
	_, _, err = NewReranker(nil)
	require.ErrorContains(t, err, "model is required")
}
