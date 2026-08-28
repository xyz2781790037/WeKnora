package web_search

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryListReturnsStableProviderIDs(t *testing.T) {
	registry := NewRegistry()
	registry.Register("zeta", nil)
	registry.Register("alpha", nil)

	assert.Equal(t, []string{"alpha", "zeta"}, registry.List())
}

func TestRegistryExternalProviderLifecycle(t *testing.T) {
	registry := NewRegistry()
	factory := func(types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error) { return nil, nil }
	require.NoError(t, registry.RegisterExternal("io.example.search", factory, types.WebSearchProviderTypeInfo{Name: "Example Search"}))

	assert.True(t, registry.Has("io.example.search"))
	found := false
	for _, providerType := range registry.ProviderTypes() {
		if providerType.ID == "io.example.search" {
			found = true
			assert.Equal(t, "Example Search", providerType.Name)
		}
	}
	assert.True(t, found)

	registry.Unregister("io.example.search")
	assert.False(t, registry.Has("io.example.search"))
}

func TestRegistryRejectsInvalidExternalProvider(t *testing.T) {
	registry := NewRegistry()
	require.Error(t, registry.RegisterExternal("", nil, types.WebSearchProviderTypeInfo{}))
}
