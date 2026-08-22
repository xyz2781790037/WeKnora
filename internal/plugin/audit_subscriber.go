package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// StartAuditSubscriber persists sandbox enforcement events produced by the
// isolated runtime. Lifecycle events are audited by PluginService with the
// human actor and are intentionally not duplicated here.
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
	var after uint64
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
			if event.GetSequence() > after {
				after = event.GetSequence()
			}
			if event.GetKind() == "network_denied" {
				persistNetworkDenied(ctx, audit, event)
			}
		}
		if !waitForRetry(ctx) {
			return
		}
	}
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
