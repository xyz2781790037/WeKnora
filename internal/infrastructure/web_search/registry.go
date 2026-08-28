package web_search

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ProviderFactory creates a new web search provider instance from parameters.
type ProviderFactory func(params types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error)

// Registry manages web search provider type registrations.
// It maps provider type IDs (e.g., "bing", "google") to their factory functions.
// Instances are created on-demand with tenant-specific parameters.
type Registry struct {
	factories        map[string]ProviderFactory
	externalMetadata map[string]types.WebSearchProviderTypeInfo
	mu               sync.RWMutex
}

// NewRegistry creates a new web search provider registry
func NewRegistry() *Registry {
	return &Registry{
		factories:        make(map[string]ProviderFactory),
		externalMetadata: make(map[string]types.WebSearchProviderTypeInfo),
	}
}

func (r *Registry) RegisterExternal(id string, factory ProviderFactory, metadata types.WebSearchProviderTypeInfo) error {
	id = strings.TrimSpace(id)
	if id == "" || factory == nil {
		return fmt.Errorf("external web search provider id and factory are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[id] = factory
	metadata.ID = id
	r.externalMetadata[id] = metadata
	return nil
}

func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.factories, id)
	delete(r.externalMetadata, id)
}

func (r *Registry) Has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.factories[id]
	return ok
}

func (r *Registry) ProviderTypes() []types.WebSearchProviderTypeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := append([]types.WebSearchProviderTypeInfo(nil), types.GetWebSearchProviderTypes()...)
	for _, metadata := range r.externalMetadata {
		result = append(result, metadata)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) ExternalMetadata(id string) (types.WebSearchProviderTypeInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	metadata, ok := r.externalMetadata[id]
	return metadata, ok
}

// Register registers a provider type factory by ID
func (r *Registry) Register(id string, factory ProviderFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[id] = factory
}

// CreateProvider creates a provider instance by type with the given parameters.
func (r *Registry) CreateProvider(providerType string, params types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error) {
	r.mu.RLock()
	factory, ok := r.factories[providerType]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("web search provider type %s not registered", providerType)
	}
	return factory(params)
}

// List returns all registered provider type IDs in stable order. It exposes
// catalog metadata only; tenant credentials and provider instances remain in
// the existing web-search service.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.factories))
	for id := range r.factories {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
