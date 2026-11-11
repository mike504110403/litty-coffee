// Package monitor 是總覽頁的系統狀態：每分鐘檢查各 IG 帳號（Meta 連線、私訊訂閱、最後收到 webhook）、
// Claude（key 是否有效、最近一次呼叫）、背景工作（webhook 積壓、排程貼文）與資料庫，最新結果放在記憶體（單機）。
// 任一項目由非紅轉紅、或由紅恢復時推 system.alert。
package monitor

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// 燈號
const (
	Green  = "green"
	Yellow = "yellow"
	Red    = "red"
)

// 門檻（見 CLAUDE.md「系統狀態」）
const (
	DefaultInterval   = time.Minute
	MetaSlow          = 3 * time.Second    // Meta 回應超過＝黃
	WebhookQuiet      = 48 * time.Hour     // 超過這麼久沒收到 webhook＝黃
	WebhookLookback   = 7 * 24 * time.Hour // 查最後收到 webhook 的範圍
	KeyCheckEvery     = 5 * time.Minute    // Claude key 最多每 5 分鐘驗一次（換 key 時立即驗）
	AISlow            = 30 * time.Second   // 最近一次 AI 呼叫超過＝黃
	AIFailRed         = 3                  // AI 連續失敗幾次＝紅
	QueueYellow       = 2 * time.Minute    // 最舊的待處理 webhook 超過＝黃
	QueueRed          = 10 * time.Minute   // 超過＝紅
	PostOverdueYellow = 5 * time.Minute    // 排程貼文該發未發超過＝黃
	PostOverdueRed    = 15 * time.Minute   // 超過＝紅（排程器可能沒在跑）
	FailedPostWindow  = 7 * 24 * time.Hour // 只算這段期間內失敗的貼文
	DBSlow            = 500 * time.Millisecond
	checkTimeout      = 10 * time.Second
)

// ErrKeyInvalid：PingAI 回這個（或包住它）＝key 被拒（401／403），燈號紅；其他錯誤視為暫時連不上（黃）。
var ErrKeyInvalid = errors.New("Claude API Key 無效")

type Light struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CheckedAt time.Time `json:"checkedAt"`
}

func rank(s string) int {
	switch s {
	case Red:
		return 2
	case Yellow:
		return 1
	}
	return 0
}

// worstOf 回最嚴重的那一個（同級取先出現的）。
func worstOf(ls ...Light) Light {
	w := ls[0]
	for _, l := range ls[1:] {
		if rank(l.Status) > rank(w.Status) {
			w = l
		}
	}
	return w
}

type PostStats struct {
	Failed      int        `json:"failed"`      // FailedPostWindow 內發佈失敗的
	Overdue     int        `json:"overdue"`     // 該發未發超過 PostOverdueYellow 的
	OverdueSecs int64      `json:"overdueSecs"` // 最久的那篇遲了幾秒
	NextAt      *time.Time `json:"nextAt"`      // 下一篇排程時間
}

type AccountStatus struct {
	Light                       // 帳號總燈號：連線與 webhook 取較嚴重者
	ID               int64      `json:"id"`
	IGUserID         string     `json:"igUserId"`
	Username         string     `json:"username"`
	Name             string     `json:"name"`
	ProfilePic       string     `json:"profilePic"`
	Active           bool       `json:"active"`
	Connection       Light      `json:"connection"`
	LatencyMs        *int64     `json:"latencyMs"`
	Webhook          Light      `json:"webhook"`
	Subscribed       bool       `json:"subscribed"`
	LastWebhookAt    *time.Time `json:"lastWebhookAt"`
	AutoReplyEnabled bool       `json:"autoReplyEnabled"`
	Posts            PostStats  `json:"posts"`
}

type ClaudeStatus struct {
	Light
	Configured       bool                   `json:"configured"`
	KeyValid         *bool                  `json:"keyValid"` // nil＝還沒驗過或暫時連不上
	KeyCheckedAt     *time.Time             `json:"keyCheckedAt"`
	KeyError         string                 `json:"keyError,omitempty"`
	LastCall         *automation.CallRecord `json:"lastCall"`
	ConsecutiveFails int                    `json:"consecutiveFails"`
}

type QueueStatus struct {
	Light
	Pending  int        `json:"pending"`
	OldestAt *time.Time `json:"oldestAt"`
}

type DBStatus struct {
	Light
	LatencyMs int64 `json:"latencyMs"`
}

// Snapshot 是一次完整檢查的結果（含全部帳號；給 API 前要經 View 依可見範圍過濾）。
type Snapshot struct {
	CheckedAt time.Time       `json:"checkedAt"`
	Accounts  []AccountStatus `json:"accounts"`
	Claude    ClaudeStatus    `json:"claude"`
	Queue     QueueStatus     `json:"queue"`
	DB        DBStatus        `json:"db"`
}

// MetaChecker 用帳號的 token 讀該 IG 帳號（ig.OAuthClient／ig.FakeOAuth 都符合）。
type MetaChecker interface {
	Account(ctx context.Context, token, igUserID string) (ig.AccountInfo, error)
}

// Alert 是 system.alert 的內容。
type Alert struct {
	Key         string    `json:"key"`
	Kind        string    `json:"kind"` // account | posts | claude | queue | db
	IGAccountID int64     `json:"igAccountId,omitempty"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	Recovered   bool      `json:"recovered"` // true＝由紅恢復
	At          time.Time `json:"at"`
}

type Monitor struct {
	DB    *pgxpool.Pool
	Meta  MetaChecker                                                                    // nil＝不打 Meta
	Creds func(ctx context.Context, accountID int64) (igUserID, token string, err error) // account.Service.Credentials
	// NoteAPIError：token 失效時寫到連線的 last_error（account.Service.NoteAPIError），設定頁才看得到；nil＝不寫
	NoteAPIError func(ctx context.Context, accountID int64, err error)
	AIKey        func() string                               // 目前生效的 Claude key（空＝未設）
	PingAI       func(ctx context.Context, key string) error // Models List；key 被拒時回 ErrKeyInvalid
	Calls        *automation.CallStats
	Hub          *realtime.Hub // 推 system.alert；nil＝不推
	Now          func() time.Time

	Interval time.Duration // 0＝DefaultInterval
	MetaSlow time.Duration // 0＝MetaSlow（測試可調小）

	checkMu sync.Mutex // 同時只跑一輪檢查
	keyHash [32]byte
	keyAt   time.Time
	keyErr  error
	keyDone bool
	prev    map[string]string // 上一輪各項目的燈號（判斷轉紅／恢復）

	mu   sync.RWMutex
	snap Snapshot
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Latest 回最近一次檢查結果（複本）；還沒檢查過時 CheckedAt 為零值。
func (m *Monitor) Latest() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.snap
	s.Accounts = slices.Clone(s.Accounts)
	return s
}

// Run 立即檢查一次，之後每 Interval 檢查，直到 ctx 結束。
func (m *Monitor) Run(ctx context.Context) {
	iv := m.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	tick := time.NewTicker(iv)
	defer tick.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Check 跑一輪完整檢查、存下結果、必要時推 system.alert，並回傳結果。
func (m *Monitor) Check(ctx context.Context) Snapshot {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	now := m.now()
	s := Snapshot{CheckedAt: now}
	s.DB = m.checkDB(ctx, now)
	if s.DB.Status == Red {
		// 資料庫連不上：帳號沿用上一輪，背景工作無法判斷
		s.Accounts = m.Latest().Accounts
		s.Queue = QueueStatus{Light: Light{Yellow, "無法檢查（資料庫連不上）", now}}
	} else {
		var err error
		if s.Accounts, err = m.checkAccounts(ctx, now); err != nil {
			log.Printf("monitor: accounts: %v", err)
			s.Accounts = m.Latest().Accounts
		}
		s.Queue = m.checkQueue(ctx, now)
	}
	s.Claude = m.checkClaude(ctx, now)

	m.mu.Lock()
	m.snap = s
	m.mu.Unlock()
	m.emitAlerts(s)
	return s
}

func (m *Monitor) checkDB(ctx context.Context, now time.Time) DBStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	err := m.DB.Ping(ctx)
	lat := time.Since(start)
	d := DBStatus{LatencyMs: lat.Milliseconds()}
	switch {
	case err != nil:
		d.Light = Light{Red, "資料庫連不上：" + clip(err.Error(), 120), now}
	case lat > DBSlow:
		d.Light = Light{Yellow, fmt.Sprintf("資料庫回應慢（%d ms）", lat.Milliseconds()), now}
	default:
		d.Light = Light{Green, fmt.Sprintf("正常（%d ms）", lat.Milliseconds()), now}
	}
	return d
}

func (m *Monitor) checkQueue(ctx context.Context, now time.Time) QueueStatus {
	var q QueueStatus
	if err := m.DB.QueryRow(ctx, `SELECT count(*), min(received_at) FROM webhook_events WHERE processed_at IS NULL`).
		Scan(&q.Pending, &q.OldestAt); err != nil {
		q.Light = Light{Yellow, "無法檢查：" + clip(err.Error(), 120), now}
		return q
	}
	q.Light = QueueLight(q.Pending, q.OldestAt, now)
	return q
}

// QueueLight：沒有積壓＝綠；最舊一筆等超過 QueueYellow＝黃、QueueRed＝紅。
func QueueLight(pending int, oldest *time.Time, now time.Time) Light {
	if pending == 0 || oldest == nil {
		return Light{Green, "沒有積壓", now}
	}
	age := now.Sub(*oldest)
	msg := fmt.Sprintf("%d 筆待處理，最舊已等 %s", pending, ago(age))
	switch {
	case age > QueueRed:
		return Light{Red, "私訊處理卡住：" + msg, now}
	case age > QueueYellow:
		return Light{Yellow, msg, now}
	}
	return Light{Green, fmt.Sprintf("%d 筆處理中", pending), now}
}

func (m *Monitor) checkAccounts(ctx context.Context, now time.Time) ([]AccountStatus, error) {
	rows, err := m.DB.Query(ctx, `
SELECT a.id, a.ig_user_id, a.username, a.name, a.profile_pic, a.active, a.webhook_subscribed, COALESCE(st.auto_reply_enabled, true)
FROM ig_accounts a LEFT JOIN automation_settings st ON st.ig_account_id = a.id ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	var list []AccountStatus
	for rows.Next() {
		var a AccountStatus
		if err := rows.Scan(&a.ID, &a.IGUserID, &a.Username, &a.Name, &a.ProfilePic, &a.Active, &a.Subscribed, &a.AutoReplyEnabled); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	last, err := m.lastWebhooks(ctx, now)
	if err != nil {
		return nil, err
	}
	posts, err := m.postStats(ctx, now)
	if err != nil {
		return nil, err
	}
	for i := range list {
		a := &list[i]
		a.Posts = posts[a.ID]
		if t, ok := last[a.IGUserID]; ok {
			a.LastWebhookAt = &t
		}
		if !a.Active {
			off := Light{Yellow, "帳號已停用", now}
			a.Connection, a.Webhook, a.Light = off, off, off
			continue
		}
		a.Connection, a.LatencyMs = m.checkConnection(ctx, a.ID, now)
		a.Webhook = WebhookLight(a.Subscribed, a.LastWebhookAt, now)
		a.Light = worstOf(a.Connection, a.Webhook)
	}
	return list, nil
}

// checkConnection 用帳號所屬連線的 token 打一次 Meta（GET /{ig-user-id}），確認 token 有效並記延遲。
func (m *Monitor) checkConnection(ctx context.Context, id int64, now time.Time) (Light, *int64) {
	if m.Creds == nil {
		return Light{Yellow, "無法檢查（未設定）", now}, nil
	}
	igID, token, err := m.Creds(ctx, id)
	switch {
	case errors.Is(err, account.ErrNotConnected):
		return Light{Red, "尚未連結，請到設定頁連結", now}, nil
	case errors.Is(err, account.ErrExpired):
		return Light{Red, "IG 授權已過期，請重新連結", now}, nil
	case errors.Is(err, account.ErrInactive):
		return Light{Yellow, "帳號已停用", now}, nil
	case err != nil:
		return Light{Red, "讀不到 IG 授權：" + clip(err.Error(), 120), now}, nil
	}
	if m.Meta == nil {
		return Light{Green, "已連結", now}, nil
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	start := time.Now()
	_, err = m.Meta.Account(cctx, token, igID)
	lat := time.Since(start)
	if err != nil && m.NoteAPIError != nil {
		m.NoteAPIError(ctx, id, err) // 內部只記 token 失效類錯誤
	}
	ms := lat.Milliseconds()
	slow := m.MetaSlow
	if slow <= 0 {
		slow = MetaSlow
	}
	switch {
	case ig.IsTokenInvalid(err):
		return Light{Red, "IG 授權已失效，請重新連結", now}, &ms
	case err != nil:
		return Light{Yellow, "Meta 連線失敗：" + clip(err.Error(), 120), now}, &ms
	case lat > slow:
		return Light{Yellow, fmt.Sprintf("Meta 回應慢（%.1f 秒）", lat.Seconds()), now}, &ms
	}
	return Light{Green, fmt.Sprintf("連線正常（%d ms）", ms), now}, &ms
}

// WebhookLight：未訂閱＝紅；太久沒收到（或 7 天內都沒有）＝黃。
func WebhookLight(subscribed bool, last *time.Time, now time.Time) Light {
	switch {
	case !subscribed:
		return Light{Red, "私訊未訂閱，請到設定頁重試訂閱", now}
	case last == nil:
		return Light{Yellow, "7 天內沒收到私訊事件", now}
	case now.Sub(*last) > WebhookQuiet:
		return Light{Yellow, ago(now.Sub(*last)) + "沒收到私訊事件", now}
	}
	return Light{Green, ago(now.Sub(*last)) + "前收到私訊事件", now}
}

// lastWebhooks 回各 IG 帳號（entry.id）最後收到 webhook 的時間（只看 WebhookLookback 內）。
func (m *Monitor) lastWebhooks(ctx context.Context, now time.Time) (map[string]time.Time, error) {
	rows, err := m.DB.Query(ctx, `
SELECT e->>'id', max(w.received_at)
FROM webhook_events w
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(w.payload->'entry') = 'array' THEN w.payload->'entry' ELSE '[]'::jsonb END) e
WHERE w.received_at > $1 AND e ? 'id'
GROUP BY 1`, now.Add(-WebhookLookback))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

func (m *Monitor) postStats(ctx context.Context, now time.Time) (map[int64]PostStats, error) {
	rows, err := m.DB.Query(ctx, `
SELECT ig_account_id,
  count(*) FILTER (WHERE status = 'failed' AND updated_at > $1::timestamptz - $3::interval),
  count(*) FILTER (WHERE status = 'scheduled' AND next_attempt_at < $1::timestamptz - $2::interval),
  min(next_attempt_at) FILTER (WHERE status = 'scheduled' AND next_attempt_at < $1::timestamptz - $2::interval),
  min(next_attempt_at) FILTER (WHERE status = 'scheduled' AND next_attempt_at >= $1::timestamptz - $2::interval)
FROM scheduled_posts WHERE ig_account_id IS NOT NULL GROUP BY 1`,
		now, fmt.Sprintf("%d seconds", int(PostOverdueYellow.Seconds())), fmt.Sprintf("%d seconds", int(FailedPostWindow.Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]PostStats{}
	for rows.Next() {
		var id int64
		var p PostStats
		var oldest *time.Time
		if err := rows.Scan(&id, &p.Failed, &p.Overdue, &oldest, &p.NextAt); err != nil {
			return nil, err
		}
		if oldest != nil {
			p.OverdueSecs = int64(now.Sub(*oldest).Seconds())
		}
		out[id] = p
	}
	return out, rows.Err()
}

// PostsLight：排程遲了超過 PostOverdueRed＝紅、超過 PostOverdueYellow 或近期有失敗＝黃。
func PostsLight(p PostStats, now time.Time) Light {
	switch {
	case p.Overdue > 0 && time.Duration(p.OverdueSecs)*time.Second > PostOverdueRed:
		return Light{Red, fmt.Sprintf("排程卡住：%d 篇貼文已遲 %s 未發", p.Overdue, ago(time.Duration(p.OverdueSecs)*time.Second)), now}
	case p.Overdue > 0:
		return Light{Yellow, fmt.Sprintf("%d 篇貼文遲了還沒發", p.Overdue), now}
	case p.Failed > 0:
		return Light{Yellow, fmt.Sprintf("%d 篇貼文發佈失敗", p.Failed), now}
	}
	return Light{Green, "排程正常", now}
}

func (m *Monitor) checkClaude(ctx context.Context, now time.Time) ClaudeStatus {
	var c ClaudeStatus
	key := ""
	if m.AIKey != nil {
		key = m.AIKey()
	}
	if key == "" {
		m.keyDone = false
		c.Light = Light{Yellow, "未設定 Claude API Key，只跑關鍵字規則", now}
		return c
	}
	c.Configured = true
	h := sha256.Sum256([]byte(key))
	if !m.keyDone || h != m.keyHash || now.Sub(m.keyAt) >= KeyCheckEvery {
		var err error
		if m.PingAI != nil {
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = m.PingAI(pctx, key)
			cancel()
		}
		m.keyHash, m.keyAt, m.keyErr, m.keyDone = h, now, err, true
	}
	at := m.keyAt
	c.KeyCheckedAt = &at
	var keyLight Light
	switch {
	case m.keyErr == nil:
		ok := true
		c.KeyValid = &ok
		keyLight = Light{Green, "API Key 有效", now}
	case errors.Is(m.keyErr, ErrKeyInvalid):
		bad := false
		c.KeyValid = &bad
		c.KeyError = clip(m.keyErr.Error(), 160)
		keyLight = Light{Red, "API Key 無效，請到設定頁更新", now}
	default:
		c.KeyError = clip(m.keyErr.Error(), 160)
		keyLight = Light{Yellow, "連不上 Claude API：" + clip(m.keyErr.Error(), 80), now}
	}
	c.LastCall, c.ConsecutiveFails = m.Calls.Last()
	c.Light = worstOf(keyLight, CallLight(c.LastCall, c.ConsecutiveFails, now))
	if c.Status == Green && c.LastCall != nil {
		c.Message = fmt.Sprintf("正常（最近一次呼叫 %.1f 秒）", float64(c.LastCall.DurationMs)/1000)
	}
	return c
}

// CallLight：最近一次真的 AI 呼叫。連續失敗 AIFailRed 次＝紅、最近一次失敗或超過 AISlow＝黃。
func CallLight(last *automation.CallRecord, fails int, now time.Time) Light {
	switch {
	case last == nil:
		return Light{Green, "還沒有 AI 呼叫紀錄", now}
	case !last.OK && fails >= AIFailRed:
		return Light{Red, fmt.Sprintf("AI 連續 %d 次呼叫失敗：%s", fails, clip(last.Error, 80)), now}
	case !last.OK:
		return Light{Yellow, "最近一次 AI 呼叫失敗：" + clip(last.Error, 80), now}
	case time.Duration(last.DurationMs)*time.Millisecond > AISlow:
		return Light{Yellow, fmt.Sprintf("最近一次 AI 呼叫偏慢（%.0f 秒）", float64(last.DurationMs)/1000), now}
	}
	return Light{Green, fmt.Sprintf("最近一次呼叫 %.1f 秒", float64(last.DurationMs)/1000), now}
}

// emitAlerts：與上一輪比，非紅→紅、紅→非紅才推；第一輪只記基準不推（重啟不重複通知）。
func (m *Monitor) emitAlerts(s Snapshot) {
	type item struct {
		key, kind, title string
		acc              int64
		l                Light
	}
	items := []item{
		{"db", "db", "資料庫", 0, s.DB.Light},
		{"queue", "queue", "背景工作", 0, s.Queue.Light},
		{"claude", "claude", "Claude", 0, s.Claude.Light},
	}
	for _, a := range s.Accounts {
		title := accountTitle(a)
		items = append(items, item{fmt.Sprintf("account:%d", a.ID), "account", title, a.ID, a.Light},
			item{fmt.Sprintf("posts:%d", a.ID), "posts", title + " 排程", a.ID, PostsLight(a.Posts, s.CheckedAt)})
	}
	first := m.prev == nil
	next := make(map[string]string, len(items))
	for _, it := range items {
		next[it.key] = it.l.Status
		if first || m.Hub == nil {
			continue
		}
		before, seen := m.prev[it.key]
		if !seen {
			before = Green // 新出現的帳號
		}
		toRed := before != Red && it.l.Status == Red
		recovered := before == Red && it.l.Status != Red
		if !toRed && !recovered {
			continue
		}
		msg := it.l.Message
		if recovered {
			msg = "已恢復：" + it.l.Message
		}
		m.Hub.Publish(realtime.Event{Type: "system.alert", AccountID: it.acc, Data: Alert{
			Key: it.key, Kind: it.kind, IGAccountID: it.acc, Title: it.title, Status: it.l.Status,
			Message: msg, Recovered: recovered, At: s.CheckedAt,
		}})
	}
	m.prev = next
}

func accountTitle(a AccountStatus) string {
	switch {
	case a.Username != "":
		return "@" + a.Username
	case a.Name != "":
		return a.Name
	}
	return a.IGUserID
}

// ago 把時間長度寫成「3 分鐘」「5 小時」「2 天」。
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "不到 1 分鐘"
	case d < time.Hour:
		return fmt.Sprintf("%d 分鐘", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小時", int(d/time.Hour))
	}
	return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
