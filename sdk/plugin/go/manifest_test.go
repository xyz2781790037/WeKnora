package plugin

import (
	"encoding/json"
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
  protocolVersion: 1.1.0
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
  image: example.test/plugin:1.0.0
  types: [data_source]
  connectorType: external_test
  config:
    schema:
      type: object
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
