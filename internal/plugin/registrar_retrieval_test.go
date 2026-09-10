package plugin

import (
	"encoding/json"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/datasource"
	pluginRetrieval "github.com/Tencent/WeKnora/internal/plugin/retrieval"
	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/stretchr/testify/require"
)

type retrievalTestGateway struct{}

func (retrievalTestGateway) Enabled() bool                                        { return true }
func (retrievalTestGateway) Runtime() (pluginv1.PluginRuntimeClient, error)       { return nil, nil }
func (retrievalTestGateway) Lifecycle() (pluginv1.PluginLifecycleClient, error)   { return nil, nil }
func (retrievalTestGateway) DataSource() (pluginv1.DataSourcePluginClient, error) { return nil, nil }
func (retrievalTestGateway) Parser() (pluginv1.DocumentParserPluginClient, error) { return nil, nil }
func (retrievalTestGateway) Search() (pluginv1.WebSearchPluginClient, error)      { return nil, nil }
func (retrievalTestGateway) Model() (pluginv1.ModelProviderPluginClient, error)   { return nil, nil }
func (retrievalTestGateway) Retrieval() (pluginv1.RetrievalEnginePluginClient, error) {
	return nil, nil
}

func TestRegistrarRetrievalEngineLifecycle(t *testing.T) {
	const engineType = types.RetrieverEngineType("test_registry_engine")
	installed := retrievalPluginForTest(t, "io.example.retrieval.registry", string(engineType))
	registrar := NewRegistrar(nil, retrievalTestGateway{}, datasource.NewConnectorRegistry(), nil)

	require.NoError(t, registrar.Register(installed))
	t.Cleanup(func() { registrar.Unregister(installed) })
	require.True(t, types.IsValidEngineType(engineType))
	require.True(t, pluginRetrieval.IsRegistered(engineType))

	registrar.Unregister(installed)
	require.False(t, types.IsValidEngineType(engineType))
	require.False(t, pluginRetrieval.IsRegistered(engineType))
}

func TestRegistrarRejectsBuiltinRetrievalEngineWithoutLeakingRegistration(t *testing.T) {
	installed := retrievalPluginForTest(t, "io.example.retrieval.conflict", string(types.QdrantRetrieverEngineType))
	registrar := NewRegistrar(nil, retrievalTestGateway{}, datasource.NewConnectorRegistry(), nil)

	require.ErrorContains(t, registrar.Register(installed), "conflicts with a built-in engine")
	require.False(t, pluginRetrieval.IsRegistered(types.QdrantRetrieverEngineType))
}

func retrievalPluginForTest(t *testing.T, pluginID, engineType string) *types.Plugin {
	t.Helper()
	manifest := &pluginsdk.Manifest{
		APIVersion: pluginsdk.ManifestAPIVersion,
		Kind:       pluginsdk.ManifestKind,
		Metadata: pluginsdk.ManifestMetadata{
			ID: pluginID, Name: "Test Retrieval", Version: "1.0.0",
		},
		Spec: pluginsdk.ManifestSpec{
			ProtocolVersion: "1.1.0", WeKnoraVersionConstraint: ">=0.2.0",
			Image: "ghcr.io/example/retrieval:1.0.0", Types: []string{"retrieval_engine"},
			Capabilities: []string{"vector"}, RetrieverEngineType: engineType,
			Config: pluginsdk.ManifestConfig{
				Schema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			Permissions: pluginsdk.ManifestPermissions{
				DataAccess: []string{"document_content", "document_metadata", "embeddings"},
			},
			DefaultTimeoutText: "30s",
		},
	}
	require.NoError(t, manifest.Validate())
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	return &types.Plugin{ID: pluginID, Origin: types.PluginOriginExternal, Manifest: encoded}
}
