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

func TestEventBusListFiltersAndReturnsLatestEventsInOrder(t *testing.T) {
	bus := newEventBus()
	bus.publish("io.example.alpha", "first", "first", nil)
	bus.publish("io.example.beta", "other", "other", nil)
	bus.publish("io.example.alpha", "second", "second", nil)
	bus.publish("io.example.alpha", "third", "third", nil)

	events := bus.list("io.example.alpha", 1, 2)
	require.Len(t, events, 2)
	assert.Equal(t, []string{"second", "third"}, []string{events[0].GetKind(), events[1].GetKind()})
	assert.Less(t, events[0].GetSequence(), events[1].GetSequence())
}
