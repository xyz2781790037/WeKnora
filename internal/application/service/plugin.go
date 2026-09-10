package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
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
	pluginRuntimeOperationTimeout    = 10 * time.Minute
	pluginRuntimeCompensationTimeout = time.Minute
	maxPluginCallTimeoutSeconds      = 60 * 60
	maxPluginManifestBytes           = 1024 * 1024
)

var (
	ErrPluginNotFound = errors.New("plugin not found")
	ErrPluginInUse    = errors.New("plugin is still in use")
)

// PluginInstallInput is shared by install and upgrade operations. ManifestYAML
// is kept out of the entity until it has passed SDK validation.
type PluginInstallInput struct {
	ManifestYAML         []byte
	ManifestURL          string
	Image                string
	CallTimeoutSeconds   int
	ActorUserID          string
	PermissionsConfirmed bool
	PermissionDigest     string
}

type pluginOperationProgress func(stage, pluginID string)

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
	marketplace *githubPluginMarketplace
	operations  *pluginOperationStore
	hostVersion string
	lockMu      sync.Mutex
	pluginLocks map[string]*pluginOperationLock
}

type pluginOperationLock struct {
	mu   sync.Mutex
	refs int
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
		marketplace: newGitHubPluginMarketplace(),
		operations:  newPluginOperationStore(),
		hostVersion: readHostVersion(),
		pluginLocks: make(map[string]*pluginOperationLock),
	}
}

func (s *PluginService) Install(ctx context.Context, input PluginInstallInput) (*types.Plugin, error) {
	return s.install(ctx, input, nil)
}

func (s *PluginService) install(
	ctx context.Context,
	input PluginInstallInput,
	progress pluginOperationProgress,
) (*types.Plugin, error) {
	reportPluginOperationProgress(progress, "manifest", "")
	if err := resolvePluginManifest(ctx, &input); err != nil {
		return nil, err
	}
	reportPluginOperationProgress(progress, "validation", "")
	manifest, transport, timeout, err := s.validateInstallInput(input)
	if err != nil {
		return nil, err
	}
	if err := validatePermissionConfirmation(input, manifest); err != nil {
		return nil, err
	}
	unlock := s.lockPlugin(manifest.Metadata.ID)
	defer unlock()
	reportPluginOperationProgress(progress, "conflicts", manifest.Metadata.ID)
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
	entity, err := pluginEntity(manifest, input.ActorUserID, input.ManifestURL, timeout, nil)
	if err != nil {
		return nil, err
	}

	reportPluginOperationProgress(progress, "runtime", manifest.Metadata.ID)
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

	reportPluginOperationProgress(progress, "persistence", manifest.Metadata.ID)
	applyRuntimeStatus(entity, runtimeStatus)
	if err := s.repo.Create(ctx, entity); err != nil {
		// Runtime install is compensatable because no persistent WeKnora record
		// references the new container yet.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), pluginRuntimeCompensationTimeout)
		_, cleanupErr := runtimeAPI.Uninstall(cleanupCtx, &pluginv1.PluginTargetRequest{PluginId: entity.ID})
		cleanupCancel()
		saveErr := fmt.Errorf("save plugin installation: %w", err)
		if cleanupErr != nil {
			return nil, errors.Join(saveErr, fmt.Errorf("remove unpersisted runtime plugin: %w", cleanupErr))
		}
		return nil, saveErr
	}
	s.auditLifecycle(ctx, types.AuditActionPluginInstalled, entity, input.ActorUserID, map[string]any{
		"version": entity.Version,
		"image":   entity.Image,
	})
	return entity, nil
}

func (s *PluginService) Upgrade(ctx context.Context, id string, input PluginInstallInput) (*types.Plugin, error) {
	return s.upgrade(ctx, id, input, nil)
}

func (s *PluginService) upgrade(
	ctx context.Context,
	id string,
	input PluginInstallInput,
	progress pluginOperationProgress,
) (*types.Plugin, error) {
	reportPluginOperationProgress(progress, "manifest", id)
	if err := resolvePluginManifest(ctx, &input); err != nil {
		return nil, err
	}
	reportPluginOperationProgress(progress, "validation", id)
	manifest, transport, timeout, err := s.validateInstallInput(input)
	if err != nil {
		return nil, err
	}
	if err := validatePermissionConfirmation(input, manifest); err != nil {
		return nil, err
	}
	unlock := s.lockPlugin(id)
	defer unlock()
	reportPluginOperationProgress(progress, "conflicts", id)
	existing, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Origin != types.PluginOriginExternal {
		return nil, errors.New("built-in plugins cannot be upgraded through the external runtime")
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

	reportPluginOperationProgress(progress, "runtime", id)
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

	reportPluginOperationProgress(progress, "persistence", id)
	sourceManifestURL := strings.TrimSpace(input.ManifestURL)
	if sourceManifestURL == "" {
		sourceManifestURL = existing.SourceManifestURL
	}
	updated, err := pluginEntity(manifest, existing.InstalledBy, sourceManifestURL, timeout, runtimeStatus)
	if err != nil {
		return nil, err
	}
	updated.Status = existing.Status
	updated.CreatedAt = existing.CreatedAt
	now := time.Now().UTC()
	updated.UpdateCheckedAt = &now
	updated.LatestVersion = updated.Version
	updated.UpdateAvailable = false
	updated.UpdateMessage = "插件已升级到最新版本"
	if existing.Status == types.PluginStatusEnabled {
		if err := s.registrar.Register(updated); err != nil {
			registerErr := fmt.Errorf("register upgraded plugin: %w", err)
			if rollbackErr := s.rollbackRuntimeUpgrade(runtimeAPI, existing); rollbackErr != nil {
				return nil, errors.Join(registerErr, fmt.Errorf("restore previous runtime plugin: %w", rollbackErr))
			}
			if restoreErr := s.registrar.Register(existing); restoreErr != nil {
				return nil, errors.Join(registerErr, fmt.Errorf("restore previous plugin registration: %w", restoreErr))
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

func reportPluginOperationProgress(progress pluginOperationProgress, stage, pluginID string) {
	if progress != nil {
		progress(stage, pluginID)
	}
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
	unlock := s.lockPlugin(id)
	defer unlock()
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
		compensationCtx, compensationCancel := context.WithTimeout(context.Background(), pluginRuntimeCompensationTimeout)
		_, _ = runtimeAPI.Stop(compensationCtx, &pluginv1.PluginTargetRequest{PluginId: id})
		compensationCancel()
		return nil, fmt.Errorf("register plugin capabilities: %w", err)
	}
	plugin.Status = types.PluginStatusEnabled
	plugin.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, plugin); err != nil {
		s.registrar.Unregister(plugin)
		compensationCtx, compensationCancel := context.WithTimeout(context.Background(), pluginRuntimeCompensationTimeout)
		_, _ = runtimeAPI.Stop(compensationCtx, &pluginv1.PluginTargetRequest{PluginId: id})
		compensationCancel()
		return nil, fmt.Errorf("save enabled plugin: %w", err)
	}
	s.auditLifecycle(ctx, types.AuditActionPluginEnabled, plugin, actorUserID, nil)
	return plugin, nil
}

func (s *PluginService) Disable(ctx context.Context, id, actorUserID string) (*types.Plugin, error) {
	unlock := s.lockPlugin(id)
	defer unlock()
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
		compensationCtx, compensationCancel := context.WithTimeout(context.Background(), pluginRuntimeCompensationTimeout)
		if status, startErr := runtimeAPI.Start(compensationCtx, &pluginv1.PluginTargetRequest{PluginId: id}); startErr == nil {
			applyRuntimeStatus(plugin, status)
			_ = s.registrar.Register(plugin)
		}
		compensationCancel()
		return nil, fmt.Errorf("save disabled plugin: %w", err)
	}
	s.auditLifecycle(ctx, types.AuditActionPluginDisabled, plugin, actorUserID, nil)
	return plugin, nil
}

func (s *PluginService) Uninstall(ctx context.Context, id, actorUserID string) error {
	unlock := s.lockPlugin(id)
	defer unlock()
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
	manifest, manifestErr := decodeInstalledManifest(plugin)
	if manifestErr != nil {
		return fmt.Errorf("decode installed plugin manifest: %w", manifestErr)
	}
	if slices.Contains(manifest.NormalizedTypes(), "retrieval_engine") {
		engineType := types.RetrieverEngineType(manifest.Spec.RetrieverEngineType)
		count, countErr := s.repo.CountVectorStoresByEngineType(ctx, engineType)
		if countErr != nil {
			return fmt.Errorf("check retrieval plugin usage: %w", countErr)
		}
		if count > 0 {
			return fmt.Errorf("%w: %d vector store instance(s) still use %s", ErrPluginInUse, count, engineType)
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

func (s *PluginService) lockPlugin(id string) func() {
	id = strings.TrimSpace(id)
	s.lockMu.Lock()
	if s.pluginLocks == nil {
		s.pluginLocks = make(map[string]*pluginOperationLock)
	}
	entry := s.pluginLocks[id]
	if entry == nil {
		entry = &pluginOperationLock{}
		s.pluginLocks[id] = entry
	}
	entry.refs++
	s.lockMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.lockMu.Lock()
		entry.refs--
		if entry.refs == 0 && s.pluginLocks[id] == entry {
			delete(s.pluginLocks, id)
		}
		s.lockMu.Unlock()
	}
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

func (s *PluginService) ListRuntimeEvents(
	ctx context.Context,
	id string,
	after uint64,
	limit uint32,
) ([]*pluginv1.RuntimeEvent, error) {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin != types.PluginOriginExternal {
		return []*pluginv1.RuntimeEvent{}, nil
	}
	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return nil, err
	}
	response, err := runtimeAPI.ListEvents(ctx, &pluginv1.ListRuntimeEventsRequest{
		PluginId:      id,
		AfterSequence: after,
		Limit:         limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list plugin runtime events: %w", err)
	}
	return response.GetEvents(), nil
}

func resolvePluginManifest(ctx context.Context, input *PluginInstallInput) error {
	if input == nil {
		return errors.New("plugin install input is required")
	}
	manifestURL := strings.TrimSpace(input.ManifestURL)
	hasYAML := len(strings.TrimSpace(string(input.ManifestYAML))) > 0
	if manifestURL == "" {
		if !hasYAML {
			return errors.New("manifest_yaml or manifest_url is required")
		}
		return nil
	}
	if hasYAML {
		return errors.New("manifest_yaml and manifest_url cannot be used together")
	}
	downloadURL, client, err := pluginManifestDownload(manifestURL)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create manifest request: %w", err)
	}
	request.Header.Set("Accept", "application/yaml, text/yaml, text/plain, application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download plugin manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download plugin manifest: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPluginManifestBytes+1))
	if err != nil {
		return fmt.Errorf("read plugin manifest: %w", err)
	}
	if len(data) > maxPluginManifestBytes {
		return fmt.Errorf("plugin manifest exceeds %d bytes", maxPluginManifestBytes)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("downloaded plugin manifest is empty")
	}
	input.ManifestYAML = data
	return nil
}

func (s *PluginService) RefreshHealth(ctx context.Context, id string) (*types.Plugin, error) {
	return s.refreshHealth(ctx, id, false)
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
	for _, engine := range types.GetVectorStoreTypes() {
		if engine.External {
			continue
		}
		result = append(result, s.builtinPlugin(
			"retrieval_engine",
			engine.Type,
			engine.DisplayName,
			"WeKnora 内置检索引擎",
			[]string{"keywords", "vector"},
			map[string]any{"retriever_engine_type": engine.Type},
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

func decodeInstalledManifest(plugin *types.Plugin) (*pluginsdk.Manifest, error) {
	if plugin == nil {
		return nil, errors.New("plugin is required")
	}
	var manifest pluginsdk.Manifest
	if err := json.Unmarshal(plugin.Manifest, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
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
	sourceManifestURL string,
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
		SourceManifestURL:        strings.TrimSpace(sourceManifestURL),
		LatestVersion:            strings.TrimPrefix(manifest.Metadata.Version, "v"),
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
