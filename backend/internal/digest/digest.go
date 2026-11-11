// Package digest 產出每個 IG 帳號的「每日私訊摘要」：前一天的總量與漲跌、熱門意圖、依重要度排序的待處理對話，
// 有 Claude key 時再加 3～5 句 AI 總結（只送統計與短節錄）。每天在帳號設定的時間（店家時區）產生前一天的摘要。
package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// Summarizer 是寫 AI 總結的模型（正式環境是 automation.LiveAI）。
type Summarizer interface {
	DigestSummary(ctx context.Context, data string) (string, error)
}

// 待處理清單的範圍與長度
const (
	maxTodo       = 20 // 存進摘要的待處理對話上限
	maxTodoForAI  = 8  // 送給 AI 的前幾則
	excerptRunes  = 60 // 顧客最後一則訊息的節錄長度
	lookbackDays  = 7  // 「仍未回覆」最多回看幾天前的對話
	topIntents    = 5
	retryAfterErr = 10 * time.Minute // 自動產生失敗後隔多久再試
)

// 摘要的 AI 總結狀態
const (
	SummaryOK     = "ok"
	SummaryNoKey  = "no_key" // 沒設 Claude key：只有統計與清單
	SummaryEmpty  = "empty"  // 當天沒有訊息也沒有待處理：不叫 AI
	SummaryFailed = "failed" // AI 呼叫失敗（統計與清單照存）
)

var (
	ErrNotFound = errors.New("還沒有每日摘要")
	ErrBadDate  = errors.New("日期格式應為 YYYY-MM-DD，且不能是今天或之後")
)

type TodoItem struct {
	ConversationID int64      `json:"conversationId"`
	Contact        string     `json:"contact"` // 顯示名：@username 或名稱
	Score          int64      `json:"score"`
	Reasons        []string   `json:"reasons"`
	Factors        Factors    `json:"factors"`
	LastText       string     `json:"lastText"` // 顧客最後一則訊息的節錄（≤60 字）
	LastInboundAt  *time.Time `json:"lastInboundAt"`
}

type Content struct {
	Date          string            `json:"date"`
	Totals        analytics.Totals  `json:"totals"`
	Previous      analytics.Totals  `json:"previous"` // 再前一天，用來算漲跌
	AutoShare     *float64          `json:"autoShare"`
	PrevAutoShare *float64          `json:"prevAutoShare"`
	MedianSecs    *float64          `json:"medianSecs"`
	Intents       []analytics.Count `json:"intents"`
	Todo          []TodoItem        `json:"todo"`
	TodoTotal     int               `json:"todoTotal"` // 截斷前的總數
	Summary       string            `json:"summary"`
	SummaryStatus string            `json:"summaryStatus"`
	SummaryError  string            `json:"summaryError,omitempty"`
}

type Digest struct {
	ID          int64     `json:"id"`
	AccountID   int64     `json:"igAccountId"`
	Date        string    `json:"date"`
	GeneratedAt time.Time `json:"generatedAt"`
	Content     Content   `json:"content"`
}

// ReadyEvent 是 digest.ready 推送的內容（只帶摘要位置，前端要看再抓）。
type ReadyEvent struct {
	AccountID int64  `json:"igAccountId"`
	Date      string `json:"date"`
	TodoCount int    `json:"todoCount"`
}

type Service struct {
	DB        *pgxpool.Pool
	Analytics *analytics.Service
	Loc       *time.Location
	AI        Summarizer            // nil＝不寫 AI 總結
	Calls     *automation.CallStats // 記 AI 呼叫結果（系統狀態用）；nil＝不記
	Hub       *realtime.Hub         // 產生後推 digest.ready；nil＝不推
	Now       func() time.Time

	mu       sync.Mutex
	failedAt map[int64]time.Time // 自動產生失敗的帳號 → 上次失敗時間
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) aiEnabled() bool {
	if s.AI == nil {
		return false
	}
	if e, ok := s.AI.(interface{ Enabled() bool }); ok {
		return e.Enabled()
	}
	return true
}

// Yesterday 回店家時區的「昨天」（YYYY-MM-DD）。
func (s *Service) Yesterday() string {
	return s.now().In(s.Loc).AddDate(0, 0, -1).Format(time.DateOnly)
}

// ParseDate 驗證摘要日期：YYYY-MM-DD，且必須早於今天（店家時區）。空字串＝昨天。
func (s *Service) ParseDate(v string) (string, error) {
	if v == "" {
		return s.Yesterday(), nil
	}
	d, err := time.ParseInLocation(time.DateOnly, v, s.Loc)
	if err != nil {
		return "", ErrBadDate
	}
	today := s.now().In(s.Loc).Format(time.DateOnly)
	if d.Format(time.DateOnly) >= today {
		return "", ErrBadDate
	}
	return d.Format(time.DateOnly), nil
}

// Latest 回帳號最新一份摘要。
func (s *Service) Latest(ctx context.Context, accountID int64) (Digest, error) {
	var d Digest
	var raw []byte
	var date time.Time
	err := s.DB.QueryRow(ctx, `SELECT id, ig_account_id, digest_date, generated_at, content FROM daily_digests
WHERE ig_account_id = $1 ORDER BY digest_date DESC LIMIT 1`, accountID).Scan(&d.ID, &d.AccountID, &date, &d.GeneratedAt, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Date = date.Format(time.DateOnly)
	err = json.Unmarshal(raw, &d.Content)
	return d, err
}

// Generate 產生（或重新產生，覆蓋）帳號某一天的摘要並推 digest.ready。date＝YYYY-MM-DD（店家時區）。
func (s *Service) Generate(ctx context.Context, accountID int64, date string) (Digest, error) {
	day, err := time.ParseInLocation(time.DateOnly, date, s.Loc)
	if err != nil {
		return Digest{}, ErrBadDate
	}
	from, to := day, day.AddDate(0, 0, 1)
	acc := []int64{accountID}
	c := Content{Date: date, SummaryStatus: SummaryNoKey}

	cur, err := s.Analytics.PeriodBetween(ctx, from, to, acc)
	if err != nil {
		return Digest{}, err
	}
	prev, err := s.Analytics.PeriodBetween(ctx, from.AddDate(0, 0, -1), from, acc)
	if err != nil {
		return Digest{}, err
	}
	c.Totals, c.Previous = cur.Totals, prev.Totals
	c.AutoShare, c.PrevAutoShare, c.MedianSecs = cur.AutoShare, prev.AutoShare, cur.Response.MedianSecs
	intents, err := s.Analytics.IntentsBetween(ctx, from, to, acc)
	if err != nil {
		return Digest{}, err
	}
	c.Intents = intents[:min(len(intents), topIntents)]

	todo, err := s.todo(ctx, accountID, from, to)
	if err != nil {
		return Digest{}, err
	}
	c.TodoTotal = len(todo)
	c.Todo = todo[:min(len(todo), maxTodo)]

	switch {
	case c.Totals.Inbound == 0 && len(c.Todo) == 0:
		c.SummaryStatus = SummaryEmpty
	case !s.aiEnabled():
		c.SummaryStatus = SummaryNoKey
	default:
		start := time.Now()
		sum, err := s.AI.DigestSummary(ctx, BuildAIData(c))
		if !errors.Is(err, automation.ErrAIDisabled) {
			s.Calls.Record("digest", start, err)
		}
		switch {
		case errors.Is(err, automation.ErrAIDisabled):
			c.SummaryStatus = SummaryNoKey
		case err != nil:
			log.Printf("digest: account %d %s: AI 總結失敗：%v", accountID, date, err)
			c.SummaryStatus, c.SummaryError = SummaryFailed, clip(err.Error(), 200)
		default:
			c.Summary, c.SummaryStatus = strings.TrimSpace(sum), SummaryOK
		}
	}

	raw, err := json.Marshal(c)
	if err != nil {
		return Digest{}, err
	}
	d := Digest{AccountID: accountID, Date: date, Content: c}
	if err := s.DB.QueryRow(ctx, `
INSERT INTO daily_digests (ig_account_id, digest_date, content, generated_at) VALUES ($1, $2::date, $3, now())
ON CONFLICT (ig_account_id, digest_date) DO UPDATE SET content = EXCLUDED.content, generated_at = EXCLUDED.generated_at
RETURNING id, generated_at`, accountID, date, raw).Scan(&d.ID, &d.GeneratedAt); err != nil {
		return Digest{}, err
	}
	if s.Hub != nil {
		s.Hub.Publish(realtime.Event{Type: "digest.ready", AccountID: accountID,
			Data: ReadyEvent{AccountID: accountID, Date: date, TodoCount: c.TodoTotal}})
	}
	return d, nil
}

// todo 列出需要處理的對話（依 Score 由高到低）。候選：當天有顧客訊息的對話，或摘要日前 lookbackDays 天內仍開著的對話；
// 狀態（未回覆、轉真人未回、待改進）以產生當下為準，意圖與問題顧客只看當天。
func (s *Service) todo(ctx context.Context, accountID int64, from, to time.Time) ([]TodoItem, error) {
	now := s.now()
	rows, err := s.DB.Query(ctx, `
SELECT c.id, ct.username, ct.name, ct.risk, c.status, c.handler, c.last_inbound_at,
       COALESCE(li.text, ''), COALESCE(lm.direction, ''),
       EXISTS (SELECT 1 FROM messages h WHERE h.conversation_id = c.id AND h.direction = 'out' AND h.sender_type = 'human'
               AND h.created_at >= c.last_inbound_at) AS human_replied,
       EXISTS (SELECT 1 FROM messages m WHERE m.conversation_id = c.id AND m.direction = 'in'
               AND m.created_at >= $2 AND m.created_at < $3) AS in_window,
       COALESCE((SELECT array_agg(DISTINCT m.meta->>'intent') FROM messages m WHERE m.conversation_id = c.id
               AND m.direction = 'in' AND m.meta ? 'intent' AND m.created_at >= $2 AND m.created_at < $3), '{}') AS intents,
       (SELECT count(*) FROM message_reviews r WHERE r.message_id IN (SELECT id FROM messages WHERE conversation_id = c.id)
               AND r.resolved_at IS NULL)::int AS open_reviews
FROM conversations c
JOIN contacts ct ON ct.id = c.contact_id
LEFT JOIN LATERAL (SELECT direction FROM messages WHERE conversation_id = c.id AND sender_type <> 'system'
                   ORDER BY created_at DESC, id DESC LIMIT 1) lm ON true
LEFT JOIN LATERAL (SELECT text FROM messages WHERE conversation_id = c.id AND direction = 'in'
                   ORDER BY created_at DESC, id DESC LIMIT 1) li ON true
WHERE ct.ig_account_id = $1 AND c.last_inbound_at >= $4`, accountID, from, to, from.AddDate(0, 0, -lookbackDays))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TodoItem{}
	for rows.Next() {
		var it TodoItem
		var username, name, risk, status, handler, lastText, lastDir string
		var humanReplied, inWindow bool
		var intents []string
		if err := rows.Scan(&it.ConversationID, &username, &name, &risk, &status, &handler, &it.LastInboundAt,
			&lastText, &lastDir, &humanReplied, &inWindow, &intents, &it.Factors.OpenReviews); err != nil {
			return nil, err
		}
		// 摘要日沒有顧客訊息的：只收「摘要日結束前就在等、到現在還開著」的對話（摘要日之後才開始的留給下一份）
		if !inWindow && (status != "open" || it.LastInboundAt == nil || !it.LastInboundAt.Before(to)) {
			continue
		}
		f := &it.Factors
		open := status == "open" && risk != "blocked" // 黑名單是刻意不回，不算未回覆
		f.Unanswered = open && lastDir == "in"
		f.HandedOff = open && handler == "human" && !humanReplied
		if inWindow {
			f.Complaint = slices.Contains(intents, "complaint")
			f.Reservation = slices.Contains(intents, "reservation")
			if risk == "watch" || risk == "blocked" {
				f.Risk = risk
			}
		}
		if it.LastInboundAt != nil {
			f.WaitMinutes = int(now.Sub(*it.LastInboundAt) / time.Minute)
		}
		if it.Score = Score(*f); it.Score == 0 {
			continue
		}
		it.Reasons = Reasons(*f)
		it.Contact = contactLabel(username, name, it.ConversationID)
		it.LastText = excerpt(lastText)
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	SortTodo(out)
	return out, nil
}

// SortTodo 依分數由高到低；同分時對話 id 小的在前（穩定）。
func SortTodo(items []TodoItem) {
	slices.SortStableFunc(items, func(a, b TodoItem) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return int(a.ConversationID - b.ConversationID)
	})
}

func contactLabel(username, name string, convID int64) string {
	switch {
	case username != "":
		return "@" + username
	case name != "":
		return name
	}
	return fmt.Sprintf("顧客 #%d", convID)
}

// excerpt 把顧客訊息壓成一行、截到 excerptRunes 字。
func excerpt(s string) string {
	return clip(strings.Join(strings.Fields(s), " "), excerptRunes)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// RunDue 為「已過了設定的摘要時間、但還沒有昨天摘要」的啟用中帳號產生摘要；回產生份數。
func (s *Service) RunDue(ctx context.Context) (int, error) {
	local := s.now().In(s.Loc)
	hhmm := local.Format("15:04")
	yesterday := local.AddDate(0, 0, -1).Format(time.DateOnly)
	rows, err := s.DB.Query(ctx, `
SELECT a.id, COALESCE(st.digest_time, $2)
FROM ig_accounts a LEFT JOIN automation_settings st ON st.ig_account_id = a.id
WHERE a.active AND NOT EXISTS (SELECT 1 FROM daily_digests d WHERE d.ig_account_id = a.id AND d.digest_date = $1::date)
ORDER BY a.id`, yesterday, automation.DefaultDigestTime)
	if err != nil {
		return 0, err
	}
	var due []int64
	for rows.Next() {
		var id int64
		var at string
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return 0, err
		}
		if hhmm >= at {
			due = append(due, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range due {
		s.mu.Lock()
		last, failed := s.failedAt[id]
		s.mu.Unlock()
		if failed && s.now().Sub(last) < retryAfterErr {
			continue
		}
		if _, err := s.Generate(ctx, id, yesterday); err != nil {
			log.Printf("digest: account %d %s: %v", id, yesterday, err)
			s.mu.Lock()
			if s.failedAt == nil {
				s.failedAt = map[int64]time.Time{}
			}
			s.failedAt[id] = s.now()
			s.mu.Unlock()
			continue
		}
		s.mu.Lock()
		delete(s.failedAt, id)
		s.mu.Unlock()
		n++
	}
	return n, nil
}

// Run 每分鐘檢查一次是否到了各帳號的摘要時間，直到 ctx 結束。
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if _, err := s.RunDue(ctx); err != nil && ctx.Err() == nil {
			log.Printf("digest: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
