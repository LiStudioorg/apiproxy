package admin

import (
	"sync"

	"web2api/internal/router"
)

type Subscriber struct {
	Ch chan map[string]interface{}
}

type Hub struct {
	mu   sync.Mutex
	subs map[*Subscriber]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[*Subscriber]struct{}{}}
}

func (h *Hub) Subscribe() *Subscriber {
	s := &Subscriber{Ch: make(chan map[string]interface{}, 32)}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.Ch)
	}
	h.mu.Unlock()
}

func (h *Hub) PushStatus(platforms []PlatformStatus) {
	h.push(map[string]interface{}{"type": "status", "payload": platforms})
}

func (h *Hub) PushLog(ev router.LogEvent) {
	h.push(map[string]interface{}{"type": "log", "payload": ev})
}

func (h *Hub) push(msg map[string]interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.Ch <- msg:
		default: // 慢客户端丢弃
		}
	}
}