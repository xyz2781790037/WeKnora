package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// StartAuditSubscriber persists runtime lifecycle, health and sandbox events
// produced by the isolated runtime. Human-triggered lifecycle audit entries
// remain separate so operators can distinguish actor actions from runtime state.
func StartAuditSubscriber(
	runtime runtimeclient.Gateway,
	audit interfaces.AuditLogService,
	cleaner interfaces.ResourceCleaner,
) {
	if runtime == nil || !runtime.Enabled() || audit == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cleaner.RegisterWithName("PluginAuditSubscriber", func() error {
		cancel()
		return nil
	})
	go subscribeRuntimeEvents(ctx, runtime, audit)
}

func subscribeRuntimeEvents(
	ctx context.Context,
	runtime runtimeclient.Gateway,
	audit interfaces.AuditLogService,
) {
	after := persistedRuntimeCursor(ctx, audit)
	for ctx.Err() == nil {
		client, err := runtime.Runtime()
		if err != nil {
			if !waitForRetry(ctx) {
				return
			}
			continue
		}
		stream, err := client.WatchEvents(ctx, &pluginv1.WatchRuntimeEventsRequest{AfterSequence: after})
		if err != nil {
			if !waitForRetry(ctx) {
				return
			}
			continue
		}
		for {
			event, recvErr := stream.Recv()
			if recvErr != nil {
				if !errors.Is(recvErr, context.Canceled) && !errors.Is(recvErr, io.EOF) {
					logger.Warnf(ctx, "[PluginAudit] runtime event stream interrupted: %v", recvErr)
				}
				break
			}
			if err := persistRuntimeEvent(ctx, audit, event); err != nil {
				logger.Warnf(ctx, "[PluginAudit] persist runtime event failed: %v", err)
				break
			}
			if event.GetSequence() > after {
				after = event.GetSequence()
			}
		}
		if !waitForRetry(ctx) {
			return
		}
	}
}

func persistedRuntimeCursor(ctx context.Context, audit interfaces.AuditLogService) uint64 {
	entries, err := audit.List(ctx, 0, &interfaces.AuditLogQuery{
		Limit: 1, Action: types.AuditActionPluginRuntimeEvent, TargetType: "plugin",
	})
	if err != nil {
		logger.Warnf(ctx, "[PluginAudit] restore runtime event cursor failed: %v", err)
		return 0
	}
	if len(entries) == 0 || entries[0] == nil {
		return 0
	}
	var details struct {
		RuntimeSequence uint64 `json:"runtime_sequence"`
	}
	if json.Unmarshal(entries[0].Details, &details) != nil {
		return 0
	}
	return details.RuntimeSequence
}

func persistNetworkDenied(ctx context.Context, audit interfaces.AuditLogService, event *pluginv1.RuntimeEvent) {
	details := map[string]any{
		"sequence": event.GetSequence(),
		"message":  event.GetMessage(),
	}
	for key, value := range event.GetDetails() {
		details[key] = value
	}
	encoded, _ := json.Marshal(details)
	entry := &types.AuditLog{
		TenantID:   0,
		ActorRole:  "plugin_runtime",
		Action:     types.AuditActionPluginNetworkDenied,
		TargetType: "plugin",
		TargetID:   event.GetPluginId(),
		Outcome:    types.AuditOutcomeDenied,
		Details:    types.JSON(encoded),
	}
	if timestamp := event.GetOccurredAt(); timestamp != nil && timestamp.IsValid() {
		entry.CreatedAt = timestamp.AsTime()
	}
	if err := audit.Log(ctx, entry); err != nil {
		logger.Warnf(ctx, "[PluginAudit] persist network denial failed: %v", err)
	}
}

func persistRuntimeEvent(ctx context.Context, audit interfaces.AuditLogService, event *pluginv1.RuntimeEvent) error {
	if event == nil || event.GetPluginId() == "" {
		return nil
	}
	if event.GetKind() == "network_denied" {
		persistNetworkDenied(ctx, audit, event)
	}
	details := map[string]any{
		"runtime_sequence": event.GetSequence(),
		"message":          event.GetMessage(),
	}
	for key, value := range event.GetDetails() {
		details[key] = value
	}
	encoded, _ := json.Marshal(details)
	entry := &types.AuditLog{
		TenantID: 0, ActorRole: "plugin_runtime",
		Action:    types.AuditActionPluginRuntimeEvent,
		ScopeType: event.GetKind(), TargetType: "plugin", TargetID: event.GetPluginId(),
		Outcome: runtimeEventOutcome(event.GetKind()), Details: types.JSON(encoded),
	}
	if timestamp := event.GetOccurredAt(); timestamp != nil && timestamp.IsValid() {
		entry.CreatedAt = timestamp.AsTime()
	}
	return audit.Log(ctx, entry)
}

func runtimeEventOutcome(kind string) types.AuditOutcome {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch {
	case strings.HasSuffix(kind, "_denied"):
		return types.AuditOutcomeDenied
	case strings.Contains(kind, "failed"), strings.Contains(kind, "error"):
		return types.AuditOutcomeFailed
	default:
		return types.AuditOutcomeSuccess
	}
}

func waitForRetry(ctx context.Context) bool {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
