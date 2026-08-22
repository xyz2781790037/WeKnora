// Package pluginruntime implements the isolated Docker supervisor for external
// WeKnora plugins.
package pluginruntime

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
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
	defaultImagePullPolicy   = imagePullPolicyAlways
	imagePullPolicyAlways    = "always"
	imagePullPolicyIfMissing = "if-not-present"
)

// Config contains runtime-only settings. Plugin authors cannot override the
// resource and network isolation defaults through their manifests.
type Config struct {
	ListenAddr       string
	ProxyListenAddr  string
	ProxyURL         string
	AuthToken        string
	DockerSocket     string
	SandboxNetwork   string
	StatePath        string
	PluginPort       int
	MemoryBytes      int64
	NanoCPUs         int64
	PIDsLimit        int64
	HealthTimeout    time.Duration
	ShutdownTimeout  time.Duration
	MaxCallTimeout   time.Duration
	ContainerDNSName string
	ImagePullPolicy  string
}

// LoadConfig reads plugin-runtime configuration from environment variables.
func LoadConfig() (Config, error) {
	config := Config{
		ListenAddr:       envOrDefault("WEKNORA_PLUGIN_RUNTIME_LISTEN", defaultRuntimeListenAddr),
		ProxyListenAddr:  envOrDefault("WEKNORA_PLUGIN_PROXY_LISTEN", defaultProxyListenAddr),
		ProxyURL:         envOrDefault("WEKNORA_PLUGIN_PROXY_URL", defaultProxyURL),
		AuthToken:        strings.TrimSpace(os.Getenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN")),
		DockerSocket:     envOrDefault("WEKNORA_PLUGIN_DOCKER_SOCKET", defaultDockerSocket),
		SandboxNetwork:   envOrDefault("WEKNORA_PLUGIN_SANDBOX_NETWORK", defaultSandboxNetwork),
		StatePath:        envOrDefault("WEKNORA_PLUGIN_RUNTIME_STATE", defaultStatePath),
		PluginPort:       envInt("WEKNORA_PLUGIN_PORT", defaultPluginPort),
		MemoryBytes:      envInt64("WEKNORA_PLUGIN_MEMORY_BYTES", defaultMemoryBytes),
		NanoCPUs:         envInt64("WEKNORA_PLUGIN_NANO_CPUS", defaultNanoCPUs),
		PIDsLimit:        envInt64("WEKNORA_PLUGIN_PIDS_LIMIT", defaultPIDsLimit),
		HealthTimeout:    envDuration("WEKNORA_PLUGIN_HEALTH_TIMEOUT", 30*time.Second),
		ShutdownTimeout:  envDuration("WEKNORA_PLUGIN_SHUTDOWN_TIMEOUT", 20*time.Second),
		MaxCallTimeout:   envDuration("WEKNORA_PLUGIN_MAX_CALL_TIMEOUT", time.Hour),
		ContainerDNSName: envOrDefault("WEKNORA_PLUGIN_RUNTIME_DNS_NAME", "weknora-plugin-runtime"),
		ImagePullPolicy:  envOrDefault("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", defaultImagePullPolicy),
	}
	if config.AuthToken == "" {
		return Config{}, errors.New("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN is required")
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

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
