package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// POST /api/conversations/{id}/suggest：AI 替真人擬回覆草稿（手動「換一組」）。
// 只回草稿給前端放進輸入框；這裡不碰 Inbox.Send／任何 IG 送訊，也不寫 messages。
// 成功時也寫進 conversation_suggestions（覆蓋）並推 suggestion.ready；背景預先產生見 automation/suggest_bg.go。
// GET /api/conversations/{id}/suggestion：讀背景預先產生（或最近一次手動）的建議，不呼叫 AI、不計入限流。

const (
	suggestPerMinute = 10
	suggestTimeout   = 60 * time.Second
)

// suggestLimiter 是每位管理員每分鐘的呼叫上限（行程內記憶體，滑動視窗）；零值可用。
type suggestLimiter struct {
	mu   sync.Mutex
	hits map[int64][]time.Time
	now  func() time.Time // 測試用；nil＝time.Now
}

// allow 記一次呼叫；超限回 false 與需等待的秒數。
func (l *suggestLimiter) allow(adminID int64) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.hits == nil {
		l.hits = map[int64][]time.Time{}
	}
	cut := now.Add(-time.Minute)
	kept := l.hits[adminID][:0]
	for _, t := range l.hits[adminID] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= suggestPerMinute {
		l.hits[adminID] = kept
		wait := int(kept[0].Add(time.Minute).Sub(now).Seconds()) + 1
		return false, max(wait, 1)
	}
	l.hits[adminID] = append(kept, now)
	// 順手清掉久沒用的管理員，避免 map 無限長
	if len(l.hits) > 256 {
		for id, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cut) {
				delete(l.hits, id)
			}
		}
	}
	return true, 0
}

// suggester 回目前可用的建議回覆 AI：測試可換 SuggestAI，正式沿用同一個 LiveAI；未啟用回 nil。
func (s *Server) suggester() automation.Suggester {
	var a automation.Suggester
	switch {
	case s.SuggestAI != nil:
		a = s.SuggestAI
	case s.CaptionAI != nil:
		a = s.CaptionAI
	default:
		return nil
	}
	if !a.Enabled() {
		return nil
	}
	return a
}

// suggestStatus 讓只有收件匣權限的人也能知道要不要顯示「AI 建議」按鈕。
func (s *Server) suggestStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"configured": s.suggester() != nil})
}

func (s *Server) suggestReply(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	ai := s.suggester()
	if ai == nil {
		writeErr(w, http.StatusServiceUnavailable, automation.ErrAIDisabled.Error())
		return
	}
	adminID := auth.UserID(r.Context())
	if ok, wait := s.suggestRL.allow(adminID); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		writeErr(w, http.StatusTooManyRequests, "AI 建議太頻繁，請稍候再試（每分鐘最多 "+strconv.Itoa(suggestPerMinute)+" 次）")
		return
	}

	// 用「對話所屬帳號」的人設與知識庫；EnsureAccount 保證各帳號各有自己的一份
	st, err := s.Auto.Store.GetSettings(r.Context(), c.IGAccountID)
	if err != nil {
		s.autoErr(w, err)
		return
	}
	kb, err := s.Auto.Store.ListKnowledge(r.Context(), c.IGAccountID)
	if err != nil {
		s.autoErr(w, err)
		return
	}
	if err := s.Auto.Store.LoadRecentPosts(r.Context(), c.IGAccountID, &st); err != nil { // 近期 IG 貼文（設定關閉時不帶）
		s.autoErr(w, err)
		return
	}
	msgs, err := s.Inbox.ListMessages(r.Context(), c.ID, 0, automation.SuggestMaxTurns)
	if err != nil {
		s.inboxErr(w, err)
		return
	}
	turns := automation.SuggestTurns(msgs)

	ctx, cancel := context.WithTimeout(r.Context(), suggestTimeout)
	defer cancel()
	start := time.Now()
	sg, err := ai.Suggest(ctx, st, kb, turns)
	if s.Auto != nil {
		s.Auto.Calls.Record("suggest", start, err) // 計入總覽的 Claude 狀態（nil-safe）
	}
	// 只記結果與耗時，不記草稿與對話內容
	log.Printf("suggest: conv=%d account=%d admin=%d model=%s ok=%v options=%d stop=%s elapsed_ms=%d err_type=%T err=%v",
		c.ID, c.IGAccountID, adminID, ai.Model(), err == nil, len(sg.Options), sg.StopReason, time.Since(start).Milliseconds(), err, err)
	switch {
	case errors.Is(err, automation.ErrNoCustomerTurn):
		s.suggest422(w, c, "no_customer_turn", "這段對話沒有顧客訊息可參考")
		return
	case errors.Is(err, automation.ErrRefused):
		s.suggest422(w, c, "refused", "AI 無法為這則訊息產生建議，請自行回覆")
		return
	case errors.Is(err, automation.ErrAIDisabled):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "AI 呼叫失敗，請稍後再試")
		return
	}
	if len(sg.Options) == 0 {
		s.suggest422(w, c, "empty", "AI 沒有產生內容，請自行回覆")
		return
	}
	for i := range sg.Options {
		if sg.Options[i].Basis == nil {
			sg.Options[i].Basis = []string{}
		}
	}
	// 也存成這個對話「最新的建議」（覆蓋背景產生的），其他開著同一對話的人收到 suggestion.ready
	if msgID, ok, err := s.lastCustomerMessage(r.Context(), c.ID); err != nil {
		log.Printf("suggest: conv=%d save: %v", c.ID, err)
	} else if ok {
		if err := s.Auto.Store.SaveSuggestion(r.Context(), c.ID, msgID, sg, ai.Model()); err != nil {
			log.Printf("suggest: conv=%d save: %v", c.ID, err)
		} else {
			automation.PublishSuggestionReady(s.Hub, c.ID, c.IGAccountID, msgID)
		}
	}
	writeJSON(w, http.StatusOK, sg)
}

// suggest422 回 422：中文訊息照舊，另帶機器可讀的 code（no_customer_turn｜refused｜empty），並記 log。
func (s *Server) suggest422(w http.ResponseWriter, c inbox.Conversation, code, msg string) {
	log.Printf("suggest: conv=%d account=%d 422 code=%s", c.ID, c.IGAccountID, code)
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg, "code": code})
}

// lastCustomerMessage 回對話最後一則未刪除的顧客訊息（手動建議存表時的 for_message_id）。
func (s *Server) lastCustomerMessage(ctx context.Context, convID int64) (int64, bool, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `
SELECT id FROM messages WHERE conversation_id = $1 AND direction = 'in' AND deleted_at IS NULL
ORDER BY created_at DESC, id DESC LIMIT 1`, convID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// getSuggestion 讀這個對話目前的建議（背景預先產生或最近一次手動）：只回針對「目前需要回覆的那則顧客訊息」的，
// 過期的一律 none。不呼叫 AI、不計入每分鐘限流；範圍外 404。
func (s *Server) getSuggestion(w http.ResponseWriter, r *http.Request) {
	c, ok := s.scopedConv(w, r)
	if !ok {
		return
	}
	v, err := s.Auto.Store.CurrentSuggestion(r.Context(), s.Inbox, c.ID)
	if err != nil {
		s.autoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
