package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

// POST /api/conversations/{id}/suggest：AI 替真人擬回覆草稿。
// 只回草稿給前端放進輸入框；這裡不碰 Inbox.Send／任何 IG 送訊，也不寫 messages。

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

// suggestTurnText 把一則訊息轉成給模型看的文字：沒文字的媒體以標記代替，顧客已收回的略過。
func suggestTurnText(m inbox.Message) string {
	if m.Deleted {
		return ""
	}
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	if m.Direction == "in" && m.Kind != "" && m.Kind != "text" {
		return "（顧客傳了一則" + map[string]string{"image": "圖片", "video": "影片", "audio": "語音"}[m.Kind] + "，沒有文字）"
	}
	return ""
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
	turns := make([]automation.Turn, 0, len(msgs))
	for _, m := range msgs {
		turns = append(turns, automation.Turn{FromCustomer: m.Direction == "in", Text: suggestTurnText(m)})
	}

	ctx, cancel := context.WithTimeout(r.Context(), suggestTimeout)
	defer cancel()
	start := time.Now()
	sg, err := ai.Suggest(ctx, st, kb, turns)
	if s.Auto != nil {
		s.Auto.Calls.Record("suggest", start, err) // 計入總覽的 Claude 狀態（nil-safe）
	}
	// 只記結果與耗時，不記草稿與對話內容
	log.Printf("suggest: conv=%d account=%d admin=%d model=%s ok=%v elapsed_ms=%d err=%v",
		c.ID, c.IGAccountID, adminID, ai.Model(), err == nil, time.Since(start).Milliseconds(), err)
	switch {
	case errors.Is(err, automation.ErrNoCustomerTurn):
		writeErr(w, http.StatusUnprocessableEntity, "這段對話沒有顧客訊息可參考")
		return
	case errors.Is(err, automation.ErrRefused):
		writeErr(w, http.StatusUnprocessableEntity, "AI 無法為這則訊息產生建議，請自行回覆")
		return
	case errors.Is(err, automation.ErrAIDisabled):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "AI 呼叫失敗，請稍後再試")
		return
	}
	if len(sg.Options) == 0 {
		writeErr(w, http.StatusUnprocessableEntity, "AI 沒有產生內容，請自行回覆")
		return
	}
	for i := range sg.Options {
		if sg.Options[i].Basis == nil {
			sg.Options[i].Basis = []string{}
		}
	}
	writeJSON(w, http.StatusOK, sg)
}
