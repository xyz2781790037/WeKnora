package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	infrawebsearch "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	modelprovider "github.com/Tencent/WeKnora/internal/models/provider"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/blang/semver/v4"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	pluginRuntimeOperationTimeout = 10 * time.Minute
	maxPluginCallTimeoutSeconds   = 60 * 60
)

var (
	ErrPluginNotFound = errors.New("plugin not found")
	ErrPluginInUse    = errors.New("plugin is used by data sources")
)

// PluginInstallInput is shared by install and upgrade operations. ManifestYAML
// is kept out of the entity until it has passed SDK validation.
type PluginInstallInput struct {
	ManifestYAML       []byte
	Image              string
	CallTimeoutSeconds int
	ActorUserID        string
}

// PluginService owns system-wide installation state and coordinates side
// effects with plugin-runtime. Tenant plugin configuration belongs to feature
// services such as DataSourceService.
type PluginService struct {
	repo        interfaces.PluginRepository
	runtime     runtimeclient.Gateway
	audit       interfaces.AuditLogService
	registrar   interfaces.PluginRegistrar
	connectors  *datasource.ConnectorRegistry
	webSearches *infrawebsearch.Registry
	hostVersion string
}

func NewPluginService(
	repo interfaces.PluginRepository,
	runtime runtimeclient.Gateway,
	audit interfaces.AuditLogService,
	registrar interfaces.PluginRegistrar,
	connectors *datasource.ConnectorRegistry,
	webSearches *infrawebsearch.Registry,
) *PluginService {
	return &PluginService{
		repo:        repo,
		runtime:     runtime,
		audit:       audit,
		registrar:   registrar,
		connectors:  connectors,
		webSearches: webSearches,
		hostVersion: readHostVersion(),
	}
}

func (s *PluginService) Install(ctx context.Context, input PluginInstallInput) (*types.Plugin, error) {
	manifest, transport, timeout, err := s.validateInstallInput(input)
	if err != nil {
		return nil, err
	}
	existing, err := s.repo.Get(ctx, manifest.Metadata.ID)
	if err != nil {
		return nil, fmt.Errorf("check installed plugin: %w", err)
	}
	if existing != nil {
		return nil, fmt.Errorf("plugin %s is already installed", manifest.Metadata.ID)
	}
	if manifest.Spec.ConnectorType != "" {
		owner, findErr := s.repo.FindByConnectorType(ctx, manifest.Spec.ConnectorType)
		if findErr != nil {
			return nil, fmt.Errorf("check connector type: %w", findErr)
		}
		if owner != nil {
			return nil, fmt.Errorf("connector type %s is already provided by plugin %s", manifest.Spec.ConnectorType, owner.ID)
		}
	}

	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, pluginRuntimeOperationTimeout)
	defer cancel()
	runtimeStatus, err := runtimeAPI.Install(opCtx, &pluginv1.InstallPluginRequest{
		Manifest:    transport,
		Image:       manifest.Spec.Image,
		CallTimeout: durationpb.New(timeout),
	})
	if err != nil {
		return nil, fmt.Errorf("install plugin in runtime: %w", err)
	}

	entity, err := pluginEntity(manifest, input.ActorUserID, timeout, runtimeStatus)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, entity); err != nil {
		// Runtime install is compensatable because no persistent WeKnora record
		// references the new container yet.
		_, _ = runtimeAPI.Uninstall(context.Background(), &pluginv1.PluginTargetRequest{PluginId: entity.ID})
		return nil, fmt.Errorf("save plugin installation: %w", err)
	}
	s.auditLifecycle(ctx, types.AuditActionPluginInstalled, entity, input.ActorUserID, map[string]any{
		"version": entity.Version,
		"image":   entity.Image,
	})
	return entity, nil
}

func (s *PluginService) Upgrade(ctx context.Context, id string, input PluginInstallInput) (*types.Plugin, error) {
	existing, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Origin != types.PluginOriginExternal {
		return nil, errors.New("built-in plugins cannot be upgraded through the external runtime")
	}
	manifest, transport, timeout, err := s.validateInstallInput(input)
	if err != nil {
		return nil, err
	}
	if manifest.Metadata.ID != existing.ID {
		return nil, errors.New("upgraded manifest id must match the installed plugin")
	}
	if manifest.Spec.ConnectorType != existing.ConnectorType {
		return nil, errors.New("connectorType cannot change during upgrade")
	}
	oldVersion, _ := semver.Parse(strings.TrimPrefix(existing.Version, "v"))
	newVersion, _ := semver.Parse(strings.TrimPrefix(manifest.Metadata.Version, "v"))
	if !newVersion.GT(oldVersion) {
		return nil, fmt.Errorf("upgrade version %s must be newer than %s", newVersion, oldVersion)
	}

	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, pluginRuntimeOperationTimeout)
	defer cancel()
	runtimeStatus, err := runtimeAPI.Upgrade(opCtx, &pluginv1.UpgradePluginRequest{
		Manifest:    transport,
		Image:       manifest.Spec.Image,
		CallTimeout: durationpb.New(timeout),
	})
	if err != nil {
		return nil, fmt.Errorf("upgrade plugin in runtime: %w", err)
	}

	updated, err := pluginEntity(manifest, existing.InstalledBy, timeout, runtimeStatus)
	if err != nil {
		return nil, err
	}
	updated.Status = existing.Status
	updated.CreatedAt = existing.CreatedAt
	if existing.Status == types.PluginStatusEnabled {
		if err := s.registrar.Register(updated); err != nil {
			registerErr := fmt.Errorf("register upgraded plugin: %w", err)
			if rollbackErr := s.rollbackRuntimeUpgrade(runtimeAPI, existing); rollbackErr != nil {
				return nil, errors.Join(registerErr, fmt.Errorf("restore previous runtime plugin: %w", rollbackErr))
			}
			return nil, registerErr
		}
	}
	if err := s.repo.Update(ctx, updated); err != nil {
		saveErr := fmt.Errorf("save upgraded plugin: %w", err)
		if rollbackErr := s.rollbackRuntimeUpgrade(runtimeAPI, existing); rollbackErr != nil {
			return nil, errors.Join(saveErr, fmt.Errorf("restore previous runtime plugin: %w", rollbackErr))
		}
		if existing.Status == types.PluginStatusEnabled {
			if registerErr := s.registrar.Register(existing); registerErr != nil {
				return nil, errors.Join(saveErr, fmt.Errorf("restore previous plugin registration: %w", registerErr))
			}
		}
		return nil, saveErr
	}
	s.auditLifecycle(ctx, types.AuditActionPluginUpgraded, updated, input.ActorUserID, map[string]any{
		"old_version": existing.Version,
		"new_version": updated.Version,
	})
	return updated, nil
}

func (s *PluginService) rollbackRuntimeUpgrade(
	runtimeAPI pluginv1.PluginRuntimeClient,
	previous *types.Plugin,
) error {
	if runtimeAPI == nil || previous == nil {
		return errors.New("previous plugin runtime state is unavailable")
	}
	var manifest pluginsdk.Manifest
	if err := json.Unmarshal(previous.Manifest, &manifest); err != nil {
		return fmt.Errorf("decode previous plugin manifest: %w", err)
	}
	transport, err := manifest.ToProto()
	if err != nil {
		return fmt.Errorf("validate previous plugin manifest: %w", err)
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), pluginRuntimeOperationTimeout)
	defer cancel()
	_, err = runtimeAPI.Upgrade(rollbackCtx, &pluginv1.UpgradePluginRequest{
		Manifest:    transport,
		Image:       previous.Image,
		CallTimeout: durationpb.New(time.Duration(previous.CallTimeoutSeconds) * time.Second),
	})
	return err
}

func (s *PluginService) Enable(ctx context.Context, id, actorUserID string) (*types.Plugin, error) {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin == types.PluginOriginBuiltin {
		return nil, errors.New("built-in plugin lifecycle is managed by WeKnora")
	}
	if plugin.Status == types.PluginStatusEnabled && plugin.RuntimeState == types.PluginRuntimeRunning {
		return plugin, nil
	}
	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, pluginRuntimeOperationTimeout)
	defer cancel()
	runtimeStatus, err := runtimeAPI.Start(opCtx, &pluginv1.PluginTargetRequest{PluginId: id})
	if err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}
	applyRuntimeStatus(plugin, runtimeStatus)
	if err := s.registrar.Register(plugin); err != nil {
		_, _ = runtimeAPI.Stop(context.Background(), &pluginv1.PluginTargetRequest{PluginId: id})
		return nil, fmt.Errorf("register plugin capabilities: %w", err)
	}
	plugin.Status = types.PluginStatusEnabled
	plugin.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, plugin); err != nil {
		s.registrar.Unregister(plugin)
		_, _ = runtimeAPI.Stop(context.Background(), &pluginv1.PluginTargetRequest{PluginId: id})
		return nil, fmt.Errorf("save enabled plugin: %w", err)
	}
	s.auditLifecycle(ctx, types.AuditActionPluginEnabled, plugin, actorUserID, nil)
	return plugin, nil
}

func (s *PluginService) Disable(ctx context.Context, id, actorUserID string) (*types.Plugin, error) {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin == types.PluginOriginBuiltin {
		return nil, errors.New("built-in plugin lifecycle is managed by WeKnora")
	}
	if plugin.Status == types.PluginStatusDisabled && plugin.RuntimeState == types.PluginRuntimeStopped {
		return plugin, nil
	}
	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, pluginRuntimeOperationTimeout)
	defer cancel()
	runtimeStatus, err := runtimeAPI.Stop(opCtx, &pluginv1.PluginTargetRequest{PluginId: id})
	if err != nil {
		return nil, fmt.Errorf("stop plugin: %w", err)
	}
	applyRuntimeStatus(plugin, runtimeStatus)
	s.registrar.Unregister(plugin)
	plugin.Status = types.PluginStatusDisabled
	plugin.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, plugin); err != nil {
		if status, startErr := runtimeAPI.Start(context.Background(), &pluginv1.PluginTargetRequest{PluginId: id}); startErr == nil {
			applyRuntimeStatus(plugin, status)
			_ = s.registrar.Register(plugin)
		}
		return nil, fmt.Errorf("save disabled plugin: %w", err)
	}
	s.auditLifecycle(ctx, types.AuditActionPluginDisabled, plugin, actorUserID, nil)
	return plugin, nil
}

func (s *PluginService) Uninstall(ctx context.Context, id, actorUserID string) error {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return err
	}
	if plugin.Origin == types.PluginOriginBuiltin {
		return errors.New("built-in plugins cannot be uninstalled")
	}
	if plugin.Status == types.PluginStatusEnabled {
		return errors.New("disable the plugin before uninstalling it")
	}
	if plugin.ConnectorType != "" {
		count, countErr := s.repo.CountDataSourcesByConnectorType(ctx, plugin.ConnectorType)
		if countErr != nil {
			return fmt.Errorf("check plugin usage: %w", countErr)
		}
		if count > 0 {
			return fmt.Errorf("%w: %d data source instance(s) still use %s", ErrPluginInUse, count, plugin.ConnectorType)
		}
	}
	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, pluginRuntimeOperationTimeout)
	defer cancel()
	if _, err := runtimeAPI.Uninstall(opCtx, &pluginv1.PluginTargetRequest{PluginId: id}); err != nil {
		return fmt.Errorf("uninstall plugin from runtime: %w", err)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete plugin installation: %w", err)
	}
	s.registrar.Unregister(plugin)
	s.auditLifecycle(ctx, types.AuditActionPluginUninstalled, plugin, actorUserID, map[string]any{
		"version": plugin.Version,
	})
	return nil
}

func (s *PluginService) Get(ctx context.Context, id string) (*types.Plugin, error) {
	return s.requirePlugin(ctx, id)
}

func (s *PluginService) List(ctx context.Context) ([]*types.Plugin, error) {
	external, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	result := append(external, s.builtinPlugins()...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Origin == result[j].Origin {
			return result[i].Name < result[j].Name
		}
		return result[i].Origin == types.PluginOriginBuiltin
	})
	return result, nil
}

func (s *PluginService) RefreshHealth(ctx context.Context, id string) (*types.Plugin, error) {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin == types.PluginOriginBuiltin {
		return plugin, nil
	}
	lifecycle, err := s.runtime.Lifecycle()
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(plugin.CallTimeoutSeconds)*time.Second)
	defer cancel()
	response, err := lifecycle.HealthCheck(callCtx, &pluginv1.HealthCheckRequest{Context: &pluginv1.InvocationContext{
		PluginId: plugin.ID,
	}})
	now := time.Now().UTC()
	if err != nil {
		plugin.RuntimeState = types.PluginRuntimeUnhealthy
		plugin.HealthMessage = err.Error()
		plugin.LastHealthAt = &now
		_ = s.repo.UpdateRuntimeState(ctx, id, plugin.RuntimeState, plugin.HealthMessage, &now)
		return nil, fmt.Errorf("check plugin health: %w", err)
	}
	plugin.LastHealthAt = &now
	plugin.HealthMessage = response.GetMessage()
	if response.GetStatus() == pluginv1.HealthCheckResponse_STATUS_SERVING {
		plugin.RuntimeState = types.PluginRuntimeRunning
	} else {
		plugin.RuntimeState = types.PluginRuntimeUnhealthy
	}
	if err := s.repo.UpdateRuntimeState(ctx, id, plugin.RuntimeState, plugin.HealthMessage, &now); err != nil {
		return nil, err
	}
	return plugin, nil
}

func (s *PluginService) validateInstallInput(
	input PluginInstallInput,
) (*pluginsdk.Manifest, *pluginv1.PluginManifest, time.Duration, error) {
	manifest, err := pluginsdk.ParseManifest(input.ManifestYAML)
	if err != nil {
		return nil, nil, 0, err
	}
	if input.Image != "" && strings.TrimSpace(input.Image) != manifest.Spec.Image {
		return nil, nil, 0, errors.New("image must match spec.image in the plugin manifest")
	}
	if _, err := pluginsdk.NegotiateProtocol(pluginsdk.ProtocolVersion, manifest.Spec.ProtocolVersion); err != nil {
		return nil, nil, 0, err
	}
	if err := validateHostVersion(s.hostVersion, manifest.Spec.WeKnoraVersionConstraint); err != nil {
		return nil, nil, 0, err
	}
	timeout := manifest.Spec.DefaultTimeout
	if input.CallTimeoutSeconds > 0 {
		timeout = time.Duration(input.CallTimeoutSeconds) * time.Second
	}
	if timeout <= 0 || timeout > maxPluginCallTimeoutSeconds*time.Second {
		return nil, nil, 0, fmt.Errorf("plugin call timeout must be between 1 and %d seconds", maxPluginCallTimeoutSeconds)
	}
	transport, err := manifest.ToProto()
	if err != nil {
		return nil, nil, 0, err
	}
	return manifest, transport, timeout, nil
}

func (s *PluginService) requirePlugin(ctx context.Context, id string) (*types.Plugin, error) {
	id = strings.TrimSpace(id)
	plugin, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		for _, builtin := range s.builtinPlugins() {
			if builtin.ID == id {
				return builtin, nil
			}
		}
		return nil, ErrPluginNotFound
	}
	return plugin, nil
}

func (s *PluginService) builtinPlugins() []*types.Plugin {
	result := make([]*types.Plugin, 0)
	if s.connectors != nil {
		for _, connector := range s.connectors.ListMetadata() {
			if connector.Origin == types.PluginOriginExternal {
				continue
			}
			plugin := s.builtinPlugin(
				"data_source",
				connector.Type,
				connector.Name,
				connector.Description,
				connector.Capabilities,
				map[string]any{
					"connector_type": connector.Type,
					"config_schema":  connector.ConfigSchema,
				},
			)
			plugin.ConnectorType = connector.Type
			result = append(result, plugin)
		}
	}

	for _, engine := range docparser.ListAllEngines(false, nil, nil) {
		result = append(result, s.builtinPlugin(
			"document_parser",
			engine.Name,
			engine.Name,
			engine.Description,
			engine.FileTypes,
			map[string]any{"file_types": engine.FileTypes},
		))
	}

	if s.webSearches != nil {
		for _, providerID := range s.webSearches.List() {
			result = append(result, s.builtinPlugin(
				"web_search",
				providerID,
				builtinDisplayName(providerID),
				"WeKnora 内置联网搜索供应商",
				[]string{"search"},
				map[string]any{"provider": providerID},
			))
		}
	}

	for _, provider := range modelprovider.List() {
		capabilities := make([]string, 0, len(provider.ModelTypes))
		for _, modelType := range provider.ModelTypes {
			capabilities = append(capabilities, string(modelType))
		}
		result = append(result, s.builtinPlugin(
			"model_provider",
			string(provider.Name),
			provider.DisplayName,
			provider.Description,
			capabilities,
			map[string]any{"provider": provider.Name, "model_types": provider.ModelTypes},
		))
	}
	return result
}

func (s *PluginService) builtinPlugin(
	pluginType, key, name, description string,
	capabilities []string,
	manifest map[string]any,
) *types.Plugin {
	typesJSON, _ := json.Marshal([]string{pluginType})
	capabilitiesJSON, _ := json.Marshal(capabilities)
	manifestJSON, _ := json.Marshal(manifest)
	return &types.Plugin{
		ID:                 "builtin." + pluginType + "." + key,
		Name:               name,
		Description:        description,
		Version:            s.hostVersion,
		ProtocolVersion:    pluginsdk.ProtocolVersion,
		Image:              "builtin",
		Origin:             types.PluginOriginBuiltin,
		Types:              types.JSON(typesJSON),
		Capabilities:       types.JSON(capabilitiesJSON),
		Manifest:           types.JSON(manifestJSON),
		Status:             types.PluginStatusEnabled,
		RuntimeState:       types.PluginRuntimeRunning,
		HealthMessage:      "managed by WeKnora",
		CallTimeoutSeconds: 0,
	}
}

func builtinDisplayName(value string) string {
	words := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for index, word := range words {
		if word != "" {
			words[index] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func (s *PluginService) auditLifecycle(
	ctx context.Context,
	action types.AuditAction,
	plugin *types.Plugin,
	actorUserID string,
	details map[string]any,
) {
	if s.audit == nil || plugin == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["plugin_id"] = plugin.ID
	details["connector_type"] = plugin.ConnectorType
	detailJSON, _ := json.Marshal(details)
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    0,
		ActorUserID: actorUserID,
		ActorRole:   "system_admin",
		Action:      action,
		TargetType:  "plugin",
		TargetID:    plugin.ID,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     types.JSON(detailJSON),
	})
}

func pluginEntity(
	manifest *pluginsdk.Manifest,
	actorUserID string,
	timeout time.Duration,
	runtimeStatus *pluginv1.PluginRuntimeStatus,
) (*types.Plugin, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode plugin manifest: %w", err)
	}
	typesJSON, _ := json.Marshal(manifest.NormalizedTypes())
	capabilitiesJSON, _ := json.Marshal(manifest.Spec.Capabilities)
	now := time.Now().UTC()
	entity := &types.Plugin{
		ID:                       manifest.Metadata.ID,
		Name:                     manifest.Metadata.Name,
		Description:              manifest.Metadata.Description,
		Version:                  strings.TrimPrefix(manifest.Metadata.Version, "v"),
		ProtocolVersion:          strings.TrimPrefix(manifest.Spec.ProtocolVersion, "v"),
		WeKnoraVersionConstraint: manifest.Spec.WeKnoraVersionConstraint,
		Image:                    manifest.Spec.Image,
		Origin:                   types.PluginOriginExternal,
		Types:                    types.JSON(typesJSON),
		Capabilities:             types.JSON(capabilitiesJSON),
		ConnectorType:            manifest.Spec.ConnectorType,
		Manifest:                 types.JSON(manifestJSON),
		Status:                   types.PluginStatusDisabled,
		RuntimeState:             types.PluginRuntimeStopped,
		CallTimeoutSeconds:       int(timeout.Seconds()),
		InstalledBy:              actorUserID,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	applyRuntimeStatus(entity, runtimeStatus)
	if err := entity.ValidateStorageFields(); err != nil {
		return nil, err
	}
	return entity, nil
}

func applyRuntimeStatus(plugin *types.Plugin, status *pluginv1.PluginRuntimeStatus) {
	if plugin == nil || status == nil {
		return
	}
	plugin.ImageDigest = status.GetImageDigest()
	plugin.RuntimeState = runtimeState(status.GetState())
	plugin.HealthMessage = status.GetMessage()
	if timestamp := status.GetUpdatedAt(); timestamp != nil && timestamp.IsValid() {
		value := timestamp.AsTime()
		plugin.LastHealthAt = &value
	}
}

func runtimeState(state pluginv1.RuntimeState) string {
	switch state {
	case pluginv1.RuntimeState_RUNTIME_STATE_STOPPED:
		return types.PluginRuntimeStopped
	case pluginv1.RuntimeState_RUNTIME_STATE_STARTING:
		return types.PluginRuntimeStarting
	case pluginv1.RuntimeState_RUNTIME_STATE_RUNNING:
		return types.PluginRuntimeRunning
	case pluginv1.RuntimeState_RUNTIME_STATE_UNHEALTHY:
		return types.PluginRuntimeUnhealthy
	case pluginv1.RuntimeState_RUNTIME_STATE_ERROR:
		return types.PluginRuntimeError
	default:
		return types.PluginRuntimeUnknown
	}
}

func validateHostVersion(hostVersion, constraint string) error {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil
	}
	hostVersion = strings.TrimPrefix(strings.TrimSpace(hostVersion), "v")
	if hostVersion == "" || hostVersion == "unknown" || hostVersion == "dev" {
		return nil
	}
	version, err := semver.Parse(hostVersion)
	if err != nil {
		return fmt.Errorf("invalid WeKnora host version %q: %w", hostVersion, err)
	}
	rangeCheck, err := semver.ParseRange(constraint)
	if err != nil {
		return fmt.Errorf("invalid weknoraVersion constraint %q: %w", constraint, err)
	}
	if !rangeCheck(version) {
		return fmt.Errorf("plugin requires WeKnora %s, current version is %s", constraint, version)
	}
	return nil
}

func readHostVersion() string {
	data, err := os.ReadFile("VERSION")
	if err != nil {
		return "dev"
	}
	version := strings.TrimSpace(string(data))
	if version == "" {
		return "dev"
	}
	return version
}
