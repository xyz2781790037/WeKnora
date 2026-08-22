package service

import (
	"context"
	"encoding/json"
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
			ProtocolVersion: "1.0.0",
			Image:           "ghcr.io/example/test:1.0.0",
			Types:           []string{"data_source"},
			ConnectorType:   "test_source",
			Config: pluginsdk.ManifestConfig{
				Schema: map[string]any{"type": "object"},
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
