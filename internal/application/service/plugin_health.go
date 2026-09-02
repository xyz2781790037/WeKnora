package service

import (
	"context"
	"fmt"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	pluginHealthInitialDelay = 15 * time.Second
	pluginHealthInterval     = 30 * time.Second
	pluginHealthCheckTimeout = 15 * time.Second
	pluginRecoveryThreshold  = 2
	pluginRecoveryCooldown   = 2 * time.Minute
)

// StartPluginHealthMonitor owns one process-local monitor. Installed state is
// persisted, so another WeKnora process can continue monitoring after restart.
func StartPluginHealthMonitor(service *PluginService, cleaner interfaces.ResourceCleaner) {
	if service == nil || service.runtime == nil || !service.runtime.Enabled() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cleaner.RegisterWithName("PluginHealthMonitor", func() error {
		cancel()
		return nil
	})
	go service.runHealthMonitor(ctx)
}

func (s *PluginService) runHealthMonitor(ctx context.Context) {
	initial := time.NewTimer(pluginHealthInitialDelay)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-initial.C:
	}
	ticker := time.NewTicker(pluginHealthInterval)
	defer ticker.Stop()
	for {
		s.refreshEnabledPluginHealth(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *PluginService) refreshEnabledPluginHealth(ctx context.Context) {
	plugins, err := s.repo.ListEnabled(ctx)
	if err != nil {
		logger.Warnf(ctx, "[PluginHealth] list enabled plugins failed: %v", err)
		return
	}
	for _, plugin := range plugins {
		if ctx.Err() != nil {
			return
		}
		if plugin == nil || plugin.Origin != types.PluginOriginExternal {
			continue
		}
		if _, err := s.refreshHealth(ctx, plugin.ID, true); err != nil {
			logger.Warnf(ctx, "[PluginHealth] plugin %s unhealthy: %v", plugin.ID, err)
		}
	}
}

func (s *PluginService) refreshHealth(ctx context.Context, id string, autoRecover bool) (*types.Plugin, error) {
	unlock := s.lockPlugin(id)
	defer unlock()
	plugin, err := s.requirePlugin(ctx, id)
	if err != nil {
		return nil, err
	}
	if plugin.Origin == types.PluginOriginBuiltin {
		return plugin, nil
	}
	lifecycle, err := s.runtime.Lifecycle()
	if err != nil {
		return nil, err
	}
	healthTimeout := time.Duration(plugin.CallTimeoutSeconds) * time.Second
	if healthTimeout <= 0 || healthTimeout > pluginHealthCheckTimeout {
		healthTimeout = pluginHealthCheckTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	response, healthErr := lifecycle.HealthCheck(callCtx, &pluginv1.HealthCheckRequest{Context: &pluginv1.InvocationContext{PluginId: plugin.ID}})
	cancel()
	now := time.Now().UTC()
	plugin.LastHealthAt = &now
	if healthErr == nil && response.GetStatus() == pluginv1.HealthCheckResponse_STATUS_SERVING {
		plugin.RuntimeState = types.PluginRuntimeRunning
		plugin.HealthMessage = response.GetMessage()
		plugin.ConsecutiveHealthFailures = 0
		if err := s.repo.Update(ctx, plugin); err != nil {
			return nil, err
		}
		return plugin, nil
	}
	plugin.RuntimeState = types.PluginRuntimeUnhealthy
	plugin.ConsecutiveHealthFailures++
	if healthErr != nil {
		plugin.HealthMessage = healthErr.Error()
	} else {
		plugin.HealthMessage = response.GetMessage()
	}
	if err := s.repo.Update(ctx, plugin); err != nil {
		return nil, err
	}
	if autoRecover && shouldRecoverPlugin(plugin, now) {
		if recovered, recoverErr := s.recoverPlugin(ctx, plugin); recoverErr == nil {
			return recovered, nil
		} else {
			return nil, fmt.Errorf("check plugin health and recover: %w", recoverErr)
		}
	}
	if healthErr != nil {
		return nil, fmt.Errorf("check plugin health: %w", healthErr)
	}
	return nil, fmt.Errorf("check plugin health: %s", plugin.HealthMessage)
}

func shouldRecoverPlugin(plugin *types.Plugin, now time.Time) bool {
	if plugin.ConsecutiveHealthFailures < pluginRecoveryThreshold {
		return false
	}
	return plugin.LastRecoveryAt == nil || now.Sub(*plugin.LastRecoveryAt) >= pluginRecoveryCooldown
}

func (s *PluginService) recoverPlugin(ctx context.Context, plugin *types.Plugin) (*types.Plugin, error) {
	runtimeAPI, err := s.runtime.Runtime()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, pluginRuntimeOperationTimeout)
	defer cancel()
	_, _ = runtimeAPI.Stop(opCtx, &pluginv1.PluginTargetRequest{PluginId: plugin.ID})
	status, err := runtimeAPI.Start(opCtx, &pluginv1.PluginTargetRequest{PluginId: plugin.ID})
	now := time.Now().UTC()
	plugin.LastRecoveryAt = &now
	plugin.RecoveryAttempts++
	if err != nil {
		plugin.RuntimeState = types.PluginRuntimeError
		plugin.HealthMessage = "自动恢复失败: " + err.Error()
		_ = s.repo.Update(ctx, plugin)
		return nil, err
	}
	applyRuntimeStatus(plugin, status)
	plugin.ConsecutiveHealthFailures = 0
	plugin.HealthMessage = "自动恢复成功: " + plugin.HealthMessage
	if err := s.registrar.Register(plugin); err != nil {
		plugin.RuntimeState = types.PluginRuntimeError
		plugin.HealthMessage = "恢复能力注册失败: " + err.Error()
		_ = s.repo.Update(ctx, plugin)
		return nil, err
	}
	if err := s.repo.Update(ctx, plugin); err != nil {
		return nil, err
	}
	return plugin, nil
}
