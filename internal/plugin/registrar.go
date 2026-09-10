// Package plugin integrates verified out-of-process plugins with WeKnora's
// existing in-process capability registries.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	core "github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	infraWebSearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	modelprovider "github.com/Tencent/WeKnora/internal/models/provider"
	pluginDatasource "github.com/Tencent/WeKnora/internal/plugin/datasource"
	pluginModel "github.com/Tencent/WeKnora/internal/plugin/model"
	pluginParser "github.com/Tencent/WeKnora/internal/plugin/parser"
	pluginRetrieval "github.com/Tencent/WeKnora/internal/plugin/retrieval"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	pluginSearch "github.com/Tencent/WeKnora/internal/plugin/search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

// Registrar owns the mapping between a plugin installation and the existing
// capability registries. It never receives tenant credentials.
type Registrar struct {
	repo        interfaces.PluginRepository
	runtime     runtimeclient.Gateway
	connectors  *core.ConnectorRegistry
	webSearches *infraWebSearch.Registry

	mu                 sync.Mutex
	connectorOwnership map[string]string
	webSearchOwnership map[string]string
	parserOwnership    map[string]string
	modelOwnership     map[string]string
	retrievalOwnership map[string]string
}

func NewRegistrar(
	repo interfaces.PluginRepository,
	runtime runtimeclient.Gateway,
	connectors *core.ConnectorRegistry,
	webSearches *infraWebSearch.Registry,
) interfaces.PluginRegistrar {
	return &Registrar{
		repo:               repo,
		runtime:            runtime,
		connectors:         connectors,
		webSearches:        webSearches,
		connectorOwnership: make(map[string]string),
		webSearchOwnership: make(map[string]string),
		parserOwnership:    make(map[string]string),
		modelOwnership:     make(map[string]string),
		retrievalOwnership: make(map[string]string),
	}
}

func (r *Registrar) Register(plugin *types.Plugin) error {
	if plugin == nil || plugin.Origin != types.PluginOriginExternal {
		return nil
	}
	manifest, err := decodeManifest(plugin.Manifest)
	if err != nil {
		return fmt.Errorf("decode manifest for plugin %s: %w", plugin.ID, err)
	}
	if containsType(manifest.NormalizedTypes(), "data_source") {
		if err := r.registerDataSource(plugin, manifest); err != nil {
			return err
		}
	}
	if containsType(manifest.NormalizedTypes(), "web_search") {
		if err := r.registerWebSearch(plugin, manifest); err != nil {
			r.unregisterDataSource(plugin)
			return err
		}
	}
	if containsType(manifest.NormalizedTypes(), "document_parser") {
		if err := r.registerParser(plugin, manifest); err != nil {
			r.unregisterWebSearch(plugin)
			r.unregisterDataSource(plugin)
			return err
		}
	}
	if containsType(manifest.NormalizedTypes(), "model_provider") {
		if err := r.registerModelProvider(plugin, manifest); err != nil {
			r.unregisterParser(plugin)
			r.unregisterWebSearch(plugin)
			r.unregisterDataSource(plugin)
			return err
		}
	}
	if containsType(manifest.NormalizedTypes(), "retrieval_engine") {
		if err := r.registerRetrievalEngine(plugin, manifest); err != nil {
			r.unregisterModelProvider(plugin)
			r.unregisterParser(plugin)
			r.unregisterWebSearch(plugin)
			r.unregisterDataSource(plugin)
			return err
		}
	}
	return nil
}

func (r *Registrar) registerDataSource(plugin *types.Plugin, manifest *pluginsdk.Manifest) error {
	connectorType := strings.TrimSpace(manifest.Spec.ConnectorType)
	connector, err := pluginDatasource.NewConnector(plugin.ID, connectorType, r.runtime, cloneSchema(manifest.Spec.Config.Schema))
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.connectorOwnership[connectorType]
	if owner != "" && owner != plugin.ID {
		return fmt.Errorf("connector type %s is already registered by plugin %s", connectorType, owner)
	}
	if owner == "" {
		if _, getErr := r.connectors.Get(connectorType); getErr == nil {
			return fmt.Errorf("connector type %s conflicts with a built-in connector", connectorType)
		}
	}
	metadata := core.ConnectorMetadata{
		Type:         connectorType,
		Name:         manifest.Metadata.Name,
		Description:  manifest.Metadata.Description,
		Icon:         manifest.Spec.Icon,
		Priority:     100,
		AuthType:     externalAuthType(manifest),
		Capabilities: append([]string(nil), manifest.Spec.Capabilities...),
		Origin:       types.PluginOriginExternal,
		PluginID:     plugin.ID,
		ConfigSchema: cloneSchema(manifest.Spec.Config.Schema),
		SecretFields: append([]string(nil), manifest.Spec.Config.SecretFields...),
	}
	if err := r.connectors.RegisterWithMetadata(connector, metadata); err != nil {
		return err
	}
	r.connectorOwnership[connectorType] = plugin.ID
	return nil
}

func (r *Registrar) registerWebSearch(plugin *types.Plugin, manifest *pluginsdk.Manifest) error {
	if r.webSearches == nil {
		return errors.New("web search registry is unavailable")
	}
	for _, field := range manifest.Spec.Config.SecretFields {
		if field != "api_key" {
			return fmt.Errorf("web search plugin secret field %q is unsupported; use api_key", field)
		}
	}
	providerID := plugin.ID
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.webSearchOwnership[providerID]
	if owner != "" && owner != plugin.ID {
		return fmt.Errorf("web search provider %s is already registered by plugin %s", providerID, owner)
	}
	if owner == "" && r.webSearches.Has(providerID) {
		return fmt.Errorf("web search provider %s conflicts with a built-in provider", providerID)
	}
	metadata := webSearchMetadata(manifest)
	if err := r.webSearches.RegisterExternal(providerID, func(params types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error) {
		return pluginSearch.NewProvider(plugin.ID, r.runtime, params, cloneSchema(manifest.Spec.Config.Schema))
	}, metadata); err != nil {
		return err
	}
	r.webSearchOwnership[providerID] = plugin.ID
	return nil
}

func (r *Registrar) registerParser(plugin *types.Plugin, manifest *pluginsdk.Manifest) error {
	if len(manifest.Spec.Config.SecretFields) > 0 {
		return errors.New("document parser plugin secret fields are not supported yet")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.parserOwnership[plugin.ID]
	if owner != "" && owner != plugin.ID {
		return fmt.Errorf("parser engine %s is already registered by plugin %s", plugin.ID, owner)
	}
	fileTypes := make([]string, 0)
	for _, capability := range manifest.Spec.Capabilities {
		if value, ok := strings.CutPrefix(capability, "file_type:"); ok && strings.TrimSpace(value) != "" {
			fileTypes = append(fileTypes, strings.TrimSpace(value))
		}
	}
	err := docparser.RegisterExternalEngineWithMetadata(plugin.ID, manifest.Metadata.Description, fileTypes, cloneSchema(manifest.Spec.Config.Schema), manifest.Spec.Config.SecretFields, func(overrides map[string]string) interfaces.DocReader {
		return pluginParser.NewReader(plugin.ID, r.runtime, overrides, cloneSchema(manifest.Spec.Config.Schema))
	})
	if err != nil {
		return err
	}
	r.parserOwnership[plugin.ID] = plugin.ID
	return nil
}

func (r *Registrar) registerModelProvider(plugin *types.Plugin, manifest *pluginsdk.Manifest) error {
	for _, field := range manifest.Spec.Config.SecretFields {
		if field != "api_key" && field != "app_secret" {
			return fmt.Errorf("model provider plugin secret field %q is unsupported", field)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.modelOwnership[plugin.ID]
	if owner != "" && owner != plugin.ID {
		return fmt.Errorf("model provider %s is already registered by plugin %s", plugin.ID, owner)
	}
	if owner == "" {
		if _, exists := modelprovider.Get(modelprovider.ProviderName(plugin.ID)); exists {
			return fmt.Errorf("model provider %s conflicts with a built-in provider", plugin.ID)
		}
	}
	if err := pluginModel.Register(plugin.ID, r.runtime, manifest.Spec.Capabilities, cloneSchema(manifest.Spec.Config.Schema)); err != nil {
		return err
	}
	modelprovider.RegisterExternal(modelProviderMetadata(manifest))
	r.modelOwnership[plugin.ID] = plugin.ID
	return nil
}

func (r *Registrar) registerRetrievalEngine(plugin *types.Plugin, manifest *pluginsdk.Manifest) error {
	engineType := types.RetrieverEngineType(strings.TrimSpace(manifest.Spec.RetrieverEngineType))
	fields, err := vectorStoreFieldsFromSchema(manifest.Spec.Config.Schema, manifest.Spec.Config.SecretFields)
	if err != nil {
		return fmt.Errorf("retrieval engine config schema: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := r.retrievalOwnership[string(engineType)]
	if owner != "" && owner != plugin.ID {
		return fmt.Errorf("retrieval engine type %s is already registered by plugin %s", engineType, owner)
	}
	if err := pluginRetrieval.Register(
		plugin.ID,
		engineType,
		r.runtime,
		manifest.Spec.Capabilities,
		cloneSchema(manifest.Spec.Config.Schema),
		manifest.Spec.Config.SecretFields,
	); err != nil {
		return err
	}
	metadata := types.VectorStoreTypeInfo{
		Type: string(engineType), DisplayName: manifest.Metadata.Name,
		ConnectionFields: fields, External: true, PluginID: plugin.ID,
	}
	if err := types.RegisterExternalVectorStoreType(metadata); err != nil {
		pluginRetrieval.Unregister(engineType, plugin.ID)
		return err
	}
	r.retrievalOwnership[string(engineType)] = plugin.ID
	return nil
}

func (r *Registrar) Unregister(plugin *types.Plugin) {
	if plugin == nil {
		return
	}
	r.unregisterDataSource(plugin)
	r.unregisterWebSearch(plugin)
	r.unregisterParser(plugin)
	r.unregisterModelProvider(plugin)
	r.unregisterRetrievalEngine(plugin)
}

func (r *Registrar) unregisterRetrievalEngine(plugin *types.Plugin) {
	if plugin == nil {
		return
	}
	manifest, err := decodeManifest(plugin.Manifest)
	if err != nil {
		return
	}
	engineType := types.RetrieverEngineType(strings.TrimSpace(manifest.Spec.RetrieverEngineType))
	if engineType == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retrievalOwnership[string(engineType)] != plugin.ID {
		return
	}
	types.UnregisterExternalVectorStoreType(engineType, plugin.ID)
	pluginRetrieval.Unregister(engineType, plugin.ID)
	delete(r.retrievalOwnership, string(engineType))
}

func (r *Registrar) unregisterDataSource(plugin *types.Plugin) {
	if plugin == nil || plugin.ConnectorType == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.connectorOwnership[plugin.ConnectorType] != plugin.ID {
		return
	}
	r.connectors.Unregister(plugin.ConnectorType)
	delete(r.connectorOwnership, plugin.ConnectorType)
}

func (r *Registrar) unregisterWebSearch(plugin *types.Plugin) {
	if plugin == nil || r.webSearches == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.webSearchOwnership[plugin.ID] != plugin.ID {
		return
	}
	r.webSearches.Unregister(plugin.ID)
	delete(r.webSearchOwnership, plugin.ID)
}

func (r *Registrar) unregisterParser(plugin *types.Plugin) {
	if plugin == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parserOwnership[plugin.ID] != plugin.ID {
		return
	}
	docparser.UnregisterExternalEngine(plugin.ID)
	delete(r.parserOwnership, plugin.ID)
}

func (r *Registrar) unregisterModelProvider(plugin *types.Plugin) {
	if plugin == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.modelOwnership[plugin.ID] != plugin.ID {
		return
	}
	pluginModel.Unregister(plugin.ID)
	modelprovider.Unregister(modelprovider.ProviderName(plugin.ID))
	delete(r.modelOwnership, plugin.ID)
}

// RestoreEnabled is called after repositories and the runtime client are ready.
// Individual failures are joined so startup can report all broken plugins.
func (r *Registrar) RestoreEnabled(ctx context.Context) error {
	plugins, err := r.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	if len(plugins) == 0 {
		return nil
	}
	runtimeAPI, err := r.runtime.Runtime()
	if err != nil {
		return err
	}
	var restoreErr error
	for _, installed := range plugins {
		status, startErr := runtimeAPI.Start(ctx, &pluginv1.PluginTargetRequest{PluginId: installed.ID})
		if startErr != nil {
			now := time.Now().UTC()
			_ = r.repo.UpdateRuntimeState(ctx, installed.ID, types.PluginRuntimeError, startErr.Error(), &now)
			restoreErr = errors.Join(restoreErr, fmt.Errorf("start plugin %s: %w", installed.ID, startErr))
			continue
		}
		applyRuntimeStatus(installed, status)
		if registerErr := r.Register(installed); registerErr != nil {
			_, stopErr := runtimeAPI.Stop(ctx, &pluginv1.PluginTargetRequest{PluginId: installed.ID})
			now := time.Now().UTC()
			message := "register restored plugin capabilities: " + registerErr.Error()
			_ = r.repo.UpdateRuntimeState(ctx, installed.ID, types.PluginRuntimeError, message, &now)
			restoreErr = errors.Join(restoreErr, fmt.Errorf("register plugin %s: %w", installed.ID, registerErr))
			if stopErr != nil {
				restoreErr = errors.Join(restoreErr, fmt.Errorf("stop unregistered plugin %s: %w", installed.ID, stopErr))
			}
			continue
		}
		now := time.Now().UTC()
		if updateErr := r.repo.UpdateRuntimeState(ctx, installed.ID, installed.RuntimeState, installed.HealthMessage, &now); updateErr != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("save restored plugin %s state: %w", installed.ID, updateErr))
		}
	}
	return restoreErr
}

func applyRuntimeStatus(plugin *types.Plugin, status *pluginv1.PluginRuntimeStatus) {
	if plugin == nil || status == nil {
		return
	}
	plugin.ImageDigest = status.GetImageDigest()
	plugin.HealthMessage = status.GetMessage()
	switch status.GetState() {
	case pluginv1.RuntimeState_RUNTIME_STATE_STOPPED:
		plugin.RuntimeState = types.PluginRuntimeStopped
	case pluginv1.RuntimeState_RUNTIME_STATE_STARTING:
		plugin.RuntimeState = types.PluginRuntimeStarting
	case pluginv1.RuntimeState_RUNTIME_STATE_RUNNING:
		plugin.RuntimeState = types.PluginRuntimeRunning
	case pluginv1.RuntimeState_RUNTIME_STATE_UNHEALTHY:
		plugin.RuntimeState = types.PluginRuntimeUnhealthy
	case pluginv1.RuntimeState_RUNTIME_STATE_ERROR:
		plugin.RuntimeState = types.PluginRuntimeError
	default:
		plugin.RuntimeState = types.PluginRuntimeUnknown
	}
}

func decodeManifest(data types.JSON) (*pluginsdk.Manifest, error) {
	var manifest pluginsdk.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func containsType(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func externalAuthType(manifest *pluginsdk.Manifest) string {
	if len(manifest.Spec.Config.SecretFields) > 0 {
		return "token"
	}
	return "custom"
}

func cloneSchema(schema map[string]any) map[string]any {
	encoded, _ := json.Marshal(schema)
	var cloned map[string]any
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func webSearchMetadata(manifest *pluginsdk.Manifest) types.WebSearchProviderTypeInfo {
	metadata := types.WebSearchProviderTypeInfo{
		ID: manifest.Metadata.ID, Name: manifest.Metadata.Name,
		Description:  manifest.Metadata.Description,
		ConfigSchema: cloneSchema(manifest.Spec.Config.Schema),
	}
	secretFields := make(map[string]bool, len(manifest.Spec.Config.SecretFields))
	for _, field := range manifest.Spec.Config.SecretFields {
		secretFields[field] = true
	}
	metadata.RequiresAPIKey = secretFields["api_key"]
	properties, _ := manifest.Spec.Config.Schema["properties"].(map[string]any)
	required := schemaRequiredFields(manifest.Spec.Config.Schema["required"])
	for key, raw := range properties {
		property, _ := raw.(map[string]any)
		switch key {
		case "api_key":
			continue
		case "engine_id":
			metadata.RequiresEngineID = required[key]
			continue
		case "base_url":
			metadata.RequiresBaseURL = required[key]
			continue
		case "proxy_url":
			metadata.SupportsProxy = true
			continue
		}
		field := types.WebSearchProviderConfigField{
			Key: key, Label: stringValue(property["title"], key),
			Type: stringValue(property["type"], "string"), Required: required[key],
			Default: schemaConfigDefault(property["default"], stringValue(property["type"], "string")), Description: stringValue(property["description"], ""),
		}
		if property["default"] == nil {
			field.Default = ""
		}
		if enumValues, ok := property["enum"].([]any); ok && len(enumValues) > 0 {
			field.Type = "select"
			for _, value := range enumValues {
				text := fmt.Sprint(value)
				field.Options = append(field.Options, types.WebSearchProviderConfigFieldOption{Label: text, Value: text})
			}
		}
		metadata.ConfigFields = append(metadata.ConfigFields, field)
	}
	sort.Slice(metadata.ConfigFields, func(i, j int) bool { return metadata.ConfigFields[i].Key < metadata.ConfigFields[j].Key })
	return metadata
}

func schemaConfigDefault(value any, valueType string) string {
	if value == nil {
		return ""
	}
	if valueType == "string" {
		return fmt.Sprint(value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func schemaRequiredFields(raw any) map[string]bool {
	result := map[string]bool{}
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			result[fmt.Sprint(value)] = true
		}
	case []string:
		for _, value := range values {
			result[value] = true
		}
	}
	return result
}

func stringValue(raw any, fallback string) string {
	if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func modelProviderMetadata(manifest *pluginsdk.Manifest) modelprovider.ProviderInfo {
	info := modelprovider.ProviderInfo{
		Name: modelprovider.ProviderName(manifest.Metadata.ID), DisplayName: manifest.Metadata.Name,
		Description: manifest.Metadata.Description, DefaultURLs: map[types.ModelType]string{},
		ConfigSchema: cloneSchema(manifest.Spec.Config.Schema),
		SecretFields: append([]string(nil), manifest.Spec.Config.SecretFields...),
	}
	for _, capability := range manifest.Spec.Capabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case "chat":
			info.ModelTypes = append(info.ModelTypes, types.ModelTypeKnowledgeQA)
		case "embed", "embedding":
			info.ModelTypes = append(info.ModelTypes, types.ModelTypeEmbedding)
		case "rerank":
			info.ModelTypes = append(info.ModelTypes, types.ModelTypeRerank)
		}
	}
	for _, field := range manifest.Spec.Config.SecretFields {
		if field == "api_key" {
			info.RequiresAuth = true
		}
	}
	properties, _ := manifest.Spec.Config.Schema["properties"].(map[string]any)
	required := schemaRequiredFields(manifest.Spec.Config.Schema["required"])
	for key, raw := range properties {
		if key == "api_key" || key == "app_secret" || key == "base_url" {
			continue
		}
		property, _ := raw.(map[string]any)
		field := modelprovider.ExtraFieldConfig{
			Key: key, Label: stringValue(property["title"], key), Type: stringValue(property["type"], "string"),
			Required: required[key], Default: fmt.Sprint(property["default"]),
			Placeholder: stringValue(property["description"], ""),
		}
		if property["default"] == nil {
			field.Default = ""
		}
		if enumValues, ok := property["enum"].([]any); ok && len(enumValues) > 0 {
			field.Type = "select"
			for _, rawValue := range enumValues {
				value := fmt.Sprint(rawValue)
				field.Options = append(field.Options, struct {
					Label string `json:"label"`
					Value string `json:"value"`
				}{Label: value, Value: value})
			}
		}
		info.ExtraFields = append(info.ExtraFields, field)
	}
	sort.Slice(info.ExtraFields, func(i, j int) bool { return info.ExtraFields[i].Key < info.ExtraFields[j].Key })
	return info
}

func vectorStoreFieldsFromSchema(schema map[string]any, secretFields []string) ([]types.VectorStoreFieldInfo, error) {
	properties, _ := schema["properties"].(map[string]any)
	required := schemaRequiredFields(schema["required"])
	secrets := make(map[string]bool, len(secretFields))
	for _, field := range secretFields {
		secrets[field] = true
	}
	fields := make([]types.VectorStoreFieldInfo, 0, len(properties))
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("property %s must be an object", name)
		}
		fieldType := stringValue(property["type"], "string")
		switch fieldType {
		case "integer", "number":
			fieldType = "number"
		case "string", "boolean", "array":
		default:
			return nil, fmt.Errorf("property %s uses unsupported type %s", name, fieldType)
		}
		field := types.VectorStoreFieldInfo{
			Name: name, Type: fieldType, Required: required[name], Sensitive: secrets[name],
			Default: property["default"], Description: stringValue(property["description"], ""),
		}
		if minimum, ok := numberValue(property["minimum"]); ok {
			field.Min = &minimum
		}
		if maximum, ok := numberValue(property["maximum"]); ok {
			field.Max = &maximum
		}
		if enumValues, ok := property["enum"].([]any); ok {
			for _, value := range enumValues {
				field.Enum = append(field.Enum, fmt.Sprint(value))
			}
		}
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields, nil
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}
