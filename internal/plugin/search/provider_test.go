package search

import (
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertSearchResultsEnforcesLimit(t *testing.T) {
	response := &pluginv1.WebSearchResponse{Results: []*pluginv1.WebSearchResult{
		{Title: "one", Url: "https://example.com/one"},
		{Title: "two", Url: "https://example.com/two"},
	}}
	results, err := convertSearchResults(response, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "one", results[0].Title)
}

func TestConvertSearchResultsRejectsUnsafeURL(t *testing.T) {
	_, err := convertSearchResults(&pluginv1.WebSearchResponse{Results: []*pluginv1.WebSearchResult{
		{Title: "unsafe", Url: "javascript:alert(1)"},
	}}, 1)
	require.ErrorContains(t, err, "invalid result URL")
}

func TestExternalSearchLimitDoesNotWrap(t *testing.T) {
	assert.Zero(t, externalSearchLimit(-1))
	assert.Equal(t, uint32(12), externalSearchLimit(12))
	if uint64(maxIntForTest()) > uint64(^uint32(0)) {
		assert.Equal(t, ^uint32(0), externalSearchLimit(maxIntForTest()))
	}
}

func maxIntForTest() int { return int(^uint(0) >> 1) }
