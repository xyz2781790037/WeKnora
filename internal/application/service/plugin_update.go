package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/blang/semver/v4"
)

func (s *PluginService) CheckUpdate(ctx context.Context, id string) (*types.Plugin, error) {
	unlock := s.lockPlugin(id)
	defer unlock()
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin != types.PluginOriginExternal {
		return nil, errors.New("built-in plugins are updated with WeKnora")
	}
	now := time.Now().UTC()
	plugin.UpdateCheckedAt = &now
	plugin.UpdateAvailable = false
	plugin.LatestVersion = plugin.Version
	if strings.TrimSpace(plugin.SourceManifestURL) == "" {
		plugin.UpdateMessage = "该插件没有可重复下载的清单地址，请手动提供新版本清单"
		if saveErr := s.repo.Update(ctx, plugin); saveErr != nil {
			return nil, saveErr
		}
		return plugin, nil
	}

	input := PluginInstallInput{ManifestURL: plugin.SourceManifestURL, CallTimeoutSeconds: plugin.CallTimeoutSeconds}
	if err := resolvePluginManifest(ctx, &input); err != nil {
		plugin.UpdateMessage = err.Error()
		_ = s.repo.Update(ctx, plugin)
		return nil, fmt.Errorf("check plugin update: %w", err)
	}
	manifest, _, _, err := s.validateInstallInput(input)
	if err != nil {
		plugin.UpdateMessage = err.Error()
		_ = s.repo.Update(ctx, plugin)
		return nil, fmt.Errorf("check plugin update: %w", err)
	}
	if manifest.Metadata.ID != plugin.ID {
		plugin.UpdateMessage = "远端清单的插件 ID 与已安装插件不一致"
		_ = s.repo.Update(ctx, plugin)
		return nil, errors.New(plugin.UpdateMessage)
	}
	current, currentErr := semver.Parse(strings.TrimPrefix(plugin.Version, "v"))
	latest, latestErr := semver.Parse(strings.TrimPrefix(manifest.Metadata.Version, "v"))
	if currentErr != nil || latestErr != nil {
		return nil, errors.New("plugin version is not valid semantic version")
	}
	plugin.LatestVersion = latest.String()
	plugin.UpdateAvailable = latest.GT(current)
	if plugin.UpdateAvailable {
		plugin.UpdateMessage = "发现可用新版本"
	} else {
		plugin.UpdateMessage = "当前已是最新版本"
	}
	if err := s.repo.Update(ctx, plugin); err != nil {
		return nil, err
	}
	return plugin, nil
}

// StartLatestUpgradeOperation starts the same rollback-safe upgrade flow as a
// manual upgrade, using the manifest URL retained at installation time.
func (s *PluginService) StartLatestUpgradeOperation(ctx context.Context, id string, input PluginInstallInput) (*PluginOperation, error) {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin != types.PluginOriginExternal {
		return nil, errors.New("built-in plugins cannot be upgraded through the external runtime")
	}
	if strings.TrimSpace(plugin.SourceManifestURL) == "" {
		return nil, errors.New("plugin has no source manifest URL; use manual upgrade")
	}
	input.ManifestYAML = nil
	input.ManifestURL = plugin.SourceManifestURL
	if input.CallTimeoutSeconds <= 0 {
		input.CallTimeoutSeconds = plugin.CallTimeoutSeconds
	}
	operation := s.StartUpgradeOperation(id, input)
	return operation, nil
}
