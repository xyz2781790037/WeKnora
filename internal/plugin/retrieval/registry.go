package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

type registration struct {
	pluginID     string
	runtime      runtimeclient.Gateway
	capabilities map[types.RetrieverType]bool
	configSchema map[string]any
	secretFields []string
}

var externalEngines = struct {
	sync.RWMutex
	items map[types.RetrieverEngineType]registration
}{items: make(map[types.RetrieverEngineType]registration)}

func Register(
	pluginID string,
	engineType types.RetrieverEngineType,
	runtime runtimeclient.Gateway,
	capabilities []string,
	configSchema map[string]any,
	secretFields []string,
) error {
	if strings.TrimSpace(pluginID) == "" || strings.TrimSpace(string(engineType)) == "" || runtime == nil {
		return errors.New("retrieval plugin id, engine type and runtime are required")
	}
	supported := make(map[types.RetrieverType]bool)
	for _, capability := range capabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case string(types.KeywordsRetrieverType):
			supported[types.KeywordsRetrieverType] = true
		case string(types.VectorRetrieverType):
			supported[types.VectorRetrieverType] = true
		}
	}
	if len(supported) == 0 {
		return errors.New("retrieval engine plugin must declare keywords or vector capability")
	}
	externalEngines.Lock()
	defer externalEngines.Unlock()
	if current, exists := externalEngines.items[engineType]; exists && current.pluginID != pluginID {
		return errors.New("retrieval engine type is already registered")
	}
	externalEngines.items[engineType] = registration{
		pluginID: pluginID, runtime: runtime, capabilities: supported,
		configSchema: cloneMap(configSchema), secretFields: append([]string(nil), secretFields...),
	}
	return nil
}

func Unregister(engineType types.RetrieverEngineType, pluginID string) {
	externalEngines.Lock()
	defer externalEngines.Unlock()
	if item, exists := externalEngines.items[engineType]; exists && item.pluginID == pluginID {
		delete(externalEngines.items, engineType)
	}
}

func IsRegistered(engineType types.RetrieverEngineType) bool {
	externalEngines.RLock()
	_, exists := externalEngines.items[engineType]
	externalEngines.RUnlock()
	return exists
}

func PrepareConfig(engineType types.RetrieverEngineType, config *types.ConnectionConfig) error {
	if config == nil {
		return errors.New("retrieval plugin config is required")
	}
	item, exists := lookup(engineType)
	if !exists {
		return errors.New("retrieval engine plugin is not enabled")
	}
	values := mergedConfig(*config)
	values = pluginsdk.NormalizeConfigValues(item.configSchema, values)
	applySchemaDefaults(item.configSchema, values)
	if err := pluginsdk.ValidateConfigValues(item.configSchema, values); err != nil {
		return err
	}
	config.PluginConfig = make(map[string]any, len(values))
	config.PluginCredentials = make(map[string]string)
	secretSet := make(map[string]bool, len(item.secretFields))
	for _, field := range item.secretFields {
		secretSet[field] = true
	}
	for key, value := range values {
		if secretSet[key] {
			text, ok := value.(string)
			if !ok {
				return errors.New("retrieval plugin secret config values must be strings")
			}
			config.PluginCredentials[key] = text
			continue
		}
		config.PluginConfig[key] = value
	}
	if len(config.PluginCredentials) == 0 {
		config.PluginCredentials = nil
	}
	return nil
}

func applySchemaDefaults(schema, values map[string]any) {
	properties, _ := schema["properties"].(map[string]any)
	for key, raw := range properties {
		if _, exists := values[key]; exists {
			continue
		}
		property, _ := raw.(map[string]any)
		value, exists := property["default"]
		if !exists {
			continue
		}
		if items, ok := value.([]any); ok {
			values[key] = append([]any(nil), items...)
			continue
		}
		values[key] = value
	}
}

func TestConnection(
	ctx context.Context,
	engineType types.RetrieverEngineType,
	config types.ConnectionConfig,
) (bool, error) {
	item, exists := lookup(engineType)
	if !exists {
		return false, nil
	}
	values := mergedConfig(config)
	if err := pluginsdk.ValidateConfigValues(item.configSchema, values); err != nil {
		return true, err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return true, fmt.Errorf("encode retrieval plugin config: %w", err)
	}
	client, err := item.runtime.Lifecycle()
	if err != nil {
		return true, err
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	response, err := client.ValidateConfig(ctx, &pluginv1.ValidateConfigRequest{
		Context:    &pluginv1.InvocationContext{PluginId: item.pluginID, TenantId: tenantID},
		ConfigJson: encoded,
	})
	if err != nil {
		return true, err
	}
	if response.GetValid() {
		return true, nil
	}
	violations := response.GetViolations()
	if len(violations) == 0 {
		return true, errors.New("retrieval plugin rejected the configuration")
	}
	parts := make([]string, 0, min(len(violations), 5))
	for _, violation := range violations[:min(len(violations), 5)] {
		if violation == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", violation.GetField(), violation.GetDescription()))
	}
	return true, fmt.Errorf("retrieval plugin rejected the configuration: %s", strings.Join(parts, "; "))
}

func NewRepository(store types.VectorStore) (interfaces.RetrieveEngineRepository, bool, error) {
	item, exists := lookup(store.EngineType)
	if !exists {
		return nil, false, nil
	}
	if err := pluginsdk.ValidateConfigValues(item.configSchema, mergedConfig(store.ConnectionConfig)); err != nil {
		return nil, true, err
	}
	return &repository{store: store, registration: item}, true, nil
}

func lookup(engineType types.RetrieverEngineType) (registration, bool) {
	externalEngines.RLock()
	item, exists := externalEngines.items[engineType]
	externalEngines.RUnlock()
	return item, exists
}

func mergedConfig(config types.ConnectionConfig) map[string]any {
	values := cloneMap(config.PluginConfig)
	if values == nil {
		values = make(map[string]any)
	}
	for key, value := range config.PluginCredentials {
		values[key] = value
	}
	return values
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
