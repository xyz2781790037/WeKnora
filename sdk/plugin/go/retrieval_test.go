package plugin

import (
	"context"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/stretchr/testify/require"
)

type retrievalTestHandler struct {
	query RetrievalQueryInput
}

func (*retrievalTestHandler) Upsert(context.Context, RetrievalUpsertInput) error { return nil }
func (h *retrievalTestHandler) Retrieve(_ context.Context, input RetrievalQueryInput) ([]RetrievalResultSet, error) {
	h.query = input
	return []RetrievalResultSet{{RetrieverType: RetrieverTypeVector, Hits: []RetrievalHit{{ChunkID: "chunk-1", Score: 0.9}}}}, nil
}
func (*retrievalTestHandler) EstimateStorage(context.Context, RetrievalEstimateInput) (int64, error) {
	return 42, nil
}
func (*retrievalTestHandler) Delete(context.Context, RetrievalDeleteInput) error { return nil }
func (*retrievalTestHandler) Copy(context.Context, RetrievalCopyInput) error     { return nil }
func (*retrievalTestHandler) UpdateChunks(context.Context, RetrievalUpdateChunksInput) error {
	return nil
}

func TestRetrievalServerConvertsQueryAndResults(t *testing.T) {
	handler := &retrievalTestHandler{}
	server := &Server{retrievalEngine: handler}
	response, err := server.Retrieve(context.Background(), &pluginv1.RetrievalRequest{
		Context:    &pluginv1.InvocationContext{PluginId: "io.example.retrieval", TenantId: 10001, InstanceId: "store-1"},
		ConfigJson: []byte(`{}`), Query: "hello", Embedding: []float32{1, 2}, TopK: 3,
		RetrieverType: pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR,
	})
	require.NoError(t, err)
	require.Equal(t, "store-1", handler.query.Invocation.InstanceID)
	require.Equal(t, RetrieverTypeVector, handler.query.RetrieverType)
	require.Equal(t, uint32(3), handler.query.TopK)
	require.Len(t, response.GetResults(), 1)
	require.Equal(t, "chunk-1", response.GetResults()[0].GetHits()[0].GetChunkId())
}
