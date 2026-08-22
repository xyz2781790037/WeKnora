package web_search

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistryListReturnsStableProviderIDs(t *testing.T) {
	registry := NewRegistry()
	registry.Register("zeta", nil)
	registry.Register("alpha", nil)

	assert.Equal(t, []string{"alpha", "zeta"}, registry.List())
}
