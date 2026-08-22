package types

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	PluginOriginBuiltin  = "builtin"
	PluginOriginExternal = "external"

	PluginStatusDisabled = "disabled"
	PluginStatusEnabled  = "enabled"
	PluginStatusError    = "error"

	PluginRuntimeUnknown   = "unknown"
	PluginRuntimeStopped   = "stopped"
	PluginRuntimeStarting  = "starting"
	PluginRuntimeRunning   = "running"
	PluginRuntimeUnhealthy = "unhealthy"
	PluginRuntimeError     = "error"
)

// Plugin is one system-wide installed extension. Tenant-specific settings and
// cursors are deliberately stored by the owning feature (for example,
// data_sources) so this table never becomes a cross-tenant secret store.
type Plugin struct {
	ID                       string     `json:"id" gorm:"type:varchar(191);primaryKey"`
	Name                     string     `json:"name" gorm:"type:varchar(255);not null"`
	Description              string     `json:"description" gorm:"type:text;not null;default:''"`
	Version                  string     `json:"version" gorm:"type:varchar(64);not null"`
	ProtocolVersion          string     `json:"protocol_version" gorm:"type:varchar(64);not null"`
	WeKnoraVersionConstraint string     `json:"weknora_version_constraint" gorm:"type:varchar(128);not null;default:''"`
	Image                    string     `json:"image" gorm:"type:text;not null"`
	ImageDigest              string     `json:"image_digest" gorm:"type:varchar(255);not null;default:''"`
	Origin                   string     `json:"origin" gorm:"type:varchar(16);not null;index"`
	Types                    JSON       `json:"types" gorm:"type:jsonb;not null"`
	Capabilities             JSON       `json:"capabilities" gorm:"type:jsonb;not null"`
	ConnectorType            string     `json:"connector_type" gorm:"type:varchar(64);index"`
	Manifest                 JSON       `json:"manifest" gorm:"type:jsonb;not null"`
	Status                   string     `json:"status" gorm:"type:varchar(16);not null;index"`
	RuntimeState             string     `json:"runtime_state" gorm:"type:varchar(16);not null"`
	HealthMessage            string     `json:"health_message" gorm:"type:text;not null;default:''"`
	LastHealthAt             *time.Time `json:"last_health_at"`
	CallTimeoutSeconds       int        `json:"call_timeout_seconds" gorm:"not null;default:120"`
	InstalledBy              string     `json:"installed_by" gorm:"type:varchar(36);not null"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

func (Plugin) TableName() string { return "plugins" }

func (p *Plugin) BeforeCreate(_ *gorm.DB) error {
	if p.Origin == "" {
		p.Origin = PluginOriginExternal
	}
	if p.Status == "" {
		p.Status = PluginStatusDisabled
	}
	if p.RuntimeState == "" {
		p.RuntimeState = PluginRuntimeUnknown
	}
	if p.CallTimeoutSeconds <= 0 {
		p.CallTimeoutSeconds = 120
	}
	return nil
}

// ValidateStorageFields checks invariants independent of the public manifest
// parser. The service performs full manifest and compatibility validation.
func (p *Plugin) ValidateStorageFields() error {
	p.ID = strings.TrimSpace(p.ID)
	p.Name = strings.TrimSpace(p.Name)
	p.Image = strings.TrimSpace(p.Image)
	if p.ID == "" || p.Name == "" || p.Version == "" || p.ProtocolVersion == "" || p.Image == "" {
		return errors.New("plugin id, name, version, protocol version and image are required")
	}
	if p.Origin != PluginOriginBuiltin && p.Origin != PluginOriginExternal {
		return errors.New("plugin origin must be builtin or external")
	}
	if p.Status != PluginStatusDisabled && p.Status != PluginStatusEnabled && p.Status != PluginStatusError {
		return errors.New("plugin status must be disabled, enabled or error")
	}
	if p.CallTimeoutSeconds <= 0 {
		return errors.New("plugin call timeout must be positive")
	}
	return nil
}
