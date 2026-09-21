package core

import (
	"encoding/json"
	"sync"
)

// Event is one message broadcast to subscribers.
type Event struct {
	Type   string          `json:"type"`
	Device *Device         `json:"device,omitempty"`
	ID     string          `json:"id,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

const (
	EventUpsert  = "upsert"
	EventExpire  = "expire"
	EventStatus  = "status"
	EventInsight = "insight"
	EventCSI     = "csi"
)

// Bus fans events out to subscribers. Publish never blocks: a subscriber that
// cannot keep up drops events rather than stalling the scanners feeding it.
type Bus struct {
	mu     sync.RWMutex
	subs   map[int]chan Event
	nextID int
	closed bool
}

func NewBus() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe returns a channel of events and a function that unsubscribes and
// closes it. The cancel function is safe to call more than once.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Event, buffer)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if sub, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(sub)
			}
		})
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Close shuts the bus down and closes every subscriber channel.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
