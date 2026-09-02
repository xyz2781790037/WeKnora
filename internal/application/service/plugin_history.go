package service

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type PluginHistoryEntry struct {
	ID          uint64            `json:"id"`
	Action      types.AuditAction `json:"action"`
	ActorUserID string            `json:"actor_user_id"`
	OldVersion  string            `json:"old_version,omitempty"`
	NewVersion  string            `json:"new_version"`
	CreatedAt   time.Time         `json:"created_at"`
}

// History reuses immutable system audit rows as the plugin version history.
// It deliberately returns only install and upgrade events, not enable/disable
// noise or any manifest configuration values.
func (s *PluginService) History(ctx context.Context, id string) ([]PluginHistoryEntry, error) {
	if _, err := s.requirePlugin(ctx, id); err != nil {
		return nil, err
	}
	if s.audit == nil {
		return []PluginHistoryEntry{}, nil
	}
	actions := []types.AuditAction{
		types.AuditActionPluginInstalled,
		types.AuditActionPluginUpgraded,
	}
	entries := make([]PluginHistoryEntry, 0)
	for _, action := range actions {
		logs, err := s.audit.List(ctx, 0, &interfaces.AuditLogQuery{
			Limit:      100,
			Action:     action,
			TargetType: "plugin",
			TargetID:   id,
		})
		if err != nil {
			return nil, err
		}
		for _, log := range logs {
			var details map[string]any
			_ = json.Unmarshal(log.Details, &details)
			entry := PluginHistoryEntry{
				ID:          log.ID,
				Action:      log.Action,
				ActorUserID: log.ActorUserID,
				OldVersion:  detailString(details, "old_version"),
				NewVersion:  detailString(details, "new_version"),
				CreatedAt:   log.CreatedAt,
			}
			if entry.NewVersion == "" {
				entry.NewVersion = detailString(details, "version")
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].ID > entries[j].ID
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	return entries, nil
}

func detailString(details map[string]any, key string) string {
	value, _ := details[key].(string)
	return value
}
