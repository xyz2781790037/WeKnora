package plugin

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
)

func TestRuntimeEventOutcomeTreatsPolicyRejectionsAsDenied(t *testing.T) {
	for _, kind := range []string{"network_denied", "data_access_denied", "quota_denied"} {
		t.Run(kind, func(t *testing.T) {
			assert.Equal(t, types.AuditOutcomeDenied, runtimeEventOutcome(kind))
		})
	}
}
