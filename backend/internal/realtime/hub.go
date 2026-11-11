package realtime

import "sync"

// Event 推給後台前端（SSE）。Data 會被 JSON 編碼。
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
	// AccountID 是事件所屬的 IG 帳號（ig_accounts.id）；SSE 依登入者可見範圍過濾，0＝不屬於任何帳號
	AccountID int64 `json:"-"`
}

// Hub 是行程內的簡易廣播；單機 MVP 足夠，多實例時換 PG LISTEN/NOTIFY。
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default: // 慢的訂閱者丟事件，前端重連時會重抓
		}
	}
}
