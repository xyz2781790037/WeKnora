package retrieval

import (
	"context"
	"math"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type testGateway struct{}

func (testGateway) Enabled() bool                                            { return true }
func (testGateway) Runtime() (pluginv1.PluginRuntimeClient, error)           { return nil, nil }
func (testGateway) Lifecycle() (pluginv1.PluginLifecycleClient, error)       { return nil, nil }
func (testGateway) DataSource() (pluginv1.DataSourcePluginClient, error)     { return nil, nil }
func (testGateway) Parser() (pluginv1.DocumentParserPluginClient, error)     { return nil, nil }
func (testGateway) Search() (pluginv1.WebSearchPluginClient, error)          { return nil, nil }
func (testGateway) Model() (pluginv1.ModelProviderPluginClient, error)       { return nil, nil }
func (testGateway) Retrieval() (pluginv1.RetrievalEnginePluginClient, error) { return nil, nil }

func TestPrepareConfigSeparatesSecretsAndAppliesDefaults(t *testing.T) {
	engineType := types.RetrieverEngineType("test_prepare_config")
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"endpoint": map[string]any{"type": "string", "default": "https://example.test"},
			"token":    map[string]any{"type": "string"},
		},
		"required": []any{"token"},
	}
	require.NoError(t, Register("io.example.retrieval", engineType, testGateway{}, []string{"vector"}, schema, []string{"token"}))
	t.Cleanup(func() { Unregister(engineType, "io.example.retrieval") })
	config := types.ConnectionConfig{PluginConfig: map[string]any{"token": "secret"}}
	require.NoError(t, PrepareConfig(engineType, &config))
	require.Equal(t, "https://example.test", config.PluginConfig["endpoint"])
	require.NotContains(t, config.PluginConfig, "token")
	require.Equal(t, "secret", config.PluginCredentials["token"])
}

func TestRepositoryRejectsInvalidHits(t *testing.T) {
	repository := &repository{
		store:        types.VectorStore{EngineType: "test"},
		registration: registration{capabilities: map[types.RetrieverType]bool{types.VectorRetrieverType: true}},
	}
	_, err := repository.convertResultSet(&pluginv1.RetrievalResultSet{
		RetrieverType: pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR,
		Hits:          []*pluginv1.RetrievalHit{{ChunkId: "", Score: 1}},
	})
	require.ErrorContains(t, err, "invalid hit")
	_, err = repository.convertResultSet(&pluginv1.RetrievalResultSet{
		RetrieverType: pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR,
		Hits:          []*pluginv1.RetrievalHit{{ChunkId: "chunk-1", SourceType: 99, Score: 1}},
	})
	require.ErrorContains(t, err, "invalid hit")
	_, err = repository.convertResultSet(&pluginv1.RetrievalResultSet{
		RetrieverType: pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR,
		Hits:          []*pluginv1.RetrievalHit{{ChunkId: "chunk-1", MatchType: 99, Score: 1}},
	})
	require.ErrorContains(t, err, "invalid hit")
	require.False(t, finiteVector([]float32{float32(math.Inf(1))}))
}

type captureRetrievalClient struct {
	pluginv1.RetrievalEnginePluginClient
	request *pluginv1.RetrievalRequest
}

func (c *captureRetrievalClient) Retrieve(
	_ context.Context,
	request *pluginv1.RetrievalRequest,
	_ ...grpc.CallOption,
) (*pluginv1.RetrievalResponse, error) {
	c.request = request
	return &pluginv1.RetrievalResponse{Results: []*pluginv1.RetrievalResultSet{{
		RetrieverType: pluginv1.PluginRetrieverType_PLUGIN_RETRIEVER_TYPE_VECTOR,
	}}}, nil
}

type captureGateway struct {
	testGateway
	client pluginv1.RetrievalEnginePluginClient
}

func (g captureGateway) Retrieval() (pluginv1.RetrievalEnginePluginClient, error) {
	return g.client, nil
}

func TestRepositoryUsesStoreTenantAndInstance(t *testing.T) {
	client := &captureRetrievalClient{}
	repository := &repository{
		store: types.VectorStore{ID: "store-1", TenantID: 42, EngineType: "test"},
		registration: registration{
			pluginID: "io.example.retrieval", runtime: captureGateway{client: client},
			capabilities: map[types.RetrieverType]bool{types.VectorRetrieverType: true},
		},
	}
	_, err := repository.Retrieve(context.Background(), types.RetrieveParams{
		RetrieverType: types.VectorRetrieverType, Embedding: []float32{1}, TopK: 3,
	})
	require.NoError(t, err)
	require.NotNil(t, client.request)
	require.Equal(t, uint64(42), client.request.GetContext().GetTenantId())
	require.Equal(t, "store-1", client.request.GetContext().GetInstanceId())
}
