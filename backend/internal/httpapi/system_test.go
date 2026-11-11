package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/digest"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/monitor"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// setupDashboard：完整後台＋模擬連上兩個帳號（A、B）＋系統狀態與每日摘要；回 owner 與 A、B。
func setupDashboard(t *testing.T) (*fullEnv, *client, int64, int64) {
	e := setupFull(t)
	if _, err := e.pool.Exec(context.Background(), `TRUNCATE daily_digests RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	owner := login(t, e.srv, "boss", "boss-password")
	connect(t, owner)
	accs := decode[[]acc](t, owner.json("GET", "/api/ig/accounts", "", 200))
	A, B := accs[0].ID, accs[1].ID
	e.s.SysHub = realtime.NewHub()
	e.s.Monitor = &monitor.Monitor{DB: e.pool, Meta: e.s.Accounts.OAuth.(*ig.FakeOAuth), Creds: e.s.Accounts.Credentials,
		AIKey: func() string { return "" }, Calls: &automation.CallStats{}, Hub: e.s.SysHub}
	e.s.Digests = &digest.Service{DB: e.pool, Analytics: e.s.Analytics, Loc: time.UTC, Hub: e.s.SysHub}
	return e, owner, A, B
}

// 員工：指定權限與可見帳號。
func staff(t *testing.T, e *fullEnv, owner *client, name string, perms []string, accounts ...int64) *client {
	t.Helper()
	ps := `[]`
	if len(perms) > 0 {
		ps = `["` + strings.Join(perms, `","`) + `"]`
	}
	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = fmt.Sprint(a)
	}
	owner.json("POST", "/api/admins", fmt.Sprintf(`{"username":%q,"displayName":%q,"password":"staff-password-1","permissions":%s,"igAccountIds":[%s]}`,
		name, name, ps, strings.Join(ids, ",")), 201)
	return login(t, e.srv, name, "staff-password-1")
}

type statusView struct {
	Meta     monitor.Light           `json:"meta"`
	Accounts []monitor.AccountStatus `json:"accounts"`
	Claude   monitor.ClaudeStatus    `json:"claude"`
	Jobs     monitor.JobsStatus      `json:"jobs"`
	DB       monitor.DBStatus        `json:"db"`
	Toggle   bool                    `json:"canToggleAutoReply"`
}

// 系統狀態 API：所有登入者都能讀，但只回看得到的帳號（安全邊界）；看不到的帳號不出現、不計入彙總。
func TestSystemStatusScope(t *testing.T) {
	e, owner, A, B := setupDashboard(t)
	// B 的排程卡住（只有看得到 B 的人會看到紅燈）
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO scheduled_posts (ig_account_id, status, scheduled_at, next_attempt_at)
VALUES ($1, 'scheduled', now() - interval '1 hour', now() - interval '1 hour')`, B); err != nil {
		t.Fatal(err)
	}
	e.s.Monitor.Check(context.Background())

	all := decode[statusView](t, owner.json("GET", "/api/system/status", "", 200))
	if len(all.Accounts) != 2 || all.Jobs.Status != monitor.Red || all.Jobs.OverduePosts != 1 || !all.Toggle {
		t.Fatalf("owner view: %+v", all)
	}
	if all.Accounts[0].Connection.Status != monitor.Green || all.Claude.Status != monitor.Yellow || all.DB.Status != monitor.Green {
		t.Fatalf("owner lights: %+v / %+v / %+v", all.Accounts[0].Connection, all.Claude, all.DB)
	}

	// 沒有任何功能權限、只看得到 A 的員工：200，但只有 A；B 的帳號資料、排程都不出現
	mei := staff(t, e, owner, "mei", nil, A)
	res := mei.json("GET", "/api/system/status", "", 200)
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), ig.FakeAccountRoastery) || strings.Contains(string(body), "littyroastery") ||
		strings.Contains(string(body), fmt.Sprintf(`"id":%d`, B)) {
		t.Fatalf("B leaked to A-only viewer: %s", body)
	}
	mine := decode[statusView](t, mei.json("GET", "/api/system/status", "", 200))
	if len(mine.Accounts) != 1 || mine.Accounts[0].ID != A || mine.Jobs.OverduePosts != 0 || mine.Jobs.Status == monitor.Red || mine.Toggle {
		t.Fatalf("A-only view: %+v", mine)
	}

	// 自動回覆開關：從總覽切（PUT 只帶 autoReplyEnabled）不影響其他設定，狀態 API 立即反映；範圍外 403
	before := decode[automation.Settings](t, owner.json("GET", fmt.Sprintf("/api/automation/settings?account=%d", A), "", 200))
	owner.json("PUT", fmt.Sprintf("/api/automation/settings?account=%d", A), `{"autoReplyEnabled":false}`, 200)
	after := decode[automation.Settings](t, owner.json("GET", fmt.Sprintf("/api/automation/settings?account=%d", A), "", 200))
	before.AutoReplyEnabled = false
	if !reflect.DeepEqual(after, before) { // Settings 含 RecentPosts（不存 DB 的 slice），不能用 ==
		t.Fatalf("partial update changed other fields:\n%+v\n%+v", before, after)
	}
	if v := decode[statusView](t, owner.json("GET", "/api/system/status", "", 200)); v.Accounts[0].AutoReplyEnabled {
		t.Fatal("status should reflect the switch immediately")
	}
	auto := staff(t, e, owner, "kai", []string{auth.PermAutomation}, A)
	auto.json("PUT", fmt.Sprintf("/api/automation/settings?account=%d", B), `{"autoReplyEnabled":false}`, 403)
	mei.json("PUT", fmt.Sprintf("/api/automation/settings?account=%d", A), `{"autoReplyEnabled":true}`, 403)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action = 'automation.update_settings' AND success`).Scan(&n)
	if n != 1 {
		t.Fatalf("switch should be audited once, got %d", n)
	}
}

// system.alert／digest.ready 的串流過濾：權限＋可見帳號；告警給超管、店長與有自動回覆權限的人（不看 IG 帳號權限）。
func TestSystemEventVisible(t *testing.T) {
	ev := func(typ string, acc int64) realtime.Event { return realtime.Event{Type: typ, AccountID: acc} }
	owner := auth.User{IsOwner: true}
	super := auth.User{IsSuper: true}
	opsA := auth.User{Permissions: []string{auth.PermAutomation}, IGAccountIDs: []int64{1}}
	autoAll := auth.User{Permissions: []string{auth.PermAutomation}}
	igOnly := auth.User{Permissions: []string{"ig_accounts"}} // 舊權限鍵：不再是告警對象
	inboxOnly := auth.User{Permissions: []string{auth.PermInbox}}
	analystA := auth.User{Permissions: []string{auth.PermAnalytics}, IGAccountIDs: []int64{1}}
	cases := []struct {
		u    auth.User
		e    realtime.Event
		want bool
	}{
		{owner, ev("system.alert", 2), true},
		{super, ev("system.alert", 2), true},
		{igOnly, ev("system.alert", 0), false},
		{opsA, ev("system.alert", 1), true},
		{opsA, ev("system.alert", 2), false},
		{opsA, ev("system.alert", 0), true},
		{autoAll, ev("system.alert", 2), true},
		{inboxOnly, ev("system.alert", 0), false},
		{analystA, ev("system.alert", 1), false},
		{analystA, ev("digest.ready", 1), true},
		{analystA, ev("digest.ready", 2), false},
		{opsA, ev("digest.ready", 1), false},
		{owner, ev("message.created", 1), false},
	}
	for i, c := range cases {
		if got := systemEventVisible(c.u, c.e); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

// 端到端：只看得到 A 的員工連 /api/system/stream，B 的告警收不到、A 的收得到。
func TestSystemStreamScope(t *testing.T) {
	e, owner, A, B := setupDashboard(t)
	ops := staff(t, e, owner, "ops", []string{auth.PermAutomation}, A)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/system/stream", nil)
	req.AddCookie(ops.cookie)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Scan() // ": connected"
	e.s.SysHub.Publish(realtime.Event{Type: "system.alert", AccountID: B, Data: monitor.Alert{Key: "account:B", Title: "B"}})
	e.s.SysHub.Publish(realtime.Event{Type: "system.alert", AccountID: A, Data: monitor.Alert{Key: "account:A", Title: "A"}})
	var lines []string
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
		if len(lines) == 2 {
			break
		}
	}
	got := strings.Join(lines, "\n")
	if !strings.HasPrefix(got, "event: system.alert\ndata: ") || !strings.Contains(got, `"key":"account:A"`) || strings.Contains(got, "account:B") {
		t.Fatalf("stream: %s", got)
	}
}

// 每日摘要 API：分析權限＋可見帳號（缺帳號 400、範圍外 403）；重新產生記操作紀錄；沒有摘要回 null。
func TestDigestAPIScope(t *testing.T) {
	e, owner, A, B := setupDashboard(t)
	simulate(t, e, owner, A, "alice", "給 A 店：今天有開嗎", "")
	simulate(t, e, owner, B, "bob", "給 B 店", "")
	// 讓訊息落在「昨天」（UTC）
	if _, err := e.pool.Exec(context.Background(), `UPDATE messages SET created_at = now() - interval '1 day';
UPDATE conversations SET last_inbound_at = now() - interval '1 day'`); err != nil {
		t.Fatal(err)
	}

	analyst := staff(t, e, owner, "ana", []string{auth.PermAnalytics}, A)
	noPerm := staff(t, e, owner, "nop", []string{auth.PermInbox}, A)
	if res := analyst.json("GET", fmt.Sprintf("/api/digests/latest?account=%d", A), "", 200); strings.TrimSpace(readAll(res)) != "null" {
		t.Fatal("no digest yet should be null")
	}
	analyst.json("GET", "/api/digests/latest", "", 400)
	analyst.json("GET", fmt.Sprintf("/api/digests/latest?account=%d", B), "", 403)
	analyst.json("POST", fmt.Sprintf("/api/digests/generate?account=%d", B), "", 403)
	noPerm.json("GET", fmt.Sprintf("/api/digests/latest?account=%d", A), "", 403)
	noPerm.json("POST", fmt.Sprintf("/api/digests/generate?account=%d", A), "", 403)
	analyst.json("POST", fmt.Sprintf("/api/digests/generate?account=%d&date=2099-01-01", A), "", 400)

	d := decode[digest.Digest](t, analyst.json("POST", fmt.Sprintf("/api/digests/generate?account=%d", A), "", 200))
	if d.AccountID != A || d.Content.Totals.Inbound != 1 || d.Content.SummaryStatus != digest.SummaryNoKey || d.Content.TodoTotal != 1 ||
		d.Content.Todo[0].Contact != "@alice" {
		t.Fatalf("generated: %+v", d)
	}
	latest := decode[digest.Digest](t, analyst.json("GET", fmt.Sprintf("/api/digests/latest?account=%d", A), "", 200))
	if latest.ID != d.ID || latest.Date != d.Date {
		t.Fatalf("latest: %+v", latest)
	}
	// B 的摘要只有看得到 B 的人能產
	owner.json("POST", fmt.Sprintf("/api/digests/generate?account=%d", B), "", 200)
	var name string
	var acc *int64
	if err := e.pool.QueryRow(context.Background(), `SELECT summary, ig_account_id FROM audit_logs WHERE action = 'digest.generate' AND success ORDER BY id LIMIT 1`).
		Scan(&name, &acc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, "重新產生每日摘要") || acc == nil || *acc != A {
		t.Fatalf("audit: %q acc=%v", name, acc)
	}
}

func readAll(res *http.Response) string {
	b, _ := io.ReadAll(res.Body)
	return string(b)
}
