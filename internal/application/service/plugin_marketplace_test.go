package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type marketplaceRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn marketplaceRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestMarketplaceSearchRepositoriesFetchesAllAccessiblePages(t *testing.T) {
	var mu sync.Mutex
	requestedPages := make([]int, 0, 3)
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		page, err := strconv.Atoi(request.URL.Query().Get("page"))
		require.NoError(t, err)
		require.Equal(t, "100", request.URL.Query().Get("per_page"))
		require.Equal(t, "updated", request.URL.Query().Get("sort"))
		mu.Lock()
		requestedPages = append(requestedPages, page)
		mu.Unlock()

		start := (page - 1) * 100
		count := min(100, max(0, 250-start))
		return marketplaceSearchResponse(http.StatusOK, 250, marketplaceRepositories(start, count), 50-page), nil
	})

	repositories, total, remaining, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.NoError(t, err)
	require.Len(t, repositories, 250)
	assert.Equal(t, 250, total)
	assert.Equal(t, 47, remaining)
	assert.Equal(t, []int{1, 2, 3}, requestedPages)
}

func TestMarketplaceSearchRepositoriesStopsAtGitHubSearchLimit(t *testing.T) {
	requests := 0
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		requests++
		page, err := strconv.Atoi(request.URL.Query().Get("page"))
		require.NoError(t, err)
		return marketplaceSearchResponse(
			http.StatusOK,
			5000,
			marketplaceRepositories((page-1)*pluginMarketplaceDiscoverySize, pluginMarketplaceDiscoverySize),
			100-page,
		), nil
	})

	repositories, total, remaining, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.NoError(t, err)
	assert.Len(t, repositories, pluginMarketplaceMaxRepositories)
	assert.Equal(t, 5000, total)
	assert.Equal(t, 90, remaining)
	assert.Equal(t, pluginMarketplaceMaxSearchPages, requests)
}

func TestMarketplaceSearchRepositoriesDeduplicatesAndStopsOnShortPage(t *testing.T) {
	requests := 0
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return marketplaceSearchResponse(http.StatusOK, 999, marketplaceRepositories(0, 100), 20), nil
		}
		return marketplaceSearchResponse(http.StatusOK, 999, []githubRepository{
			marketplaceRepository(99),
			marketplaceRepository(100),
		}, 19), nil
	})

	repositories, total, remaining, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.NoError(t, err)
	assert.Len(t, repositories, 101)
	assert.Equal(t, 999, total)
	assert.Equal(t, 19, remaining)
	assert.Equal(t, 2, requests)
}

func TestMarketplaceSearchRepositoriesReturnsLaterPageError(t *testing.T) {
	requests := 0
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return marketplaceSearchResponse(http.StatusOK, 200, marketplaceRepositories(0, 100), 10), nil
		}
		return marketplaceHTTPResponse(http.StatusBadGateway, `{"message":"temporary failure"}`, "9"), nil
	})

	_, _, _, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.ErrorContains(t, err, "HTTP 502")
	assert.Equal(t, 2, requests)
}

func TestMarketplaceSearchRepositoriesDoesNotMisreportForbiddenWithoutRateLimitHeader(t *testing.T) {
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		return marketplaceHTTPResponse(http.StatusForbidden, `{"message":"forbidden"}`, ""), nil
	})

	_, _, _, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
	assert.NotContains(t, err.Error(), "rate limit exceeded")
}

func TestMarketplaceSearchRepositoriesReportsExplicitRateLimitExhaustion(t *testing.T) {
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		return marketplaceHTTPResponse(http.StatusForbidden, `{"message":"rate limited"}`, "0"), nil
	})

	_, _, _, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.ErrorContains(t, err, "rate limit exceeded")
}

func TestMarketplaceSearchRepositoriesRejectsOversizedResponse(t *testing.T) {
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		body := `{"total_count":0,"items":[]}` + strings.Repeat(" ", pluginMarketplaceMaxResponseBytes)
		return marketplaceHTTPResponse(http.StatusOK, body, "10"), nil
	})

	_, _, _, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.ErrorContains(t, err, "response exceeds")
}

func TestMarketplaceSearchRepositoriesRejectsMalformedResponse(t *testing.T) {
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		return marketplaceHTTPResponse(http.StatusOK, `{"total_count":1,"items":[`, "10"), nil
	})

	_, _, _, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.ErrorContains(t, err, "decode GitHub marketplace response")
}

func TestMarketplaceSearchRepositoriesClampsNegativeTotal(t *testing.T) {
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		return marketplaceSearchResponse(http.StatusOK, -10, nil, 10), nil
	})

	repositories, total, remaining, err := marketplace.searchRepositories(context.Background(), PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.NoError(t, err)
	assert.Empty(t, repositories)
	assert.Zero(t, total)
	assert.Equal(t, 10, remaining)
}

func TestMarketplaceSearchRepositoriesHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})

	_, _, _, err := marketplace.searchRepositories(ctx, PluginMarketplaceQuery{
		Page: 1, PageSize: pluginMarketplaceDiscoverySize,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestCloneMarketplaceResultDoesNotShareMutableItemState(t *testing.T) {
	original := &PluginMarketplaceResult{Items: []PluginMarketplaceItem{{
		Types:        []string{"data_source"},
		Capabilities: []string{"incremental"},
		ConfigSchema: map[string]any{"properties": map[string]any{"branch": map[string]any{"type": "string"}}},
		SecretFields: []string{"token"},
		Permissions:  pluginsdk.ManifestPermissions{AllowedHosts: []string{"api.github.com"}, DataAccess: []string{"document_content"}},
	}}}

	cloned := cloneMarketplaceResult(original)
	cloned.Items[0].Types[0] = "model_provider"
	cloned.Items[0].Capabilities[0] = "chat"
	cloned.Items[0].SecretFields[0] = "api_key"
	cloned.Items[0].Permissions.AllowedHosts[0] = "example.com"
	cloned.Items[0].Permissions.DataAccess[0] = "conversation"
	cloned.Items[0].ConfigSchema["properties"].(map[string]any)["branch"].(map[string]any)["type"] = "integer"

	assert.Equal(t, "data_source", original.Items[0].Types[0])
	assert.Equal(t, "incremental", original.Items[0].Capabilities[0])
	assert.Equal(t, "token", original.Items[0].SecretFields[0])
	assert.Equal(t, "api.github.com", original.Items[0].Permissions.AllowedHosts[0])
	assert.Equal(t, "document_content", original.Items[0].Permissions.DataAccess[0])
	assert.Equal(t, "string", original.Items[0].ConfigSchema["properties"].(map[string]any)["branch"].(map[string]any)["type"])
}

func TestMarketplaceRefreshFallsBackToIsolatedStaleCache(t *testing.T) {
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		return marketplaceHTTPResponse(http.StatusBadGateway, `{"message":"temporary failure"}`, "10"), nil
	})
	marketplace.cache = make(map[string]pluginMarketplaceCacheEntry)
	query := PluginMarketplaceQuery{Page: 1, PageSize: 12, Refresh: true}
	cacheKey := ":::updated:false:1:12"
	marketplace.store(cacheKey, &PluginMarketplaceResult{
		Items:    []PluginMarketplaceItem{{ID: "io.test.cached", Types: []string{"data_source"}}},
		CachedAt: time.Now().Add(-time.Minute),
	})

	result, err := marketplace.list(context.Background(), "0.2.0", query)
	require.NoError(t, err)
	require.True(t, result.Stale)
	require.Len(t, result.Items, 1)
	result.Items[0].Types[0] = "model_provider"

	cached := marketplace.stale(cacheKey)
	require.NotNil(t, cached)
	assert.False(t, cached.Stale)
	assert.Equal(t, "data_source", cached.Items[0].Types[0])
}

func TestMarketplaceListReusesValidatedInventoryAcrossPages(t *testing.T) {
	var searchRequests atomic.Int32
	var manifestLoads atomic.Int32
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		searchRequests.Add(1)
		page, err := strconv.Atoi(request.URL.Query().Get("page"))
		require.NoError(t, err)
		start := (page - 1) * pluginMarketplaceDiscoverySize
		count := min(pluginMarketplaceDiscoverySize, max(0, 250-start))
		return marketplaceSearchResponse(http.StatusOK, 250, marketplaceRepositories(start, count), 100-page), nil
	})
	marketplace.loadItem = func(_ context.Context, _ string, repository githubRepository) (*PluginMarketplaceItem, error) {
		manifestLoads.Add(1)
		return &PluginMarketplaceItem{
			ID:         "io.test." + strings.ReplaceAll(repository.FullName, "/", "."),
			Name:       repository.FullName,
			Types:      []string{"data_source"},
			Repository: repository.FullName,
			UpdatedAt:  repository.UpdatedAt,
			Compatible: true,
		}, nil
	}

	first, err := marketplace.list(context.Background(), "0.2.0", PluginMarketplaceQuery{Page: 1, PageSize: 24})
	require.NoError(t, err)
	require.Len(t, first.Items, 24)
	assert.Equal(t, 250, first.RepositoryCount)
	assert.Equal(t, 250, first.GitHubRepositoryCount)
	assert.Equal(t, "owner/plugin-0249", first.Items[0].Repository)

	second, err := marketplace.list(context.Background(), "0.2.0", PluginMarketplaceQuery{Page: 2, PageSize: 24})
	require.NoError(t, err)
	require.Len(t, second.Items, 24)
	assert.Equal(t, "owner/plugin-0225", second.Items[0].Repository)
	assert.EqualValues(t, 3, searchRequests.Load())
	assert.EqualValues(t, 250, manifestLoads.Load())
}

func TestMarketplaceListCoalescesConcurrentInventoryBuilds(t *testing.T) {
	var searchRequests atomic.Int32
	var manifestLoads atomic.Int32
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		searchRequests.Add(1)
		page, err := strconv.Atoi(request.URL.Query().Get("page"))
		if err != nil {
			return nil, err
		}
		start := (page - 1) * pluginMarketplaceDiscoverySize
		count := min(pluginMarketplaceDiscoverySize, max(0, 150-start))
		return marketplaceSearchResponse(http.StatusOK, 150, marketplaceRepositories(start, count), 100-page), nil
	})
	marketplace.loadItem = func(_ context.Context, _ string, repository githubRepository) (*PluginMarketplaceItem, error) {
		manifestLoads.Add(1)
		time.Sleep(time.Millisecond)
		return &PluginMarketplaceItem{
			ID: repository.FullName, Name: repository.FullName, Repository: repository.FullName, Compatible: true,
		}, nil
	}

	const callers = 12
	start := make(chan struct{})
	errorsByCaller := make(chan error, callers)
	var callersDone sync.WaitGroup
	callersDone.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer callersDone.Done()
			<-start
			result, err := marketplace.list(context.Background(), "0.2.0", PluginMarketplaceQuery{Page: 1, PageSize: 12})
			if err == nil && len(result.Items) != 12 {
				err = fmt.Errorf("unexpected item count %d", len(result.Items))
			}
			errorsByCaller <- err
		}()
	}
	close(start)
	callersDone.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		require.NoError(t, err)
	}
	assert.EqualValues(t, 2, searchRequests.Load())
	assert.EqualValues(t, 150, manifestLoads.Load())
}

func TestMarketplaceRefreshFallsBackToStaleValidatedInventory(t *testing.T) {
	var failRefresh atomic.Bool
	marketplace := testMarketplace(func(request *http.Request) (*http.Response, error) {
		if failRefresh.Load() {
			return marketplaceHTTPResponse(http.StatusBadGateway, `{"message":"temporary failure"}`, "10"), nil
		}
		return marketplaceSearchResponse(http.StatusOK, 1, marketplaceRepositories(0, 1), 10), nil
	})
	marketplace.loadItem = func(_ context.Context, _ string, repository githubRepository) (*PluginMarketplaceItem, error) {
		return &PluginMarketplaceItem{
			ID: repository.FullName, Name: repository.FullName, Repository: repository.FullName, Compatible: true,
		}, nil
	}

	fresh, err := marketplace.list(context.Background(), "0.2.0", PluginMarketplaceQuery{Page: 1, PageSize: 12})
	require.NoError(t, err)
	require.False(t, fresh.Stale)
	failRefresh.Store(true)

	stale, err := marketplace.list(context.Background(), "0.2.0", PluginMarketplaceQuery{Page: 1, PageSize: 12, Refresh: true})
	require.NoError(t, err)
	require.True(t, stale.Stale)
	require.Len(t, stale.Items, 1)
	assert.Equal(t, "owner/plugin-0000", stale.Items[0].Repository)
}

func TestMarketplacePageBoundsHugePageWithoutOverflow(t *testing.T) {
	items := []PluginMarketplaceItem{{ID: "one"}, {ID: "two"}}
	page := marketplacePage(items, int(^uint(0)>>1), pluginMarketplaceMaxPageSize)
	assert.Empty(t, page)
	assert.Equal(t, []PluginMarketplaceItem{{ID: "one"}, {ID: "two"}}, items)
}

func testMarketplace(roundTrip marketplaceRoundTripFunc) *githubPluginMarketplace {
	return &githubPluginMarketplace{
		client:         &http.Client{Transport: roundTrip},
		cache:          make(map[string]pluginMarketplaceCacheEntry),
		officialOwners: make(map[string]struct{}),
		verifiedOwners: make(map[string]struct{}),
	}
}

func marketplaceRepositories(start, count int) []githubRepository {
	result := make([]githubRepository, 0, count)
	for index := 0; index < count; index++ {
		result = append(result, marketplaceRepository(start+index))
	}
	return result
}

func marketplaceRepository(index int) githubRepository {
	repository := githubRepository{
		FullName:      fmt.Sprintf("owner/plugin-%04d", index),
		HTMLURL:       fmt.Sprintf("https://github.com/owner/plugin-%04d", index),
		DefaultBranch: "main",
		Topics:        []string{pluginMarketplaceTopic},
		UpdatedAt:     time.Unix(int64(index), 0).UTC(),
	}
	repository.Owner.Login = "owner"
	return repository
}

func marketplaceSearchResponse(status, total int, repositories []githubRepository, remaining int) *http.Response {
	body, err := json.Marshal(githubRepositorySearchResponse{TotalCount: total, Items: repositories})
	if err != nil {
		panic(err)
	}
	return marketplaceHTTPResponse(status, string(body), strconv.Itoa(remaining))
}

func marketplaceHTTPResponse(status int, body, remaining string) *http.Response {
	header := make(http.Header)
	if strings.TrimSpace(remaining) != "" {
		header.Set("X-RateLimit-Remaining", remaining)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
