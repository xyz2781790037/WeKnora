package pluginruntime

import (
	"sync"
	"time"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const runtimeEventHistorySize = 1000

type eventBus struct {
	mu          sync.Mutex
	next        uint64
	history     []*pluginv1.RuntimeEvent
	subscribers map[uint64]chan *pluginv1.RuntimeEvent
	nextSubID   uint64
}

func newEventBus() *eventBus {
	return &eventBus{
		next:        1,
		subscribers: make(map[uint64]chan *pluginv1.RuntimeEvent),
	}
}

func (b *eventBus) publish(pluginID, kind, message string, details map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	event := &pluginv1.RuntimeEvent{
		Sequence:   b.next,
		OccurredAt: timestamppb.New(time.Now().UTC()),
		PluginId:   pluginID,
		Kind:       kind,
		Message:    message,
		Details:    details,
	}
	b.next++
	b.history = append(b.history, event)
	if len(b.history) > runtimeEventHistorySize {
		b.history = append([]*pluginv1.RuntimeEvent(nil), b.history[len(b.history)-runtimeEventHistorySize:]...)
	}
	for id, subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
			close(subscriber)
			delete(b.subscribers, id)
		}
	}
}

func (b *eventBus) subscribe(after uint64) ([]*pluginv1.RuntimeEvent, uint64, <-chan *pluginv1.RuntimeEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Sequence numbers are scoped to one plugin-runtime process. A subscriber
	// may reconnect after the runtime restarted while still holding a sequence
	// from the previous process. In that case replay this process's retained
	// history instead of filtering every new low sequence out.
	if after >= b.next && after != 0 {
		after = 0
	}
	backlog := make([]*pluginv1.RuntimeEvent, 0)
	for _, event := range b.history {
		if event.GetSequence() > after {
			backlog = append(backlog, event)
		}
	}
	id := b.nextSubID
	b.nextSubID++
	channel := make(chan *pluginv1.RuntimeEvent, 128)
	b.subscribers[id] = channel
	return backlog, id, channel
}

func (b *eventBus) unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	channel, ok := b.subscribers[id]
	if !ok {
		return
	}
	delete(b.subscribers, id)
	close(channel)
}
