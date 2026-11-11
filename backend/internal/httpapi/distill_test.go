package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// fakeDistiller 記下收到的輸入、回預先設定的建議；不會有任何網路呼叫。
type fakeDistiller struct {
	mu    sync.Mutex
	calls int
	in    automation.DistillInput
	out   []automation.DistillSuggestion
	err   error
}

func (f *fakeDistiller) Enabled() bool { return true }
func (f *fakeDistiller) Model() string { return "fake" }
func (f *fakeDistiller) Distill(_ context.Context, in automation.DistillInput) ([]automation.DistillSuggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.in = in
	return f.out, f.err
}

type distillEnv struct {
	*fullEnv
	owner  *client
	staffA *client // 自動回覆權限、只看 A
	A, B   int64
	ai     *fakeDistiller
	ruleA  int64
	kbA    int64
	ruleB  int64
}

type seedMsg struct {
	in    bool
	human bool // 真人（sent_by_admin_id）；false＋!in＝AI
	text  string
	ago   time.Duration
}

// seedConv 直接寫一段對話（不經 webhook，方便控制時間、送出者、黑名單）。
func seedConv(t *testing.T, e *fullEnv, acc int64, username, name, risk string, msgs ...seedMsg) {
	t.Helper()
	ctx := context.Background()
	var contact, conv int64
	if err := e.pool.QueryRow(ctx, `INSERT INTO contacts (ig_account_id, igsid, username, name, risk) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		acc, fmt.Sprintf("sid-%s-%d", username, acc), username, name, risk).Scan(&contact); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO conversations (contact_id) VALUES ($1) RETURNING id`, contact).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		dir, sender := "out", "ai"
		var admin *int64
		switch {
		case m.in:
			dir, sender = "in", "contact"
		case m.human:
			sender = "human"
			one := int64(1)
			admin = &one
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO messages (conversation_id, direction, sender_type, text, created_at, sent_by_admin_id, meta)
VALUES ($1, $2, $3, $4, now() - make_interval(secs => $5), $6, CASE WHEN $2 = 'in' THEN '{"intent":"location"}'::jsonb ELSE '{}'::jsonb END)`,
			conv, dir, sender, m.text, m.ago.Seconds(), admin); err != nil {
			t.Fatal(err)
		}
	}
}

func setupDistill(t *testing.T) *distillEnv {
	e, owner, A, B := setupAudit(t)
	ctx := context.Background()
	store := e.s.Auto.Store
	mk := func(acc int64, name, kw, reply, title, content string) (int64, int64) {
		r, err := store.CreateRule(ctx, acc, automation.Rule{Name: name, MatchType: "contains", Keywords: []string{kw}, Reply: reply, Enabled: true, Priority: 5})
		if err != nil {
			t.Fatal(err)
		}
		k, err := store.SaveKnowledge(ctx, acc, automation.Knowledge{Title: title, Content: content, Enabled: true, Sort: 3})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID, k.ID
	}
	ruleA, kbA := mk(A, "A店菜單", "菜單", "A店菜單在精選動態", "A店營業時間", "週二至週日 9:00-18:00")
	ruleB, _ := mk(B, "B店菜單", "價目", "B店手沖 200 元", "B店交通", "捷運站旁")

	h := time.Hour
	seedConv(t, e, A, "alice", "Alice Lin", "",
		seedMsg{in: true, text: "請問有停車位嗎？我電話 0912-345-678", ago: 50 * h},
		seedMsg{human: true, text: "Alice 你好，店旁邊有付費停車場", ago: 49 * h})
	seedConv(t, e, A, "carol", "Carol", "",
		seedMsg{in: true, text: "停車位在哪", ago: 10 * h},
		seedMsg{in: false, text: "已幫您轉給店員", ago: 10*h - time.Minute},
		seedMsg{human: true, text: "旁邊有付費停車場喔", ago: 9 * h})
	seedConv(t, e, A, "troll", "Troll", "blocked",
		seedMsg{in: true, text: "黑名單顧客的問題", ago: 5 * h},
		seedMsg{human: true, text: "黑名單也回了", ago: 4 * h})
	seedConv(t, e, A, "olduser", "Old", "",
		seedMsg{in: true, text: "四十天前的問題", ago: 40 * 24 * h},
		seedMsg{human: true, text: "四十天前的回答", ago: 40*24*h - time.Minute})
	seedConv(t, e, B, "bob", "Bob", "",
		seedMsg{in: true, text: "B店的問題", ago: 3 * h},
		seedMsg{human: true, text: "B店的回答", ago: 2 * h})

	ownerUser, _ := e.auth.Me(ctx, 1)
	if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "autoa", Password: "staff-password",
		Permissions: []string{auth.PermAutomation}, IGAccountIDs: []int64{A}}); err != nil {
		t.Fatal(err)
	}
	ai := &fakeDistiller{}
	e.s.DistillAI = ai
	return &distillEnv{fullEnv: e, owner: owner, staffA: login(t, e.srv, "autoa", "staff-password"), A: A, B: B, ai: ai,
		ruleA: ruleA, kbA: kbA, ruleB: ruleB}
}

func distillPath(acc int64, days int) string {
	return fmt.Sprintf("/api/automation/distill?account=%d&days=%d", acc, days)
}

func (e *distillEnv) standardOutput() {
	e.ai.out = []automation.DistillSuggestion{
		{Type: "rule", Action: "create", Rule: &automation.DistillRuleDraft{Name: "停車", Keywords: []string{"停車", "車位"}, MatchType: "contains",
			Reply: "店旁邊有付費停車場喔"}, Reason: "常問停車", Evidence: []string{"請問有停車位嗎"}, Confidence: "high"},
		{Type: "knowledge", Action: "create", Knowledge: &automation.DistillKnowledgeDraft{Title: "寵物", Content: "可否帶寵物：（請填寫）"},
			NeedsInput: true, Reason: "有人問帶狗", Evidence: []string{"可以帶狗嗎"}, Confidence: "medium"},
		{Type: "knowledge", Action: "update", TargetID: e.kbA, Knowledge: &automation.DistillKnowledgeDraft{Title: "A店營業時間",
			Content: "週二至週日 9:00-18:00\n週一公休"}, Reason: "店員說週一公休", Confidence: "high"},
		{Type: "rule", Action: "update", TargetID: e.ruleB, Rule: &automation.DistillRuleDraft{Name: "偷改B", Keywords: []string{"價目"},
			MatchType: "contains", Reply: "x"}}, // 指向別的帳號的規則 → 一定被丟掉
	}
}

func (e *distillEnv) items(c *client, acc int64, status string) []automation.DistillItem {
	return decode[[]automation.DistillItem](e.t(), c.json("GET", fmt.Sprintf("/api/automation/distill/suggestions?account=%d&status=%s", acc, status), "", 200))
}

func (e *distillEnv) t() *testing.T { return e.owner.t }

func TestDistillAssemblesAccountMaterialAndStoresSuggestions(t *testing.T) {
	e := setupDistill(t)
	e.standardOutput()
	res := decode[struct{ Run automation.DistillRun }](t, e.owner.json("POST", distillPath(e.A, 30), "", http.StatusOK))
	if res.Run.Status != "done" || res.Run.SuggestionCount != 3 || res.Run.PairCount != 2 || res.Run.Days != 30 {
		t.Fatalf("run: %+v", res.Run)
	}
	in := e.ai.in
	// 素材：A 的兩段停車對話（問法不同各一組）；黑名單、40 天前、B 帳號都不在；名稱去掉、電話遮蔽
	if len(in.Source.Pairs) != 2 {
		t.Fatalf("pairs: %+v", in.Source.Pairs)
	}
	mat := automation.DistillUserMessage(in.Source)
	for _, bad := range []string{"黑名單", "四十天前", "B店", "Alice", "alice", "carol", "0912-345-678"} {
		if strings.Contains(mat, bad) {
			t.Errorf("material contains %q:\n%s", bad, mat)
		}
	}
	if !strings.Contains(mat, automation.Masked) || !strings.Contains(mat, "<data>") || !strings.Contains(mat, "location：") {
		t.Errorf("material:\n%s", mat)
	}
	// 交給 AI 的既有規則／知識庫只有 A 的
	sys := automation.DistillSystemPrompt(in.Rules, in.Knowledge)
	if !strings.Contains(sys, "A店菜單在精選動態") || !strings.Contains(sys, "A店營業時間") || strings.Contains(sys, "B店") ||
		strings.Contains(sys, "付費停車場") {
		t.Fatalf("system prompt:\n%s", sys)
	}
	items := e.items(e.owner, e.A, "pending")
	if len(items) != 3 {
		t.Fatalf("items: %+v", items)
	}
	var upd *automation.DistillItem
	for i := range items {
		if items[i].Suggestion.Action == "update" {
			upd = &items[i]
		}
	}
	if upd == nil || upd.Suggestion.BeforeKnowledge == nil || upd.Suggestion.BeforeKnowledge.Content != "週二至週日 9:00-18:00" {
		t.Fatalf("update item should carry before snapshot: %+v", upd)
	}
	runs := decode[[]automation.DistillRun](t, e.owner.json("GET", fmt.Sprintf("/api/automation/distill/runs?account=%d", e.A), "", 200))
	if len(runs) != 1 || runs[0].CreatedBy == "" || runs[0].FinishedAt == nil {
		t.Fatalf("runs: %+v", runs)
	}
	if rows := auditRows(t, e.fullEnv, "automation.distill"); len(rows) != 1 || rows[0].Summary == "" || rows[0].Account == nil || *rows[0].Account != e.A {
		t.Fatalf("audit: %+v", rows)
	}
	// 新一批完成後，舊的待審建議由系統略過
	idOf(t, e.pool, `UPDATE distill_runs SET created_at = now() - interval '11 minutes' RETURNING id`)
	e.ai.out = e.ai.out[:1]
	e.owner.json("POST", distillPath(e.A, 7), "", http.StatusOK)
	if got := e.items(e.owner, e.A, "pending"); len(got) != 1 {
		t.Fatalf("after second run pending=%d", len(got))
	}
	if got := e.items(e.owner, e.A, "dismissed"); len(got) != 3 || got[0].DecidedBy != "" {
		t.Fatalf("superseded: %+v", got)
	}
}

func itemPath(id int64, op string) string {
	return fmt.Sprintf("/api/automation/distill/suggestions/%d/%s", id, op)
}

func TestDistillAcceptAndDismiss(t *testing.T) {
	e := setupDistill(t)
	e.standardOutput()
	e.ai.out = append(e.ai.out, automation.DistillSuggestion{Type: "rule", Action: "update", TargetID: e.ruleA,
		Rule: &automation.DistillRuleDraft{Name: "A店菜單", Keywords: []string{"菜單", "價目表"}, MatchType: "contains", Reply: "菜單在精選動態喔"}})
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusOK)
	byKey := map[string]automation.DistillItem{}
	for _, it := range e.items(e.owner, e.A, "pending") {
		byKey[it.Suggestion.Type+"/"+it.Suggestion.Action] = it
	}
	ctx := context.Background()
	store := e.s.Auto.Store

	// 規則新增：直接採用 → 建立啟用中的規則；再採用一次 → 409
	ruleNew := byKey["rule/create"]
	got := decode[automation.DistillItem](t, e.owner.json("POST", itemPath(ruleNew.ID, "accept"), "", 200))
	if got.Status != "accepted" || got.ResultID == nil || got.DecidedBy == "" {
		t.Fatalf("accepted: %+v", got)
	}
	rules, _ := store.ListRules(ctx, e.A, false)
	var created *automation.Rule
	for i := range rules {
		if rules[i].ID == *got.ResultID {
			created = &rules[i]
		}
	}
	if created == nil || created.Name != "停車" || !created.Enabled || created.Priority != 100 {
		t.Fatalf("rule not created: %+v", rules)
	}
	e.owner.json("POST", itemPath(ruleNew.ID, "accept"), "", http.StatusConflict)

	// 規則修改：編輯後採用，regex 不合法 → 400（Rule.Validate）、狀態仍 pending；合法 → 更新原規則，保留優先順序
	ruleUpd := byKey["rule/update"]
	e.owner.json("POST", itemPath(ruleUpd.ID, "accept"), `{"rule":{"name":"A店菜單","keywords":["("],"matchType":"regex","reply":"x"}}`, http.StatusBadRequest)
	e.owner.json("POST", itemPath(ruleUpd.ID, "accept"), `{"rule":{"name":"","keywords":["菜單"],"matchType":"contains","reply":"x"}}`, http.StatusBadRequest)
	got = decode[automation.DistillItem](t, e.owner.json("POST", itemPath(ruleUpd.ID, "accept"),
		`{"rule":{"name":"A店菜單","keywords":["菜單","menu"],"matchType":"contains","reply":"菜單看精選動態","handoff":false}}`, 200))
	if !got.Suggestion.Edited || *got.ResultID != e.ruleA || got.Suggestion.Rule.Reply != "菜單看精選動態" {
		t.Fatalf("edited accept: %+v", got)
	}
	rules, _ = store.ListRules(ctx, e.A, false)
	for _, r := range rules {
		if r.ID == e.ruleA && (r.Reply != "菜單看精選動態" || r.Priority != 5 || len(r.Keywords) != 2) {
			t.Fatalf("rule not updated: %+v", r)
		}
	}

	// needsInput 的知識庫：沒帶內容 → 400；內容還是骨架 → 400；補好 → 新增
	kbNew := byKey["knowledge/create"]
	e.owner.json("POST", itemPath(kbNew.ID, "accept"), "", http.StatusBadRequest)
	e.owner.json("POST", itemPath(kbNew.ID, "accept"), `{"knowledge":{"title":"寵物","content":"可否帶寵物：（請填寫）"}}`, http.StatusBadRequest)
	got = decode[automation.DistillItem](t, e.owner.json("POST", itemPath(kbNew.ID, "accept"), `{"knowledge":{"title":"寵物","content":"戶外座位可帶寵物"}}`, 200))
	kb, _ := store.ListKnowledge(ctx, e.A)
	found := false
	for _, k := range kb {
		if k.ID == *got.ResultID && k.Title == "寵物" && k.Content == "戶外座位可帶寵物" && k.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("knowledge not created: %+v", kb)
	}

	// 知識庫修改：直接採用 → 更新原條目（保留排序）
	kbUpd := byKey["knowledge/update"]
	e.owner.json("POST", itemPath(kbUpd.ID, "accept"), "{}", 200)
	kb, _ = store.ListKnowledge(ctx, e.A)
	for _, k := range kb {
		if k.ID == e.kbA && (k.Content != "週二至週日 9:00-18:00\n週一公休" || k.Sort != 3) {
			t.Fatalf("knowledge not updated: %+v", k)
		}
	}
	if rows := auditRows(t, e.fullEnv, "automation.distill_accept"); len(rows) < 4 || rows[len(rows)-1].TargetType != "distill_suggestion" ||
		rows[len(rows)-1].Account == nil || *rows[len(rows)-1].Account != e.A {
		t.Fatalf("audit accept: %+v", rows)
	}

	// 略過：第二輪的新建議
	idOf(t, e.pool, `UPDATE distill_runs SET created_at = now() - interval '11 minutes' RETURNING id`)
	e.ai.out = e.ai.out[:1]
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusOK)
	p := e.items(e.owner, e.A, "pending")
	if len(p) != 1 {
		t.Fatalf("pending: %+v", p)
	}
	got = decode[automation.DistillItem](t, e.owner.json("POST", itemPath(p[0].ID, "dismiss"), "", 200))
	if got.Status != "dismissed" || got.DecidedBy == "" {
		t.Fatalf("dismiss: %+v", got)
	}
	e.owner.json("POST", itemPath(p[0].ID, "dismiss"), "", http.StatusConflict)
	e.owner.json("POST", itemPath(p[0].ID, "accept"), "", http.StatusConflict)
	if rows := auditRows(t, e.fullEnv, "automation.distill_dismiss"); len(rows) != 2 {
		t.Fatalf("audit dismiss: %+v", rows)
	}
}

func TestDistillConcurrencyRateLimitAndNoKey(t *testing.T) {
	e := setupDistill(t)
	e.standardOutput()
	// 已有 running → 409（不呼叫 AI）
	runID := idOf(t, e.pool, `INSERT INTO distill_runs (ig_account_id, days) VALUES ($1, 30) RETURNING id`, e.A)
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusConflict)
	// 行程中斷留下、超過 5 分鐘的 running 會被標 failed，不會永遠卡住；但它算一次呼叫 → 落在冷卻內 429
	idOf(t, e.pool, `UPDATE distill_runs SET created_at = now() - interval '6 minutes' WHERE id = $1 RETURNING id`, runID)
	res := e.owner.do("POST", distillPath(e.A, 30), "")
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Fatalf("stale running then cooldown: %d", res.StatusCode)
	}
	if e.ai.calls != 0 {
		t.Fatal("AI should not be called")
	}
	idOf(t, e.pool, `UPDATE distill_runs SET created_at = now() - interval '11 minutes' WHERE id = $1 RETURNING id`, runID)
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusOK)
	// 同一帳號 10 分鐘內第二次 → 429；別的帳號不受影響
	e.owner.json("POST", distillPath(e.A, 90), "", http.StatusTooManyRequests)
	e.owner.json("POST", distillPath(e.B, 30), "", http.StatusOK)
	// 並行：兩個請求同時進來，至多一個成功、另一個 409／429
	idOf(t, e.pool, `UPDATE distill_runs SET created_at = now() - interval '11 minutes' WHERE ig_account_id = $1 RETURNING 1`, e.B)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := e.owner.do("POST", distillPath(e.B, 30), "")
			codes[i] = r.StatusCode
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict, http.StatusTooManyRequests:
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("concurrent: %v", codes)
	}
	// 參數錯誤
	e.owner.json("POST", distillPath(e.A, 14), "", http.StatusBadRequest)
	// 沒有 key → 503
	e.s.DistillAI = nil
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusServiceUnavailable)
	e.s.CaptionAI = &automation.LiveAI{}
	e.owner.json("POST", distillPath(e.A, 30), "", http.StatusServiceUnavailable)
}

func TestDistillEmptyMaterialSkipsAIAndCooldown(t *testing.T) {
	e := setupDistill(t)
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM messages`); err != nil {
		t.Fatal(err)
	}
	res := decode[struct{ Run automation.DistillRun }](t, e.owner.json("POST", distillPath(e.A, 7), "", http.StatusOK))
	if res.Run.Status != "empty" || e.ai.calls != 0 {
		t.Fatalf("empty run: %+v calls=%d", res.Run, e.ai.calls)
	}
	e.owner.json("POST", distillPath(e.A, 7), "", http.StatusOK) // 沒呼叫 AI 的不算冷卻
}

func TestDistillAIFailureRecorded(t *testing.T) {
	e := setupDistill(t)
	e.ai.err = automation.ErrRefused
	res := e.owner.do("POST", distillPath(e.A, 30), "")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refused: %d", res.StatusCode)
	}
	runs := decode[[]automation.DistillRun](t, e.owner.json("GET", fmt.Sprintf("/api/automation/distill/runs?account=%d", e.A), "", 200))
	if len(runs) != 1 || runs[0].Status != "failed" || runs[0].Error == "" {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestDistillScope(t *testing.T) {
	e := setupDistill(t)
	e.standardOutput()
	e.owner.json("POST", distillPath(e.B, 30), "", http.StatusOK)
	bItem := e.items(e.owner, e.B, "")[0]
	s := e.staffA
	s.json("POST", distillPath(e.B, 30), "", http.StatusForbidden)
	s.json("GET", fmt.Sprintf("/api/automation/distill/runs?account=%d", e.B), "", http.StatusForbidden)
	s.json("GET", fmt.Sprintf("/api/automation/distill/suggestions?account=%d", e.B), "", http.StatusForbidden)
	s.json("POST", itemPath(bItem.ID, "accept"), "", http.StatusNotFound)
	s.json("POST", itemPath(bItem.ID, "dismiss"), "", http.StatusNotFound)
	s.json("POST", itemPath(999999, "dismiss"), "", http.StatusNotFound)
	if got := e.items(e.owner, e.B, "pending"); len(got) == 0 {
		t.Fatal("B 的建議不應被動到")
	}
	// 自己的帳號可以
	s.json("POST", distillPath(e.A, 30), "", http.StatusOK)
	a := e.items(s, e.A, "pending")
	s.json("POST", itemPath(a[0].ID, "dismiss"), "", http.StatusOK)
	// 沒有自動回覆權限 → 403
	ctx := context.Background()
	ownerUser, _ := e.auth.Me(ctx, 1)
	if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "inboxonly", Password: "staff-password",
		Permissions: []string{auth.PermInbox}}); err != nil {
		t.Fatal(err)
	}
	login(t, e.srv, "inboxonly", "staff-password").json("GET", fmt.Sprintf("/api/automation/distill/runs?account=%d", e.A), "", http.StatusForbidden)
}

func TestDistillWeekly(t *testing.T) {
	e := setupDistill(t)
	e.standardOutput()
	ctx := context.Background()
	// 每週開關存在設定裡（預設關）
	e.owner.json("PUT", fmt.Sprintf("/api/automation/settings?account=%d", e.A), `{"distillWeekly":true}`, 200)
	st := decode[automation.Settings](t, e.owner.json("GET", fmt.Sprintf("/api/automation/settings?account=%d", e.A), "", 200))
	if !st.DistillWeekly {
		t.Fatal("distillWeekly not saved")
	}
	taipei, _ := time.LoadLocation("Asia/Taipei")
	now := time.Date(2026, 11, 16, 9, 5, 0, 0, taipei) // 週一 09:05
	var ready []automation.DistillRun
	d := &automation.Distiller{Store: e.s.Auto.Store, AI: e.ai, Loc: taipei, Now: func() time.Time { return now },
		OnReady: func(r automation.DistillRun) { ready = append(ready, r) }}
	now = time.Date(2026, 11, 16, 8, 59, 0, 0, taipei) // 還沒到時間
	if n, err := d.RunWeeklyDue(ctx); err != nil || n != 0 {
		t.Fatalf("before 09:00: n=%d err=%v", n, err)
	}
	now = time.Date(2026, 11, 17, 10, 0, 0, 0, taipei) // 週二
	if n, _ := d.RunWeeklyDue(ctx); n != 0 {
		t.Fatal("not monday")
	}
	now = time.Date(2026, 11, 16, 9, 5, 0, 0, taipei)
	if n, err := d.RunWeeklyDue(ctx); err != nil || n != 1 {
		t.Fatalf("weekly: n=%d err=%v", n, err)
	}
	if len(ready) != 1 || ready[0].IGAccountID != e.A || ready[0].Trigger != "weekly" || ready[0].Days != 7 {
		t.Fatalf("ready: %+v", ready)
	}
	if n, _ := d.RunWeeklyDue(ctx); n != 0 {
		t.Fatal("same week should not run again")
	}
	// distill.ready：有自動回覆權限且看得到該帳號才收得到
	ev := realtime.Event{Type: "distill.ready", AccountID: e.B}
	staff, _ := e.auth.Me(ctx, idOf(t, e.pool, `SELECT id FROM admin_users WHERE username = 'autoa'`))
	ownerUser, _ := e.auth.Me(ctx, 1)
	if systemEventVisible(staff, ev) || !systemEventVisible(ownerUser, ev) {
		t.Fatal("distill.ready visibility")
	}
	ev.AccountID = e.A
	if !systemEventVisible(staff, ev) {
		t.Fatal("staff should see own account")
	}
}
