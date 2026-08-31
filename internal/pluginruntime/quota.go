package pluginruntime

import (
	"fmt"
	"sync"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pluginCallQuota is owned by Manager.quotaMu. Tokens refill continuously so
// callers cannot create a large burst at a wall-clock minute boundary.
type pluginCallQuota struct {
	active      uint32
	tokens      float64
	lastRefill  time.Time
	capacity    uint32
	concurrency uint32
}

func (m *Manager) beginCapabilityCall(
	item *installation,
	capability, method string,
) (func(), error) {
	limits, err := resolvePluginLimits(item.Manifest, m.config)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	pluginID := item.Manifest.GetId()
	now := time.Now()
	m.quotaMu.Lock()
	if m.callQuotas == nil {
		m.callQuotas = make(map[string]*pluginCallQuota)
	}
	quota := m.callQuotas[pluginID]
	if quota == nil {
		quota = &pluginCallQuota{
			tokens:      float64(limits.callsPerMinute),
			lastRefill:  now,
			capacity:    limits.callsPerMinute,
			concurrency: limits.maxConcurrency,
		}
		m.callQuotas[pluginID] = quota
	}
	quota.capacity = limits.callsPerMinute
	quota.concurrency = limits.maxConcurrency
	quota.tokens += now.Sub(quota.lastRefill).Minutes() * float64(quota.capacity)
	if quota.tokens > float64(quota.capacity) {
		quota.tokens = float64(quota.capacity)
	}
	quota.lastRefill = now
	if quota.active >= quota.concurrency {
		limit := quota.concurrency
		m.quotaMu.Unlock()
		m.publishQuotaDenied(pluginID, capability, method, "concurrency", limit)
		return nil, status.Errorf(codes.ResourceExhausted, "plugin concurrency limit %d reached", limit)
	}
	if quota.tokens < 1 {
		limit := quota.capacity
		m.quotaMu.Unlock()
		m.publishQuotaDenied(pluginID, capability, method, "rate", limit)
		return nil, status.Errorf(codes.ResourceExhausted, "plugin call limit %d per minute reached", limit)
	}
	quota.tokens--
	quota.active++
	m.quotaMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.quotaMu.Lock()
			if current := m.callQuotas[pluginID]; current != nil && current.active > 0 {
				current.active--
			}
			m.quotaMu.Unlock()
		})
	}, nil
}

func (m *Manager) publishQuotaDenied(pluginID, capability, method, limitType string, limit uint32) {
	if m.events == nil {
		return
	}
	m.events.publish(pluginID, "quota_denied", "plugin call quota exceeded", map[string]string{
		"capability": capability,
		"method":     method,
		"limit_type": limitType,
		"limit":      fmt.Sprint(limit),
	})
}

func (m *Manager) removeCallQuota(pluginID string) {
	m.quotaMu.Lock()
	delete(m.callQuotas, pluginID)
	m.quotaMu.Unlock()
}

func manifestHasDataAccess(item *installation, required ...pluginv1.DataAccess) (pluginv1.DataAccess, bool) {
	declared := make(map[pluginv1.DataAccess]struct{})
	if permissions := item.Manifest.GetPermissions(); permissions != nil {
		for _, value := range permissions.GetDataAccess() {
			declared[value] = struct{}{}
		}
	}
	for _, value := range required {
		if _, ok := declared[value]; !ok {
			return value, false
		}
	}
	return pluginv1.DataAccess_DATA_ACCESS_UNSPECIFIED, true
}
