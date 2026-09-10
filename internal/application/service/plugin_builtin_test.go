package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
	infrawebsearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinPluginsIncludeAllFiveCapabilityFamilies(t *testing.T) {
	connectors := datasource.NewConnectorRegistry()
	require.NoError(t, connectors.Register(&builtinCatalogConnector{}))
	searches := infrawebsearch.NewRegistry()
	searches.Register("catalog_search", nil)

	service := &PluginService{
		connectors:  connectors,
		webSearches: searches,
		hostVersion: "test",
	}

	families := make(map[string]bool)
	for _, plugin := range service.builtinPlugins() {
		assert.Equal(t, types.PluginOriginBuiltin, plugin.Origin)
		var pluginTypes []string
		require.NoError(t, json.Unmarshal(plugin.Types, &pluginTypes))
		for _, pluginType := range pluginTypes {
			families[pluginType] = true
		}
	}

	assert.True(t, families["data_source"])
	assert.True(t, families["document_parser"])
	assert.True(t, families["web_search"])
	assert.True(t, families["model_provider"])
	assert.True(t, families["retrieval_engine"])
}

type builtinCatalogConnector struct{}

func (*builtinCatalogConnector) Type() string { return "catalog_test" }

func (*builtinCatalogConnector) Validate(context.Context, *types.DataSourceConfig) error { return nil }

func (*builtinCatalogConnector) ListResources(
	context.Context,
	*types.DataSourceConfig,
	string,
) ([]types.Resource, error) {
	return nil, nil
}

func (*builtinCatalogConnector) ResolveResourceAncestors(
	context.Context,
	*types.DataSourceConfig,
	[]string,
) ([]string, error) {
	return nil, nil
}

func (*builtinCatalogConnector) FetchAll(
	context.Context,
	*types.DataSourceConfig,
	[]string,
) ([]types.FetchedItem, error) {
	return nil, nil
}

func (*builtinCatalogConnector) FetchIncremental(
	context.Context,
	*types.DataSourceConfig,
	*types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	return nil, nil, nil
}
