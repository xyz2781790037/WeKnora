package pluginruntime

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testImageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestResolvePluginLimitsInheritsAndCapsRuntimeValues(t *testing.T) {
	config := Config{
		MemoryBytes: 1024, NanoCPUs: 1000, PIDsLimit: 10,
		MaxConcurrency: 4, CallsPerMinute: 60,
	}
	manifest := validRuntimeManifest()
	manifest.Resources = &pluginv1.PluginResourceLimits{
		MemoryBytes: 512, NanoCpus: 500, PidsLimit: 5,
		MaxConcurrency: 2, CallsPerMinute: 30,
	}

	limits, err := resolvePluginLimits(manifest, config)
	require.NoError(t, err)
	assert.Equal(t, int64(512), limits.memoryBytes)
	assert.Equal(t, uint32(2), limits.maxConcurrency)
	assert.Equal(t, uint32(30), limits.callsPerMinute)

	manifest.Resources.MemoryBytes = 2048
	_, err = resolvePluginLimits(manifest, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds runtime limit")
}

func TestResourcePolicyFingerprintChangesWithAnyLimit(t *testing.T) {
	baseline := effectivePluginLimits{
		memoryBytes: 512, nanoCPUs: 500, pidsLimit: 5,
		maxConcurrency: 2, callsPerMinute: 30,
	}
	changed := baseline
	changed.callsPerMinute++

	assert.NotEqual(t, resourcePolicyFingerprint(baseline), resourcePolicyFingerprint(changed))
}

func TestVerifyPluginSupplyChainAcceptsTrustedSignatureAndRejectsMutation(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	manifest := validRuntimeManifest()
	manifest.SupplyChain = &pluginv1.PluginSupplyChain{
		Publisher:        "io.example",
		SourceRepository: "https://github.com/example/test-plugin",
		ImageDigest:      testImageDigest,
	}
	payload, err := pluginsdk.SupplyChainSigningPayload(
		manifest.GetId(),
		manifest.GetVersion(),
		manifest.GetImage(),
		manifest.GetSupplyChain().GetPublisher(),
		manifest.GetSupplyChain().GetSourceRepository(),
		manifest.GetSupplyChain().GetImageDigest(),
	)
	require.NoError(t, err)
	manifest.SupplyChain.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	config := Config{TrustedPublishers: map[string]string{
		"io.example": base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
	}}

	require.NoError(t, verifyPluginSupplyChain(manifest, "ghcr.io/example/test@"+testImageDigest, config))
	manifest.Version = "0.2.0"
	require.Error(t, verifyPluginSupplyChain(manifest, testImageDigest, config))
}

func TestVerifyPluginSupplyChainStrictModeRejectsUnsignedManifest(t *testing.T) {
	err := verifyPluginSupplyChain(validRuntimeManifest(), testImageDigest, Config{RequireSignatures: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature is required")
}

func TestManifestHasDataAccessRequiresEveryDeclaredInput(t *testing.T) {
	item := &installation{Manifest: validRuntimeManifest()}
	item.Manifest.Permissions.DataAccess = []pluginv1.DataAccess{
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT,
	}

	missing, ok := manifestHasDataAccess(
		item,
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_CONTENT,
		pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_METADATA,
	)
	assert.False(t, ok)
	assert.Equal(t, pluginv1.DataAccess_DATA_ACCESS_DOCUMENT_METADATA, missing)
}

func TestBeginCapabilityCallEnforcesConcurrencyAndRate(t *testing.T) {
	manifest := validRuntimeManifest()
	manifest.Resources = &pluginv1.PluginResourceLimits{MaxConcurrency: 1, CallsPerMinute: 1}
	item := &installation{Manifest: manifest}
	manager := &Manager{
		config:     Config{MaxConcurrency: 2, CallsPerMinute: 2},
		callQuotas: make(map[string]*pluginCallQuota),
	}

	release, err := manager.beginCapabilityCall(item, "data_source", "Sync")
	require.NoError(t, err)
	_, err = manager.beginCapabilityCall(item, "data_source", "Sync")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	release()

	_, err = manager.beginCapabilityCall(item, "data_source", "Sync")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}
