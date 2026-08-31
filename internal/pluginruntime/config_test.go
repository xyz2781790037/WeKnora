package pluginruntime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validRuntimeAuthToken = "0123456789abcdef0123456789abcdef"

func TestLoadConfigDefaultsToAlwaysPull(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", validRuntimeAuthToken)
	t.Setenv("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", "")

	config, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, imagePullPolicyAlways, config.ImagePullPolicy)
}

func TestLoadConfigAcceptsIfNotPresentPullPolicy(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", validRuntimeAuthToken)
	t.Setenv("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", imagePullPolicyIfMissing)

	config, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, imagePullPolicyIfMissing, config.ImagePullPolicy)
}

func TestLoadConfigRejectsUnknownPullPolicy(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", validRuntimeAuthToken)
	t.Setenv("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", "sometimes")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WEKNORA_PLUGIN_IMAGE_PULL_POLICY")
}

func TestLoadConfigRejectsInvalidSignaturePolicy(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", validRuntimeAuthToken)
	t.Setenv("WEKNORA_PLUGIN_REQUIRE_SIGNATURES", "tru")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WEKNORA_PLUGIN_REQUIRE_SIGNATURES")
}

func TestLoadConfigReadsTrustedPublisherKeys(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", validRuntimeAuthToken)
	t.Setenv("WEKNORA_PLUGIN_TRUSTED_PUBLISHERS_JSON", `{"io.example":"public-key"}`)

	config, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "public-key", config.TrustedPublishers["io.example"])
}

func TestLoadConfigRejectsShortAuthToken(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", "short-token")
	_, err := LoadConfig()
	require.ErrorContains(t, err, "at least")
}

func TestLoadConfigRejectsInvalidResourceEnvironment(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", validRuntimeAuthToken)
	t.Setenv("WEKNORA_PLUGIN_MEMORY_BYTES", "five-hundred-megabytes")
	_, err := LoadConfig()
	require.ErrorContains(t, err, "WEKNORA_PLUGIN_MEMORY_BYTES")
}

func TestLoadConfigAcceptsVisibleASCIIToken(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", strings.Repeat("z", 32))
	_, err := LoadConfig()
	require.NoError(t, err)
}
