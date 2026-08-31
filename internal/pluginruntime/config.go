// Package pluginruntime implements the isolated Docker supervisor for external
// WeKnora plugins.
package pluginruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/plugin/runtimeauth"
)

const (
	defaultRuntimeListenAddr = ":9091"
	defaultProxyListenAddr   = ":18080"
	defaultProxyURL          = "http://weknora-plugin-runtime:18080"
	defaultSandboxNetwork    = "weknora-plugin-sandbox"
	defaultDockerSocket      = "/var/run/docker.sock"
	defaultStatePath         = "/var/lib/weknora-plugin-runtime/state.json"
	defaultPluginPort        = 9000
	defaultMemoryBytes       = 512 * 1024 * 1024
	defaultNanoCPUs          = int64(1_000_000_000)
	defaultPIDsLimit         = int64(128)
	defaultMaxConcurrency    = uint32(8)
	defaultCallsPerMinute    = uint32(120)
	defaultImagePullPolicy   = imagePullPolicyAlways
	imagePullPolicyAlways    = "always"
	imagePullPolicyIfMissing = "if-not-present"
)

// Config contains runtime-only settings. Plugin authors cannot override the
// resource and network isolation defaults through their manifests.
type Config struct {
	ListenAddr        string
	ProxyListenAddr   string
	ProxyURL          string
	AuthToken         string
	DockerSocket      string
	SandboxNetwork    string
	StatePath         string
	PluginPort        int
	MemoryBytes       int64
	NanoCPUs          int64
	PIDsLimit         int64
	HealthTimeout     time.Duration
	ShutdownTimeout   time.Duration
	MaxCallTimeout    time.Duration
	ContainerDNSName  string
	RuntimeContainer  string
	ImagePullPolicy   string
	MaxConcurrency    uint32
	CallsPerMinute    uint32
	RequireSignatures bool
	TrustedPublishers map[string]string
}

// LoadConfig reads plugin-runtime configuration from environment variables.
func LoadConfig() (Config, error) {
	authToken, err := runtimeauth.Resolve()
	if err != nil {
		return Config{}, fmt.Errorf("resolve plugin runtime auth token: %w", err)
	}
	runtimeContainer, err := os.Hostname()
	if err != nil {
		return Config{}, fmt.Errorf("resolve plugin runtime container id: %w", err)
	}
	requireSignatures, err := envStrictBool("WEKNORA_PLUGIN_REQUIRE_SIGNATURES", false)
	if err != nil {
		return Config{}, err
	}
	pluginPort, err := envInt("WEKNORA_PLUGIN_PORT", defaultPluginPort)
	if err != nil {
		return Config{}, err
	}
	memoryBytes, err := envInt64("WEKNORA_PLUGIN_MEMORY_BYTES", defaultMemoryBytes)
	if err != nil {
		return Config{}, err
	}
	nanoCPUs, err := envInt64("WEKNORA_PLUGIN_NANO_CPUS", defaultNanoCPUs)
	if err != nil {
		return Config{}, err
	}
	pidsLimit, err := envInt64("WEKNORA_PLUGIN_PIDS_LIMIT", defaultPIDsLimit)
	if err != nil {
		return Config{}, err
	}
	healthTimeout, err := envDuration("WEKNORA_PLUGIN_HEALTH_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := envDuration("WEKNORA_PLUGIN_SHUTDOWN_TIMEOUT", 20*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxCallTimeout, err := envDuration("WEKNORA_PLUGIN_MAX_CALL_TIMEOUT", time.Hour)
	if err != nil {
		return Config{}, err
	}
	maxConcurrency, err := envUint32("WEKNORA_PLUGIN_MAX_CONCURRENCY", defaultMaxConcurrency)
	if err != nil {
		return Config{}, err
	}
	callsPerMinute, err := envUint32("WEKNORA_PLUGIN_CALLS_PER_MINUTE", defaultCallsPerMinute)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		ListenAddr:        envOrDefault("WEKNORA_PLUGIN_RUNTIME_LISTEN", defaultRuntimeListenAddr),
		ProxyListenAddr:   envOrDefault("WEKNORA_PLUGIN_PROXY_LISTEN", defaultProxyListenAddr),
		ProxyURL:          envOrDefault("WEKNORA_PLUGIN_PROXY_URL", defaultProxyURL),
		AuthToken:         authToken,
		DockerSocket:      envOrDefault("WEKNORA_PLUGIN_DOCKER_SOCKET", defaultDockerSocket),
		SandboxNetwork:    envOrDefault("WEKNORA_PLUGIN_SANDBOX_NETWORK", defaultSandboxNetwork),
		StatePath:         envOrDefault("WEKNORA_PLUGIN_RUNTIME_STATE", defaultStatePath),
		PluginPort:        pluginPort,
		MemoryBytes:       memoryBytes,
		NanoCPUs:          nanoCPUs,
		PIDsLimit:         pidsLimit,
		HealthTimeout:     healthTimeout,
		ShutdownTimeout:   shutdownTimeout,
		MaxCallTimeout:    maxCallTimeout,
		ContainerDNSName:  envOrDefault("WEKNORA_PLUGIN_RUNTIME_DNS_NAME", "weknora-plugin-runtime"),
		RuntimeContainer:  envOrDefault("WEKNORA_PLUGIN_RUNTIME_CONTAINER", runtimeContainer),
		ImagePullPolicy:   envOrDefault("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", defaultImagePullPolicy),
		MaxConcurrency:    maxConcurrency,
		CallsPerMinute:    callsPerMinute,
		RequireSignatures: requireSignatures,
	}
	if raw := strings.TrimSpace(os.Getenv("WEKNORA_PLUGIN_TRUSTED_PUBLISHERS_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.TrustedPublishers); err != nil {
			return Config{}, fmt.Errorf("decode WEKNORA_PLUGIN_TRUSTED_PUBLISHERS_JSON: %w", err)
		}
	}
	if config.AuthToken == "" {
		return Config{}, errors.New("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN or WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN_FILE is required")
	}
	if err := runtimeauth.Validate(config.AuthToken); err != nil {
		return Config{}, err
	}
	if config.PluginPort <= 0 || config.PluginPort > 65535 {
		return Config{}, errors.New("WEKNORA_PLUGIN_PORT must be between 1 and 65535")
	}
	if config.MemoryBytes <= 0 || config.NanoCPUs <= 0 || config.PIDsLimit <= 0 {
		return Config{}, errors.New("plugin resource limits must be positive")
	}
	if config.HealthTimeout <= 0 || config.ShutdownTimeout <= 0 || config.MaxCallTimeout <= 0 {
		return Config{}, errors.New("plugin runtime timeouts must be positive")
	}
	if strings.TrimSpace(config.RuntimeContainer) == "" || strings.TrimSpace(config.ContainerDNSName) == "" {
		return Config{}, errors.New("plugin runtime container identity and DNS name are required")
	}
	if config.MaxConcurrency == 0 || config.CallsPerMinute == 0 {
		return Config{}, errors.New("plugin concurrency and calls-per-minute limits must be positive")
	}
	if config.ImagePullPolicy != imagePullPolicyAlways && config.ImagePullPolicy != imagePullPolicyIfMissing {
		return Config{}, errors.New("WEKNORA_PLUGIN_IMAGE_PULL_POLICY must be always or if-not-present")
	}
	return config, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func envInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func envUint32(name string, fallback uint32) (uint32, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer", name)
	}
	return uint32(parsed), nil
}

func envStrictBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", name)
	}
	return parsed, nil
}
