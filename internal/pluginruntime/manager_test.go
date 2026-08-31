package pluginruntime

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

func validRuntimeManifest() *pluginv1.PluginManifest {
	return &pluginv1.PluginManifest{
		Id:              "io.test.source",
		Name:            "Test Source",
		Version:         "0.1.0",
		ProtocolVersion: "1.0.0",
		Image:           "ghcr.io/example/test:0.1.0",
		Types:           []pluginv1.PluginType{pluginv1.PluginType_PLUGIN_TYPE_DATA_SOURCE},
		Config:          &pluginv1.ConfigSchema{JsonSchema: `{"type":"object","properties":{}}`},
		Permissions:     &pluginv1.PluginPermissions{},
	}
}

func TestValidateInstallRejectsManifestImageMismatch(t *testing.T) {
	manager := &Manager{config: Config{MaxCallTimeout: time.Hour}}
	_, _, _, err := manager.validateInstall(
		validRuntimeManifest(),
		"ghcr.io/example/other:0.1.0",
		durationpb.New(time.Minute),
	)
	if err == nil {
		t.Fatal("manifest/image mismatch must fail")
	}
}

func TestValidateInstallRejectsNetworkHostsWhenNetworkDisabled(t *testing.T) {
	manager := &Manager{config: Config{MaxCallTimeout: time.Hour}}
	manifest := validRuntimeManifest()
	manifest.Permissions.AllowedHosts = []string{"api.github.com"}
	_, _, _, err := manager.validateInstall(manifest, manifest.Image, durationpb.New(time.Minute))
	if err == nil {
		t.Fatal("network-disabled plugin must not declare hosts")
	}
}

func TestValidateInstallClonesAcceptedManifest(t *testing.T) {
	manager := &Manager{config: Config{MaxCallTimeout: time.Hour}}
	manifest := validRuntimeManifest()
	manifest.Permissions.Network = true
	manifest.Permissions.AllowedHosts = []string{"api.github.com"}
	validated, image, timeout, err := manager.validateInstall(manifest, manifest.Image, durationpb.New(45*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Name = "mutated"
	if validated.GetName() != "Test Source" || image != manifest.Image || timeout != 45*time.Second {
		t.Fatalf("unexpected validation result: %+v %s %s", validated, image, timeout)
	}
}

func TestSameInstallRequestRequiresExactManifestAndTimeout(t *testing.T) {
	manifest := validRuntimeManifest()
	existing := &installation{Manifest: manifest, Image: manifest.Image, CallTimeout: time.Minute}
	assert.True(t, sameInstallRequest(existing, proto.Clone(manifest).(*pluginv1.PluginManifest), manifest.Image, time.Minute))

	changed := proto.Clone(manifest).(*pluginv1.PluginManifest)
	changed.Permissions.Network = true
	changed.Permissions.AllowedHosts = []string{"api.example.com"}
	assert.False(t, sameInstallRequest(existing, changed, manifest.Image, time.Minute))
	assert.False(t, sameInstallRequest(existing, manifest, manifest.Image, 2*time.Minute))
}

func TestValidateInstallRejectsMalformedConfigSchema(t *testing.T) {
	manager := &Manager{config: Config{MaxCallTimeout: time.Hour}}
	manifest := validRuntimeManifest()
	manifest.Config.JsonSchema = `{"type":"object","properties":{},"required":"token"}`
	_, _, _, err := manager.validateInstall(manifest, manifest.Image, durationpb.New(time.Minute))
	require.ErrorContains(t, err, "required must be an array")
}

func TestVerifyHandshakeRequiresExactImageManifest(t *testing.T) {
	expected := validRuntimeManifest()
	actual := proto.Clone(expected).(*pluginv1.PluginManifest)
	actual.Permissions.Network = true
	actual.Permissions.AllowedHosts = []string{"api.example.com"}

	err := verifyHandshake(expected, &pluginv1.HandshakeResponse{
		Manifest: actual, NegotiatedProtocolVersion: "1.0.0",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "manifest mismatch")
}

func TestVerifyHandshakeRequiresNegotiatedProtocolVersion(t *testing.T) {
	expected := validRuntimeManifest()
	err := verifyHandshake(expected, &pluginv1.HandshakeResponse{
		Manifest: expected, NegotiatedProtocolVersion: "1.1.0",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negotiated protocol mismatch")
}

func TestVerifyHandshakeAcceptsExactManifest(t *testing.T) {
	expected := validRuntimeManifest()
	require.NoError(t, verifyHandshake(expected, &pluginv1.HandshakeResponse{
		Manifest: expected, NegotiatedProtocolVersion: "1.0.0",
	}))
}

func TestInstallationImagePrefersImmutableDigest(t *testing.T) {
	item := &installation{
		Image:       "ghcr.io/example/test:latest",
		ImageDigest: "ghcr.io/example/test@sha256:abc",
	}
	assert.Equal(t, item.ImageDigest, installationImage(item))

	item.ImageDigest = ""
	assert.Equal(t, item.Image, installationImage(item))
}

func TestEnsureImageAlwaysPullsByDefault(t *testing.T) {
	docker := &imagePolicyDocker{digest: "sha256:local"}
	manager := &Manager{docker: docker}

	require.NoError(t, manager.ensureImage(context.Background(), "example:test"))
	assert.Equal(t, 1, docker.pullCalls)
	assert.Equal(t, 0, docker.inspectCalls)
}

func TestEnsureImageSkipsPullWhenLocalImageExists(t *testing.T) {
	docker := &imagePolicyDocker{digest: "sha256:local"}
	manager := &Manager{config: Config{ImagePullPolicy: imagePullPolicyIfMissing}, docker: docker}

	require.NoError(t, manager.ensureImage(context.Background(), "example:test"))
	assert.Equal(t, 0, docker.pullCalls)
	assert.Equal(t, 1, docker.inspectCalls)
}

func TestEnsureImagePullsWhenLocalImageIsMissing(t *testing.T) {
	docker := &imagePolicyDocker{inspectErr: assert.AnError}
	manager := &Manager{config: Config{ImagePullPolicy: imagePullPolicyIfMissing}, docker: docker}

	require.NoError(t, manager.ensureImage(context.Background(), "example:test"))
	assert.Equal(t, 1, docker.pullCalls)
	assert.Equal(t, 1, docker.inspectCalls)
}

func TestUninstallIsIdempotentWhenPluginIsAlreadyAbsent(t *testing.T) {
	manager := &Manager{
		installations: make(map[string]*installation),
		pluginLocks:   make(map[string]*sync.Mutex),
	}
	require.NoError(t, manager.Uninstall(context.Background(), "io.test.absent"))
}

func TestPutKeepsMemoryUnchangedWhenStateSaveFails(t *testing.T) {
	manager := &Manager{
		store:         stateStore{path: t.TempDir()}, // Rename cannot replace an existing directory.
		installations: make(map[string]*installation),
	}
	item := &installation{Manifest: validRuntimeManifest()}

	require.Error(t, manager.put(item))
	assert.Nil(t, manager.get(item.Manifest.GetId()))
}

func TestRemoveKeepsMemoryUnchangedWhenStateSaveFails(t *testing.T) {
	manifest := validRuntimeManifest()
	item := &installation{Manifest: manifest}
	manager := &Manager{
		store: stateStore{
			path: filepath.Clean(t.TempDir()), // Rename cannot replace an existing directory.
		},
		installations: map[string]*installation{manifest.GetId(): item},
	}

	require.Error(t, manager.remove(manifest.GetId()))
	assert.NotNil(t, manager.get(manifest.GetId()))
}

type imagePolicyDocker struct {
	digest       string
	inspectErr   error
	pullCalls    int
	inspectCalls int
}

func (*imagePolicyDocker) ping(context.Context) error                          { return nil }
func (*imagePolicyDocker) ensureInternalNetwork(context.Context, string) error { return nil }
func (*imagePolicyDocker) connectContainerToNetwork(context.Context, string, string, []string) error {
	return nil
}
func (*imagePolicyDocker) disconnectContainerFromNetwork(context.Context, string, string) error {
	return nil
}
func (*imagePolicyDocker) removeNetwork(context.Context, string) error { return nil }
func (d *imagePolicyDocker) pullImage(context.Context, string) error {
	d.pullCalls++
	return nil
}
func (d *imagePolicyDocker) imageDigest(context.Context, string) (string, error) {
	d.inspectCalls++
	return d.digest, d.inspectErr
}
func (*imagePolicyDocker) createContainer(context.Context, containerSpec) error { return nil }
func (*imagePolicyDocker) startContainer(context.Context, string) error         { return nil }
func (*imagePolicyDocker) stopContainer(context.Context, string, time.Duration) error {
	return nil
}
func (*imagePolicyDocker) removeContainer(context.Context, string) error { return nil }
func (*imagePolicyDocker) containerState(context.Context, string) (bool, bool, error) {
	return false, false, nil
}
func (*imagePolicyDocker) containerNetworkMode(context.Context, string) (string, error) {
	return "", nil
}
