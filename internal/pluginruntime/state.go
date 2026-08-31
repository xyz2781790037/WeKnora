package pluginruntime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type installation struct {
	Manifest       *pluginv1.PluginManifest
	Image          string
	ImageDigest    string
	ContainerName  string
	ProxyToken     string
	CallTimeout    time.Duration
	ResourcePolicy string
	State          pluginv1.RuntimeState
	Message        string
	UpdatedAt      time.Time
}

type persistedState struct {
	Installations []persistedInstallation `json:"installations"`
}

type persistedInstallation struct {
	Manifest       json.RawMessage       `json:"manifest"`
	Image          string                `json:"image"`
	ImageDigest    string                `json:"image_digest"`
	ContainerName  string                `json:"container_name"`
	ProxyToken     string                `json:"proxy_token"`
	CallTimeoutNS  int64                 `json:"call_timeout_ns"`
	ResourcePolicy string                `json:"resource_policy,omitempty"`
	State          pluginv1.RuntimeState `json:"state"`
	Message        string                `json:"message"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

type stateStore struct {
	path string
}

const maxRuntimeStateBytes int64 = 16 * 1024 * 1024

func (s stateStore) load() (map[string]*installation, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]*installation), nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect plugin runtime state: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("plugin runtime state must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("plugin runtime state permissions %04o expose secrets; expected 0600", info.Mode().Perm())
	}
	if info.Size() > maxRuntimeStateBytes {
		return nil, fmt.Errorf("plugin runtime state exceeds %d bytes", maxRuntimeStateBytes)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read plugin runtime state: %w", err)
	}
	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("decode plugin runtime state: %w", err)
	}
	result := make(map[string]*installation, len(persisted.Installations))
	for _, item := range persisted.Installations {
		manifest := &pluginv1.PluginManifest{}
		if err := protojson.Unmarshal(item.Manifest, manifest); err != nil {
			return nil, fmt.Errorf("decode manifest from runtime state: %w", err)
		}
		if manifest.GetId() == "" {
			return nil, errors.New("runtime state contains plugin without id")
		}
		if _, exists := result[manifest.GetId()]; exists {
			return nil, fmt.Errorf("runtime state contains duplicate plugin id %q", manifest.GetId())
		}
		result[manifest.GetId()] = &installation{
			Manifest:       manifest,
			Image:          item.Image,
			ImageDigest:    item.ImageDigest,
			ContainerName:  item.ContainerName,
			ProxyToken:     item.ProxyToken,
			CallTimeout:    time.Duration(item.CallTimeoutNS),
			ResourcePolicy: item.ResourcePolicy,
			State:          item.State,
			Message:        item.Message,
			UpdatedAt:      item.UpdatedAt,
		}
	}
	return result, nil
}

func (s stateStore) save(installations map[string]*installation) error {
	persisted := persistedState{Installations: make([]persistedInstallation, 0, len(installations))}
	ids := make([]string, 0, len(installations))
	for id := range installations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := installations[id]
		manifest, err := protojson.Marshal(item.Manifest)
		if err != nil {
			return fmt.Errorf("encode plugin manifest state: %w", err)
		}
		persisted.Installations = append(persisted.Installations, persistedInstallation{
			Manifest:       manifest,
			Image:          item.Image,
			ImageDigest:    item.ImageDigest,
			ContainerName:  item.ContainerName,
			ProxyToken:     item.ProxyToken,
			CallTimeoutNS:  int64(item.CallTimeout),
			ResourcePolicy: item.ResourcePolicy,
			State:          item.State,
			Message:        item.Message,
			UpdatedAt:      item.UpdatedAt,
		})
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plugin runtime state: %w", err)
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create plugin runtime state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".plugin-state-*")
	if err != nil {
		return fmt.Errorf("create temporary plugin runtime state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write plugin runtime state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync plugin runtime state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close plugin runtime state: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace plugin runtime state: %w", err)
	}
	return nil
}

func containerName(pluginID string) string {
	digest := sha256.Sum256([]byte(pluginID))
	return "weknora-plugin-" + hex.EncodeToString(digest[:8])
}

// pluginNetworkName returns a stable private network name without exposing the
// plugin ID through Docker metadata. Each plugin receives a different network.
func pluginNetworkName(base, pluginID string) string {
	digest := sha256.Sum256([]byte(pluginID))
	return base + "-" + hex.EncodeToString(digest[:8])
}

func newProxyToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
