package datasource

import (
	"context"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

type registryTestConnector struct{ connectorType string }

func (c *registryTestConnector) Type() string                                          { return c.connectorType }
func (*registryTestConnector) Validate(context.Context, *types.DataSourceConfig) error { return nil }
func (*registryTestConnector) ListResources(context.Context, *types.DataSourceConfig, string) ([]types.Resource, error) {
	return nil, nil
}
func (*registryTestConnector) ResolveResourceAncestors(context.Context, *types.DataSourceConfig, []string) ([]string, error) {
	return nil, nil
}
func (*registryTestConnector) FetchAll(context.Context, *types.DataSourceConfig, []string) ([]types.FetchedItem, error) {
	return nil, nil
}
func (*registryTestConnector) FetchIncremental(context.Context, *types.DataSourceConfig, *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	return nil, nil, nil
}

func TestConnectorRegistryDynamicMetadataAndConcurrentReads(t *testing.T) {
	registry := NewConnectorRegistry()
	connector := &registryTestConnector{connectorType: "external_test"}
	if err := registry.RegisterWithMetadata(connector, ConnectorMetadata{
		Type: "external_test", Name: "External Test", Origin: "external",
	}); err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if _, err := registry.Get("external_test"); err != nil {
					t.Errorf("concurrent get failed: %v", err)
					return
				}
				_ = registry.ListMetadata()
			}
		}()
	}
	workers.Wait()

	metadata := registry.ListMetadata()
	if len(metadata) != 1 || metadata[0].PluginID != "" || metadata[0].Name != "External Test" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	registry.Unregister("external_test")
	if _, err := registry.Get("external_test"); err == nil {
		t.Fatal("connector should be unavailable after unregister")
	}
}
