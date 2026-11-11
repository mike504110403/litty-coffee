package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
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
	d       AIDecision
	calls   int
	persona string      // 最近一次呼叫用的人設
	kb      []Knowledge // 最近一次呼叫用的知識庫
}

func (f *fakeAI) Decide(_ context.Context, st Settings, kb []Knowledge, _ []Turn) (AIDecision, error) {
	f.calls++
	f.persona, f.kb = st.Persona, kb
	return f.d, nil
}

// setup 清空資料、放一份範本（ai 開、不等待、轉真人訊息「請稍等店員」）並連好測試帳號 "acct"，回傳其 id。
func setup(t *testing.T) (*Engine, *fakeAI, int64) {
	pool := testdb.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts, ig_connections,
  auto_rules, knowledge_items, automation_settings RESTART IDENTITY CASCADE;
INSERT INTO automation_settings (ig_account_id, ai_enabled, debounce_secs, handoff_message, persona) VALUES (NULL, true, 0, '請稍等店員', '範本人設');
INSERT INTO knowledge_items (title, content) VALUES ('營業時間', '範本：10-18')`); err != nil {
		t.Fatal(err)
	}
	acc := testdb.ConnectedAccount(t, pool, "acct")
	ai := &fakeAI{}
	inb := inbox.NewService(pool, ig.NewFake(), realtime.NewHub())
	return &Engine{Store: &Store{DB: pool}, Inbox: inb, AI: ai, Ctx: ctx}, ai, acc
}

// customerSays 直接走 inbox 的 webhook 入庫路徑（送給我方帳號 "acct"），回傳對話 ID 與訊息。
func customerSays(t *testing.T, e *Engine, igsid, mid, text string) (int64, inbox.Message) {
	return customerSaysTo(t, e, "acct", igsid, mid, text)
}

func customerSaysTo(t *testing.T, e *Engine, accountIGID, igsid, mid, text string) (int64, inbox.Message) {
	ctx := context.Background()
	var got inbox.Message
	var conv int64
	e.Inbox.OnInbound = func(c int64, m inbox.Message) { conv, got = c, m }
	ts := time.Now().UnixMilli()
	b, _ := json.Marshal(map[string]any{"object": "instagram", "entry": []any{map[string]any{"id": accountIGID, "messaging": []any{
		map[string]any{"sender": map[string]string{"id": igsid}, "recipient": map[string]string{"id": accountIGID}, "timestamp": ts,
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
	e, ai, acc := setup(t)
	ctx := context.Background()
	st, _ := e.Store.GetSettings(ctx, acc)
	if _, err := e.Store.CreateRule(ctx, acc, Rule{Name: "菜單", MatchType: "contains", Keywords: []string{"菜單"}, Reply: "菜單在精選限動喔", Enabled: true, Priority: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.CreateRule(ctx, acc, Rule{Name: "客訴", MatchType: "contains", Keywords: []string{"退錢"}, Handoff: true, Enabled: true, Priority: 5}); err != nil {
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

// 每個帳號用自己的設定、知識庫與規則；新帳號第一次用到時從範本複製；命中數記在該帳號的規則上。
func TestEngineUsesConversationAccountSettings(t *testing.T) {
	e, ai, a1 := setup(t)
	ctx := context.Background()
	a2 := testdb.ConnectedAccount(t, e.Store.DB, "acct-2")

	// 第一次用到 → 從範本複製
	st2, err := e.Store.GetSettings(ctx, a2)
	if err != nil || st2.Persona != "範本人設" || !st2.AIEnabled {
		t.Fatalf("copied settings: %+v %v", st2, err)
	}
	kb2, _ := e.Store.ListKnowledge(ctx, a2)
	if len(kb2) != 1 || kb2[0].Content != "範本：10-18" {
		t.Fatalf("copied kb: %+v", kb2)
	}

	// 兩個帳號各自調整
	st1, _ := e.Store.GetSettings(ctx, a1)
	st1.Persona = "一號店人設"
	if _, err := e.Store.SaveSettings(ctx, a1, st1); err != nil {
		t.Fatal(err)
	}
	st2.Persona = "二號店人設"
	if _, err := e.Store.SaveSettings(ctx, a2, st2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.SaveKnowledge(ctx, a2, Knowledge{ID: kb2[0].ID, Title: "營業時間", Content: "二號店：12-20", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	r1, _ := e.Store.CreateRule(ctx, a1, Rule{Name: "菜單", MatchType: "contains", Keywords: []string{"菜單"}, Reply: "一號店菜單", Enabled: true})
	r2, _ := e.Store.CreateRule(ctx, a2, Rule{Name: "菜單", MatchType: "contains", Keywords: []string{"菜單"}, Reply: "二號店菜單", Enabled: true})

	// 規則：訊息進二號店 → 用二號店的規則
	conv, m := customerSaysTo(t, e, "acct-2", "u1", "m1", "給我菜單")
	if err := e.handle(ctx, conv, m.ID, st2); err != nil {
		t.Fatal(err)
	}
	out := lastOut(t, e, conv)
	if out.Text != "二號店菜單" {
		t.Fatalf("account 2 rule reply: %q", out.Text)
	}
	var ruleID int64
	_ = e.Store.DB.QueryRow(ctx, `SELECT (meta->>'ruleId')::bigint FROM messages WHERE id = $1`, out.ID).Scan(&ruleID)
	if ruleID != r2.ID {
		t.Fatalf("meta.ruleId=%d want %d", ruleID, r2.ID)
	}
	rules1, _ := e.Store.ListRules(ctx, a1, false)
	rules2, _ := e.Store.ListRules(ctx, a2, false)
	if rules1[0].ID != r1.ID || rules1[0].HitCount != 0 || rules2[0].HitCount != 1 {
		t.Fatalf("hit counts: a1=%+v a2=%+v", rules1, rules2)
	}

	// AI：訊息進一號店 → 一號店的人設與知識庫
	ai.d = AIDecision{Action: "reply", Reply: "好的", Intent: "hours"}
	conv, m = customerSaysTo(t, e, "acct", "u9", "m2", "幾點開")
	st1, _ = e.Store.GetSettings(ctx, a1)
	if err := e.handle(ctx, conv, m.ID, st1); err != nil {
		t.Fatal(err)
	}
	if ai.persona != "一號店人設" || len(ai.kb) != 1 || ai.kb[0].Content != "範本：10-18" {
		t.Fatalf("account 1 AI used persona=%q kb=%+v", ai.persona, ai.kb)
	}
	// 二號店 → 二號店的知識庫
	conv, m = customerSaysTo(t, e, "acct-2", "u9", "m3", "幾點開")
	if err := e.handle(ctx, conv, m.ID, st2); err != nil {
		t.Fatal(err)
	}
	if ai.persona != "二號店人設" || ai.kb[0].Content != "二號店：12-20" {
		t.Fatalf("account 2 AI used persona=%q kb=%+v", ai.persona, ai.kb)
	}

	// 跨帳號改不到別人的規則／知識
	if _, err := e.Store.UpdateRule(ctx, a1, Rule{ID: r2.ID, Name: "x", MatchType: "contains", Keywords: []string{"x"}, Reply: "x"}); err != ErrNotFound {
		t.Fatalf("update other account's rule: %v", err)
	}
	if err := e.Store.DeleteKnowledge(ctx, a1, kb2[0].ID); err != ErrNotFound {
		t.Fatalf("delete other account's kb: %v", err)
	}
	if err := e.Store.EnsureAccount(ctx, 9999); err != ErrNotFound {
		t.Fatalf("unknown account: %v", err)
	}
}
