package monitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

// fakeMeta：依 IG 帳號 ID 回錯誤或延遲。
type fakeMeta struct {
	mu    sync.Mutex
	err   map[string]error
	delay map[string]time.Duration
	calls []string // 收到的 (token, igUserID)
}

func (f *fakeMeta) Account(_ context.Context, token, igUserID string) (ig.AccountInfo, error) {
	f.mu.Lock()
	err, d := f.err[igUserID], f.delay[igUserID]
	f.calls = append(f.calls, token+"|"+igUserID)
	f.mu.Unlock()
	time.Sleep(d)
	if err != nil {
		return ig.AccountInfo{}, err
	}
	return ig.AccountInfo{UserID: igUserID}, nil
}

func (f *fakeMeta) set(id string, err error, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err[id], f.delay[id] = err, d
}

type env struct {
	m    *Monitor
	meta *fakeMeta
	pool *pgxpool.Pool
	A, B int64
	ping *int
	now  *time.Time
}

func setup(t *testing.T) *env {
	pool := testdb.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts, ig_connections,
  scheduled_posts, automation_settings RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	A := testdb.ConnectedAccount(t, pool, "acc-a")
	B := testdb.ConnectedAccount(t, pool, "acc-b")
	if _, err := pool.Exec(ctx, `UPDATE ig_accounts SET webhook_subscribed = true, username = ig_user_id`); err != nil {
		t.Fatal(err)
	}
	meta := &fakeMeta{err: map[string]error{}, delay: map[string]time.Duration{}}
	pings := 0
	now := time.Now()
	e := &env{meta: meta, pool: pool, A: A, B: B, ping: &pings, now: &now}
	e.m = &Monitor{
		DB: pool, Meta: meta, Calls: &automation.CallStats{}, Hub: realtime.NewHub(),
		Creds: func(_ context.Context, id int64) (string, string, error) {
			var igID string
			err := pool.QueryRow(ctx, `SELECT ig_user_id FROM ig_accounts WHERE id = $1`, id).Scan(&igID)
			return igID, "tok-" + igID, err
		},
		AIKey:  func() string { return "" },
		PingAI: func(context.Context, string) error { pings++; return nil },
		Now:    func() time.Time { return *e.now },
	}
	return e
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) webhookAt(t *testing.T, igID string, at time.Time, processed bool) {
	t.Helper()
	payload := fmt.Sprintf(`{"object":"instagram","entry":[{"id":%q,"time":1}]}`, igID)
	var done any
	if processed {
		done = at
	}
	e.exec(t, `INSERT INTO webhook_events (payload, received_at, processed_at) VALUES ($1, $2, $3)`, payload, at, done)
}

func acct(s Snapshot, id int64) AccountStatus {
	for _, a := range s.Accounts {
		if a.ID == id {
			return a
		}
	}
	return AccountStatus{}
}

// Meta：成功（綠＋延遲）、token 失效（紅）、一般錯誤（黃）、回應慢（黃）；webhook 最後收到時間；彙總依可見帳號。
func TestAccountChecks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := *e.now
	e.webhookAt(t, "acc-a", now.Add(-5*time.Minute), true)
	e.webhookAt(t, "acc-b", now.Add(-72*time.Hour), true)
	e.meta.set("acc-b", &ig.APIError{Status: 400, Code: 190, Message: "Error validating access token"}, 0)

	s := e.m.Check(ctx)
	a, b := acct(s, e.A), acct(s, e.B)
	if a.Connection.Status != Green || a.LatencyMs == nil || !strings.Contains(a.Connection.Message, "連線正常") {
		t.Fatalf("A connection: %+v", a.Connection)
	}
	if a.Webhook.Status != Green || a.LastWebhookAt == nil || a.Status != Green || !a.AutoReplyEnabled {
		t.Fatalf("A webhook/overall: %+v", a)
	}
	if b.Connection.Status != Red || !strings.Contains(b.Connection.Message, "授權已失效") || b.Status != Red {
		t.Fatalf("B token invalid: %+v", b)
	}
	if b.Webhook.Status != Yellow || !strings.Contains(b.Webhook.Message, "3 天") {
		t.Fatalf("B webhook quiet: %+v", b.Webhook)
	}
	// 每個帳號用自己的 token 打自己的 ID
	if got := strings.Join(e.meta.calls, ","); got != "tok-acc-a|acc-a,tok-acc-b|acc-b" {
		t.Fatalf("meta calls: %s", got)
	}
	if v := s.View(func(int64) bool { return true }); v.Meta.Status != Red || v.Meta.Message != "1 個帳號異常" {
		t.Fatalf("meta aggregate (all): %+v", v.Meta)
	}
	if v := s.View(func(id int64) bool { return id == e.A }); v.Meta.Status != Green || len(v.Accounts) != 1 {
		t.Fatalf("meta aggregate (only A): %+v", v.Meta)
	}

	// 一般錯誤＝黃；回應慢＝黃
	e.meta.set("acc-b", errors.New("dial tcp: i/o timeout"), 0)
	e.m.MetaSlow = 20 * time.Millisecond
	e.meta.set("acc-a", nil, 60*time.Millisecond)
	s = e.m.Check(ctx)
	if b := acct(s, e.B); b.Connection.Status != Yellow || !strings.Contains(b.Connection.Message, "Meta 連線失敗") {
		t.Fatalf("B network error: %+v", b.Connection)
	}
	if a := acct(s, e.A); a.Connection.Status != Yellow || !strings.Contains(a.Connection.Message, "回應慢") || *a.LatencyMs < 60 {
		t.Fatalf("A slow: %+v latency=%v", a.Connection, a.LatencyMs)
	}

	// 未訂閱＝紅；停用＝黃且不打 Meta；沒連結＝紅；自動回覆關閉照實回報
	e.meta.set("acc-a", nil, 0)
	e.exec(t, `UPDATE ig_accounts SET webhook_subscribed = false WHERE id = $1`, e.A)
	e.exec(t, `UPDATE ig_accounts SET active = false WHERE id = $1`, e.B)
	e.exec(t, `INSERT INTO automation_settings (ig_account_id, auto_reply_enabled) VALUES ($1, false)`, e.A)
	e.meta.calls = nil
	s = e.m.Check(ctx)
	if a := acct(s, e.A); a.Webhook.Status != Red || a.Status != Red || a.AutoReplyEnabled {
		t.Fatalf("A unsubscribed: %+v", a)
	}
	if b := acct(s, e.B); b.Status != Yellow || b.Message != "帳號已停用" {
		t.Fatalf("B inactive: %+v", b)
	}
	if len(e.meta.calls) != 1 {
		t.Fatalf("inactive account should not hit Meta: %v", e.meta.calls)
	}
	e.m.Creds = func(context.Context, int64) (string, string, error) { return "acc-a", "", account.ErrNotConnected }
	if a := acct(e.m.Check(ctx), e.A); a.Connection.Status != Red || !strings.Contains(a.Connection.Message, "尚未連結") {
		t.Fatalf("not connected: %+v", a.Connection)
	}
}

// Claude：未設 key＝黃（不打 API）；key 無效＝紅；最多每 5 分鐘驗一次、換 key 立即驗；最近呼叫失敗 黃／連續 3 次 紅。
func TestClaudeCheck(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	s := e.m.Check(ctx)
	if s.Claude.Status != Yellow || s.Claude.Configured || *e.ping != 0 || !strings.Contains(s.Claude.Message, "未設定") {
		t.Fatalf("unset: %+v pings=%d", s.Claude, *e.ping)
	}

	key := "sk-bad"
	e.m.AIKey = func() string { return key }
	e.m.PingAI = func(context.Context, string) error {
		*e.ping++
		if key == "sk-bad" {
			return fmt.Errorf("%w（401）", ErrKeyInvalid)
		}
		return nil
	}
	s = e.m.Check(ctx)
	if s.Claude.Status != Red || s.Claude.KeyValid == nil || *s.Claude.KeyValid || *e.ping != 1 {
		t.Fatalf("invalid key: %+v pings=%d", s.Claude, *e.ping)
	}
	*e.now = e.now.Add(4 * time.Minute)
	e.m.Check(ctx)
	if *e.ping != 1 {
		t.Fatalf("key should be checked at most every 5 minutes, pings=%d", *e.ping)
	}
	key = "sk-good" // 換 key：立即重驗
	s = e.m.Check(ctx)
	if s.Claude.Status != Green || *e.ping != 2 || !*s.Claude.KeyValid {
		t.Fatalf("new key: %+v pings=%d", s.Claude, *e.ping)
	}
	*e.now = e.now.Add(5 * time.Minute)
	e.m.Check(ctx)
	if *e.ping != 3 {
		t.Fatalf("recheck after 5 minutes, pings=%d", *e.ping)
	}
	// 暫時連不上＝黃
	e.m.PingAI = func(context.Context, string) error { return errors.New("連線逾時") }
	*e.now = e.now.Add(5 * time.Minute)
	if s = e.m.Check(ctx); s.Claude.Status != Yellow || s.Claude.KeyValid != nil {
		t.Fatalf("unreachable: %+v", s.Claude)
	}
	e.m.PingAI = func(context.Context, string) error { return nil }
	*e.now = e.now.Add(5 * time.Minute)

	// 最近一次真的呼叫
	e.m.Calls.Record("reply", time.Now().Add(-1200*time.Millisecond), nil)
	if s = e.m.Check(ctx); s.Claude.Status != Green || s.Claude.LastCall == nil || !strings.Contains(s.Claude.Message, "1.2 秒") {
		t.Fatalf("last call ok: %+v", s.Claude)
	}
	e.m.Calls.Record("reply", time.Now(), errors.New("overloaded"))
	if s = e.m.Check(ctx); s.Claude.Status != Yellow || !strings.Contains(s.Claude.Message, "overloaded") {
		t.Fatalf("one failure: %+v", s.Claude)
	}
	e.m.Calls.Record("digest", time.Now(), errors.New("overloaded"))
	e.m.Calls.Record("reply", time.Now(), errors.New("overloaded"))
	if s = e.m.Check(ctx); s.Claude.Status != Red || s.Claude.ConsecutiveFails != 3 {
		t.Fatalf("three failures: %+v", s.Claude)
	}
	e.m.Calls.Record("reply", time.Now().Add(-31*time.Second), nil)
	if s = e.m.Check(ctx); s.Claude.Status != Yellow || !strings.Contains(s.Claude.Message, "偏慢") {
		t.Fatalf("slow call: %+v", s.Claude)
	}
}

// 背景工作：webhook 積壓（最舊 >2 分黃、>10 分紅）、排程遲發（>5 分黃、>15 分紅）、失敗貼文（黃）；排程只算看得到的帳號。
func TestBacklogAndPosts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := *e.now
	all := func(int64) bool { return true }

	s := e.m.Check(ctx)
	if v := s.View(all); v.Jobs.Status != Green || v.Jobs.Pending != 0 || s.DB.Status != Green {
		t.Fatalf("idle: %+v db=%+v", v.Jobs, s.DB)
	}
	e.webhookAt(t, "acc-a", now.Add(-30*time.Second), false)
	if v := e.m.Check(ctx).View(all); v.Jobs.Status != Green || v.Jobs.Pending != 1 {
		t.Fatalf("fresh backlog: %+v", v.Jobs)
	}
	e.webhookAt(t, "acc-a", now.Add(-3*time.Minute), false)
	if v := e.m.Check(ctx).View(all); v.Jobs.Status != Yellow || v.Jobs.Pending != 2 {
		t.Fatalf("3 min backlog: %+v", v.Jobs)
	}
	e.webhookAt(t, "acc-a", now.Add(-11*time.Minute), false)
	s = e.m.Check(ctx)
	if s.Queue.Status != Red || !strings.Contains(s.Queue.Message, "3 筆待處理") {
		t.Fatalf("11 min backlog: %+v", s.Queue)
	}
	e.exec(t, `UPDATE webhook_events SET processed_at = now()`)

	// B 的排程遲發 20 分鐘＝紅，但只看得到 A 的人看不到也不算
	e.exec(t, `INSERT INTO scheduled_posts (ig_account_id, status, scheduled_at, next_attempt_at) VALUES ($1, 'scheduled', $2, $2)`,
		e.B, now.Add(-20*time.Minute))
	e.exec(t, `INSERT INTO scheduled_posts (ig_account_id, status, scheduled_at, next_attempt_at) VALUES ($1, 'scheduled', $2, $2)`,
		e.A, now.Add(2*time.Hour))
	s = e.m.Check(ctx)
	if v := s.View(all); v.Jobs.Status != Red || v.Jobs.OverduePosts != 1 || v.Jobs.NextPostAt == nil {
		t.Fatalf("overdue (all): %+v", v.Jobs)
	}
	v := s.View(func(id int64) bool { return id == e.A })
	if v.Jobs.Status != Green || v.Jobs.OverduePosts != 0 || v.Jobs.NextPostAt == nil || !v.Jobs.NextPostAt.After(now) {
		t.Fatalf("overdue hidden from A-only viewer: %+v", v.Jobs)
	}
	e.exec(t, `UPDATE scheduled_posts SET next_attempt_at = $2 WHERE ig_account_id = $1`, e.B, now.Add(-6*time.Minute))
	if v := e.m.Check(ctx).View(all); v.Jobs.Status != Yellow {
		t.Fatalf("6 min overdue: %+v", v.Jobs)
	}
	e.exec(t, `UPDATE scheduled_posts SET status = 'failed', next_attempt_at = NULL WHERE ig_account_id = $1`, e.B)
	if v := e.m.Check(ctx).View(all); v.Jobs.Status != Yellow || v.Jobs.FailedPosts != 1 {
		t.Fatalf("failed post: %+v", v.Jobs)
	}
}

// system.alert 只在「非紅→紅」與「紅→恢復」時推一次；第一輪只記基準；帳號事件帶 AccountID。
func TestAlertsOnlyOnChange(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.webhookAt(t, "acc-a", e.now.Add(-time.Minute), true)
	e.webhookAt(t, "acc-b", e.now.Add(-time.Minute), true)
	e.meta.set("acc-b", &ig.APIError{Status: 401, Message: "bad"}, 0) // B 一開始就紅
	ch, unsub := e.m.Hub.Subscribe()
	defer unsub()
	drain := func() []realtime.Event {
		var out []realtime.Event
		for {
			select {
			case ev := <-ch:
				out = append(out, ev)
			default:
				return out
			}
		}
	}

	e.m.Check(ctx)
	e.m.Check(ctx)
	if evs := drain(); len(evs) != 0 {
		t.Fatalf("baseline / unchanged should not alert: %+v", evs)
	}
	e.meta.set("acc-a", &ig.APIError{Status: 400, Code: 190}, 0) // A 轉紅
	e.m.Check(ctx)
	evs := drain()
	if len(evs) != 1 || evs[0].Type != "system.alert" || evs[0].AccountID != e.A {
		t.Fatalf("A turned red: %+v", evs)
	}
	al := evs[0].Data.(Alert)
	if al.Kind != "account" || al.Status != Red || al.Recovered || al.Title != "@acc-a" {
		t.Fatalf("alert: %+v", al)
	}
	e.m.Check(ctx)
	if evs := drain(); len(evs) != 0 {
		t.Fatalf("still red should not alert again: %+v", evs)
	}
	e.meta.set("acc-a", nil, 0) // 恢復
	e.m.Check(ctx)
	evs = drain()
	if len(evs) != 1 || !evs[0].Data.(Alert).Recovered || evs[0].Data.(Alert).Status != Green {
		t.Fatalf("recovered: %+v", evs)
	}
	// 黃不推：只是回應慢
	e.m.MetaSlow = 10 * time.Millisecond
	e.meta.set("acc-a", nil, 30*time.Millisecond)
	e.m.Check(ctx)
	if evs := drain(); len(evs) != 0 {
		t.Fatalf("yellow should not alert: %+v", evs)
	}
	// 全系統項目：AccountID=0
	e.webhookAt(t, "acc-a", e.now.Add(-20*time.Minute), false)
	e.m.Check(ctx)
	evs = drain()
	if len(evs) != 1 || evs[0].AccountID != 0 || evs[0].Data.(Alert).Kind != "queue" {
		t.Fatalf("queue alert: %+v", evs)
	}
}

func TestThresholdHelpers(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	cases := []struct {
		got  Light
		want string
	}{
		{QueueLight(0, nil, now), Green},
		{QueueLight(5, at(119*time.Second), now), Green},
		{QueueLight(5, at(121*time.Second), now), Yellow},
		{QueueLight(5, at(601*time.Second), now), Red},
		{WebhookLight(true, at(47*time.Hour), now), Green},
		{WebhookLight(true, at(49*time.Hour), now), Yellow},
		{WebhookLight(true, nil, now), Yellow},
		{WebhookLight(false, at(time.Minute), now), Red},
		{PostsLight(PostStats{}, now), Green},
		{PostsLight(PostStats{Failed: 1}, now), Yellow},
		{PostsLight(PostStats{Overdue: 1, OverdueSecs: 400}, now), Yellow},
		{PostsLight(PostStats{Overdue: 1, OverdueSecs: 901}, now), Red},
		{CallLight(nil, 0, now), Green},
		{CallLight(&automation.CallRecord{OK: false}, 2, now), Yellow},
		{CallLight(&automation.CallRecord{OK: false}, 3, now), Red},
		{CallLight(&automation.CallRecord{OK: true, DurationMs: 31000}, 0, now), Yellow},
	}
	for i, c := range cases {
		if c.got.Status != c.want {
			t.Errorf("case %d: %+v, want %s", i, c.got, c.want)
		}
	}
}
