package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxRuntimeMessageSize = 8 * 1024 * 1024

type dockerEngine interface {
	ping(context.Context) error
	ensureInternalNetwork(context.Context, string) error
	connectContainerToNetwork(context.Context, string, string, []string) error
	disconnectContainerFromNetwork(context.Context, string, string) error
	removeNetwork(context.Context, string) error
	pullImage(context.Context, string) error
	imageDigest(context.Context, string) (string, error)
	createContainer(context.Context, containerSpec) error
	startContainer(context.Context, string) error
	stopContainer(context.Context, string, time.Duration) error
	removeContainer(context.Context, string) error
	containerState(context.Context, string) (exists bool, running bool, err error)
	containerNetworkMode(context.Context, string) (string, error)
}

// Manager is the single owner of plugin container and connection lifecycle.
// Slow operations are serialized per plugin, not globally, so installing one
// image cannot pause proxy policy checks or calls to another plugin.
type Manager struct {
	config Config
	docker dockerEngine
	store  stateStore
	events *eventBus

	mu            sync.RWMutex
	installations map[string]*installation
	connections   map[string]*grpc.ClientConn
	lockMu        sync.Mutex
	pluginLocks   map[string]*sync.Mutex
	quotaMu       sync.Mutex
	callQuotas    map[string]*pluginCallQuota
}

func NewManager(ctx context.Context, config Config, events *eventBus) (*Manager, error) {
	docker, err := newDockerClient(config.DockerSocket)
	if err != nil {
		return nil, err
	}
	if err := docker.ping(ctx); err != nil {
		return nil, fmt.Errorf("connect to Docker Engine: %w", err)
	}
	if err := docker.ensureInternalNetwork(ctx, config.SandboxNetwork); err != nil {
		return nil, fmt.Errorf("ensure plugin sandbox network: %w", err)
	}
	return newManagerWithDocker(ctx, config, events, docker)
}

func newManagerWithDocker(
	ctx context.Context,
	config Config,
	events *eventBus,
	docker dockerEngine,
) (*Manager, error) {
	store := stateStore{path: config.StatePath}
	installations, err := store.load()
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		config:        config,
		docker:        docker,
		store:         store,
		events:        events,
		installations: installations,
		connections:   make(map[string]*grpc.ClientConn),
		pluginLocks:   make(map[string]*sync.Mutex),
		callQuotas:    make(map[string]*pluginCallQuota),
	}
	for id, item := range installations {
		if supplyErr := verifyPluginSupplyChain(item.Manifest, item.ImageDigest, config); supplyErr != nil {
			_, running, inspectErr := docker.containerState(ctx, item.ContainerName)
			if inspectErr == nil && running {
				_ = docker.stopContainer(ctx, item.ContainerName, config.ShutdownTimeout)
			}
			item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
			item.Message = fmt.Sprintf("apply plugin supply-chain policy: %v", supplyErr)
			item.UpdatedAt = time.Now().UTC()
			installations[id] = item
			continue
		}
		limits, limitsErr := resolvePluginLimits(item.Manifest, config)
		if limitsErr != nil {
			_, running, inspectErr := docker.containerState(ctx, item.ContainerName)
			if inspectErr == nil && running {
				_ = docker.stopContainer(ctx, item.ContainerName, config.ShutdownTimeout)
			}
			item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
			item.Message = fmt.Sprintf("apply plugin resource policy: %v", limitsErr)
			item.UpdatedAt = time.Now().UTC()
			installations[id] = item
			continue
		}
		expectedResourcePolicy := resourcePolicyFingerprint(limits)
		targetNetwork, networkErr := manager.ensurePluginNetwork(ctx, id)
		if networkErr != nil {
			item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
			item.Message = fmt.Sprintf("prepare isolated network: %v", networkErr)
			item.UpdatedAt = time.Now().UTC()
			installations[id] = item
			continue
		}
		exists, running, inspectErr := docker.containerState(ctx, item.ContainerName)
		switch {
		case inspectErr != nil:
			item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
			item.Message = inspectErr.Error()
		case !exists || !running:
			item.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
			item.Message = "plugin is stopped"
		default:
			item.State = pluginv1.RuntimeState_RUNTIME_STATE_RUNNING
			item.Message = "plugin is running"
		}
		if exists && inspectErr == nil {
			currentNetwork, networkModeErr := docker.containerNetworkMode(ctx, item.ContainerName)
			if networkModeErr != nil {
				item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
				item.Message = networkModeErr.Error()
			} else if currentNetwork != targetNetwork {
				if migrationErr := manager.migrateContainerNetwork(ctx, item, running); migrationErr != nil {
					item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
					item.Message = fmt.Sprintf("migrate isolated network: %v", migrationErr)
				}
			} else if item.ResourcePolicy != expectedResourcePolicy {
				if policyErr := manager.reapplyContainerPolicy(ctx, item, running); policyErr != nil {
					item.State = pluginv1.RuntimeState_RUNTIME_STATE_ERROR
					item.Message = fmt.Sprintf("reapply plugin resource policy: %v", policyErr)
				}
			}
		}
		item.UpdatedAt = time.Now().UTC()
		installations[id] = item
	}
	if err := manager.persist(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Install(
	ctx context.Context,
	req *pluginv1.InstallPluginRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	manifest, image, timeout, err := m.validateInstall(req.GetManifest(), req.GetImage(), req.GetCallTimeout())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRuntimePlugin, err)
	}
	unlock := m.lockPlugin(manifest.GetId())
	defer unlock()
	if existing := m.get(manifest.GetId()); existing != nil {
		if sameInstallRequest(existing, manifest, image, timeout) {
			return runtimeStatus(existing), nil
		}
		return nil, statusErrorAlreadyExists(manifest.GetId())
	}

	m.events.publish(manifest.GetId(), "installing", "pulling plugin image", map[string]string{"image": image})
	if err := m.ensureImage(ctx, image); err != nil {
		return nil, fmt.Errorf("pull plugin image: %w", err)
	}
	digest, err := m.docker.imageDigest(ctx, image)
	if err != nil {
		return nil, fmt.Errorf("inspect plugin image: %w", err)
	}
	if err := verifyPluginSupplyChain(manifest, digest, m.config); err != nil {
		return nil, fmt.Errorf("verify plugin supply chain: %w", err)
	}
	proxyToken, err := newProxyToken()
	if err != nil {
		return nil, fmt.Errorf("create plugin proxy token: %w", err)
	}
	item := &installation{
		Manifest:      manifest,
		Image:         image,
		ImageDigest:   digest,
		ContainerName: containerName(manifest.GetId()),
		ProxyToken:    proxyToken,
		CallTimeout:   timeout,
		State:         pluginv1.RuntimeState_RUNTIME_STATE_INSTALLING,
		Message:       "verifying plugin",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := m.recreateAndStart(ctx, item); err != nil {
		_ = m.docker.removeContainer(context.Background(), item.ContainerName)
		_ = m.cleanupPluginNetwork(context.Background(), item.Manifest.GetId())
		m.events.publish(item.Manifest.GetId(), "install_failed", "plugin verification failed", map[string]string{"error": err.Error()})
		return nil, err
	}
	if err := m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout); err != nil {
		_ = m.docker.removeContainer(context.Background(), item.ContainerName)
		_ = m.cleanupPluginNetwork(context.Background(), item.Manifest.GetId())
		return nil, fmt.Errorf("stop verified plugin: %w", err)
	}
	m.closeConnection(item.Manifest.GetId())
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
	item.Message = "plugin installed and verified"
	item.UpdatedAt = time.Now().UTC()
	if err := m.put(item); err != nil {
		_ = m.docker.removeContainer(context.Background(), item.ContainerName)
		_ = m.cleanupPluginNetwork(context.Background(), item.Manifest.GetId())
		return nil, err
	}
	m.events.publish(item.Manifest.GetId(), "installed", item.Message, map[string]string{
		"version": item.Manifest.GetVersion(),
		"digest":  item.ImageDigest,
	})
	return runtimeStatus(item), nil
}

func (m *Manager) Upgrade(
	ctx context.Context,
	req *pluginv1.UpgradePluginRequest,
) (*pluginv1.PluginRuntimeStatus, error) {
	manifest, image, timeout, err := m.validateInstall(req.GetManifest(), req.GetImage(), req.GetCallTimeout())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRuntimePlugin, err)
	}
	unlock := m.lockPlugin(manifest.GetId())
	defer unlock()
	previous := m.get(manifest.GetId())
	if previous == nil {
		return nil, ErrRuntimePluginNotFound
	}
	_, wasRunning, err := m.docker.containerState(ctx, previous.ContainerName)
	if err != nil {
		return nil, err
	}
	m.events.publish(manifest.GetId(), "upgrading", "pulling upgraded plugin image", map[string]string{"image": image})
	if err := m.ensureImage(ctx, image); err != nil {
		return nil, fmt.Errorf("pull upgraded plugin image: %w", err)
	}
	digest, err := m.docker.imageDigest(ctx, image)
	if err != nil {
		return nil, err
	}
	if err := verifyPluginSupplyChain(manifest, digest, m.config); err != nil {
		return nil, fmt.Errorf("verify upgraded plugin supply chain: %w", err)
	}
	next := cloneInstallation(previous)
	next.Manifest = manifest
	next.Image = image
	next.ImageDigest = digest
	next.CallTimeout = timeout
	next.State = pluginv1.RuntimeState_RUNTIME_STATE_STARTING
	next.Message = "verifying upgraded plugin"
	next.UpdatedAt = time.Now().UTC()

	if err := m.docker.stopContainer(ctx, previous.ContainerName, m.config.ShutdownTimeout); err != nil {
		return nil, m.failUpgradeWithRollback(previous, manifest.GetVersion(), wasRunning, err)
	}
	m.closeConnection(previous.Manifest.GetId())
	if err := m.docker.removeContainer(ctx, previous.ContainerName); err != nil {
		return nil, m.failUpgradeWithRollback(previous, manifest.GetVersion(), wasRunning, err)
	}
	if err := m.recreateAndStart(ctx, next); err != nil {
		return nil, m.failUpgradeWithRollback(previous, manifest.GetVersion(), wasRunning, err)
	}
	if !wasRunning {
		if err := m.docker.stopContainer(ctx, next.ContainerName, m.config.ShutdownTimeout); err != nil {
			return nil, m.failUpgradeWithRollback(previous, manifest.GetVersion(), wasRunning, err)
		}
		m.closeConnection(next.Manifest.GetId())
		next.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
		next.Message = "plugin upgraded and stopped"
	} else {
		next.State = pluginv1.RuntimeState_RUNTIME_STATE_RUNNING
		next.Message = "plugin upgraded and running"
	}
	next.UpdatedAt = time.Now().UTC()
	if err := m.put(next); err != nil {
		return nil, m.failUpgradeWithRollback(previous, manifest.GetVersion(), wasRunning, err)
	}
	m.events.publish(next.Manifest.GetId(), "upgraded", next.Message, map[string]string{
		"version": next.Manifest.GetVersion(),
		"digest":  next.ImageDigest,
	})
	return runtimeStatus(next), nil
}

func (m *Manager) Start(ctx context.Context, pluginID string) (*pluginv1.PluginRuntimeStatus, error) {
	unlock := m.lockPlugin(pluginID)
	defer unlock()
	item := m.get(pluginID)
	if item == nil {
		return nil, ErrRuntimePluginNotFound
	}
	if err := verifyPluginSupplyChain(item.Manifest, item.ImageDigest, m.config); err != nil {
		return nil, fmt.Errorf("verify plugin supply chain: %w", err)
	}
	limits, err := resolvePluginLimits(item.Manifest, m.config)
	if err != nil {
		return nil, fmt.Errorf("apply plugin resource policy: %w", err)
	}
	exists, running, err := m.docker.containerState(ctx, item.ContainerName)
	if err != nil {
		return nil, err
	}
	if exists && item.ResourcePolicy != resourcePolicyFingerprint(limits) {
		m.closeConnection(pluginID)
		if err := m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout); err != nil {
			return nil, err
		}
		if err := m.docker.removeContainer(ctx, item.ContainerName); err != nil {
			return nil, err
		}
		if err := m.createContainer(ctx, item); err != nil {
			return nil, err
		}
		running = false
	}
	if !exists {
		if err := m.createContainer(ctx, item); err != nil {
			return nil, err
		}
	}
	if !running {
		if err := m.docker.startContainer(ctx, item.ContainerName); err != nil {
			return nil, err
		}
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_STARTING
	item.Message = "waiting for plugin health check"
	item.UpdatedAt = time.Now().UTC()
	if err := m.waitReady(ctx, item); err != nil {
		item.State = pluginv1.RuntimeState_RUNTIME_STATE_UNHEALTHY
		item.Message = err.Error()
		item.UpdatedAt = time.Now().UTC()
		_ = m.put(item)
		return nil, err
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_RUNNING
	item.Message = "plugin is running"
	item.UpdatedAt = time.Now().UTC()
	if err := m.put(item); err != nil {
		return nil, err
	}
	m.events.publish(pluginID, "started", item.Message, nil)
	return runtimeStatus(item), nil
}

func (m *Manager) Stop(ctx context.Context, pluginID string) (*pluginv1.PluginRuntimeStatus, error) {
	unlock := m.lockPlugin(pluginID)
	defer unlock()
	item := m.get(pluginID)
	if item == nil {
		return nil, ErrRuntimePluginNotFound
	}
	if err := m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout); err != nil {
		return nil, err
	}
	m.closeConnection(pluginID)
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
	item.Message = "plugin is stopped"
	item.UpdatedAt = time.Now().UTC()
	if err := m.put(item); err != nil {
		return nil, err
	}
	m.events.publish(pluginID, "stopped", item.Message, nil)
	return runtimeStatus(item), nil
}

func (m *Manager) Uninstall(ctx context.Context, pluginID string) error {
	unlock := m.lockPlugin(pluginID)
	defer unlock()
	item := m.get(pluginID)
	if item == nil {
		// Uninstall is idempotent so the host can safely retry after the
		// container was removed but its database transaction failed.
		return nil
	}
	if err := m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout); err != nil {
		return err
	}
	m.closeConnection(pluginID)
	if err := m.docker.removeContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	if err := m.cleanupPluginNetwork(ctx, pluginID); err != nil {
		return fmt.Errorf("remove isolated plugin network: %w", err)
	}
	if err := m.remove(pluginID); err != nil {
		return err
	}
	m.removeCallQuota(pluginID)
	m.events.publish(pluginID, "uninstalled", "plugin container removed", nil)
	return nil
}

func (m *Manager) Status(pluginID string) (*pluginv1.PluginRuntimeStatus, error) {
	item := m.get(pluginID)
	if item == nil {
		return nil, ErrRuntimePluginNotFound
	}
	return runtimeStatus(item), nil
}

func (m *Manager) Statuses() []*pluginv1.PluginRuntimeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*pluginv1.PluginRuntimeStatus, 0, len(m.installations))
	for _, item := range m.installations {
		result = append(result, runtimeStatus(item))
	}
	return result
}

func (m *Manager) pluginClient(pluginID string) (*grpc.ClientConn, *installation, error) {
	item := m.get(pluginID)
	if item == nil {
		return nil, nil, ErrRuntimePluginNotFound
	}
	if item.State != pluginv1.RuntimeState_RUNTIME_STATE_RUNNING &&
		item.State != pluginv1.RuntimeState_RUNTIME_STATE_STARTING {
		return nil, nil, fmt.Errorf("plugin %s is not running", pluginID)
	}
	m.mu.RLock()
	connection := m.connections[pluginID]
	m.mu.RUnlock()
	if connection != nil {
		return connection, item, nil
	}
	target := net.JoinHostPort(item.ContainerName, fmt.Sprint(m.config.PluginPort))
	created, err := grpc.NewClient(
		"dns:///"+target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxRuntimeMessageSize),
			grpc.MaxCallSendMsgSize(maxRuntimeMessageSize),
		),
	)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	if existing := m.connections[pluginID]; existing != nil {
		m.mu.Unlock()
		_ = created.Close()
		return existing, item, nil
	}
	m.connections[pluginID] = created
	m.mu.Unlock()
	return created, item, nil
}

func (m *Manager) proxyPolicy(pluginID, token string) ([]string, bool) {
	m.mu.RLock()
	item := m.installations[pluginID]
	if item == nil || !secureTokenEqual(token, item.ProxyToken) {
		m.mu.RUnlock()
		return nil, false
	}
	permissions := item.Manifest.GetPermissions()
	if permissions == nil || !permissions.GetNetwork() {
		m.mu.RUnlock()
		return nil, true
	}
	hosts := append([]string(nil), permissions.GetAllowedHosts()...)
	m.mu.RUnlock()
	return hosts, true
}

func (m *Manager) validateInstall(
	manifest *pluginv1.PluginManifest,
	image string,
	timeoutValue *durationpb.Duration,
) (*pluginv1.PluginManifest, string, time.Duration, error) {
	if manifest == nil {
		return nil, "", 0, errors.New("plugin manifest is required")
	}
	if strings.TrimSpace(manifest.GetId()) == "" || strings.TrimSpace(manifest.GetName()) == "" {
		return nil, "", 0, errors.New("plugin id and name are required")
	}
	if manifest.GetVersion() == "" || manifest.GetProtocolVersion() == "" {
		return nil, "", 0, errors.New("plugin and protocol versions are required")
	}
	if _, err := pluginsdk.NegotiateProtocol(pluginsdk.ProtocolVersion, manifest.GetProtocolVersion()); err != nil {
		return nil, "", 0, err
	}
	image = strings.TrimSpace(image)
	if image == "" || image != strings.TrimSpace(manifest.GetImage()) {
		return nil, "", 0, errors.New("runtime image must match manifest image")
	}
	if len(manifest.GetTypes()) == 0 {
		return nil, "", 0, errors.New("plugin must declare at least one type")
	}
	if manifest.GetConfig() == nil || !json.Valid([]byte(manifest.GetConfig().GetJsonSchema())) {
		return nil, "", 0, errors.New("plugin config JSON Schema is invalid")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(manifest.GetConfig().GetJsonSchema()), &schema); err != nil {
		return nil, "", 0, errors.New("plugin config JSON Schema must be an object")
	}
	if err := pluginsdk.ValidateConfigSchema(schema); err != nil {
		return nil, "", 0, err
	}
	if permissions := manifest.GetPermissions(); permissions != nil {
		if !permissions.GetNetwork() && len(permissions.GetAllowedHosts()) > 0 {
			return nil, "", 0, errors.New("network-disabled plugin cannot declare allowed hosts")
		}
		for _, host := range permissions.GetAllowedHosts() {
			if strings.ContainsAny(host, "/?#@") || strings.TrimSpace(host) == "" {
				return nil, "", 0, fmt.Errorf("invalid allowed host %q", host)
			}
		}
	}
	timeout := 2 * time.Minute
	if timeoutValue != nil {
		if !timeoutValue.IsValid() {
			return nil, "", 0, errors.New("plugin call timeout is invalid")
		}
		timeout = timeoutValue.AsDuration()
	}
	if timeout <= 0 || timeout > m.config.MaxCallTimeout {
		return nil, "", 0, fmt.Errorf("plugin call timeout must be between 1ns and %s", m.config.MaxCallTimeout)
	}
	if _, err := resolvePluginLimits(manifest, m.config); err != nil {
		return nil, "", 0, err
	}
	return proto.Clone(manifest).(*pluginv1.PluginManifest), image, timeout, nil
}

func sameInstallRequest(existing *installation, manifest *pluginv1.PluginManifest, image string, timeout time.Duration) bool {
	return existing != nil &&
		existing.Image == image &&
		existing.CallTimeout == timeout &&
		proto.Equal(existing.Manifest, manifest)
}

func (m *Manager) recreateAndStart(ctx context.Context, item *installation) error {
	_ = m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout)
	if err := m.docker.removeContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	if err := m.createContainer(ctx, item); err != nil {
		return err
	}
	if err := m.docker.startContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_STARTING
	return m.waitReady(ctx, item)
}

func (m *Manager) createContainer(ctx context.Context, item *installation) error {
	network, err := m.ensurePluginNetwork(ctx, item.Manifest.GetId())
	if err != nil {
		return err
	}
	limits, err := resolvePluginLimits(item.Manifest, m.config)
	if err != nil {
		return err
	}
	if err := m.docker.createContainer(ctx, containerSpec{
		PluginID:      item.Manifest.GetId(),
		PluginVersion: item.Manifest.GetVersion(),
		Image:         installationImage(item),
		Name:          item.ContainerName,
		Network:       network,
		ProxyURL:      m.config.ProxyURL,
		ProxyToken:    item.ProxyToken,
		PluginPort:    m.config.PluginPort,
		MemoryBytes:   limits.memoryBytes,
		NanoCPUs:      limits.nanoCPUs,
		PIDsLimit:     limits.pidsLimit,
	}); err != nil {
		return err
	}
	item.ResourcePolicy = resourcePolicyFingerprint(limits)
	return nil
}

func (m *Manager) ensurePluginNetwork(ctx context.Context, pluginID string) (string, error) {
	network := pluginNetworkName(m.config.SandboxNetwork, pluginID)
	if err := m.docker.ensureInternalNetwork(ctx, network); err != nil {
		return "", err
	}
	if err := m.docker.connectContainerToNetwork(
		ctx,
		network,
		m.config.RuntimeContainer,
		[]string{m.config.ContainerDNSName},
	); err != nil {
		return "", err
	}
	return network, nil
}

func (m *Manager) cleanupPluginNetwork(ctx context.Context, pluginID string) error {
	network := pluginNetworkName(m.config.SandboxNetwork, pluginID)
	if err := m.docker.disconnectContainerFromNetwork(ctx, network, m.config.RuntimeContainer); err != nil {
		return err
	}
	return m.docker.removeNetwork(ctx, network)
}

func (m *Manager) migrateContainerNetwork(ctx context.Context, item *installation, wasRunning bool) error {
	if err := m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout); err != nil {
		return err
	}
	if err := m.docker.removeContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	if err := m.createContainer(ctx, item); err != nil {
		return err
	}
	if !wasRunning {
		item.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
		item.Message = "plugin migrated to isolated network"
		return nil
	}
	if err := m.docker.startContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_STARTING
	if err := m.waitReady(ctx, item); err != nil {
		return err
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_RUNNING
	item.Message = "plugin migrated to isolated network"
	m.events.publish(item.Manifest.GetId(), "network_isolated", item.Message, nil)
	return nil
}

func (m *Manager) reapplyContainerPolicy(ctx context.Context, item *installation, wasRunning bool) error {
	if err := m.docker.stopContainer(ctx, item.ContainerName, m.config.ShutdownTimeout); err != nil {
		return err
	}
	if err := m.docker.removeContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	if err := m.createContainer(ctx, item); err != nil {
		return err
	}
	if !wasRunning {
		item.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
		item.Message = "plugin resource policy reapplied"
		return nil
	}
	if err := m.docker.startContainer(ctx, item.ContainerName); err != nil {
		return err
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_STARTING
	if err := m.waitReady(ctx, item); err != nil {
		return err
	}
	item.State = pluginv1.RuntimeState_RUNTIME_STATE_RUNNING
	item.Message = "plugin resource policy reapplied"
	if m.events != nil {
		m.events.publish(item.Manifest.GetId(), "resource_policy_applied", item.Message, nil)
	}
	return nil
}

func installationImage(item *installation) string {
	if item.ImageDigest != "" {
		return item.ImageDigest
	}
	// Backward compatibility for state written before image digests were
	// persisted. Fresh installs and upgrades always use the immutable digest.
	return item.Image
}

func (m *Manager) ensureImage(ctx context.Context, image string) error {
	if m.config.ImagePullPolicy == imagePullPolicyIfMissing {
		if _, err := m.docker.imageDigest(ctx, image); err == nil {
			return nil
		}
	}
	return m.docker.pullImage(ctx, image)
}

func (m *Manager) waitReady(ctx context.Context, item *installation) error {
	connection, err := m.dialPlugin(item)
	if err != nil {
		return err
	}
	defer connection.Close()
	lifecycle := pluginv1.NewPluginLifecycleClient(connection)

	deadline := time.Now().Add(m.config.HealthTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}

		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		handshake, callErr := lifecycle.Handshake(probeCtx, &pluginv1.HandshakeRequest{
			Context:             &pluginv1.InvocationContext{PluginId: item.Manifest.GetId()},
			HostProtocolVersion: pluginsdk.ProtocolVersion,
			HostVersion:         "unknown",
		})
		cancel()
		if callErr == nil {
			callErr = verifyHandshake(item.Manifest, handshake)
		}
		if callErr == nil {
			healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Second)
			health, healthErr := lifecycle.HealthCheck(healthCtx, &pluginv1.HealthCheckRequest{
				Context: &pluginv1.InvocationContext{PluginId: item.Manifest.GetId()},
			})
			healthCancel()
			if healthErr != nil {
				callErr = healthErr
			} else if health.GetStatus() != pluginv1.HealthCheckResponse_STATUS_SERVING {
				callErr = fmt.Errorf("plugin health is %s: %s", health.GetStatus(), health.GetMessage())
			}
		}
		if callErr == nil {
			return nil
		}
		lastErr = callErr

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if lastErr == nil {
		lastErr = errors.New("plugin did not become ready")
	}
	return fmt.Errorf("plugin health check timed out: %w", lastErr)
}

func (m *Manager) dialPlugin(item *installation) (*grpc.ClientConn, error) {
	target := net.JoinHostPort(item.ContainerName, fmt.Sprint(m.config.PluginPort))
	return grpc.NewClient(
		"dns:///"+target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxRuntimeMessageSize),
			grpc.MaxCallSendMsgSize(maxRuntimeMessageSize),
		),
	)
}

func (m *Manager) rollbackUpgrade(ctx context.Context, previous *installation, wasRunning bool) error {
	m.closeConnection(previous.Manifest.GetId())
	if err := m.docker.stopContainer(ctx, previous.ContainerName, m.config.ShutdownTimeout); err != nil {
		return fmt.Errorf("stop failed upgrade container: %w", err)
	}
	if err := m.docker.removeContainer(ctx, previous.ContainerName); err != nil {
		return fmt.Errorf("remove failed upgrade container: %w", err)
	}
	if err := m.createContainer(ctx, previous); err != nil {
		return fmt.Errorf("recreate previous plugin container: %w", err)
	}
	if wasRunning {
		if err := m.docker.startContainer(ctx, previous.ContainerName); err != nil {
			return err
		}
		previous.State = pluginv1.RuntimeState_RUNTIME_STATE_STARTING
		if err := m.waitReady(ctx, previous); err != nil {
			return err
		}
		previous.State = pluginv1.RuntimeState_RUNTIME_STATE_RUNNING
		previous.Message = "previous plugin version restored"
	} else {
		previous.State = pluginv1.RuntimeState_RUNTIME_STATE_STOPPED
		previous.Message = "previous stopped plugin version restored"
	}
	previous.UpdatedAt = time.Now().UTC()
	return m.put(previous)
}

func (m *Manager) failUpgradeWithRollback(
	previous *installation,
	attemptedVersion string,
	wasRunning bool,
	upgradeErr error,
) error {
	pluginID := previous.Manifest.GetId()
	details := map[string]string{
		"from_version": previous.Manifest.GetVersion(),
		"to_version":   attemptedVersion,
		"error":        upgradeErr.Error(),
	}
	m.events.publish(pluginID, "rollback_started", "plugin upgrade failed; restoring previous version", details)

	rollbackTimeout := m.config.HealthTimeout + 2*m.config.ShutdownTimeout + 10*time.Second
	rollbackCtx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	if rollbackErr := m.rollbackUpgrade(rollbackCtx, previous, wasRunning); rollbackErr != nil {
		failedDetails := make(map[string]string, len(details)+1)
		for key, value := range details {
			failedDetails[key] = value
		}
		failedDetails["rollback_error"] = rollbackErr.Error()
		m.events.publish(pluginID, "rollback_failed", "plugin upgrade rollback failed", failedDetails)
		return fmt.Errorf("upgrade failed: %v; rollback failed: %w", upgradeErr, rollbackErr)
	}
	m.events.publish(pluginID, "rollback_succeeded", "previous plugin version restored", details)
	return fmt.Errorf("upgrade failed and previous version was restored: %w", upgradeErr)
}

func (m *Manager) get(pluginID string) *installation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneInstallation(m.installations[pluginID])
}

func (m *Manager) put(item *installation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate := cloneInstallationMap(m.installations)
	candidate[item.Manifest.GetId()] = cloneInstallation(item)
	if err := m.store.save(candidate); err != nil {
		return err
	}
	m.installations = candidate
	return nil
}

func (m *Manager) remove(pluginID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate := cloneInstallationMap(m.installations)
	delete(candidate, pluginID)
	if err := m.store.save(candidate); err != nil {
		return err
	}
	m.installations = candidate
	return nil
}

func (m *Manager) persist() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.save(m.installations)
}

func (m *Manager) closeConnection(pluginID string) {
	m.mu.Lock()
	connection := m.connections[pluginID]
	delete(m.connections, pluginID)
	m.mu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
}

func (m *Manager) lockPlugin(pluginID string) func() {
	m.lockMu.Lock()
	lock := m.pluginLocks[pluginID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.pluginLocks[pluginID] = lock
	}
	m.lockMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func cloneInstallation(item *installation) *installation {
	if item == nil {
		return nil
	}
	result := *item
	result.Manifest = proto.Clone(item.Manifest).(*pluginv1.PluginManifest)
	return &result
}

func cloneInstallationMap(source map[string]*installation) map[string]*installation {
	result := make(map[string]*installation, len(source))
	for id, item := range source {
		result[id] = cloneInstallation(item)
	}
	return result
}

func runtimeStatus(item *installation) *pluginv1.PluginRuntimeStatus {
	if item == nil {
		return nil
	}
	return &pluginv1.PluginRuntimeStatus{
		PluginId:    item.Manifest.GetId(),
		Version:     item.Manifest.GetVersion(),
		Image:       item.Image,
		ImageDigest: item.ImageDigest,
		State:       item.State,
		Message:     item.Message,
		UpdatedAt:   timestamppb.New(item.UpdatedAt),
	}
}

func verifyHandshake(expected *pluginv1.PluginManifest, response *pluginv1.HandshakeResponse) error {
	if response == nil || response.GetManifest() == nil {
		return errors.New("plugin handshake returned no manifest")
	}
	actual := response.GetManifest()
	if !proto.Equal(expected, actual) {
		return fmt.Errorf(
			"plugin image manifest mismatch: expected %s@%s, got %s@%s; all manifest fields must match",
			expected.GetId(), expected.GetVersion(), actual.GetId(), actual.GetVersion(),
		)
	}
	negotiated, err := pluginsdk.NegotiateProtocol(pluginsdk.ProtocolVersion, actual.GetProtocolVersion())
	if err != nil {
		return err
	}
	if response.GetNegotiatedProtocolVersion() != negotiated {
		return fmt.Errorf(
			"plugin negotiated protocol mismatch: expected %s, got %s",
			negotiated,
			response.GetNegotiatedProtocolVersion(),
		)
	}
	return nil
}

func statusErrorAlreadyExists(pluginID string) error {
	return fmt.Errorf("%w: plugin %s has a different version or image", ErrRuntimePluginAlreadyExists, pluginID)
}

var (
	ErrRuntimePluginNotFound      = errors.New("plugin is not installed in runtime")
	ErrRuntimePluginAlreadyExists = errors.New("plugin is already installed in runtime")
	ErrInvalidRuntimePlugin       = errors.New("invalid plugin installation")
)
