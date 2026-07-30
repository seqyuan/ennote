package events

import (
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type Hub struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[string]map[uint64]chan domain.RunEvent
}

func NewHub() *Hub {
	return &Hub{subscribers: make(map[string]map[uint64]chan domain.RunEvent)}
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
