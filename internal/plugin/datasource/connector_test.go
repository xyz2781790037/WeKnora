package datasource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestEncodePluginConfigMergesSettingsAndCredentials(t *testing.T) {
	encoded, err := encodePluginConfig(&types.DataSourceConfig{
		Settings:    map[string]interface{}{"owner": "acme", "branch": "main"},
		Credentials: map[string]interface{}{"token": "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal(encoded, &values); err != nil {
		t.Fatal(err)
	}
	if values["owner"] != "acme" || values["token"] != "secret" {
		t.Fatalf("unexpected merged config: %#v", values)
	}
}

func TestEncodePluginConfigRejectsSecretCollision(t *testing.T) {
	_, err := encodePluginConfig(&types.DataSourceConfig{
		Settings:    map[string]interface{}{"token": "plain"},
		Credentials: map[string]interface{}{"token": "secret"},
	})
	if err == nil {
		t.Fatal("expected duplicate settings/credentials key to be rejected")
	}
}

func TestDocumentAssemblerChecksSequenceAndChecksum(t *testing.T) {
	content := []byte("hello plugin")
	digest := sha256.Sum256(content)
	assembler := documentAssembler{maxBytes: 1024}
	if err := assembler.begin(&pluginv1.DocumentBegin{
		ExternalId:  "doc-1",
		FileName:    "doc.md",
		ContentSize: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := assembler.chunk(&pluginv1.DocumentChunk{
		ExternalId: "doc-1",
		Sequence:   1,
		Data:       content,
	}); err == nil {
		t.Fatal("out-of-order chunk should fail")
	}

	assembler = documentAssembler{maxBytes: 1024}
	_ = assembler.begin(&pluginv1.DocumentBegin{ExternalId: "doc-1", FileName: "doc.md", ContentSize: int64(len(content))})
	if err := assembler.chunk(&pluginv1.DocumentChunk{ExternalId: "doc-1", Sequence: 0, Data: content}); err != nil {
		t.Fatal(err)
	}
	item, err := assembler.end(&pluginv1.DocumentEnd{
		ExternalId: "doc-1",
		Sha256:     hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(item.Content) != string(content) || item.ExternalID != "doc-1" {
		t.Fatalf("unexpected assembled item: %#v", item)
	}
}

func TestExternalCursorRoundTripAndRejectsInvalidData(t *testing.T) {
	raw := []byte(`{"commit":"abc"}`)
	stored := encodeCursor(raw)
	decoded, err := decodeCursor(stored)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded cursor = %s", decoded)
	}
	stored.ConnectorCursor[opaqueCursorKey] = "not-base64!"
	if _, err := decodeCursor(stored); err == nil {
		t.Fatal("invalid base64 cursor should fail")
	}
}
