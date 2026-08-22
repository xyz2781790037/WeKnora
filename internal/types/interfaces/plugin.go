package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// PluginRepository stores system-wide plugin installations. It never stores
// tenant instance configuration or credentials.
type PluginRepository interface {
	Create(ctx context.Context, plugin *types.Plugin) error
	Get(ctx context.Context, id string) (*types.Plugin, error)
	List(ctx context.Context) ([]*types.Plugin, error)
	ListEnabled(ctx context.Context) ([]*types.Plugin, error)
	FindByConnectorType(ctx context.Context, connectorType string) (*types.Plugin, error)
	CountDataSourcesByConnectorType(ctx context.Context, connectorType string) (int64, error)
	Update(ctx context.Context, plugin *types.Plugin) error
	UpdateRuntimeState(
		ctx context.Context,
		id string,
		runtimeState string,
		healthMessage string,
		lastHealthAt *time.Time,
	) error
	Delete(ctx context.Context, id string) error
}

// PluginRegistrar exposes enabled plugin capabilities to existing in-process
// registries without letting the plugin access WeKnora storage directly.
type PluginRegistrar interface {
	Register(plugin *types.Plugin) error
	Unregister(plugin *types.Plugin)
	RestoreEnabled(ctx context.Context) error
}
