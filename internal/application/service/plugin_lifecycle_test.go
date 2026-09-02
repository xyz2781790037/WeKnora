package service

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestRollbackRuntimeUpgradeUsesPreviousManifestImageAndTimeout(t *testing.T) {
	manifest := pluginsdk.Manifest{
		APIVersion: pluginsdk.ManifestAPIVersion,
		Kind:       pluginsdk.ManifestKind,
		Metadata: pluginsdk.ManifestMetadata{
			ID: "io.test.source", Name: "Test Source", Version: "1.0.0",
		},
		Spec: pluginsdk.ManifestSpec{
			ProtocolVersion:          "1.0.0",
			WeKnoraVersionConstraint: ">=0.2.0",
			Image:                    "ghcr.io/example/test:1.0.0",
			Types:                    []string{"data_source"},
			ConnectorType:            "test_source",
			Config: pluginsdk.ManifestConfig{
				Schema: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			DefaultTimeoutText: "2m",
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	require.NoError(t, err)
	previous := &types.Plugin{
		Manifest: types.JSON(manifestJSON), Image: manifest.Spec.Image, CallTimeoutSeconds: 75,
	}
	client := &captureRollbackRuntimeClient{}

	require.NoError(t, (&PluginService{}).rollbackRuntimeUpgrade(client, previous))
	require.NotNil(t, client.request)
	assert.Equal(t, manifest.Spec.Image, client.request.GetImage())
	assert.Equal(t, "1.0.0", client.request.GetManifest().GetVersion())
	assert.Equal(t, 75*time.Second, client.request.GetCallTimeout().AsDuration())
}

func TestPluginServiceLockSerializesSamePlugin(t *testing.T) {
	service := &PluginService{}
	firstUnlock := service.lockPlugin("io.test.plugin")
	var entered atomic.Bool
	done := make(chan struct{})
	go func() {
		unlock := service.lockPlugin("io.test.plugin")
		entered.Store(true)
		unlock()
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	assert.False(t, entered.Load())
	firstUnlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("same-plugin operation did not resume after lock release")
	}
	assert.True(t, entered.Load())
	assert.Empty(t, service.pluginLocks)
}

func TestResolvePluginManifestRejectsInvalidSources(t *testing.T) {
	tests := []struct {
		name  string
		input PluginInstallInput
		want  string
	}{
		{name: "missing", input: PluginInstallInput{}, want: "manifest_yaml or manifest_url is required"},
		{name: "both", input: PluginInstallInput{ManifestYAML: []byte("manifest"), ManifestURL: "https://example.com/plugin.yaml"}, want: "cannot be used together"},
		{name: "http", input: PluginInstallInput{ManifestURL: "http://example.com/plugin.yaml"}, want: "absolute HTTPS URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := resolvePluginManifest(context.Background(), &test.input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestPluginManifestDownloadNormalizesGitHubBlobURL(t *testing.T) {
	downloadURL, client, err := pluginManifestDownload(
		"https://github.com/xyz2781790037/weknora-github-source-plugin/blob/feat/github-source-plugin/plugin.yaml",
	)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t,
		"https://raw.githubusercontent.com/xyz2781790037/weknora-github-source-plugin/feat/github-source-plugin/plugin.yaml",
		downloadURL,
	)
}

func TestPluginManifestDownloadRejectsNonBlobGitHubPage(t *testing.T) {
	_, _, err := pluginManifestDownload("https://github.com/xyz2781790037/weknora-github-source-plugin")
	require.ErrorContains(t, err, "/owner/repository/blob/ref/plugin.yaml")
}

func TestGitHubManifestDownloadLive(t *testing.T) {
	if os.Getenv("WEKNORA_PLUGIN_MANIFEST_LIVE_TEST") != "1" {
		t.Skip("set WEKNORA_PLUGIN_MANIFEST_LIVE_TEST=1 to run the external manifest download")
	}
	input := PluginInstallInput{ManifestURL: "https://github.com/xyz2781790037/weknora-github-source-plugin/blob/feat/github-source-plugin/plugin.yaml"}
	require.NoError(t, resolvePluginManifest(context.Background(), &input))
	require.Contains(t, string(input.ManifestYAML), "id: io.weknora.datasource.github")
}

type captureRollbackRuntimeClient struct {
	pluginv1.PluginRuntimeClient
	request *pluginv1.UpgradePluginRequest
}

func (c *captureRollbackRuntimeClient) Upgrade(
	_ context.Context,
	request *pluginv1.UpgradePluginRequest,
	_ ...grpc.CallOption,
) (*pluginv1.PluginRuntimeStatus, error) {
	c.request = request
	return &pluginv1.PluginRuntimeStatus{}, nil
}
