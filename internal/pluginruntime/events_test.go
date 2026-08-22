package pluginruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventBusReplaysCurrentHistoryAfterRuntimeSequenceReset(t *testing.T) {
	bus := newEventBus()
	bus.publish("io.weknora.test", "network_denied", "denied", nil)

	backlog, subscriptionID, _ := bus.subscribe(42)
	defer bus.unsubscribe(subscriptionID)

	require.Len(t, backlog, 1)
	assert.Equal(t, uint64(1), backlog[0].GetSequence())
	assert.Equal(t, "network_denied", backlog[0].GetKind())
}

func TestEventBusOnlyReplaysEventsAfterKnownSequence(t *testing.T) {
	bus := newEventBus()
	bus.publish("io.weknora.test", "first", "first", nil)
	bus.publish("io.weknora.test", "second", "second", nil)

	backlog, subscriptionID, _ := bus.subscribe(1)
	defer bus.unsubscribe(subscriptionID)

	require.Len(t, backlog, 1)
	assert.Equal(t, "second", backlog[0].GetKind())
}
