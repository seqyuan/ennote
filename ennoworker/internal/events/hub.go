package events

import (
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type Hub struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[string]map[uint64]chan domain.RunEvent
	// liveSubscribers mirrors subscribers but carries LiveRunEvent instead.
	liveSubs map[string]map[uint64]chan domain.LiveRunEvent
}

func NewHub() *Hub {
	return &Hub{
		subscribers: make(map[string]map[uint64]chan domain.RunEvent),
		liveSubs:    make(map[string]map[uint64]chan domain.LiveRunEvent),
	}
}

// SubscribeLive returns a channel of LiveRunEvent for the given runID. The
// channel is buffered to capacity; when full, new events are dropped
// (non-blocking publish). Call unsubscribe to stop the subscription and close
// the channel.
func (h *Hub) SubscribeLive(runID string, capacity int) (<-chan domain.LiveRunEvent, func()) {
	if capacity <= 0 {
		capacity = 64
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan domain.LiveRunEvent, capacity)
	if h.liveSubs[runID] == nil {
		h.liveSubs[runID] = make(map[uint64]chan domain.LiveRunEvent)
	}
	h.liveSubs[runID][id] = ch

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if subs := h.liveSubs[runID]; subs != nil {
				if existing, ok := subs[id]; ok {
					delete(subs, id)
					close(existing)
				}
				if len(subs) == 0 {
					delete(h.liveSubs, runID)
				}
			}
		})
	}
	return ch, unsubscribe
}

// PublishLive sends a LiveRunEvent to all subscribers for the runID.
// Non-blocking: on a full channel the oldest buffered event is dropped
// and the sub is kept alive.
//
// @mode emit
func (h *Hub) PublishLive(event domain.LiveRunEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.liveSubs[event.RunID] {
		select {
		case sub <- event:
		default:
			// Channel full — drain one then retry so the sub stays alive
			// and only the oldest event is lost.
			select {
			case <-sub:
			default:
			}
			select {
			case sub <- event:
			default:
				// Still full; event is dropped.
			}
		}
	}
}

func (h *Hub) Subscribe(runID string, capacity int) (<-chan domain.RunEvent, func()) {
	if capacity <= 0 {
		capacity = 64
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	channel := make(chan domain.RunEvent, capacity)
	if h.subscribers[runID] == nil {
		h.subscribers[runID] = make(map[uint64]chan domain.RunEvent)
	}
	h.subscribers[runID][id] = channel

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if subscribers := h.subscribers[runID]; subscribers != nil {
				if existing, ok := subscribers[id]; ok {
					delete(subscribers, id)
					close(existing)
				}
				if len(subscribers) == 0 {
					delete(h.subscribers, runID)
				}
			}
		})
	}
	return channel, unsubscribe
}

// Publish delivers durable RunEvents to every subscriber for the event's runID.
// Non-blocking: a subscriber whose buffer is full is removed and its channel
// closed (the SSE client reconnects and re-syncs via Last-Event-ID).
//
// @mode emit
func (h *Hub) Publish(events ...domain.RunEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, event := range events {
		for id, subscriber := range h.subscribers[event.RunID] {
			select {
			case subscriber <- event:
			default:
				delete(h.subscribers[event.RunID], id)
				close(subscriber)
			}
		}
		if len(h.subscribers[event.RunID]) == 0 {
			delete(h.subscribers, event.RunID)
		}
	}
}
