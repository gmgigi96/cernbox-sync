package daemon

import (
	"sync"

	"github.com/gmgigi96/cernbox-sync/ipc"
)

// eventBus distributes daemon events to all subscribed clients.
// Each subscriber gets its own buffered channel; slow subscribers have events
// dropped rather than blocking the publisher.
type eventBus struct {
	mu          sync.Mutex
	subscribers map[chan ipc.Event]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{
		subscribers: make(map[chan ipc.Event]struct{}),
	}
}

// subscribe creates and registers a new event channel for a client.
func (b *eventBus) subscribe() chan ipc.Event {
	ch := make(chan ipc.Event, 64)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// unsubscribe removes the channel from the bus and closes it, causing any
// range loop on the channel to exit cleanly.
func (b *eventBus) unsubscribe(ch chan ipc.Event) {
	b.mu.Lock()
	if _, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// publish sends e to every subscriber. If a subscriber's buffer is full, the
// event is dropped for that subscriber only.
func (b *eventBus) publish(e ipc.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- e:
		default:
			// subscriber too slow — drop this event for it
		}
	}
}
