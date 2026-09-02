package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type PluginRuntimeEventQuery struct {
	AfterID       uint64
	Limit         int
	Kind          string
	Outcome       types.AuditOutcome
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
}

func (s *PluginService) ListPersistedRuntimeEvents(ctx context.Context, id string, query PluginRuntimeEventQuery) ([]*pluginv1.RuntimeEvent, error) {
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin != types.PluginOriginExternal || s.audit == nil {
		return []*pluginv1.RuntimeEvent{}, nil
	}
	entries, err := s.audit.List(ctx, 0, &interfaces.AuditLogQuery{
		AfterID: query.AfterID, Limit: query.Limit,
		Action: types.AuditActionPluginRuntimeEvent, Outcome: query.Outcome,
		ScopeType: query.Kind, TargetType: "plugin", TargetID: id,
		CreatedAfter: query.CreatedAfter, CreatedBefore: query.CreatedBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("list persisted plugin runtime events: %w", err)
	}
	result := make([]*pluginv1.RuntimeEvent, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		var stored map[string]any
		_ = json.Unmarshal(entry.Details, &stored)
		message, _ := stored["message"].(string)
		details := make(map[string]string, len(stored))
		for key, value := range stored {
			if key == "message" {
				continue
			}
			details[key] = fmt.Sprint(value)
		}
		details["outcome"] = string(entry.Outcome)
		result = append(result, &pluginv1.RuntimeEvent{
			Sequence: entry.ID, OccurredAt: timestamppb.New(entry.CreatedAt),
			PluginId: id, Kind: entry.ScopeType, Message: message, Details: details,
		})
	}
	return result, nil
}
