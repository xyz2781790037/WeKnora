package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifest(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.weknora.datasource.test
  name: Test source
  version: 1.2.3
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: example.test/plugin:1.2.3
  types: [data_source]
  connectorType: external_test
  defaultTimeout: 30s
  config:
    schema:
      type: object
      properties:
        token:
          type: string
    secretFields: [token]
  permissions:
    network: true
    allowedHosts: [api.example.test]
    dataAccess: [document_content]
`))
	require.NoError(t, err)
	assert.Equal(t, "io.weknora.datasource.test", manifest.Metadata.ID)
	assert.Equal(t, "30s", manifest.Spec.DefaultTimeout.String())

	transport, err := manifest.ToProto()
	require.NoError(t, err)
	assert.Equal(t, "external_test", transport.GetConnectorType())
	assert.Equal(t, "object", mustSchema(t, transport.GetConfig().GetJsonSchema())["type"])
}

func TestManifestRejectsUnknownSecretField(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.weknora.datasource.test
  name: Test source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: example.test/plugin:1.0.0
  types: [data_source]
  connectorType: external_test
  config:
    schema:
      type: object
      properties:
        endpoint:
          type: string
    secretFields: [token]
  permissions:
    network: false
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

func TestManifestRejectsNonStringSecretField(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.weknora.datasource.test
  name: Test source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: example.test/plugin:1.0.0
  types: [data_source]
  connectorType: external_test
  config:
    schema:
      type: object
      properties:
        tokens:
          type: array
          items: {type: string}
    secretFields: [tokens]
  permissions:
    network: false
`))
	require.ErrorContains(t, err, "must have type string")
}

func TestManifestRejectsAllowedHostsWithoutNetwork(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.weknora.datasource.test
  name: Test source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: example.test/plugin:1.0.0
  types: [data_source]
  connectorType: external_test
  config:
    schema:
      type: object
      properties: {}
  permissions:
    network: false
    allowedHosts: [api.example.test]
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowedHosts")
}

func TestManifestRejectsUnknownFields(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.weknora.datasource.test
  name: Test source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: example.test/plugin:1.0.0
  types: [data_source]
  connectorType: external_test
  unexpectedField: true
  config:
    schema:
      type: object
  permissions:
    network: false
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpectedField")
}

func TestManifestRejectsMultipleYAMLDocuments(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.weknora.datasource.test
  name: Test source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: example.test/plugin:1.0.0
  types: [data_source]
  connectorType: external_test
  config:
    schema:
      type: object
  permissions:
    network: false
---
kind: Plugin
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one YAML document")
}

func TestManifestConvertsSupplyChainAndResourceLimits(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.example.datasource.signed
  name: Signed source
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: ghcr.io/example/signed:1.0.0
  types: [data_source]
  connectorType: signed_source
  config:
    schema:
      type: object
      properties: {}
  permissions:
    network: false
  supplyChain:
    publisher: io.example
    sourceRepository: https://github.com/example/signed
    imageDigest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  resources:
    memoryBytes: 268435456
    nanoCPUs: 500000000
    pidsLimit: 64
    maxConcurrency: 4
    callsPerMinute: 60
`))
	require.NoError(t, err)
	transport, err := manifest.ToProto()
	require.NoError(t, err)
	assert.Equal(t, "io.example", transport.GetSupplyChain().GetPublisher())
	assert.Equal(t, int64(268435456), transport.GetResources().GetMemoryBytes())
	assert.Equal(t, uint32(4), transport.GetResources().GetMaxConcurrency())
}

func TestDocumentParserRequiresEveryInputDataPermission(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.example.parser.test
  name: Test parser
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: ghcr.io/example/parser:1.0.0
  types: [document_parser]
  capabilities: [file_type:pdf]
  config:
    schema:
      type: object
      properties: {}
  permissions:
    network: false
    dataAccess: [document_content]
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "document_metadata")
}

func TestRetrievalEngineManifest(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.example.retrieval.test
  name: Test retrieval
  version: 1.0.0
spec:
  protocolVersion: 1.1.0
  weknoraVersion: ">=0.2.0"
  image: ghcr.io/example/retrieval:1.0.0
  types: [retrieval_engine]
  capabilities: [keywords, vector]
  retrieverEngineType: example_retrieval
  config:
    schema:
      type: object
      properties: {}
  permissions:
    network: false
    dataAccess: [document_content, document_metadata, query_text, embeddings]
`))
	require.NoError(t, err)
	transport, err := manifest.ToProto()
	require.NoError(t, err)
	assert.Equal(t, "example_retrieval", transport.GetRetrieverEngineType())
}

func TestRetrievalEngineRequiresEmbeddingPermission(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.example.retrieval.test
  name: Test retrieval
  version: 1.0.0
spec:
  protocolVersion: 1.1.0
  weknoraVersion: ">=0.2.0"
  image: ghcr.io/example/retrieval:1.0.0
  types: [retrieval_engine]
  capabilities: [vector]
  retrieverEngineType: example_retrieval
  config:
    schema:
      type: object
      properties: {}
  permissions:
    network: false
    dataAccess: [document_content, document_metadata]
`))
	require.ErrorContains(t, err, "embeddings")
}

func TestRetrievalEngineRequiresProtocol11(t *testing.T) {
	_, err := ParseManifest([]byte(`
apiVersion: weknora.io/v1
kind: Plugin
metadata:
  id: io.example.retrieval.test
  name: Test retrieval
  version: 1.0.0
spec:
  protocolVersion: 1.0.0
  weknoraVersion: ">=0.2.0"
  image: ghcr.io/example/retrieval:1.0.0
  types: [retrieval_engine]
  capabilities: [keywords]
  retrieverEngineType: example_retrieval
  config:
    schema:
      type: object
      properties: {}
  permissions:
    network: false
    dataAccess: [document_content, document_metadata, query_text]
`))
	require.ErrorContains(t, err, "protocolVersion >=1.1.0")
}

func TestRetrieverEngineTypePatternBounds(t *testing.T) {
	assert.True(t, retrieverEngineTypePattern.MatchString("x"))
	assert.True(t, retrieverEngineTypePattern.MatchString("x"+strings.Repeat("1", 49)))
	assert.False(t, retrieverEngineTypePattern.MatchString("x"+strings.Repeat("1", 50)))
}

func TestNegotiateProtocol(t *testing.T) {
	version, err := NegotiateProtocol("1.3.0", "1.1.2")
	require.NoError(t, err)
	assert.Equal(t, "1.1.2", version)

	_, err = NegotiateProtocol("1.3.0", "2.0.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible")
}

func mustSchema(t *testing.T, raw string) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &value))
	return value
}
