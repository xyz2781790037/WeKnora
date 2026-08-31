package pluginruntime

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

type effectivePluginLimits struct {
	memoryBytes    int64
	nanoCPUs       int64
	pidsLimit      int64
	maxConcurrency uint32
	callsPerMinute uint32
}

func resolvePluginLimits(manifest *pluginv1.PluginManifest, config Config) (effectivePluginLimits, error) {
	limits := effectivePluginLimits{
		memoryBytes:    positiveOr(config.MemoryBytes, defaultMemoryBytes),
		nanoCPUs:       positiveOr(config.NanoCPUs, defaultNanoCPUs),
		pidsLimit:      positiveOr(config.PIDsLimit, defaultPIDsLimit),
		maxConcurrency: positiveOrUint32(config.MaxConcurrency, defaultMaxConcurrency),
		callsPerMinute: positiveOrUint32(config.CallsPerMinute, defaultCallsPerMinute),
	}
	requested := manifest.GetResources()
	if requested == nil {
		return limits, nil
	}
	if requested.GetMemoryBytes() < 0 || requested.GetNanoCpus() < 0 || requested.GetPidsLimit() < 0 {
		return effectivePluginLimits{}, errors.New("plugin resource limits cannot be negative")
	}
	if requested.GetMemoryBytes() > limits.memoryBytes || requested.GetNanoCpus() > limits.nanoCPUs || requested.GetPidsLimit() > limits.pidsLimit {
		return effectivePluginLimits{}, errors.New("plugin container resource request exceeds runtime limit")
	}
	if requested.GetMaxConcurrency() > limits.maxConcurrency {
		return effectivePluginLimits{}, errors.New("plugin maxConcurrency exceeds runtime limit")
	}
	if requested.GetCallsPerMinute() > limits.callsPerMinute {
		return effectivePluginLimits{}, errors.New("plugin callsPerMinute exceeds runtime limit")
	}
	limits.memoryBytes = positiveOr(requested.GetMemoryBytes(), limits.memoryBytes)
	limits.nanoCPUs = positiveOr(requested.GetNanoCpus(), limits.nanoCPUs)
	limits.pidsLimit = positiveOr(requested.GetPidsLimit(), limits.pidsLimit)
	limits.maxConcurrency = positiveOrUint32(requested.GetMaxConcurrency(), limits.maxConcurrency)
	limits.callsPerMinute = positiveOrUint32(requested.GetCallsPerMinute(), limits.callsPerMinute)
	return limits, nil
}

func positiveOr(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func positiveOrUint32(value, fallback uint32) uint32 {
	if value > 0 {
		return value
	}
	return fallback
}

func resourcePolicyFingerprint(limits effectivePluginLimits) string {
	encoded := fmt.Sprintf(
		"memory=%d\nnano_cpus=%d\npids=%d\nconcurrency=%d\ncalls_per_minute=%d\n",
		limits.memoryBytes,
		limits.nanoCPUs,
		limits.pidsLimit,
		limits.maxConcurrency,
		limits.callsPerMinute,
	)
	digest := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(digest[:])
}

func verifyPluginSupplyChain(manifest *pluginv1.PluginManifest, actualImageDigest string, config Config) error {
	supply := manifest.GetSupplyChain()
	if supply == nil || (supply.GetPublisher() == "" && supply.GetImageDigest() == "" && supply.GetSignature() == "") {
		if config.RequireSignatures {
			return errors.New("plugin image signature is required by runtime policy")
		}
		return nil
	}
	publisher := strings.TrimSpace(supply.GetPublisher())
	encodedKey, trusted := config.TrustedPublishers[publisher]
	if !trusted {
		return fmt.Errorf("plugin publisher %q is not trusted", publisher)
	}
	expectedDigest := strings.TrimSpace(supply.GetImageDigest())
	actualDigest := canonicalImageDigest(actualImageDigest)
	if actualDigest == "" || expectedDigest != actualDigest {
		return fmt.Errorf("plugin image digest mismatch: manifest=%s actual=%s", expectedDigest, actualDigest)
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("trusted publisher %q has an invalid Ed25519 public key", publisher)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(supply.GetSignature()))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("plugin supply-chain signature is invalid")
	}
	payload, err := pluginsdk.SupplyChainSigningPayload(
		manifest.GetId(),
		manifest.GetVersion(),
		manifest.GetImage(),
		publisher,
		supply.GetSourceRepository(),
		expectedDigest,
	)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("plugin supply-chain signature verification failed")
	}
	return nil
}

func canonicalImageDigest(value string) string {
	value = strings.TrimSpace(value)
	if _, digest, ok := strings.Cut(value, "@"); ok {
		value = digest
	}
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return ""
	}
	return strings.ToLower(value)
}
