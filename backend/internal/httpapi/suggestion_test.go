package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/automation"
)

func suggestionPath(conv int64) string { return fmt.Sprintf("/api/conversations/%d/suggestion", conv) }

// failSuggester 回指定的錯誤或 0 個版本。
type failSuggester struct {
	err   error
	calls int
}

func (f *failSuggester) Enabled() bool { return true }
func (f *failSuggester) Model() string { return "fake" }
func (f *failSuggester) Suggest(context.Context, automation.Settings, []automation.Knowledge, []automation.Turn) (automation.Suggestion, error) {
	f.calls++
	return automation.Suggestion{}, f.err
}

// GET /suggestion：合約欄位；手動「換一組」成功後存表；範圍外 404、沒收件匣權限 403；不計入每分鐘限流、不呼叫 AI。
func TestGetSuggestionContractAndScope(t *testing.T) {
	e := setupSuggest(t)
	var raw map[string]any
	if err := json.NewDecoder(e.owner.json("GET", suggestionPath(e.convA), "", http.StatusOK).Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "forMessageId", "options", "warning", "error", "createdAt"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("回應缺 %q：%v", k, raw)
		}
	}
	msgA := idOf(t, e.pool, `SELECT id FROM messages WHERE conversation_id = $1 AND direction = 'in'`, e.convA)
	if raw["status"] != "none" || raw["forMessageId"].(float64) != float64(msgA) || len(raw["options"].([]any)) != 0 {
		t.Fatalf("還沒產生：%v", raw)
	}

	e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	v := decode[automation.SuggestionView](t, e.staffA.json("GET", suggestionPath(e.convA), "", http.StatusOK))
	if v.Status != "ready" || v.ForMessageID != msgA || len(v.Options) != 3 || v.Options[0].Text != fakeDraft ||
		v.Options[1].Basis == nil || v.Warning != "知識庫沒有價格資訊" || v.CreatedAt == nil {
		t.Fatalf("手動換一組後應存表：%+v", v)
	}

	e.staffA.json("GET", suggestionPath(e.convB), "", http.StatusNotFound) // 範圍外
	e.staffA.json("GET", suggestionPath(99999), "", http.StatusNotFound)
	e.noInbox.json("GET", suggestionPath(e.convA), "", http.StatusForbidden)

	calls := e.ai.calls
	for i := 0; i < suggestPerMinute+5; i++ { // 不計入限流
		e.staffA.json("GET", suggestionPath(e.convA), "", http.StatusOK)
	}
	if e.ai.calls != calls {
		t.Fatal("GET 不呼叫 AI")
	}
	e.staffA.json("POST", suggestPath(e.convA), "", http.StatusOK) // POST 限額沒被 GET 吃掉

	// 新的顧客訊息進來：舊建議不再回
	simulate(t, e.fullEnv, e.owner, e.A, "alice", "還有一個問題", "")
	if v := decode[automation.SuggestionView](t, e.owner.json("GET", suggestionPath(e.convA), "", http.StatusOK)); v.Status != "none" || v.ForMessageID == msgA {
		t.Fatalf("過期建議不得回：%+v", v)
	}
	// 真人回覆後：沒有需要回覆的訊息
	e.owner.json("POST", fmt.Sprintf("/api/conversations/%d/messages", e.convA), `{"text":"好的"}`, http.StatusCreated)
	if v := decode[automation.SuggestionView](t, e.owner.json("GET", suggestionPath(e.convA), "", http.StatusOK)); v.Status != "none" || v.ForMessageID != 0 {
		t.Fatalf("真人回覆後：%+v", v)
	}
}

// POST /suggest 的 422 帶真正原因的 code；訊息照舊中文；失敗不寫表。
func TestSuggest422Codes(t *testing.T) {
	e := setupSuggest(t)
	for _, c := range []struct {
		err  error
		code string
		msg  string
	}{
		{automation.ErrNoCustomerTurn, "no_customer_turn", "這段對話沒有顧客訊息可參考"},
		{automation.ErrRefused, "refused", "AI 無法為這則訊息產生建議，請自行回覆"},
		{nil, "empty", "AI 沒有產生內容，請自行回覆"},
	} {
		e.s.SuggestAI = &failSuggester{err: c.err}
		res := decode[map[string]string](t, e.owner.json("POST", suggestPath(e.convA), "", http.StatusUnprocessableEntity))
		if res["code"] != c.code || res["error"] != c.msg {
			t.Fatalf("%s: %v", c.code, res)
		}
	}
	if v := decode[automation.SuggestionView](t, e.owner.json("GET", suggestionPath(e.convA), "", http.StatusOK)); v.Status != "none" {
		t.Fatalf("失敗不寫表：%+v", v)
	}
}

// suggestion.ready 只推給看得到該帳號的人。
func TestSuggestionReadyEventScope(t *testing.T) {
	e := setupSuggest(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/inbox/stream", nil)
	req.AddCookie(e.staffA.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	type ev struct {
		ConversationID int64 `json:"conversationId"`
		IGAccountID    int64 `json:"igAccountId"`
		ForMessageID   int64 `json:"forMessageId"`
	}
	got := make(chan ev, 10)
	go func() {
		sc := bufio.NewScanner(res.Body)
		typ := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && typ == "suggestion.ready":
				var d ev
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
				got <- d
			}
		}
	}()
	time.Sleep(100 * time.Millisecond) // 等串流訂閱好
	e.owner.json("POST", suggestPath(e.convB), "", http.StatusOK)
	e.owner.json("POST", suggestPath(e.convA), "", http.StatusOK)
	msgA := idOf(t, e.pool, `SELECT id FROM messages WHERE conversation_id = $1 AND direction = 'in'`, e.convA)
	select {
	case d := <-got:
		if d != (ev{ConversationID: e.convA, IGAccountID: e.A, ForMessageID: msgA}) {
			t.Fatalf("只看 A 的人收到：%+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("沒收到 A 的 suggestion.ready")
	}
	select {
	case d := <-got:
		t.Fatalf("不該收到第二個（B 的）事件：%+v", d)
	case <-time.After(300 * time.Millisecond):
	}
}

// 自動回覆設定的 humanIdleMinutes：預設 15，只收 0/5/10/15/30/60。
func TestAutoSettingsHumanIdleMinutes(t *testing.T) {
	e := setupSuggest(t)
	path := fmt.Sprintf("/api/automation/settings?account=%d", e.A)
	type st struct {
		HumanIdleMinutes int  `json:"humanIdleMinutes"`
		AutoReplyEnabled bool `json:"autoReplyEnabled"`
	}
	if got := decode[st](t, e.owner.json("GET", path, "", http.StatusOK)); got.HumanIdleMinutes != 15 {
		t.Fatalf("預設 15：%+v", got)
	}
	for _, n := range []int{0, 5, 10, 15, 30, 60} {
		got := decode[st](t, e.owner.json("PUT", path, fmt.Sprintf(`{"humanIdleMinutes":%d}`, n), http.StatusOK))
		if got.HumanIdleMinutes != n || !got.AutoReplyEnabled {
			t.Fatalf("%d: %+v", n, got)
		}
	}
	for _, n := range []int{1, 7, 20, 45, 90, -5} {
		e.owner.json("PUT", path, fmt.Sprintf(`{"humanIdleMinutes":%d}`, n), http.StatusBadRequest)
	}
	if got := decode[st](t, e.owner.json("GET", path, "", http.StatusOK)); got.HumanIdleMinutes != 60 {
		t.Fatalf("不合法的值不得寫入：%+v", got)
	}
	// 其他帳號不受影響
	if got := decode[st](t, e.owner.json("GET", fmt.Sprintf("/api/automation/settings?account=%d", e.B), "", http.StatusOK)); got.HumanIdleMinutes != 15 {
		t.Fatalf("B: %+v", got)
	}
}
