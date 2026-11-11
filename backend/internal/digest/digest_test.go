package digest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

// 排序：未回覆（等越久越前）＞ 轉真人未回 ＞ 客訴／訂位 ＞ 問題顧客／黑名單 ＞ 待改進；全都沒有＝0（不列）。
func TestScoreOrder(t *testing.T) {
	items := []TodoItem{
		{ConversationID: 1, Factors: Factors{OpenReviews: 2, WaitMinutes: 9000}},
		{ConversationID: 2, Factors: Factors{Risk: "watch", WaitMinutes: 10}},
		{ConversationID: 3, Factors: Factors{Reservation: true, WaitMinutes: 5}},
		{ConversationID: 4, Factors: Factors{Complaint: true, Risk: "blocked", OpenReviews: 1}},
		{ConversationID: 5, Factors: Factors{HandedOff: true, WaitMinutes: 1}},
		{ConversationID: 6, Factors: Factors{Unanswered: true, WaitMinutes: 30}},
		{ConversationID: 7, Factors: Factors{Unanswered: true, WaitMinutes: 600}},
		{ConversationID: 8, Factors: Factors{Unanswered: true, HandedOff: true, WaitMinutes: 3}},
		{ConversationID: 9, Factors: Factors{Complaint: true, WaitMinutes: 120}},
		{ConversationID: 10, Factors: Factors{HandedOff: true, Complaint: true, Risk: "watch", OpenReviews: 3, WaitMinutes: 99999999}},
	}
	for i := range items {
		items[i].Score = Score(items[i].Factors)
	}
	SortTodo(items)
	var got []int64
	for _, it := range items {
		got = append(got, it.ConversationID)
	}
	want := []int64{8, 7, 6, 10, 5, 4, 9, 3, 2, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if Score(Factors{WaitMinutes: 500}) != 0 {
		t.Fatal("等待時間本身不構成待處理")
	}
	// 較高一類永遠勝過所有較低類別加最長等待
	if Score(Factors{HandedOff: true}) <= Score(Factors{Complaint: true, Risk: "blocked", OpenReviews: 9, WaitMinutes: 1 << 30}) {
		t.Fatal("tier must dominate lower tiers")
	}
	if r := Reasons(Factors{Unanswered: true, WaitMinutes: 185, Complaint: true, Risk: "watch", OpenReviews: 2}); strings.Join(r, "、") != "未回覆 3 小時、客訴、問題顧客、待改進 2 則" {
		t.Fatalf("reasons: %v", r)
	}
}

type fakeSummarizer struct {
	data  string
	calls int
	out   string
	err   error
}

func (f *fakeSummarizer) DigestSummary(_ context.Context, data string) (string, error) {
	f.calls++
	f.data = data
	return f.out, f.err
}

type env struct {
	s    *Service
	pool *pgxpool.Pool
	acc  int64
	day  string // 摘要日（昨天）
	now  time.Time
}

func setup(t *testing.T) *env {
	pool := testdb.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE daily_digests, message_reviews, messages, conversations, contacts, ig_accounts, ig_connections,
  automation_settings RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	acc := testdb.ConnectedAccount(t, pool, "acct")
	loc, _ := time.LoadLocation("Asia/Taipei")
	now := time.Date(2026, 11, 12, 9, 30, 0, 0, loc)
	s := &Service{DB: pool, Analytics: &analytics.Service{DB: pool, Loc: loc}, Loc: loc, Hub: realtime.NewHub(),
		Calls: &automation.CallStats{}, Now: func() time.Time { return now }}
	return &env{s: s, pool: pool, acc: acc, day: "2026-11-11", now: now}
}

// conv 建一段對話；msgs 依序為 (direction, sender, text, intent, 幾分鐘前)。回對話 id 與最後一則訊息 id。
type msg struct {
	dir, sender, text, intent string
	ago                       time.Duration
}

func (e *env) conv(t *testing.T, igsid, username, risk, status, handler string, msgs ...msg) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	var contact, conv, last int64
	if err := e.pool.QueryRow(ctx, `INSERT INTO contacts (ig_account_id, igsid, username, risk) VALUES ($1, $2, $3, $4) RETURNING id`,
		e.acc, igsid, username, risk).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO conversations (contact_id, status, handler) VALUES ($1, $2, $3) RETURNING id`,
		contact, status, handler).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		meta := `{}`
		if m.intent != "" {
			meta = `{"intent":"` + m.intent + `"}`
		}
		at := e.now.Add(-m.ago)
		if err := e.pool.QueryRow(ctx, `INSERT INTO messages (conversation_id, direction, sender_type, text, meta, created_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, conv, m.dir, m.sender, m.text, meta, at).Scan(&last); err != nil {
			t.Fatal(err)
		}
		if m.dir == "in" {
			if _, err := e.pool.Exec(ctx, `UPDATE conversations SET last_inbound_at = $2 WHERE id = $1`, conv, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	return conv, last
}

// 昨天（11/11）＝ now 往前 9.5～33.5 小時
const (
	h = time.Hour
	m = time.Minute
)

func (e *env) seed(t *testing.T) map[string]int64 {
	ids := map[string]int64{}
	// 昨晚 23:00 問了沒人回（等 10.5 小時）
	ids["unanswered-long"], _ = e.conv(t, "u1", "amy", "", "open", "bot",
		msg{"in", "contact", "請問明天有開嗎？\n想帶朋友去", "hours", 10*h + 30*m})
	// 今早問的、還沒回（等 20 分鐘）；也算未回覆
	ids["unanswered-short"], _ = e.conv(t, "u2", "ben", "", "open", "bot",
		msg{"in", "contact", "昨天的蛋糕還有嗎", "menu", 12 * h},
		msg{"out", "ai", "有喔", "", 11 * h},
		msg{"in", "contact", "那我要留兩塊", "reservation", 20 * m})
	// 已轉真人、AI 送了轉真人訊息、真人還沒回
	ids["handoff"], _ = e.conv(t, "u3", "cat", "", "open", "human",
		msg{"in", "contact", "我要包場 30 人", "business", 15 * h},
		msg{"out", "bot", "請稍等店員", "", 15 * h})
	// 客訴，但 AI／真人都回了（只有客訴這一類）
	ids["complaint"], _ = e.conv(t, "u4", "dan", "", "open", "human",
		msg{"in", "contact", "咖啡是冷的", "complaint", 14 * h},
		msg{"out", "human", "很抱歉！", "", 13 * h})
	// 黑名單：刻意不回，不算未回覆，只算黑名單
	ids["blocked"], _ = e.conv(t, "u5", "eve", "blocked", "open", "bot",
		msg{"in", "contact", "垃圾訊息", "", 16 * h})
	// 正常回完的對話：不列
	ids["fine"], _ = e.conv(t, "u6", "fay", "", "open", "bot",
		msg{"in", "contact", "營業時間？", "hours", 17 * h},
		msg{"out", "ai", "10-18", "", 17 * h})
	// 待改進：AI 回了但被標回答有誤
	var aiMsg int64
	ids["review"], aiMsg = e.conv(t, "u7", "gus", "", "open", "bot",
		msg{"in", "contact", "有素食嗎", "menu", 18 * h},
		msg{"out", "ai", "沒有", "", 18 * h})
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO message_reviews (message_id, ig_account_id, kind, note) VALUES ($1, $2, 'ai_wrong', '有燕麥奶')`,
		aiMsg, e.acc); err != nil {
		t.Fatal(err)
	}
	// 前天（11/10）的訊息：算進「前一天」比較，不列待處理（已關閉）
	e.conv(t, "u8", "hal", "", "closed", "bot",
		msg{"in", "contact", "前天的問題", "", 40 * h})
	return ids
}

func (e *env) todoOrder(d Digest) []int64 {
	var out []int64
	for _, it := range d.Content.Todo {
		out = append(out, it.ConversationID)
	}
	return out
}

// 沒有 key：照樣產出統計與待處理清單（依重要度），沒有 AI 總結；推 digest.ready；重新產生＝覆蓋同一份。
func TestGenerateWithoutAI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ids := e.seed(t)
	ch, unsub := e.s.Hub.Subscribe()
	defer unsub()

	d, err := e.s.Generate(ctx, e.acc, e.day)
	if err != nil {
		t.Fatal(err)
	}
	c := d.Content
	if c.SummaryStatus != SummaryNoKey || c.Summary != "" {
		t.Fatalf("no key: status=%s summary=%q", c.SummaryStatus, c.Summary)
	}
	// 昨天的顧客訊息：u1、u2 第一則、u3、u4、u5、u6、u7 = 7（u2 今早那則與 u8 前天的不算）
	if c.Totals.Inbound != 7 || c.Previous.Inbound != 1 || c.Date != e.day {
		t.Fatalf("totals: %+v prev=%+v", c.Totals, c.Previous)
	}
	intents := map[string]int{}
	for _, it := range c.Intents {
		intents[it.Key] = it.Count
	}
	if intents["hours"] != 2 || intents["menu"] != 2 || intents["complaint"] != 1 || intents["reservation"] != 0 || c.Intents[0].Count != 2 {
		t.Fatalf("intents: %+v", c.Intents)
	}
	want := []int64{ids["unanswered-long"], ids["unanswered-short"], ids["handoff"], ids["complaint"], ids["blocked"], ids["review"]}
	got := e.todoOrder(d)
	if len(got) != len(want) {
		t.Fatalf("todo = %v, want %v (%+v)", got, want, c.Todo)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("todo = %v, want %v", got, want)
		}
	}
	first := c.Todo[0]
	if first.Contact != "@amy" || first.LastText != "請問明天有開嗎？ 想帶朋友去" || first.Factors.WaitMinutes != 630 ||
		strings.Join(first.Reasons, "、") != "未回覆 10 小時" {
		t.Fatalf("first item: %+v", first)
	}
	// 第二則：今早的「留兩塊」是訂位意圖，但不在摘要日內 → 不算訂位；昨天的意圖是 menu
	if c.Todo[1].Factors.Reservation || !c.Todo[1].Factors.Unanswered {
		t.Fatalf("second item: %+v", c.Todo[1].Factors)
	}
	if f := c.Todo[4].Factors; f.Risk != "blocked" || f.Unanswered {
		t.Fatalf("blocked item: %+v", f)
	}
	select {
	case ev := <-ch:
		if ev.Type != "digest.ready" || ev.AccountID != e.acc || ev.Data.(ReadyEvent).TodoCount != 6 {
			t.Fatalf("event: %+v", ev)
		}
	default:
		t.Fatal("digest.ready not published")
	}

	// 回覆了 amy 之後重新產生：覆蓋同一份，清單少一則
	if _, err := e.pool.Exec(ctx, `INSERT INTO messages (conversation_id, direction, sender_type, text) VALUES ($1, 'out', 'human', '有開喔')`,
		ids["unanswered-long"]); err != nil {
		t.Fatal(err)
	}
	d2, err := e.s.Generate(ctx, e.acc, e.day)
	if err != nil {
		t.Fatal(err)
	}
	if d2.ID != d.ID || d2.Content.TodoTotal != 5 {
		t.Fatalf("regenerate: id %d→%d todo %d", d.ID, d2.ID, d2.Content.TodoTotal)
	}
	latest, err := e.s.Latest(ctx, e.acc)
	if err != nil || latest.Date != e.day || latest.Content.TodoTotal != 5 {
		t.Fatalf("latest: %+v %v", latest, err)
	}
}

// 有 AI：只送彙總與短節錄（不含顧客名稱），存下總結；AI 失敗照存統計；當天沒訊息不叫 AI。
func TestGenerateWithAI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ai := &fakeSummarizer{out: "  昨天訊息比前天多，主要在問營業時間。#1 等最久，請先回。  "}
	e.s.AI = ai
	ids := e.seed(t)
	_ = ids
	// 一則超長訊息：節錄必須截斷
	e.conv(t, "u9", "ivy", "", "open", "bot", msg{"in", "contact", strings.Repeat("長", 500), "", 11 * h})

	d, err := e.s.Generate(ctx, e.acc, e.day)
	if err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || d.Content.SummaryStatus != SummaryOK || d.Content.Summary != "昨天訊息比前天多，主要在問營業時間。#1 等最久，請先回。" {
		t.Fatalf("summary: %+v calls=%d", d.Content, ai.calls)
	}
	data := ai.data
	for _, name := range []string{"amy", "ben", "cat", "dan", "eve", "gus", "ivy", "acct"} {
		if strings.Contains(data, name) {
			t.Fatalf("AI data must not contain contact/account names (%s):\n%s", name, data)
		}
	}
	if !strings.Contains(data, "顧客訊息 8（前一天 1）") || !strings.Contains(data, "#1 未回覆") || strings.Contains(data, strings.Repeat("長", 61)) {
		t.Fatalf("AI data:\n%s", data)
	}
	if n := len([]rune(data)); n > automation.DigestDataMaxRunes {
		t.Fatalf("AI data too long: %d", n)
	}
	if last, _ := e.s.Calls.Last(); last == nil || !last.OK || last.Kind != "digest" {
		t.Fatalf("call stats: %+v", last)
	}

	// AI 失敗：統計與清單照存、記錯誤
	ai.err = errors.New("overloaded")
	d, err = e.s.Generate(ctx, e.acc, e.day)
	if err != nil || d.Content.SummaryStatus != SummaryFailed || d.Content.SummaryError != "overloaded" || d.Content.TodoTotal == 0 {
		t.Fatalf("ai failure: %+v %v", d.Content, err)
	}
	if last, fails := e.s.Calls.Last(); last.OK || fails != 1 {
		t.Fatalf("call stats after failure: %+v", last)
	}

	// 沒訊息的日子不叫 AI
	ai.calls = 0
	d, err = e.s.Generate(ctx, e.acc, "2026-11-01")
	if err != nil || d.Content.SummaryStatus != SummaryEmpty || ai.calls != 0 {
		t.Fatalf("empty day: %+v calls=%d", d.Content, ai.calls)
	}
}

// 排程：到了帳號設定的時間才產生昨天的摘要，每天只產一次；停用的帳號不產。日期驗證。
func TestRunDue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO automation_settings (ig_account_id, digest_time) VALUES ($1, '10:00')`, e.acc); err != nil {
		t.Fatal(err)
	}
	off := testdb.ConnectedAccount(t, e.pool, "off")
	if _, err := e.pool.Exec(ctx, `UPDATE ig_accounts SET active = false WHERE id = $1`, off); err != nil {
		t.Fatal(err)
	}
	other := testdb.ConnectedAccount(t, e.pool, "other") // 沒有設定列＝預設 09:00
	if n, err := e.s.RunDue(ctx); err != nil || n != 1 { // 09:30：只有 other 到時間
		t.Fatalf("09:30 run: n=%d err=%v", n, err)
	}
	if _, err := e.s.Latest(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Latest(ctx, e.acc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("acc should wait until 10:00: %v", err)
	}
	later := e.now.Add(45 * time.Minute)
	e.s.Now = func() time.Time { return later }
	if n, _ := e.s.RunDue(ctx); n != 1 {
		t.Fatalf("10:15 run: n=%d", n)
	}
	if n, _ := e.s.RunDue(ctx); n != 0 {
		t.Fatalf("second run same day: n=%d", n)
	}
	if _, err := e.s.Latest(ctx, off); !errors.Is(err, ErrNotFound) {
		t.Fatal("inactive account should not get a digest")
	}
	for _, bad := range []string{"2026-11-12", "2026-12-01", "11/11", "2026-13-01"} {
		if _, err := e.s.ParseDate(bad); err == nil {
			t.Errorf("date %q should be rejected", bad)
		}
	}
	if d, err := e.s.ParseDate(""); err != nil || d != "2026-11-11" {
		t.Fatalf("default date: %q %v", d, err)
	}
}

// 系統事件訊息（sender_type=system）不算回覆：最後一則是系統事件時仍是「未回覆」，也不進統計。
func TestSystemMessageNotCountedAsReply(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	conv, _ := e.conv(t, "u1", "amy", "", "open", "bot",
		msg{"in", "contact", "請問有開嗎", "hours", 12 * h},
		msg{"out", "system", "真人 15 分鐘未回覆，已交回 AI", "", 11 * h})
	d, err := e.s.Generate(ctx, e.acc, e.day)
	if err != nil {
		t.Fatal(err)
	}
	c := d.Content
	if len(c.Todo) != 1 || c.Todo[0].ConversationID != conv || !c.Todo[0].Factors.Unanswered || c.Todo[0].LastText != "請問有開嗎" {
		t.Fatalf("todo: %+v", c.Todo)
	}
	if c.Totals.Inbound != 1 || c.Totals.OutboundAuto != 0 || c.Totals.OutboundHuman != 0 {
		t.Fatalf("totals: %+v", c.Totals)
	}
}
