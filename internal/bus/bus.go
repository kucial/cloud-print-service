// Package bus is a tiny in-process pub/sub used for SSE streaming and job callbacks.
package bus

import (
	"sync"
	"time"
)

// Event is a state-change notification. Topics: "printer.status", "job.state".
type Event struct {
	Topic string    `json:"topic"`
	Data  any       `json:"data"`
	At    time.Time `json:"at"`
}

// Bus is a fan-out with lossy delivery: slow subscribers drop events rather than
// stalling the printer poller or job worker. Consumers must be able to recover state
// via the REST endpoints (SSE is an optimisation, never the source of truth).
type Bus struct {
	mu     sync.RWMutex
	subs   map[int]chan Event
	nextID int
}

func New() *Bus { return &Bus{subs: map[int]chan Event{}} }

func (b *Bus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // drop
		}
	}
}

// Subscribe returns a channel of events and a cancel func that must be called to release it.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 32
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if c, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(c)
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
