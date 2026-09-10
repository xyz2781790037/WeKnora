package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type pluginRepository struct {
	db *gorm.DB
}

// NewPluginRepository creates the system-wide plugin installation repository.
func NewPluginRepository(db *gorm.DB) interfaces.PluginRepository {
	return &pluginRepository{db: db}
}

func (r *pluginRepository) Create(ctx context.Context, plugin *types.Plugin) error {
	return r.db.WithContext(ctx).Create(plugin).Error
}

func (r *pluginRepository) Get(ctx context.Context, id string) (*types.Plugin, error) {
	var plugin types.Plugin
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&plugin).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &plugin, nil
}

func (r *pluginRepository) List(ctx context.Context) ([]*types.Plugin, error) {
	var plugins []*types.Plugin
	err := r.db.WithContext(ctx).Order("created_at ASC, id ASC").Find(&plugins).Error
	return plugins, err
}

func (r *pluginRepository) ListEnabled(ctx context.Context) ([]*types.Plugin, error) {
	var plugins []*types.Plugin
	err := r.db.WithContext(ctx).
		Where("status = ?", types.PluginStatusEnabled).
		Order("created_at ASC, id ASC").
		Find(&plugins).Error
	return plugins, err
}

func (r *pluginRepository) FindByConnectorType(
	ctx context.Context,
	connectorType string,
) (*types.Plugin, error) {
	var plugin types.Plugin
	if err := r.db.WithContext(ctx).
		Where("connector_type = ?", connectorType).
		First(&plugin).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &plugin, nil
}

func (r *pluginRepository) CountDataSourcesByConnectorType(
	ctx context.Context,
	connectorType string,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.DataSource{}).
		Where("type = ?", connectorType).
		Count(&count).Error
	return count, err
}

func (r *pluginRepository) CountVectorStoresByEngineType(
	ctx context.Context,
	engineType types.RetrieverEngineType,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.VectorStore{}).
		Where("engine_type = ?", engineType).
		Count(&count).Error
	return count, err
}

func (r *pluginRepository) Update(ctx context.Context, plugin *types.Plugin) error {
	return r.db.WithContext(ctx).
		Model(&types.Plugin{}).
		Where("id = ?", plugin.ID).
		Select(
			"name",
			"description",
			"version",
			"protocol_version",
			"we_knora_version_constraint",
			"image",
			"image_digest",
			"types",
			"capabilities",
			"connector_type",
			"manifest",
			"status",
			"runtime_state",
			"health_message",
			"last_health_at",
			"consecutive_health_failures",
			"recovery_attempts",
			"last_recovery_at",
			"source_manifest_url",
			"latest_version",
			"update_available",
			"update_checked_at",
			"update_message",
			"call_timeout_seconds",
			"updated_at",
		).
		Updates(plugin).Error
}

func (r *pluginRepository) UpdateRuntimeState(
	ctx context.Context,
	id string,
	runtimeState string,
	healthMessage string,
	lastHealthAt *time.Time,
) error {
	return r.db.WithContext(ctx).
		Model(&types.Plugin{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"runtime_state":  runtimeState,
			"health_message": healthMessage,
			"last_health_at": lastHealthAt,
			"updated_at":     time.Now().UTC(),
		}).Error
}

func (r *pluginRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.Plugin{}).Error
}
