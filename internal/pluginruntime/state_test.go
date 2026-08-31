package pluginruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestStateStoreLoadRejectsInsecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"installations":[]}`), 0o644))

	_, err := (stateStore{path: path}).load()
	require.ErrorContains(t, err, "expose secrets")
}

func TestStateStoreLoadRejectsNonRegularFile(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	link := filepath.Join(directory, "state.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"installations":[]}`), 0o600))
	require.NoError(t, os.Symlink(target, link))

	_, err := (stateStore{path: link}).load()
	require.ErrorContains(t, err, "regular file")
}

func TestStateStoreLoadRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxRuntimeStateBytes+1))
	require.NoError(t, file.Close())

	_, err = (stateStore{path: path}).load()
	require.ErrorContains(t, err, "exceeds")
}

func TestStateStoreLoadRejectsDuplicatePluginID(t *testing.T) {
	manifest, err := protojson.Marshal(&pluginv1.PluginManifest{Id: "io.test.duplicate"})
	require.NoError(t, err)
	encoded, err := json.Marshal(persistedState{Installations: []persistedInstallation{
		{Manifest: manifest},
		{Manifest: manifest},
	}})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, encoded, 0o600))

	_, err = (stateStore{path: path}).load()
	require.ErrorContains(t, err, "duplicate plugin id")
}

func TestStateStoreSaveUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := stateStore{path: path}
	require.NoError(t, store.save(map[string]*installation{
		"io.test.plugin": {
			Manifest:   &pluginv1.PluginManifest{Id: "io.test.plugin"},
			ProxyToken: strings.Repeat("a", 64),
		},
	}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = store.load()
	require.NoError(t, err)
}
