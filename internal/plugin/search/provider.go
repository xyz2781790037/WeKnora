package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

type Provider struct {
	pluginID     string
	runtime      runtimeclient.Gateway
	params       types.WebSearchProviderParameters
	configSchema map[string]any
}

func NewProvider(pluginID string, runtime runtimeclient.Gateway, params types.WebSearchProviderParameters, configSchema map[string]any) (*Provider, error) {
	if strings.TrimSpace(pluginID) == "" || runtime == nil {
		return nil, errors.New("plugin id and runtime are required")
	}
	return &Provider{pluginID: pluginID, runtime: runtime, params: params, configSchema: configSchema}, nil
}

func (p *Provider) Name() string { return p.pluginID }

func (p *Provider) Search(ctx context.Context, query string, maxResults int, includeDate bool) ([]*types.WebSearchResult, error) {
	limit := externalSearchLimit(maxResults)
	if limit == 0 {
		return []*types.WebSearchResult{}, nil
	}
	client, err := p.runtime.Search()
	if err != nil {
		return nil, err
	}
	configJSON, err := encodeConfig(p.params, p.configSchema)
	if err != nil {
		return nil, err
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	response, err := client.Search(ctx, &pluginv1.WebSearchRequest{
		Context:    &pluginv1.InvocationContext{PluginId: p.pluginID, TenantId: tenantID},
		ConfigJson: configJSON,
		Query:      query,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return convertSearchResults(response, int(limit))
}

func externalSearchLimit(maxResults int) uint32 {
	if maxResults <= 0 {
		return 0
	}
	if uint64(maxResults) > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(maxResults)
}

func convertSearchResults(response *pluginv1.WebSearchResponse, limit int) ([]*types.WebSearchResult, error) {
	if limit <= 0 {
		return []*types.WebSearchResult{}, nil
	}
	items := response.GetResults()
	if len(items) > limit {
		items = items[:limit]
	}
	results := make([]*types.WebSearchResult, 0, len(items))
	for _, item := range items {
		if item == nil {
			return nil, errors.New("web search plugin returned an empty result")
		}
		parsedURL, err := url.Parse(strings.TrimSpace(item.GetUrl()))
		if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return nil, fmt.Errorf("web search plugin returned invalid result URL %q", item.GetUrl())
		}
		result := &types.WebSearchResult{
			Title: item.GetTitle(), URL: parsedURL.String(), Snippet: item.GetSnippet(),
			Content: item.GetSnippet(), Source: item.GetSource(),
		}
		if publishedAt := item.GetPublishedAt(); publishedAt != nil && publishedAt.IsValid() {
			value := publishedAt.AsTime().UTC()
			result.PublishedAt = &value
		}
		results = append(results, result)
	}
	return results, nil
}

func encodeConfig(params types.WebSearchProviderParameters, configSchema map[string]any) ([]byte, error) {
	values := make(map[string]any, len(params.ExtraConfig)+4)
	for key, value := range params.ExtraConfig {
		values[key] = value
	}
	if params.APIKey != "" {
		values["api_key"] = params.APIKey
	}
	if params.EngineID != "" {
		values["engine_id"] = params.EngineID
	}
	if params.BaseURL != "" {
		values["base_url"] = params.BaseURL
	}
	if params.ProxyURL != "" {
		values["proxy_url"] = params.ProxyURL
	}
	values = pluginsdk.NormalizeConfigValues(configSchema, values)
	if err := pluginsdk.ValidateConfigValues(configSchema, values); err != nil {
		return nil, fmt.Errorf("invalid web search plugin config: %w", err)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode web search plugin config: %w", err)
	}
	return encoded, nil
}
