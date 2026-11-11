package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
)

const fakeDraft = "您好～營業時間是 9:00-18:00 喔 XYZ-7731"

// fakeSuggester 記下每次收到的帳號資料，回固定草稿；不會有任何網路呼叫。
type fakeSuggester struct {
	mu    sync.Mutex
	calls int
	st    automation.Settings
	kb    []automation.Knowledge
	turns []automation.Turn
}

func (f *fakeSuggester) Enabled() bool { return true }
func (f *fakeSuggester) Model() string { return "fake" }
func (f *fakeSuggester) Suggest(_ context.Context, st automation.Settings, kb []automation.Knowledge, turns []automation.Turn) (automation.Suggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.st, f.kb, f.turns = st, kb, turns
	return automation.Suggestion{Warning: "知識庫沒有價格資訊", Options: []automation.SuggestOption{
		{Label: "直接回答", Text: fakeDraft, Basis: []string{"A店營業時間"}},
		{Label: "簡短版", Text: "9-18 喔", Basis: nil},
		{Label: "請顧客補充", Text: "請問想哪天來呢？", Basis: []string{}},
	}}, nil
}

type suggestEnv struct {
	*fullEnv
	owner      *client
	A, B       int64
	convA      int64
	convB      int64
	ai         *fakeSuggester
	staffA     *client // 只看 A、有收件匣權限
	noInbox    *client // 沒有收件匣權限
	convsCount func() int
}

func setupSuggest(t *testing.T) *suggestEnv {
	e, owner, A, B := setupAudit(t)
	ctx := context.Background()
	store := e.s.Auto.Store
	// A、B 各有不同的人設與知識庫
	for acc, d := range map[int64][3]string{A: {"A店人設：溫柔的茶香店員", "A店營業時間", "週一至週五 9:00-18:00"}, B: {"B店人設：豪邁的烘豆師", "B店菜單", "手沖 200 元"}} {
		st, err := store.GetSettings(ctx, acc)
		if err != nil {
			t.Fatal(err)
		}
		st.Persona = d[0]
		if _, err := store.SaveSettings(ctx, acc, st); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveKnowledge(ctx, acc, automation.Knowledge{Title: d[1], Content: d[2], Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	simulate(t, e, owner, A, "alice", "你們幾點開？", "")
	simulate(t, e, owner, B, "bob", "有賣手沖嗎", "")
	convA := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, A)
	convB := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, B)

	ownerUser, _ := e.auth.Me(ctx, 1)
	if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "staffa", Password: "staff-password",
		Permissions: []string{auth.PermInbox}, IGAccountIDs: []int64{A}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "noinbox", Password: "staff-password",
		Permissions: []string{auth.PermContacts}}); err != nil {
		t.Fatal(err)
	}
	ai := &fakeSuggester{}
	e.s.SuggestAI = ai
	return &suggestEnv{fullEnv: e, owner: owner, A: A, B: B, convA: convA, convB: convB, ai: ai,
		staffA:  login(t, e.srv, "staffa", "staff-password"),
		noInbox: login(t, e.srv, "noinbox", "staff-password"),
		convsCount: func() int {
			return idOfInt(t, e, `SELECT count(*) FROM messages`)
		}}
}

func idOfInt(t *testing.T, e *fullEnv, q string) int {
	t.Helper()
	return int(idOf(t, e.pool, q))
}

func suggestPath(conv int64) string { return fmt.Sprintf("/api/conversations/%d/suggest", conv) }

func TestSuggestReturnsDraftWithAccountDataOnly(t *testing.T) {
	e := setupSuggest(t)
	res := e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	out := decode[automation.Suggestion](t, res)
	if len(out.Options) != 3 || out.Options[0].Text != fakeDraft || out.Options[0].Label != "直接回答" || len(out.Options[0].Basis) != 1 ||
		out.Options[0].Basis[0] != "A店營業時間" || out.Options[1].Basis == nil || out.Warning != "知識庫沒有價格資訊" {
		t.Fatalf("response: %+v", out)
	}
	// 呼叫端交給 AI 的是該對話所屬帳號（A）的人設與知識庫，不含 B
	if e.ai.st.Persona != "A店人設：溫柔的茶香店員" || len(e.ai.kb) != 1 || e.ai.kb[0].Title != "A店營業時間" {
		t.Fatalf("A 的設定／知識庫：%+v %+v", e.ai.st, e.ai.kb)
	}
	if len(e.ai.turns) != 1 || !e.ai.turns[0].FromCustomer || e.ai.turns[0].Text != "你們幾點開？" {
		t.Fatalf("turns: %+v", e.ai.turns)
	}
	// 用真正的 prompt 組裝器驗：A 的 prompt 有 A、沒有 B
	prompt := automation.SuggestSystemPrompt(e.ai.st, e.ai.kb)
	user, _ := automation.SuggestUserMessage(e.ai.turns)
	if !strings.Contains(prompt, "A店人設") || !strings.Contains(prompt, "A店營業時間") || strings.Contains(prompt, "B店") || strings.Contains(prompt, "手沖") {
		t.Fatalf("prompt 內容不對：\n%s", prompt)
	}
	if !strings.Contains(user, "<conversation>") || !strings.Contains(user, "顧客：你們幾點開？") || strings.Contains(prompt, "你們幾點開") {
		t.Fatalf("顧客訊息應包在 <conversation> 資料區、不進 system：\n%s", user)
	}
	// B 對話拿到 B 的資料
	e.owner.json("POST", suggestPath(e.convB), "", http.StatusOK)
	if e.ai.st.Persona != "B店人設：豪邁的烘豆師" || len(e.ai.kb) != 1 || e.ai.kb[0].Title != "B店菜單" {
		t.Fatalf("B 的設定／知識庫：%+v %+v", e.ai.st, e.ai.kb)
	}
}

func TestSuggestStatus(t *testing.T) {
	e := setupSuggest(t)
	type st struct{ Configured bool }
	if !decode[st](t, e.staffA.json("GET", "/api/inbox/ai", "", 200)).Configured {
		t.Fatal("有 key 應為 configured")
	}
	e.s.SuggestAI = nil
	e.s.CaptionAI = &automation.LiveAI{}
	if decode[st](t, e.staffA.json("GET", "/api/inbox/ai", "", 200)).Configured {
		t.Fatal("沒 key 應為未設定")
	}
	e.noInbox.json("GET", "/api/inbox/ai", "", http.StatusForbidden)
}

func TestSuggestNeverSendsToIG(t *testing.T) {
	e := setupSuggest(t)
	sentBefore, msgsBefore := len(e.s.Fake.Sent()), e.convsCount()
	for i := 0; i < 3; i++ {
		e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	}
	if got := len(e.s.Fake.Sent()); got != sentBefore {
		t.Fatalf("suggest 不得送出任何訊息：sent %d -> %d", sentBefore, got)
	}
	if got := e.convsCount(); got != msgsBefore {
		t.Fatalf("suggest 不得寫入 messages：%d -> %d", msgsBefore, got)
	}
}

func TestSuggestNoKey503(t *testing.T) {
	e := setupSuggest(t)
	e.s.SuggestAI = nil
	e.s.CaptionAI = &automation.LiveAI{} // 沒 key
	res := e.owner.json("POST", suggestPath(e.convA), "", http.StatusServiceUnavailable)
	if m := decode[map[string]string](t, res); m["error"] != "尚未設定 Claude API Key" {
		t.Fatalf("error: %v", m)
	}
	// CaptionAI 完全沒掛也一樣
	e.s.CaptionAI = nil
	e.owner.json("POST", suggestPath(e.convA), "", http.StatusServiceUnavailable)
	if e.ai.calls != 0 {
		t.Fatal("沒 key 不該呼叫 AI")
	}
}

func TestSuggestScopeAndPermission(t *testing.T) {
	e := setupSuggest(t)
	e.staffA.json("POST", suggestPath(e.convB), "", http.StatusNotFound) // 範圍外
	e.staffA.json("POST", suggestPath(99999), "", http.StatusNotFound)
	e.noInbox.json("POST", suggestPath(e.convA), "", http.StatusForbidden) // 沒有收件匣權限
	if e.ai.calls != 0 {
		t.Fatalf("被擋的請求不該呼叫 AI（%d 次）", e.ai.calls)
	}
	e.staffA.json("POST", suggestPath(e.convA), "", http.StatusOK) // 範圍內可用
	if e.ai.calls != 1 {
		t.Fatalf("calls = %d", e.ai.calls)
	}
}

func TestSuggestRateLimitPerAdmin(t *testing.T) {
	e := setupSuggest(t)
	for i := 0; i < suggestPerMinute; i++ {
		e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	}
	res := e.owner.json("POST", suggestPath(e.convA), "", http.StatusTooManyRequests)
	if res.Header.Get("Retry-After") == "" {
		t.Error("429 應帶 Retry-After")
	}
	if e.ai.calls != suggestPerMinute {
		t.Fatalf("超限的請求不該呼叫 AI：%d", e.ai.calls)
	}
	// 上限是每位管理員各自計算
	e.staffA.json("POST", suggestPath(e.convA), "", http.StatusOK)
}

func TestSuggestAuditHasNoGeneratedText(t *testing.T) {
	e := setupSuggest(t)
	e.owner.json("POST", suggestPath(e.convA), `{"text":"BODY-SHOULD-NOT-BE-LOGGED"}`, http.StatusOK)
	row := oneRow(t, e.fullEnv, "inbox.suggest")
	if row.Summary != "AI 建議回覆" || row.TargetType != "conversation" || row.TargetID != fmt.Sprint(e.convA) || row.Account == nil || *row.Account != e.A || !row.Success {
		t.Fatalf("audit row: %+v", row)
	}
	b, _ := json.Marshal(row)
	for _, bad := range []string{"XYZ-7731", "9:00-18:00", "知識庫沒有價格", "BODY-SHOULD-NOT-BE-LOGGED"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("操作紀錄不得含 %q：%s", bad, b)
		}
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE detail::text LIKE '%XYZ-7731%' OR summary LIKE '%XYZ-7731%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit_logs 含草稿：%d %v", n, err)
	}
}
