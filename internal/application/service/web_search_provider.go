package service

import (
	"context"
	"fmt"
	"strings"

	infra_web_search "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

// webSearchProviderService implements interfaces.WebSearchProviderService
type webSearchProviderService struct {
	repo     interfaces.WebSearchProviderRepository
	registry *infra_web_search.Registry
}

// NewWebSearchProviderService creates a new web search provider service
func NewWebSearchProviderService(repo interfaces.WebSearchProviderRepository, registry *infra_web_search.Registry) interfaces.WebSearchProviderService {
	return &webSearchProviderService{repo: repo, registry: registry}
}

// CreateProvider creates a new web search provider configuration.
func (s *webSearchProviderService) CreateProvider(ctx context.Context, provider *types.WebSearchProviderEntity) error {
	if provider.TenantID == 0 {
		return fmt.Errorf("tenant ID is required")
	}

	if s.registry == nil || !s.registry.Has(string(provider.Provider)) {
		return fmt.Errorf("invalid provider type: %s", provider.Provider)
	}

	if err := validateProviderParameters(provider.Provider, provider.Parameters); err != nil {
		return err
	}
	if err := validateExternalWebSearchParameters(s.registry, provider.Provider, provider.Parameters); err != nil {
		return err
	}

	if provider.IsDefault {
		if err := s.repo.ClearDefault(ctx, provider.TenantID, ""); err != nil {
			logger.Warnf(ctx, "Failed to clear default providers: %v", err)
		}
	}

	logger.Infof(ctx, "Creating web search provider: tenant=%d, name=%s, type=%s", provider.TenantID, provider.Name, provider.Provider)
	return s.repo.Create(ctx, provider)
}

// UpdateProvider updates an existing provider.
func (s *webSearchProviderService) UpdateProvider(ctx context.Context, provider *types.WebSearchProviderEntity) error {
	if provider.TenantID == 0 {
		return fmt.Errorf("tenant ID is required")
	}

	// Validate provider type if set
	if provider.Provider != "" && (s.registry == nil || !s.registry.Has(string(provider.Provider))) {
		return fmt.Errorf("invalid provider type: %s", provider.Provider)
	}

	if provider.IsDefault {
		if err := s.repo.ClearDefault(ctx, provider.TenantID, provider.ID); err != nil {
			logger.Warnf(ctx, "Failed to clear default providers: %v", err)
		}
	}

	if provider.Provider != "" {
		if err := validateProviderParameters(provider.Provider, provider.Parameters); err != nil {
			return err
		}
		if err := validateExternalWebSearchParameters(s.registry, provider.Provider, provider.Parameters); err != nil {
			return err
		}
	}

	logger.Infof(ctx, "Updating web search provider: tenant=%d, id=%s", provider.TenantID, provider.ID)
	return s.repo.Update(ctx, provider)
}

// UpdateProviderCredentials writes the api_key credential field. Web search
// providers are stateless from our side — every search call rebuilds a
// transport from current Parameters — so no cache invalidation is required.
func (s *webSearchProviderService) UpdateProviderCredentials(
	ctx context.Context, tenantID uint64, id string, apiKey *string,
) (*types.WebSearchProviderEntity, error) {
	existing, err := s.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("web search provider not found")
	}

	if apiKey != nil && *apiKey != "" && *apiKey != existing.Parameters.APIKey {
		existing.Parameters.APIKey = *apiKey
		if err := s.repo.Update(ctx, existing); err != nil {
			return nil, err
		}
		logger.Infof(ctx, "WebSearch provider credentials updated: tenant=%d id=%s", tenantID, id)
	}
	return existing, nil
}

// ClearProviderCredential clears the api_key credential. Idempotent.
func (s *webSearchProviderService) ClearProviderCredential(
	ctx context.Context, tenantID uint64, id, field string,
) error {
	if field != "api_key" {
		return fmt.Errorf("unknown credential field: %s", field)
	}
	existing, err := s.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("web search provider not found")
	}
	if existing.Parameters.APIKey == "" {
		return nil
	}
	existing.Parameters.APIKey = ""
	if err := s.repo.Update(ctx, existing); err != nil {
		return err
	}
	logger.Infof(ctx, "WebSearch provider credential cleared by user: tenant=%d id=%s field=%s", tenantID, id, field)
	return nil
}

// DeleteProvider deletes a provider by tenant + id.
func (s *webSearchProviderService) DeleteProvider(ctx context.Context, tenantID uint64, id string) error {
	logger.Infof(ctx, "Deleting web search provider: tenant=%d, id=%s", tenantID, id)
	return s.repo.Delete(ctx, tenantID, id)
}

// isValidProviderType reports whether provider is one of the built-in types.
// External types are resolved through Registry.Has at the service boundary.
func isValidProviderType(provider types.WebSearchProviderType) bool {
	for _, item := range types.GetWebSearchProviderTypes() {
		if item.ID == string(provider) {
			return true
		}
	}
	return false
}

// validateProviderParameters validates required parameters for each provider type
func validateProviderParameters(provider types.WebSearchProviderType, params types.WebSearchProviderParameters) error {
	switch provider {
	case types.WebSearchProviderTypeBing:
		if params.APIKey == "" {
			return fmt.Errorf("API key is required for Bing provider")
		}
	case types.WebSearchProviderTypeGoogle:
		if params.APIKey == "" {
			return fmt.Errorf("API key is required for Google provider")
		}
		if params.EngineID == "" {
			return fmt.Errorf("engine ID is required for Google provider")
		}
	case types.WebSearchProviderTypeTavily:
		if params.APIKey == "" {
			return fmt.Errorf("API key is required for Tavily provider")
		}
	case types.WebSearchProviderTypeOllama:
		if params.APIKey == "" {
			return fmt.Errorf("API key is required for Ollama provider")
		}
	case types.WebSearchProviderTypeBaidu:
		if params.APIKey == "" {
			return fmt.Errorf("API key is required for Baidu provider")
		}
	case types.WebSearchProviderTypeExa:
		if params.APIKey == "" {
			return fmt.Errorf("API key is required for Exa provider")
		}
	case types.WebSearchProviderTypeZhipu:
		if err := infra_web_search.ValidateZhipuParameters(params); err != nil {
			return err
		}
	case types.WebSearchProviderTypeMetaso:
		if err := infra_web_search.ValidateMetasoParameters(params); err != nil {
			return err
		}
	case types.WebSearchProviderTypeDuckDuckGo:
		// No API key required
	case types.WebSearchProviderTypeKeenable:
		// No API key required (keyless by default; an optional key lifts the rate limit)
	case types.WebSearchProviderTypeSearxng:
		if err := infra_web_search.ValidateSearxngBaseURL(params.BaseURL); err != nil {
			return err
		}
	}
	if err := validateOptionalProxyURL(params.ProxyURL); err != nil {
		return err
	}
	return nil
}

func validateExternalWebSearchParameters(registry *infra_web_search.Registry, provider types.WebSearchProviderType, params types.WebSearchProviderParameters) error {
	if registry == nil {
		return nil
	}
	metadata, ok := registry.ExternalMetadata(string(provider))
	if !ok || len(metadata.ConfigSchema) == 0 {
		return nil
	}
	values := make(map[string]any, len(params.ExtraConfig)+4)
	for key, value := range params.ExtraConfig {
		values[key] = value
	}
	for key, value := range map[string]string{
		"api_key": params.APIKey, "engine_id": params.EngineID,
		"base_url": params.BaseURL, "proxy_url": params.ProxyURL,
	} {
		if strings.TrimSpace(value) != "" {
			values[key] = value
		}
	}
	if err := pluginsdk.ValidateConfigValues(metadata.ConfigSchema, values); err != nil {
		return fmt.Errorf("invalid external web search configuration: %w", err)
	}
	return nil
}

func validateOptionalProxyURL(proxyURL string) error {
	return infra_web_search.ValidateProxyURL(proxyURL)
}
