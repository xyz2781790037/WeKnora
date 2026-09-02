package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

// PluginManifestInspection is a secret-free, immutable view of the exact
// permissions an administrator must approve before installation or upgrade.
type PluginManifestInspection struct {
	ID                       string                        `json:"id"`
	Name                     string                        `json:"name"`
	Version                  string                        `json:"version"`
	Types                    []string                      `json:"types"`
	Capabilities             []string                      `json:"capabilities"`
	Permissions              pluginsdk.ManifestPermissions `json:"permissions"`
	SupplyChain              pluginsdk.ManifestSupplyChain `json:"supply_chain"`
	Resources                pluginsdk.ManifestResources   `json:"resources"`
	SecretFields             []string                      `json:"secret_fields"`
	ConfigSchema             map[string]any                `json:"config_schema"`
	PermissionDigest         string                        `json:"permission_digest"`
	PermissionChanges        []string                      `json:"permission_changes"`
	WeKnoraVersionConstraint string                        `json:"weknora_version_constraint"`
}

func (s *PluginService) InspectManifest(ctx context.Context, installedID string, input PluginInstallInput) (*PluginManifestInspection, error) {
	if err := resolvePluginManifest(ctx, &input); err != nil {
		return nil, err
	}
	manifest, _, _, err := s.validateInstallInput(input)
	if err != nil {
		return nil, err
	}
	inspection := &PluginManifestInspection{
		ID: manifest.Metadata.ID, Name: manifest.Metadata.Name,
		Version:                  strings.TrimPrefix(manifest.Metadata.Version, "v"),
		Types:                    append([]string{}, manifest.NormalizedTypes()...),
		Capabilities:             append([]string{}, manifest.Spec.Capabilities...),
		Permissions:              cloneManifestPermissions(manifest.Spec.Permissions),
		SupplyChain:              manifest.Spec.SupplyChain,
		Resources:                manifest.Spec.Resources,
		SecretFields:             append([]string{}, manifest.Spec.Config.SecretFields...),
		ConfigSchema:             clonePluginSchema(manifest.Spec.Config.Schema),
		PermissionChanges:        []string{},
		WeKnoraVersionConstraint: manifest.Spec.WeKnoraVersionConstraint,
	}
	inspection.PermissionDigest = permissionDigest(manifest)
	if strings.TrimSpace(installedID) != "" {
		installed, getErr := s.requirePlugin(ctx, installedID)
		if getErr != nil {
			return nil, getErr
		}
		if installed.ID != manifest.Metadata.ID {
			return nil, errors.New("inspected manifest id must match the installed plugin")
		}
		inspection.PermissionChanges = compareManifestPermissions(installed.Manifest, manifest)
	}
	return inspection, nil
}

func (s *PluginService) TypeSpecifications() []pluginsdk.TypeSpecification {
	return pluginsdk.TypeSpecifications()
}

func validatePermissionConfirmation(input PluginInstallInput, manifest *pluginsdk.Manifest) error {
	if !input.PermissionsConfirmed {
		return errors.New("plugin permissions must be confirmed before installation or upgrade")
	}
	expected := permissionDigest(manifest)
	if strings.TrimSpace(input.PermissionDigest) == "" || !strings.EqualFold(strings.TrimSpace(input.PermissionDigest), expected) {
		return errors.New("plugin permission confirmation is stale; inspect the manifest again")
	}
	return nil
}

func permissionDigest(manifest *pluginsdk.Manifest) string {
	if manifest == nil {
		return ""
	}
	// Bind the confirmation to the complete normalized manifest, not only
	// the visible permission arrays. This closes the preview/install race for
	// image, schema and compatibility changes at the same manifest URL.
	encoded, _ := json.Marshal(manifest)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func compareManifestPermissions(current types.JSON, next *pluginsdk.Manifest) []string {
	var previous pluginsdk.Manifest
	if json.Unmarshal(current, &previous) != nil {
		return []string{"无法读取旧版权限，请重新确认全部权限"}
	}
	changes := make([]string, 0)
	if previous.Spec.Permissions.Network != next.Spec.Permissions.Network {
		changes = append(changes, "外网访问权限发生变化")
	}
	if !sameStringSet(previous.Spec.Permissions.AllowedHosts, next.Spec.Permissions.AllowedHosts) {
		changes = append(changes, "允许访问的域名发生变化")
	}
	if !sameStringSet(previous.Spec.Permissions.DataAccess, next.Spec.Permissions.DataAccess) {
		changes = append(changes, "数据访问范围发生变化")
	}
	if !sameStringSet(previous.Spec.Capabilities, next.Spec.Capabilities) {
		changes = append(changes, "插件能力发生变化")
	}
	if !sameStringSet(previous.Spec.Config.SecretFields, next.Spec.Config.SecretFields) {
		changes = append(changes, "密钥字段发生变化")
	}
	if previous.Spec.SupplyChain != next.Spec.SupplyChain {
		changes = append(changes, "供应链发布者、源码或镜像签名发生变化")
	}
	if previous.Spec.Resources != next.Spec.Resources {
		changes = append(changes, "插件资源与调用额度发生变化")
	}
	return changes
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func cloneManifestPermissions(value pluginsdk.ManifestPermissions) pluginsdk.ManifestPermissions {
	value.AllowedHosts = append([]string{}, value.AllowedHosts...)
	value.DataAccess = append([]string{}, value.DataAccess...)
	return value
}

func clonePluginSchema(schema map[string]any) map[string]any {
	encoded, _ := json.Marshal(schema)
	var cloned map[string]any
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}
