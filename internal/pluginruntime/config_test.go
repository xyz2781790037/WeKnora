package pluginruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaultsToAlwaysPull(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", "test-token")
	t.Setenv("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", "")

	config, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, imagePullPolicyAlways, config.ImagePullPolicy)
}

func TestLoadConfigAcceptsIfNotPresentPullPolicy(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", "test-token")
	t.Setenv("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", imagePullPolicyIfMissing)

	config, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, imagePullPolicyIfMissing, config.ImagePullPolicy)
}

func TestLoadConfigRejectsUnknownPullPolicy(t *testing.T) {
	t.Setenv("WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN", "test-token")
	t.Setenv("WEKNORA_PLUGIN_IMAGE_PULL_POLICY", "sometimes")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WEKNORA_PLUGIN_IMAGE_PULL_POLICY")
}
