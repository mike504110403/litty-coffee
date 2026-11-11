package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
)

type riskOut struct {
	ID         int64  `json:"id"`
	Risk       string `json:"risk"`
	RiskReason string `json:"riskReason"`
	RiskSource string `json:"riskSource"`
	RiskBy     string `json:"riskBy"`
	Note       string `json:"note"`
}

// 標記與黑名單的 API：標 AI 回答有誤 → 待改進清單帶出問題與回覆 → 標為已處理；顧客黑名單／問題顧客；
// 以及可見帳號範圍（清單 403、單筆 404）與權限（收件匣也能改標記，但不能改備註）。
func TestFlagsAPI(t *testing.T) {
	e := setupFull(t)
	ctx := context.Background()
	owner := login(t, e.srv, "boss", "boss-password")
	connect(t, owner)
	accs := decode[[]acc](t, owner.json("GET", "/api/ig/accounts", "", 200))
	A, B := accs[0].ID, accs[1].ID
	simulate(t, e, owner, A, "alice", "今天開到幾點", "")
	simulate(t, e, owner, B, "bob", "有素食嗎", "")
	convA := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, A)
	convB := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, B)
	contactA := idOf(t, e.pool, `SELECT id FROM contacts WHERE ig_account_id = $1`, A)
	contactB := idOf(t, e.pool, `SELECT id FROM contacts WHERE ig_account_id = $1`, B)
	custA := idOf(t, e.pool, `SELECT id FROM messages WHERE conversation_id = $1 AND direction = 'in'`, convA)
	custB := idOf(t, e.pool, `SELECT id FROM messages WHERE conversation_id = $1 AND direction = 'in'`, convB)
	aiA, err := e.s.Inbox.Send(ctx, convA, "我們營業到晚上十點喔", "ai")
	if err != nil {
		t.Fatal(err)
	}
	aiB, err := e.s.Inbox.Send(ctx, convB, "有的", "bot")
	if err != nil {
		t.Fatal(err)
	}
	humanA, err := e.s.Inbox.SendAsAdmin(ctx, convA, "真人回覆", 1)
	if err != nil {
		t.Fatal(err)
	}
	// B 有一筆 AI 自動記的沒把握
	if err := e.s.Auto.Store.AddUnsure(ctx, custB, B, "知識庫沒寫素食"); err != nil {
		t.Fatal(err)
	}

	// ---- 標記 AI 回答有誤 ----
	owner.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiA.ID), `{"note":"  "}`, 400)   // note 必填
	owner.json("POST", fmt.Sprintf("/api/messages/%d/reviews", custA), `{"note":"x"}`, 400)     // 顧客訊息不能標
	owner.json("POST", fmt.Sprintf("/api/messages/%d/reviews", humanA.ID), `{"note":"x"}`, 400) // 真人回覆不能標
	owner.json("POST", "/api/messages/999999/reviews", `{"note":"x"}`, 404)
	rv := decode[automation.Review](t, owner.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiA.ID), `{"note":"其實只到八點"}`, 201))
	if rv.Kind != automation.ReviewAIWrong || rv.Question != "今天開到幾點" || rv.Reply != "我們營業到晚上十點喔" || rv.ReplySender != "ai" ||
		rv.Note != "其實只到八點" || rv.ConversationID != convA || rv.IGAccountID != A || rv.CreatedBy == "" || rv.ContactName != "alice" {
		t.Fatalf("ai_wrong review: %+v", rv)
	}
	owner.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiA.ID), `{"note":"again"}`, 409) // 未處理前不重複

	// ---- 待改進清單 ----
	all := decode[[]automation.Review](t, owner.json("GET", "/api/reviews?status=open", "", 200))
	if len(all) != 2 {
		t.Fatalf("open reviews: %+v", all)
	}
	wrong := decode[[]automation.Review](t, owner.json("GET", fmt.Sprintf("/api/reviews?account=%d&kind=ai_wrong&status=open", A), "", 200))
	if len(wrong) != 1 || wrong[0].ID != rv.ID || wrong[0].Question != "今天開到幾點" || wrong[0].Reply != "我們營業到晚上十點喔" {
		t.Fatalf("filtered ai_wrong: %+v", wrong)
	}
	unsure := decode[[]automation.Review](t, owner.json("GET", "/api/reviews?kind=ai_unsure", "", 200))
	if len(unsure) != 1 || unsure[0].Question != "有素食嗎" || unsure[0].Reply != "" || unsure[0].Note != "知識庫沒寫素食" || unsure[0].IGAccountID != B {
		t.Fatalf("ai_unsure: %+v", unsure)
	}
	owner.json("GET", "/api/reviews?kind=bad", "", 400)
	owner.json("GET", "/api/reviews?status=bad", "", 400)
	if n := decode[map[string]int](t, owner.json("GET", "/api/reviews/open-count", "", 200)); n["count"] != 2 {
		t.Fatalf("open count %v", n)
	}

	// ---- 標為已處理 ----
	done := decode[automation.Review](t, owner.json("POST", fmt.Sprintf("/api/reviews/%d/resolve", rv.ID), "", 200))
	if done.ResolvedAt == nil || done.ResolvedBy == "" {
		t.Fatalf("resolve: %+v", done)
	}
	if l := decode[[]automation.Review](t, owner.json("GET", "/api/reviews?kind=ai_wrong&status=open", "", 200)); len(l) != 0 {
		t.Fatalf("open after resolve: %+v", l)
	}
	if l := decode[[]automation.Review](t, owner.json("GET", "/api/reviews?status=resolved", "", 200)); len(l) != 1 || l[0].ID != rv.ID {
		t.Fatalf("resolved list: %+v", l)
	}
	owner.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiA.ID), `{"note":"又錯了"}`, 201) // 處理完可再標
	owner.json("POST", "/api/reviews/999999/resolve", "", 404)

	// ---- 顧客標記 ----
	events, unsub := e.s.Hub.Subscribe()
	defer unsub()
	got := decode[crm.ContactDetail](t, owner.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactB), `{"risk":"blocked","riskReason":"一直傳廣告"}`, 200))
	if got.Risk != "blocked" || got.RiskReason != "一直傳廣告" || got.RiskSource != "manual" || got.RiskBy == "" || got.RiskAt == nil {
		t.Fatalf("block: %+v", got.Contact)
	}
	var pushed *inbox.Conversation
	for len(events) > 0 {
		if ev := <-events; ev.Type == "conversation.updated" {
			c := ev.Data.(inbox.Conversation)
			pushed = &c
		}
	}
	if pushed == nil || pushed.ID != convB || pushed.Contact.Risk != "blocked" || pushed.Contact.RiskReason != "一直傳廣告" || pushed.Contact.RiskSource != "manual" {
		t.Fatalf("conversation.updated: %+v", pushed)
	}
	if c := decode[inbox.Conversation](t, owner.json("GET", fmt.Sprintf("/api/conversations/%d", convB), "", 200)); c.Contact.Risk != "blocked" {
		t.Fatalf("conversation contact risk: %+v", c.Contact)
	}
	owner.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"risk":"watch","riskReason":"常要求折扣"}`, 200)
	if l := decode[[]crm.Contact](t, owner.json("GET", "/api/contacts?risk=blocked", "", 200)); len(l) != 1 || l[0].ID != contactB {
		t.Fatalf("risk=blocked: %+v", l)
	}
	if l := decode[[]crm.Contact](t, owner.json("GET", "/api/contacts?risk=watch", "", 200)); len(l) != 1 || l[0].ID != contactA || l[0].RiskReason != "常要求折扣" {
		t.Fatalf("risk=watch: %+v", l)
	}
	owner.json("GET", "/api/contacts?risk=bad", "", 400)
	owner.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"risk":"evil"}`, 400)
	// 解除：risk 清空，但留下誰、何時解除
	got = decode[crm.ContactDetail](t, owner.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactB), `{"risk":"","riskReason":"誤會一場"}`, 200))
	if got.Risk != "" || got.RiskSource != "manual" || got.RiskBy == "" || got.RiskAt == nil || got.RiskReason != "誤會一場" {
		t.Fatalf("clear: %+v", got.Contact)
	}
	owner.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactB), `{"risk":"blocked","riskReason":"又來"}`, 200)

	// ---- 權限：收件匣可改標記、不能改備註；顧客權限也能改；都沒有則 403 ----
	ownerUser, _ := e.auth.Me(ctx, 1)
	mk := func(name string, perms []string, accounts []int64) *client {
		if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: name, Password: name + "-password",
			Permissions: perms, IGAccountIDs: accounts}); err != nil {
			t.Fatal(err)
		}
		return login(t, e.srv, name, name+"-password")
	}
	inboxOnly := mk("inboxonly", []string{auth.PermInbox}, nil)
	r := decode[riskOut](t, inboxOnly.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"risk":"blocked","riskReason":"騷擾"}`, 200))
	if r.Risk != "blocked" || r.RiskBy != "inboxonly" {
		t.Fatalf("inbox-only risk: %+v", r)
	}
	inboxOnly.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"note":"x"}`, 403)
	inboxOnly.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"risk":"","tagIds":[]}`, 403)
	inboxOnly.json("GET", "/api/reviews", "", 403)
	inboxOnly.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiB.ID), `{"note":"答錯"}`, 201)
	contactsOnly := mk("contactsonly", []string{auth.PermContacts}, nil)
	contactsOnly.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"risk":"watch"}`, 200)
	contactsOnly.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiB.ID), `{"note":"x"}`, 403)
	analyticsOnly := mk("analyticsonly", []string{auth.PermAnalytics}, nil)
	analyticsOnly.json("PATCH", fmt.Sprintf("/api/contacts/%d", contactA), `{"risk":"watch"}`, 403)
	autoOnly := mk("autoonly", []string{auth.PermAutomation}, nil)
	autoOnly.json("GET", "/api/reviews", "", 200)
	autoOnly.json("POST", fmt.Sprintf("/api/messages/%d/reviews", aiB.ID), `{"note":"x"}`, 403)

	// ---- 可見範圍：只看 A 的員工（全部功能權限） ----
	staff := mk("staff", auth.AllPerms, []int64{A})
	reviewB := idOf(t, e.pool, `SELECT id FROM message_reviews WHERE ig_account_id = $1 AND kind = 'ai_unsure'`, B)
	p := fmt.Sprintf
	for _, pr := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", p("/api/reviews?account=%d", B), "", 403},
		{"GET", p("/api/reviews/open-count?account=%d", B), "", 403},
		{"POST", p("/api/reviews/%d/resolve", reviewB), "", 404},
		{"POST", p("/api/messages/%d/reviews", aiB.ID), `{"note":"x"}`, 404},
		{"POST", p("/api/messages/%d/reviews", custB), `{"note":"x"}`, 404},
		{"PATCH", p("/api/contacts/%d", contactB), `{"risk":""}`, 404},
		{"GET", p("/api/contacts?account=%d&risk=blocked", B), "", 403},
	} {
		if got := staff.status(pr.method, pr.path, pr.body); got != pr.want {
			t.Errorf("staff %s %s = %d, want %d", pr.method, pr.path, got, pr.want)
		}
	}
	if l := decode[[]automation.Review](t, staff.json("GET", "/api/reviews", "", 200)); len(l) == 0 {
		t.Error("staff should see A's reviews")
	} else {
		for _, x := range l {
			if x.IGAccountID != A {
				t.Errorf("staff sees review of account %d", x.IGAccountID)
			}
		}
	}
	if l := decode[[]crm.Contact](t, staff.json("GET", "/api/contacts?risk=blocked", "", 200)); len(l) != 0 {
		t.Errorf("staff blocked list should not include B: %+v", l)
	}
	if n := decode[map[string]int](t, staff.json("GET", "/api/reviews/open-count", "", 200)); n["count"] != 1 {
		t.Errorf("staff open count %v", n)
	}
	// B 的黑名單沒被員工動到
	if c := decode[inbox.Conversation](t, owner.json("GET", p("/api/conversations/%d", convB), "", 200)); c.Contact.Risk != "blocked" {
		t.Errorf("B contact risk changed: %+v", c.Contact)
	}
	if code := owner.status("POST", p("/api/reviews/%d/resolve", reviewB), ""); code != http.StatusOK {
		t.Errorf("owner resolve B = %d", code)
	}
}
