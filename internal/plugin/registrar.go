// Package plugin integrates verified out-of-process plugins with WeKnora's
// existing in-process capability registries.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	core "github.com/Tencent/WeKnora/internal/datasource"
	pluginDatasource "github.com/Tencent/WeKnora/internal/plugin/datasource"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

// Registrar owns the mapping between a plugin installation and the existing
// capability registries. It never receives tenant credentials.
type Registrar struct {
	repo       interfaces.PluginRepository
	runtime    runtimeclient.Gateway
	connectors *core.ConnectorRegistry

	mu                 sync.Mutex
	connectorOwnership map[string]string
}

func NewRegistrar(
	repo interfaces.PluginRepository,
	runtime runtimeclient.Gateway,
	connectors *core.ConnectorRegistry,
) interfaces.PluginRegistrar {
	return &Registrar{
		repo:               repo,
		runtime:            runtime,
		connectors:         connectors,
		connectorOwnership: make(map[string]string),
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
	if !containsType(manifest.NormalizedTypes(), "data_source") {
		return nil
	}
	connectorType := strings.TrimSpace(manifest.Spec.ConnectorType)
	connector, err := pluginDatasource.NewConnector(plugin.ID, connectorType, r.runtime)
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

func (r *Registrar) Unregister(plugin *types.Plugin) {
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
			restoreErr = errors.Join(restoreErr, fmt.Errorf("start plugin %s: %w", installed.ID, startErr))
			continue
		}
		installed.RuntimeState = types.PluginRuntimeRunning
		installed.HealthMessage = status.GetMessage()
		if registerErr := r.Register(installed); registerErr != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("register plugin %s: %w", installed.ID, registerErr))
		}
	}
	return restoreErr
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
