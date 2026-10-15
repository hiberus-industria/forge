package daemon

import (
	"sync"

	"github.com/hiberus-industria/forge/internal/daemon/localapi"
)

// hub fans captured events out to live subscribers (TUI). Slow subscribers
// miss events rather than block capture. History (/v1/live/recent) is served
// from the SQLite buffer, which also knows each event's sync state.
type hub struct {
	mu   sync.Mutex
	subs map[chan localapi.LiveEvent]struct{}
}

func newHub() *hub { return &hub{subs: map[chan localapi.LiveEvent]struct{}{}} }

func (h *hub) publish(ev localapi.LiveEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (h *hub) subscribe() (<-chan localapi.LiveEvent, func()) {
	ch := make(chan localapi.LiveEvent, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}
