package plugindev

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/stretchr/testify/require"
)

func TestSignManifestProducesVerifiableEd25519Signature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "publisher.pem")
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0o600))

	manifestPath := filepath.Join(directory, "plugin.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.example.datasource.test
  name: Test source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: ghcr.io/example/test:1.0.0
  types: [data_source]
  connectorType: example_test
  config:
    schema:
      type: object
      properties: {}
    secretFields: []
  permissions:
    network: false
    dataAccess: [document_content, document_metadata]
  supplyChain:
    publisher: io.example
    sourceRepository: https://github.com/example/test
    imageDigest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`), 0o600))

	result, err := SignManifest(manifestPath, keyPath)
	require.NoError(t, err)
	signature, err := base64.StdEncoding.DecodeString(result.Signature)
	require.NoError(t, err)
	require.Equal(t, base64.StdEncoding.EncodeToString(publicKey), result.PublicKey)
	payload, err := pluginsdk.SupplyChainSigningPayload(
		"io.example.datasource.test",
		"1.0.0",
		"ghcr.io/example/test:1.0.0",
		"io.example",
		"https://github.com/example/test",
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	)
	require.NoError(t, err)
	require.True(t, ed25519.Verify(publicKey, payload, signature))
}
