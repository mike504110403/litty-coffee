package automation

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

func TestMatchRule(t *testing.T) {
	rules := []Rule{
		{ID: 1, Enabled: false, MatchType: "contains", Keywords: []string{"菜單"}},
		{ID: 2, Enabled: true, MatchType: "exact", Keywords: []string{"MENU"}},
		{ID: 3, Enabled: true, MatchType: "contains", Keywords: []string{"菜單", "價目"}},
		{ID: 4, Enabled: true, MatchType: "regex", Keywords: []string{`訂\s*位|預約`}},
	}
	cases := map[string]int64{
		"ｍｅｎｕ":        2, // 全形＋大小寫
		" menu ":      2,
		"menu please": 0,
		"請問菜單在哪":      3, // 停用的 1 不算
		"想看價目表":       3,
		"我要訂 位":       4,
		"可以預約嗎":       4,
		"今天有開嗎":       0,
	}
	for text, want := range cases {
		r, ok := MatchRule(rules, text)
		if (want == 0 && ok) || (want != 0 && r.ID != want) {
			t.Errorf("%q: got %d ok=%v, want %d", text, r.ID, ok, want)
		}
	}
}

func TestRuleValidate(t *testing.T) {
	bad := []Rule{
		{Name: "", MatchType: "contains", Keywords: []string{"a"}, Reply: "x"},
		{Name: "n", MatchType: "contains", Keywords: []string{" "}, Reply: "x"},
		{Name: "n", MatchType: "contains", Keywords: []string{"a"}},
		{Name: "n", MatchType: "regex", Keywords: []string{"("}, Reply: "x"},
		{Name: "n", MatchType: "fuzzy", Keywords: []string{"a"}, Reply: "x"},
	}
	for i, r := range bad {
		if r.Validate() == nil {
			t.Errorf("case %d should fail", i)
		}
	}
	ok := Rule{Name: "客訴", MatchType: "contains", Keywords: []string{"退錢"}, Handoff: true}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildMessages(t *testing.T) {
	msgs, lastIsUser := buildMessages([]Turn{
		{FromCustomer: false, Text: "（店家先發的）"},
		{FromCustomer: true, Text: "你好"},
		{FromCustomer: true, Text: "請問有營業嗎"},
		{FromCustomer: false, Text: "有喔"},
		{FromCustomer: true, Text: ""},
		{FromCustomer: true, Text: "幾點關"},
	})
	if len(msgs) != 3 || !lastIsUser {
		t.Fatalf("got %d messages lastIsUser=%v", len(msgs), lastIsUser)
	}
	b, _ := json.Marshal(msgs[0])
	if string(b) != `{"content":[{"text":"你好\n請問有營業嗎","type":"text"}],"role":"user"}` {
		t.Fatalf("first message: %s", b)
	}
}

// ---- 整合測試（需要 TEST_DATABASE_URL） ----

type fakeAI struct {
	d     AIDecision
	calls int
}

func (f *fakeAI) Decide(context.Context, Settings, []Knowledge, []Turn) (AIDecision, error) {
	f.calls++
	return f.d, nil
}

func setup(t *testing.T) (*Engine, *fakeAI) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts, auto_rules RESTART IDENTITY CASCADE;
UPDATE automation_settings SET ai_enabled = true, debounce_secs = 0, handoff_message = '請稍等店員'`); err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{}
	inb := inbox.NewService(pool, ig.NewFake(), realtime.NewHub())
	return &Engine{Store: &Store{DB: pool}, Inbox: inb, AI: ai, Ctx: ctx}, ai
}

// customerSays 直接走 inbox 的 webhook 入庫路徑，回傳對話 ID 與訊息。
func customerSays(t *testing.T, e *Engine, igsid, mid, text string) (int64, inbox.Message) {
	ctx := context.Background()
	var got inbox.Message
	var conv int64
	e.Inbox.OnInbound = func(c int64, m inbox.Message) { conv, got = c, m }
	ts := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{"object": "instagram", "entry": []any{map[string]any{"id": "acct", "messaging": []any{
		map[string]any{"sender": map[string]string{"id": igsid}, "recipient": map[string]string{"id": "acct"}, "timestamp": ts,
			"message": map[string]any{"mid": mid, "text": text}}}}}})
	if err := e.Inbox.EnqueueWebhook(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Inbox.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	return conv, got
}

func lastOut(t *testing.T, e *Engine, conv int64) inbox.Message {
	msgs, err := e.Inbox.ListMessages(context.Background(), conv, 0, 50)
	if err != nil || len(msgs) == 0 {
		t.Fatal(err)
	}
	return msgs[len(msgs)-1]
}

func TestEngineFlow(t *testing.T) {
	e, ai := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx)
	if _, err := e.Store.CreateRule(ctx, Rule{Name: "菜單", MatchType: "contains", Keywords: []string{"菜單"}, Reply: "菜單在精選限動喔", Enabled: true, Priority: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.CreateRule(ctx, Rule{Name: "客訴", MatchType: "contains", Keywords: []string{"退錢"}, Handoff: true, Enabled: true, Priority: 5}); err != nil {
		t.Fatal(err)
	}

	// 1. 關鍵字命中 → bot 回覆、不呼叫 AI
	conv, m := customerSays(t, e, "u1", "m1", "請問菜單在哪")
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	if out := lastOut(t, e, conv); out.SenderType != "bot" || out.Text != "菜單在精選限動喔" || ai.calls != 0 {
		t.Fatalf("rule reply: %+v calls=%d", out, ai.calls)
	}

	// 2. 沒命中 → AI 回覆
	ai.d = AIDecision{Action: "reply", Reply: "今天營業到八點喔", Intent: "hours", Reason: "問營業時間"}
	conv, m = customerSays(t, e, "u1", "m2", "今天開到幾點")
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	if out := lastOut(t, e, conv); out.SenderType != "ai" || out.Text != "今天營業到八點喔" {
		t.Fatalf("ai reply: %+v", out)
	}
	var intent string
	_ = e.Inbox.DB.QueryRow(ctx, `SELECT meta->>'intent' FROM messages WHERE id=$1`, m.ID).Scan(&intent)
	if intent != "hours" {
		t.Fatalf("inbound intent tag = %q", intent)
	}

	// 3. 較舊的訊息（已有更新的訊息）→ 不處理
	before := ai.calls
	if err := e.handle(ctx, conv, m.ID-1, st); err != nil || ai.calls != before {
		t.Fatalf("stale message should be skipped: err=%v calls=%d", err, ai.calls)
	}

	// 4. 規則要求轉真人 → handler=human，之後 AI 不再回
	conv, m = customerSays(t, e, "u1", "m3", "我要退錢")
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	c, _ := e.Inbox.GetConversation(ctx, conv)
	if c.Handler != "human" {
		t.Fatalf("handoff rule: handler=%s", c.Handler)
	}
	conv, m = customerSays(t, e, "u1", "m4", "人呢")
	before = ai.calls
	if err := e.handle(ctx, conv, m.ID, st); err != nil || ai.calls != before {
		t.Fatalf("human handler must pause bot: err=%v", err)
	}

	// 5. AI 判斷轉真人 → 送固定訊息＋切 human
	ai.d = AIDecision{Action: "handoff", Intent: "reservation", Reason: "要確認空位"}
	conv, m = customerSays(t, e, "u2", "m5", "週六下午還有位子嗎")
	if err := e.handle(ctx, conv, m.ID, st); err != nil {
		t.Fatal(err)
	}
	c, _ = e.Inbox.GetConversation(ctx, conv)
	if out := lastOut(t, e, conv); out.Text != "請稍等店員" || c.Handler != "human" {
		t.Fatalf("ai handoff: %+v handler=%s", out, c.Handler)
	}
}
