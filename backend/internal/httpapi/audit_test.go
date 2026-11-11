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

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/appsettings"
	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
)

// setupAudit：完整環境＋憑證 store＋已連結兩個模擬帳號（A、B）。
func setupAudit(t *testing.T) (*fullEnv, *client, int64, int64) {
	e := setupFull(t)
	ctx := context.Background()
	box, _ := secrets.FromKey("audit-test")
	if _, err := e.pool.Exec(ctx, `TRUNCATE app_settings, audit_logs`); err != nil {
		t.Fatal(err)
	}
	e.s.Creds = appsettings.New(e.pool, box, appsettings.Values{})
	if err := e.s.Creds.Load(ctx); err != nil {
		t.Fatal(err)
	}
	owner := login(t, e.srv, "boss", "boss-password")
	connect(t, owner)
	accs := decode[[]acc](t, owner.json("GET", "/api/ig/accounts", "", 200))
	return e, owner, accs[0].ID, accs[1].ID
}

type auditRow struct {
	AdminID    *int64
	AdminName  string
	Summary    string
	TargetType string
	TargetID   string
	Account    *int64
	Detail     map[string]any
	IP         string
	Status     int
	Success    bool
}

func auditRows(t *testing.T, e *fullEnv, action string) []auditRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `
SELECT admin_id, admin_name, summary, target_type, target_id, ig_account_id, detail, ip, status, success
FROM audit_logs WHERE action = $1 ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.AdminID, &r.AdminName, &r.Summary, &r.TargetType, &r.TargetID, &r.Account, &r.Detail,
			&r.IP, &r.Status, &r.Success); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func oneRow(t *testing.T, e *fullEnv, action string) auditRow {
	t.Helper()
	rows := auditRows(t, e, action)
	if len(rows) != 1 {
		t.Fatalf("%s: %d rows, want 1", action, len(rows))
	}
	return rows[0]
}

// router.go 每條登入後的異動路由都要有中文名稱；登記表裡也不能有已不存在的路由。
func TestRouteNamesCoverRouter(t *testing.T) {
	e := setupFull(t)
	seen := map[string]bool{}
	err := chi.Walk(e.s.Routes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !isMutating(method) || !strings.HasPrefix(route, "/api/") || route == "/api/auth/login" || route == "/api/auth/logout" ||
			route == "/api/auth/totp" {
			return nil
		}
		rt, ok := audit.LookupRoute(method, route)
		if !ok {
			t.Errorf("%s %s 沒有登記操作紀錄名稱（internal/audit/actions.go）", method, route)
		}
		seen[rt.Action] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, a := range audit.Actions() {
		if keys[a.Key] {
			t.Errorf("動作鍵重複：%s", a.Key)
		}
		keys[a.Key] = true
	}
	for _, rt := range audit.Routes {
		if !seen[rt.Action] {
			t.Errorf("登記了不存在的路由：%s %s", rt.Method, rt.Pattern)
		}
	}
}

// 單一登入（HTTP 端）：後登入踢掉先登入；被踢記一次；停用、重設密碼、登出都讓舊 token 失效；SSE 也斷線。
func TestSingleSessionHTTP(t *testing.T) {
	old := streamPing
	streamPing = 100 * time.Millisecond
	defer func() { streamPing = old }()
	e, first, A, _ := setupAudit(t)
	ctx := context.Background()

	// SSE：second 開串流，third 登入後下一次心跳就斷
	second := login(t, e.srv, "boss", "boss-password")
	res := first.do("GET", "/api/auth/me", "")
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 401 || body["code"] != auth.ErrCodeSessionReplaced || body["error"] != "此帳號已在其他裝置登入，你已被登出" {
		t.Fatalf("first after second login: %d %v", res.StatusCode, body)
	}
	if got := first.status("POST", "/api/tags", `{"name":"x"}`); got != 401 {
		t.Fatalf("first again: %d", got)
	}
	second.json("GET", "/api/auth/me", "", 200)
	if rows := auditRows(t, e, audit.ActionKicked); len(rows) != 1 || rows[0].AdminName != "boss" || rows[0].Detail["session"] == "" {
		t.Fatalf("kicked rows: %+v", rows)
	}
	if rows := auditRows(t, e, "tag.create"); len(rows) != 0 {
		t.Fatalf("kicked request should not be logged as an action: %+v", rows)
	}

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/inbox/stream", nil)
	req.AddCookie(second.cookie)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
		}
	}()
	time.Sleep(2 * streamPing)
	owner := login(t, e.srv, "boss", "boss-password")
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("replaced session's stream should close")
	}

	// 停用 → 舊 token 失效；重新啟用後舊 token 仍失效
	ownerUser, _ := e.auth.Me(ctx, 1)
	mk := func(name string, perms []string, accounts []int64) (*client, int64) {
		u, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: name, Password: name + "-password",
			Permissions: perms, IGAccountIDs: accounts})
		if err != nil {
			t.Fatal(err)
		}
		return login(t, e.srv, name, name+"-password"), u.ID
	}
	mei, meiID := mk("mei", []string{auth.PermInbox}, []int64{A})
	mei.json("GET", "/api/conversations", "", 200)
	owner.json("PATCH", fmt.Sprintf("/api/admins/%d", meiID), `{"disabled":true}`, 200)
	mei.json("GET", "/api/conversations", "", 401)
	owner.json("PATCH", fmt.Sprintf("/api/admins/%d", meiID), `{"disabled":false}`, 200)
	mei.json("GET", "/api/conversations", "", 401)
	mei = login(t, e.srv, "mei", "mei-password")
	mei.json("GET", "/api/conversations", "", 200)

	// 重設密碼 → 舊 token 失效
	owner.json("POST", fmt.Sprintf("/api/admins/%d/password", meiID), `{"password":"brand-new-pw"}`, 204)
	mei.json("GET", "/api/conversations", "", 401)
	mei = login(t, e.srv, "mei", "brand-new-pw")

	// 改自己的密碼不踢自己
	mei.json("POST", "/api/auth/password", `{"current":"brand-new-pw","next":"brand-new-pw2"}`, 204)
	mei.json("GET", "/api/conversations", "", 200)

	// 登出 → token 失效，記一筆登出
	mei.json("POST", "/api/auth/logout", "", 204)
	mei.json("GET", "/api/conversations", "", 401)
	if r := oneRow(t, e, audit.ActionLogout); r.AdminID == nil || *r.AdminID != meiID {
		t.Fatalf("logout row: %+v", r)
	}

	// 管理員清單帶 lastSeenAt／online
	type adminList struct {
		Admins []struct {
			Username   string     `json:"username"`
			Online     bool       `json:"online"`
			LastSeenAt *time.Time `json:"lastSeenAt"`
		} `json:"admins"`
	}
	list := decode[adminList](t, owner.json("GET", "/api/admins", "", 200))
	for _, a := range list.Admins {
		switch a.Username {
		case "boss":
			if !a.Online || a.LastSeenAt == nil {
				t.Errorf("boss should be online: %+v", a)
			}
		case "mei":
			if a.Online || a.LastSeenAt == nil {
				t.Errorf("mei logged out: %+v", a)
			}
		}
	}
}

// 異動請求記一筆、中文名稱正確、帳號歸屬正確；機密遮蔽；失敗也記；登入成功／失敗。
func TestAuditRecordsMutations(t *testing.T) {
	e, owner, A, B := setupAudit(t)
	ctx := context.Background()
	simulate(t, e, owner, A, "alice", "請問有位子嗎", "")
	convA := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, A)

	long := strings.Repeat("好", 300)
	owner.json("POST", fmt.Sprintf("/api/conversations/%d/messages", convA), fmt.Sprintf(`{"text":%q}`, long), 201)
	r := oneRow(t, e, "inbox.reply")
	if r.Summary != "回覆私訊" || r.TargetType != "conversation" || r.TargetID != fmt.Sprint(convA) ||
		r.Account == nil || *r.Account != A || r.Status != 201 || !r.Success || r.AdminName != "boss" || r.IP != "127.0.0.1" {
		t.Fatalf("reply row: %+v", r)
	}
	text, _ := r.Detail["body"].(map[string]any)["text"].(string)
	if []rune(text)[200] != '…' || len([]rune(text)) != 201 {
		t.Fatalf("message text should be truncated to 200: %d runes", len([]rune(text)))
	}
	if r.Detail["route"] != "/api/conversations/{id}/messages" {
		t.Fatalf("detail: %v", r.Detail)
	}

	// 子路由（/automation）：handler 跑完才拿得到完整樣式與 URL 參數；帳號來自 ?account=
	rule := decode[automation.Rule](t, owner.json("POST", fmt.Sprintf("/api/automation/rules?account=%d", A),
		`{"name":"嗨","matchType":"contains","keywords":["嗨"],"reply":"嗨嗨","enabled":true}`, 201))
	owner.json("DELETE", fmt.Sprintf("/api/automation/rules/%d?account=%d", rule.ID, A), "", 204)
	if r := oneRow(t, e, "automation.create_rule"); r.Summary != "新增關鍵字規則" || r.Account == nil || *r.Account != A {
		t.Fatalf("create rule row: %+v", r)
	}
	if r := oneRow(t, e, "automation.delete_rule"); r.TargetID != fmt.Sprint(rule.ID) || r.Account == nil || *r.Account != A {
		t.Fatalf("delete rule row: %+v", r)
	}

	// 機密遮蔽：憑證、改密碼、重設密碼、新增管理員、登入失敗都不得留下原文
	newSuper(t, e.srv.URL).json("PUT", "/api/settings/credentials", `{"igAppId":"1234567890","igAppSecret":"SUPERSECRET-meta",
"anthropicApiKey":"sk-ant-SUPERSECRET","igVerifyToken":"VERIFY-SUPERSECRET","aiModel":"claude-haiku-4-5"}`, 200)
	c := oneRow(t, e, "credentials.update")
	b, _ := c.Detail["body"].(map[string]any)
	if b["igAppId"] != "1234567890" || b["aiModel"] != "claude-haiku-4-5" || b["igAppSecret"] != audit.Redacted ||
		b["anthropicApiKey"] != audit.Redacted || b["igVerifyToken"] != audit.Redacted {
		t.Fatalf("credentials body: %v", b)
	}
	if r := oneRow(t, e, "credentials.update"); r.Summary != "修改串接憑證" {
		t.Fatalf("credentials summary: %q", r.Summary)
	}
	owner.json("POST", "/api/admins", `{"username":"mei","displayName":"小美","password":"CREATE-SUPERSECRET","permissions":["inbox"],"igAccountIds":[`+fmt.Sprint(A)+`]}`, 201)
	meiID := idOf(t, e.pool, `SELECT id FROM admin_users WHERE username = 'mei'`)
	if b, _ := oneRow(t, e, "admin.create").Detail["body"].(map[string]any); b["password"] != audit.Redacted || b["username"] != "mei" {
		t.Fatalf("create admin body: %v", b)
	}
	owner.json("POST", fmt.Sprintf("/api/admins/%d/password", meiID), `{"password":"RESET-SUPERSECRET"}`, 204)
	if r := oneRow(t, e, "admin.reset_password"); r.Detail["body"] != nil || r.TargetID != fmt.Sprint(meiID) || r.Summary != "重設管理員密碼" {
		t.Fatalf("reset row: %+v", r)
	}
	mei := login(t, e.srv, "mei", "RESET-SUPERSECRET")
	mei.json("POST", "/api/auth/password", `{"current":"RESET-SUPERSECRET","next":"CHANGE-SUPERSECRET"}`, 204)
	if r := oneRow(t, e, "auth.change_password"); r.Detail["body"] != nil || r.AdminID == nil || *r.AdminID != meiID {
		t.Fatalf("change password row: %+v", r)
	}
	res, err := http.Post(e.srv.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"mei","password":"LOGIN-SUPERSECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res, err = http.Post(e.srv.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"ghost","password":"GHOST-SUPERSECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var leaks int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE detail::text LIKE '%SUPERSECRET%'
  OR summary LIKE '%SUPERSECRET%' OR admin_name LIKE '%SUPERSECRET%'`).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Fatalf("%d audit rows contain secrets", leaks)
	}

	// 登入成功／失敗
	fails := auditRows(t, e, audit.ActionLoginFailed)
	if len(fails) != 2 || fails[0].AdminID == nil || *fails[0].AdminID != meiID || fails[0].Success ||
		fails[0].Detail["username"] != "mei" || fails[1].AdminID != nil || fails[1].AdminName != "ghost" || fails[1].Status != 401 {
		t.Fatalf("login failed rows: %+v", fails)
	}
	logins := auditRows(t, e, audit.ActionLogin)
	if len(logins) != 2 || logins[0].AdminName != "boss" || logins[1].AdminID == nil || *logins[1].AdminID != meiID {
		t.Fatalf("login rows: %+v", logins)
	}

	// 失敗的請求也記（權限不足 403、找不到 404），標失敗
	mei = login(t, e.srv, "mei", "CHANGE-SUPERSECRET")
	mei.json("POST", "/api/tags", `{"name":"x"}`, 403)
	if r := oneRow(t, e, "tag.create"); r.Success || r.Status != 403 || r.AdminID == nil || *r.AdminID != meiID {
		t.Fatalf("forbidden row: %+v", r)
	}
	owner.json("PATCH", "/api/admins/999", `{"disabled":true}`, 404)
	if rows := auditRows(t, e, "admin.update"); len(rows) != 1 || rows[0].Success || rows[0].Status != 404 || rows[0].TargetID != "999" {
		t.Fatalf("not found row: %+v", rows)
	}

	// OAuth 連結成功（setupAudit 時）也有紀錄
	if r := oneRow(t, e, audit.ActionIGConnect); !r.Success || r.TargetType != "connection" {
		t.Fatalf("connect row: %+v", r)
	}
	_ = B
}

type auditList struct {
	Items []struct {
		ID          int64          `json:"id"`
		Action      string         `json:"action"`
		ActionName  string         `json:"actionName"`
		TargetLabel string         `json:"targetLabel"`
		IGAccountID *int64         `json:"igAccountId"`
		AdminName   string         `json:"adminName"`
		Detail      map[string]any `json:"detail"`
	} `json:"items"`
	Total int `json:"total"`
}

// 查詢 API：權限、可見帳號範圍、篩選、分頁。
func TestAuditListScopeAndFilters(t *testing.T) {
	e, owner, A, B := setupAudit(t)
	ctx := context.Background()
	simulate(t, e, owner, A, "alice", "A 的客人", "")
	simulate(t, e, owner, B, "bob", "B 的客人", "")
	convA := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, A)
	convB := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, B)
	owner.json("POST", fmt.Sprintf("/api/conversations/%d/messages", convA), `{"text":"給 A"}`, 201)
	owner.json("POST", fmt.Sprintf("/api/conversations/%d/messages", convB), `{"text":"給 B"}`, 201)
	owner.json("POST", "/api/tags", `{"name":"常客"}`, 201) // 不屬於帳號

	ownerUser, _ := e.auth.Me(ctx, 1)
	for _, in := range []auth.AdminInput{
		{Username: "auditA", Password: "auditA-password", Permissions: []string{auth.PermAudit}, IGAccountIDs: []int64{A}},
		{Username: "noaudit", Password: "noaudit-password", Permissions: []string{auth.PermInbox}},
	} {
		if _, err := e.auth.CreateAdmin(ctx, ownerUser, in); err != nil {
			t.Fatal(err)
		}
	}
	login(t, e.srv, "noaudit", "noaudit-password").json("GET", "/api/audit-logs", "", 403)
	auditor := login(t, e.srv, "auditA", "auditA-password")

	all := decode[auditList](t, owner.json("GET", "/api/audit-logs?pageSize=200", "", 200))
	var sawB bool
	for _, it := range all.Items {
		if it.IGAccountID != nil && *it.IGAccountID == B {
			sawB = true
		}
	}
	if !sawB || all.Total != len(all.Items) {
		t.Fatalf("owner should see everything: total=%d items=%d sawB=%v", all.Total, len(all.Items), sawB)
	}

	scoped := decode[auditList](t, auditor.json("GET", "/api/audit-logs?pageSize=200", "", 200))
	var sawA, sawNull bool
	for _, it := range scoped.Items {
		switch {
		case it.IGAccountID == nil:
			sawNull = true
		case *it.IGAccountID == A:
			sawA = true
		default:
			t.Fatalf("scoped auditor sees account %d: %+v", *it.IGAccountID, it)
		}
	}
	if !sawA || !sawNull {
		t.Fatalf("scoped: sawA=%v sawNull=%v", sawA, sawNull)
	}
	auditor.json("GET", fmt.Sprintf("/api/audit-logs?account=%d", B), "", 403)
	onlyA := decode[auditList](t, auditor.json("GET", fmt.Sprintf("/api/audit-logs?account=%d&action=inbox.reply", A), "", 200))
	if onlyA.Total != 1 || onlyA.Items[0].ActionName != "回覆私訊" || onlyA.Items[0].TargetLabel != fmt.Sprintf("對話 #%d", convA) {
		t.Fatalf("filter account+action: %+v", onlyA)
	}

	// 篩管理員、日期、分頁
	byAdmin := decode[auditList](t, owner.json("GET", "/api/audit-logs?admin=1&pageSize=2&page=1", "", 200))
	if len(byAdmin.Items) != 2 || byAdmin.Total < 3 {
		t.Fatalf("admin filter page: %+v", byAdmin)
	}
	page2 := decode[auditList](t, owner.json("GET", "/api/audit-logs?admin=1&pageSize=2&page=2", "", 200))
	if len(page2.Items) == 0 || page2.Items[0].ID >= byAdmin.Items[1].ID {
		t.Fatalf("page 2 should be older: %+v", page2)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if got := decode[auditList](t, owner.json("GET", "/api/audit-logs?from="+future, "", 200)); got.Total != 0 {
		t.Fatalf("from future: %d", got.Total)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if got := decode[auditList](t, owner.json("GET", "/api/audit-logs?to="+past, "", 200)); got.Total != 0 {
		t.Fatalf("to past: %d", got.Total)
	}
	owner.json("GET", "/api/audit-logs?from=bad", "", 400)

	acts := decode[[]audit.ActionName](t, owner.json("GET", "/api/audit-logs/actions", "", 200))
	found := false
	for _, a := range acts {
		if a.Key == "automation.update_settings" && a.Name == "修改 AI 設定" {
			found = true
		}
	}
	if !found || len(acts) != len(audit.Routes)+7 {
		t.Fatalf("actions: %d %v", len(acts), found)
	}
}

// 保留 365 天：超過的每日清掉。
func TestAuditCleanup(t *testing.T) {
	e := setupFull(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `TRUNCATE audit_logs;
INSERT INTO audit_logs (action, created_at) VALUES ('old', now() - interval '366 days'), ('keep', now() - interval '364 days'), ('new', now())`); err != nil {
		t.Fatal(err)
	}
	st := &audit.Store{DB: e.pool}
	n, err := st.Cleanup(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	var left []string
	rows, _ := e.pool.Query(ctx, `SELECT action FROM audit_logs ORDER BY id`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		left = append(left, a)
	}
	rows.Close()
	if strings.Join(left, ",") != "keep,new" {
		t.Fatalf("left: %v", left)
	}
}
