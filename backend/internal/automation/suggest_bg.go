package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// 「AI 建議回覆」背景預先產生：顧客訊息入庫、且這段對話不會由 AI 自動回覆（handler=human、自動回覆關閉、AI 轉真人之後）時，
// 等合併秒數沒有新訊息就先產生一組建議存進 conversation_suggestions（每個對話只留最新一筆），完成推 suggestion.ready。
// 收件匣打開對話時 GET /api/conversations/{id}/suggestion 直接拿，不用等 AI。

// 建議狀態（API 的 status）。none 不存表：目前沒有需要回覆的顧客訊息，或還沒排程／不產生。
const (
	SuggestionReady   = "ready"
	SuggestionPending = "pending"
	SuggestionFailed  = "failed"
	SuggestionNone    = "none"
)

// 背景產生的預設值
const (
	DefaultSuggestConcurrency = 3                // 同時最多幾個背景 AI 呼叫，超過排隊
	DefaultSuggestHourlyCap   = 60               // 每帳號每小時背景產生上限（費用保護）
	DefaultSuggestTimeout     = 60 * time.Second // 單次 AI 呼叫逾時
)

// 失敗原因（存進表的 error，給店員看）
const (
	suggestErrNoCustomer = "這段對話沒有顧客訊息可參考"
	suggestErrRefused    = "AI 無法為這則訊息產生建議，請自行回覆"
	suggestErrEmpty      = "AI 沒有產生內容，請自行回覆"
	suggestErrCall       = "AI 呼叫失敗，請稍後再試"
)

// StoredSuggestion 是 conversation_suggestions 的一筆。
type StoredSuggestion struct {
	ConversationID int64
	ForMessageID   int64
	Status         string
	Options        []SuggestOption
	Warning        string
	Error          string
	Model          string
	CreatedAt      time.Time
}

// SuggestionView 是 GET /api/conversations/{id}/suggestion 的回應。
type SuggestionView struct {
	Status       string          `json:"status"` // ready | pending | failed | none
	ForMessageID int64           `json:"forMessageId"`
	Options      []SuggestOption `json:"options"`
	Warning      string          `json:"warning"`
	Error        string          `json:"error"`
	CreatedAt    *time.Time      `json:"createdAt"`
}

// SuggestionReadyEvent 是 SSE suggestion.ready 的資料。
type SuggestionReadyEvent struct {
	ConversationID int64 `json:"conversationId"`
	IGAccountID    int64 `json:"igAccountId"`
	ForMessageID   int64 `json:"forMessageId"`
}

// PublishSuggestionReady 推 suggestion.ready（只推給看得到該帳號的人：Hub 依 AccountID 過濾）。
func PublishSuggestionReady(hub *realtime.Hub, convID, accountID, msgID int64) {
	if hub == nil {
		return
	}
	hub.Publish(realtime.Event{Type: "suggestion.ready", AccountID: accountID,
		Data: SuggestionReadyEvent{ConversationID: convID, IGAccountID: accountID, ForMessageID: msgID}})
}

func optionsJSON(opts []SuggestOption) []byte {
	out := make([]SuggestOption, len(opts))
	for i, o := range opts {
		if o.Basis == nil {
			o.Basis = []string{}
		}
		out[i] = o
	}
	b, _ := json.Marshal(out)
	return b
}

// GetSuggestion 讀對話目前存的建議；沒有回 ok=false。
func (s *Store) GetSuggestion(ctx context.Context, convID int64) (StoredSuggestion, bool, error) {
	r := StoredSuggestion{ConversationID: convID}
	var opts []byte
	err := s.DB.QueryRow(ctx, `
SELECT for_message_id, status, options, warning, error, model, created_at FROM conversation_suggestions WHERE conversation_id = $1`, convID).
		Scan(&r.ForMessageID, &r.Status, &opts, &r.Warning, &r.Error, &r.Model, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if err := json.Unmarshal(opts, &r.Options); err != nil {
		return r, false, err
	}
	if r.Options == nil {
		r.Options = []SuggestOption{}
	}
	return r, true, nil
}

// SaveSuggestion 手動「換一組」成功：直接覆蓋成 ready（不論原本是哪則、什麼狀態）。
func (s *Store) SaveSuggestion(ctx context.Context, convID, msgID int64, sg Suggestion, model string) error {
	_, err := s.DB.Exec(ctx, `
INSERT INTO conversation_suggestions (conversation_id, for_message_id, status, options, warning, error, model, created_at, updated_at)
VALUES ($1, $2, 'ready', $3, $4, '', $5, now(), now())
ON CONFLICT (conversation_id) DO UPDATE SET for_message_id = EXCLUDED.for_message_id, status = 'ready', options = EXCLUDED.options,
  warning = EXCLUDED.warning, error = '', model = EXCLUDED.model, created_at = now(), updated_at = now()`,
		convID, msgID, optionsJSON(sg.Options), sg.Warning, model)
	return err
}

// InvalidateSuggestion 新顧客訊息進來：不是針對 keepMsgID 的舊建議一律刪掉（之後的 GET 不會回過期的）。
func (s *Store) InvalidateSuggestion(ctx context.Context, convID, keepMsgID int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM conversation_suggestions WHERE conversation_id = $1 AND for_message_id <> $2`, convID, keepMsgID)
	return err
}

// markSuggestionPending 排程：這個對話的建議改成「針對 msgID、產生中」。
func (s *Store) markSuggestionPending(ctx context.Context, convID, msgID int64) error {
	_, err := s.DB.Exec(ctx, `
INSERT INTO conversation_suggestions (conversation_id, for_message_id, status) VALUES ($1, $2, 'pending')
ON CONFLICT (conversation_id) DO UPDATE SET for_message_id = EXCLUDED.for_message_id, status = 'pending', options = '[]',
  warning = '', error = '', model = '', created_at = now(), updated_at = now()`, convID, msgID)
	return err
}

// finishSuggestion 背景產生完：只有表上仍是「針對 msgID、pending」才寫入（期間被新訊息作廢或被手動換一組覆蓋就丟掉）。
func (s *Store) finishSuggestion(ctx context.Context, convID, msgID int64, sg Suggestion, model, errText string) (bool, error) {
	status := SuggestionReady
	if errText != "" {
		status, sg = SuggestionFailed, Suggestion{}
	}
	tag, err := s.DB.Exec(ctx, `
UPDATE conversation_suggestions SET status = $3, options = $4, warning = $5, error = $6, model = $7, created_at = now(), updated_at = now()
WHERE conversation_id = $1 AND for_message_id = $2 AND status = 'pending'`,
		convID, msgID, status, optionsJSON(sg.Options), sg.Warning, errText, model)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// dropPendingSuggestion 不產生了（沒 key、超過每小時上限、已不需要回覆）：刪掉針對 msgID 的 pending，GET 回 none。
func (s *Store) dropPendingSuggestion(ctx context.Context, convID, msgID int64) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM conversation_suggestions WHERE conversation_id = $1 AND for_message_id = $2 AND status = 'pending'`,
		convID, msgID)
	return err
}

// CurrentSuggestion 組 GET 的回應：只有表上那筆針對「目前需要回覆的那則顧客訊息」才回，否則 none。
func (s *Store) CurrentSuggestion(ctx context.Context, inb *inbox.Service, convID int64) (SuggestionView, error) {
	v := SuggestionView{Status: SuggestionNone, Options: []SuggestOption{}}
	msgID, _, ok, err := inb.UnansweredInbound(ctx, convID)
	if err != nil || !ok {
		return v, err
	}
	v.ForMessageID = msgID
	r, found, err := s.GetSuggestion(ctx, convID)
	if err != nil || !found || r.ForMessageID != msgID {
		return v, err
	}
	at := r.CreatedAt
	v.Status, v.Options, v.Warning, v.Error, v.CreatedAt = r.Status, r.Options, r.Warning, r.Error, &at
	return v, nil
}

// SuggestWorker 背景產生建議回覆。零值不可用：Store、Inbox 必填；AI 為 nil 或未啟用（沒 key）＝不產生、不報錯。
type SuggestWorker struct {
	Store *Store
	Inbox *inbox.Service
	AI    Suggester
	Calls *CallStats // 計入總覽的 Claude 狀態（nil＝不記）
	Ctx   context.Context

	Concurrency int           // 0＝DefaultSuggestConcurrency
	HourlyCap   int           // 0＝DefaultSuggestHourlyCap；<0＝不限
	Timeout     time.Duration // 0＝DefaultSuggestTimeout
	Now         func() time.Time

	once     sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	inflight map[int64]int64       // convID → 排隊／產生中的 msgID
	hits     map[int64][]time.Time // accountID → 最近一小時的背景產生時間
	wg       sync.WaitGroup
}

func (w *SuggestWorker) init() {
	w.once.Do(func() {
		n := w.Concurrency
		if n <= 0 {
			n = DefaultSuggestConcurrency
		}
		w.sem = make(chan struct{}, n)
		w.inflight = map[int64]int64{}
		w.hits = map[int64][]time.Time{}
	})
}

func (w *SuggestWorker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *SuggestWorker) ctx() context.Context {
	if w.Ctx != nil {
		return w.Ctx
	}
	return context.Background()
}

func (w *SuggestWorker) enabled() bool { return w != nil && w.AI != nil && w.AI.Enabled() }

// Invalidate 新顧客訊息入庫時呼叫：舊的建議作廢。nil 接收者＝不做。
func (w *SuggestWorker) Invalidate(ctx context.Context, convID, newMsgID int64) {
	if w == nil {
		return
	}
	if err := w.Store.InvalidateSuggestion(ctx, convID, newMsgID); err != nil {
		log.Printf("suggest-bg: invalidate conv=%d: %v", convID, err)
	}
}

// Schedule 排一次背景產生（非同步）：msgID 必須仍是「需要回覆的最後一則顧客訊息」，同一則只產生一次。nil 接收者＝不做。
func (w *SuggestWorker) Schedule(convID, msgID int64) {
	if w == nil {
		return
	}
	w.init()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run(w.ctx(), convID, msgID)
	}()
}

// Wait 等目前排程中的背景產生都結束（測試用）。
func (w *SuggestWorker) Wait() { w.wg.Wait() }

// Resume 啟動時把上次中斷留下的 pending 重新排程（條件不符的會自然略過並清掉）。
func (w *SuggestWorker) Resume(ctx context.Context) {
	if w == nil {
		return
	}
	rows, err := w.Store.DB.Query(ctx, `SELECT conversation_id, for_message_id FROM conversation_suggestions WHERE status = 'pending'`)
	if err != nil {
		log.Printf("suggest-bg: resume: %v", err)
		return
	}
	var todo [][2]int64
	for rows.Next() {
		var c, m int64
		if err := rows.Scan(&c, &m); err == nil {
			todo = append(todo, [2]int64{c, m})
		}
	}
	rows.Close()
	for _, t := range todo {
		w.Schedule(t[0], t[1])
	}
}

// allowHourly 每帳號每小時背景產生上限（行程內滑動視窗；重啟歸零）。
func (w *SuggestWorker) allowHourly(accountID int64) bool {
	limit := w.HourlyCap
	if limit == 0 {
		limit = DefaultSuggestHourlyCap
	}
	if limit < 0 {
		return true
	}
	now := w.now()
	cut := now.Add(-time.Hour)
	kept := w.hits[accountID][:0]
	for _, t := range w.hits[accountID] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		w.hits[accountID] = kept
		return false
	}
	w.hits[accountID] = append(kept, now)
	return true
}

func (w *SuggestWorker) run(ctx context.Context, convID, msgID int64) {
	if !w.enabled() {
		// 地端沒設 Claude key（含 DEV_SIMULATOR）：不產生、不報錯；不留 pending
		_ = w.Store.dropPendingSuggestion(ctx, convID, msgID)
		return
	}
	conv, err := w.Inbox.GetConversation(ctx, convID)
	if err != nil {
		if !errors.Is(err, inbox.ErrNotFound) {
			log.Printf("suggest-bg: conv=%d: %v", convID, err)
		}
		return
	}
	if conv.Contact.Risk == inbox.RiskBlocked {
		_ = w.Store.dropPendingSuggestion(ctx, convID, msgID)
		return // 黑名單不產生
	}
	if !w.stillWanted(ctx, convID, msgID) {
		_ = w.Store.dropPendingSuggestion(ctx, convID, msgID) // 有更新的顧客訊息或真人已回（含重啟前留下的 pending）
		return
	}
	w.mu.Lock()
	if w.inflight[convID] == msgID {
		w.mu.Unlock()
		return // 已在排隊或產生中
	}
	w.inflight[convID] = msgID
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.inflight[convID] == msgID {
			delete(w.inflight, convID)
		}
		w.mu.Unlock()
	}()
	// 先佔 inflight 再查表：產生完會先寫表才放掉 inflight，所以同一則不會產生兩次
	if r, found, err := w.Store.GetSuggestion(ctx, convID); err != nil {
		log.Printf("suggest-bg: conv=%d: %v", convID, err)
		return
	} else if found && r.ForMessageID == msgID && r.Status != SuggestionPending {
		return // 同一則顧客訊息只產生一次（或已被手動換一組）
	}
	w.mu.Lock()
	allowed := w.allowHourly(conv.IGAccountID)
	w.mu.Unlock()
	if !allowed {
		log.Printf("suggest-bg: account=%d 超過每小時背景產生上限，conv=%d msg=%d 不產生", conv.IGAccountID, convID, msgID)
		_ = w.Store.dropPendingSuggestion(ctx, convID, msgID)
		return
	}

	if err := w.Store.markSuggestionPending(ctx, convID, msgID); err != nil {
		log.Printf("suggest-bg: conv=%d mark pending: %v", convID, err)
		return
	}
	select { // 同時最多 N 個，其餘在這裡排隊
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-w.sem }()
	if !w.stillWanted(ctx, convID, msgID) { // 排隊期間顧客又傳了／真人已回
		_ = w.Store.dropPendingSuggestion(ctx, convID, msgID)
		return
	}
	w.generate(ctx, conv, msgID)
}

// stillWanted：msgID 仍是需要回覆的最後一則顧客訊息。
func (w *SuggestWorker) stillWanted(ctx context.Context, convID, msgID int64) bool {
	latest, _, ok, err := w.Inbox.UnansweredInbound(ctx, convID)
	if err != nil {
		log.Printf("suggest-bg: conv=%d: %v", convID, err)
		return false
	}
	return ok && latest == msgID
}

func (w *SuggestWorker) generate(ctx context.Context, conv inbox.Conversation, msgID int64) {
	acc := conv.IGAccountID
	st, err := w.Store.GetSettings(ctx, acc)
	if err != nil {
		w.fail(ctx, conv, msgID, "", fmt.Errorf("settings: %w", err), suggestErrCall)
		return
	}
	kb, err := w.Store.ListKnowledge(ctx, acc)
	if err != nil {
		w.fail(ctx, conv, msgID, "", fmt.Errorf("knowledge: %w", err), suggestErrCall)
		return
	}
	if err := w.Store.LoadRecentPosts(ctx, acc, &st); err != nil {
		w.fail(ctx, conv, msgID, "", fmt.Errorf("posts: %w", err), suggestErrCall)
		return
	}
	msgs, err := w.Inbox.ListMessages(ctx, conv.ID, 0, SuggestMaxTurns)
	if err != nil {
		w.fail(ctx, conv, msgID, "", fmt.Errorf("messages: %w", err), suggestErrCall)
		return
	}
	turns := SuggestTurns(msgs)
	model := w.AI.Model()
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = DefaultSuggestTimeout
	}
	var sg Suggestion
	for attempt := 1; attempt <= 2; attempt++ { // 0 個版本時自動重試一次
		cctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		sg, err = w.AI.Suggest(cctx, st, kb, turns)
		cancel()
		if !errors.Is(err, ErrAIDisabled) {
			w.Calls.Record("suggest", start, err)
		}
		log.Printf("suggest-bg: conv=%d account=%d msg=%d model=%s attempt=%d ok=%v options=%d stop=%s elapsed_ms=%d err_type=%T err=%v",
			conv.ID, acc, msgID, model, attempt, err == nil, len(sg.Options), sg.StopReason, time.Since(start).Milliseconds(), err, err)
		if err != nil || len(sg.Options) > 0 {
			break
		}
	}
	switch {
	case errors.Is(err, ErrAIDisabled):
		_ = w.Store.dropPendingSuggestion(ctx, conv.ID, msgID) // 剛好被拿掉 key：不產生、不報錯
		return
	case errors.Is(err, ErrNoCustomerTurn):
		w.fail(ctx, conv, msgID, model, err, suggestErrNoCustomer)
		return
	case errors.Is(err, ErrRefused):
		w.fail(ctx, conv, msgID, model, err, suggestErrRefused)
		return
	case err != nil:
		w.fail(ctx, conv, msgID, model, err, suggestErrCall)
		return
	case len(sg.Options) == 0:
		w.fail(ctx, conv, msgID, model, fmt.Errorf("0 個版本（stop=%s，已重試）", sg.StopReason), suggestErrEmpty)
		return
	}
	ok, err := w.Store.finishSuggestion(ctx, conv.ID, msgID, sg, model, "")
	if err != nil {
		log.Printf("suggest-bg: conv=%d save: %v", conv.ID, err)
		return
	}
	if ok {
		PublishSuggestionReady(w.Inbox.Hub, conv.ID, acc, msgID)
	}
}

// fail 把失敗原因寫進表（中文）並記 log；仍推 suggestion.ready，前端 GET 會拿到 status=failed。
func (w *SuggestWorker) fail(ctx context.Context, conv inbox.Conversation, msgID int64, model string, cause error, reason string) {
	log.Printf("suggest-bg: conv=%d account=%d msg=%d failed: %s (err_type=%T err=%v)", conv.ID, conv.IGAccountID, msgID, reason, cause, cause)
	ok, err := w.Store.finishSuggestion(ctx, conv.ID, msgID, Suggestion{}, model, reason)
	if err != nil {
		log.Printf("suggest-bg: conv=%d save failure: %v", conv.ID, err)
		return
	}
	if ok {
		PublishSuggestionReady(w.Inbox.Hub, conv.ID, conv.IGAccountID, msgID)
	}
}
