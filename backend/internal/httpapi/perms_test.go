package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

// 整合測試：需要地端 PG，TEST_DATABASE_URL 未設時跳過。
func setupServer(t *testing.T) (*httptest.Server, *auth.Service, *pgxpool.Pool) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE admin_users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	authSvc := &auth.Service{DB: pool, Secret: []byte("test-secret"), Audit: &audit.Store{DB: pool}}
	if err := authSvc.EnsureAdmin(context.Background(), "boss", "boss-password"); err != nil {
		t.Fatal(err)
	}
	store := &media.Store{DB: pool, Dir: t.TempDir()}
	s := &Server{
		DB: pool, Auth: authSvc, Hub: realtime.NewHub(), Audit: authSvc.Audit,
		Inbox:     inbox.NewService(pool, ig.NewFake(), realtime.NewHub()),
		CRM:       &crm.Service{DB: pool},
		Analytics: &analytics.Service{DB: pool, Loc: time.UTC},
		Media:     store,
		Posts:     posts.NewService(pool, store),
		Accounts:  &account.Service{DB: pool},
	}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)
	return srv, authSvc, pool
}

type client struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
}

func login(t *testing.T, srv *httptest.Server, user, pw string) *client {
	t.Helper()
	res, err := http.Post(srv.URL+"/api/auth/login", "application/json",
		strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, user, pw)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login %s: %d", user, res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName {
			return &client{t: t, base: srv.URL, cookie: c}
		}
	}
	t.Fatalf("login %s: no cookie", user)
	return nil
}

func (c *client) do(method, path, body string) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(c.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { res.Body.Close() })
	return res
}

func (c *client) status(method, path, body string) int { return c.do(method, path, body).StatusCode }

// 每個權限一個探針：有權限時的預期狀態（壞 body 的探針在解碼就回 400，不會碰到測試沒接的依賴）。
var probes = []struct {
	perm, method, path, body string
	want                     int
}{
	{auth.PermInbox, "GET", "/api/conversations", "", 200},
	{auth.PermInbox, "GET", "/api/dev/status", "", 200},
	{auth.PermContacts, "GET", "/api/contacts", "", 200},
	{auth.PermContacts, "GET", "/api/tags", "", 200},
	{auth.PermAutomation, "POST", "/api/automation/rules", "x", 400},
	{auth.PermSchedule, "GET", "/api/posts", "", 200},
	{auth.PermAnalytics, "GET", "/api/analytics/summary", "", 200},
	{auth.PermIGAccounts, "GET", "/api/settings", "", 200},
	{auth.PermCredentials, "PUT", "/api/settings/credentials", "x", 400},
	{auth.PermAdmins, "GET", "/api/admins", "", 200},
	{auth.PermAudit, "GET", "/api/audit-logs", "", 200},
	{auth.PermAudit, "GET", "/api/audit-logs/actions", "", 200},
	{auth.PermAudit, "GET", "/api/audit-logs/admins", "", 200},
}

func TestEachPermissionGatesItsRoutes(t *testing.T) {
	srv, authSvc, _ := setupServer(t)
	owner, err := authSvc.Me(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for i, perm := range auth.AllPerms {
		name := fmt.Sprintf("only-%d", i)
		if _, err := authSvc.CreateAdmin(context.Background(), owner,
			auth.AdminInput{Username: name, Password: "password123", Permissions: []string{perm}}); err != nil {
			t.Fatal(err)
		}
		c := login(t, srv, name, "password123")
		for _, p := range probes {
			want := http.StatusForbidden
			if p.perm == perm {
				want = p.want
			}
			if got := c.status(p.method, p.path, p.body); got != want {
				t.Errorf("user with only %s: %s %s = %d, want %d", perm, p.method, p.path, got, want)
			}
		}
		// 所有人都能用的
		if got := c.status("GET", "/api/auth/me", ""); got != 200 {
			t.Errorf("only %s: /auth/me = %d", perm, got)
		}
		if got := c.status("POST", "/api/auth/password", `{"current":"wrong","next":"whatever12"}`); got != 400 {
			t.Errorf("only %s: /auth/password = %d, want 400 (reached handler)", perm, got)
		}
	}

	boss := login(t, srv, "boss", "boss-password")
	for _, p := range probes {
		if got := boss.status(p.method, p.path, p.body); got != p.want {
			t.Errorf("owner: %s %s = %d, want %d", p.method, p.path, got, p.want)
		}
	}
	var me auth.User
	if err := json.NewDecoder(boss.do("GET", "/api/auth/me", "").Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if !me.IsOwner || len(me.Permissions) != len(auth.AllPerms) {
		t.Fatalf("owner /auth/me = %+v", me)
	}
}

func TestDisabledUserCookieRejected(t *testing.T) {
	srv, _, _ := setupServer(t)
	boss := login(t, srv, "boss", "boss-password")
	res := boss.do("POST", "/api/admins", `{"username":"mei","displayName":"小美","password":"password123","permissions":["inbox"]}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", res.StatusCode)
	}
	var u auth.User
	if err := json.NewDecoder(res.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	mei := login(t, srv, "mei", "password123")
	if got := mei.status("GET", "/api/conversations", ""); got != 200 {
		t.Fatalf("staff inbox = %d", got)
	}
	if got := mei.status("GET", "/api/posts", ""); got != 403 {
		t.Fatalf("staff posts = %d", got)
	}
	// 沒有 admins 權限不能管理員工
	if got := mei.status("PATCH", fmt.Sprintf("/api/admins/%d", u.ID), `{"permissions":["inbox","admins"]}`); got != 403 {
		t.Fatalf("staff self-escalation = %d", got)
	}
	if got := boss.status("PATCH", fmt.Sprintf("/api/admins/%d", u.ID), `{"disabled":true}`); got != 200 {
		t.Fatalf("disable: %d", got)
	}
	if got := mei.status("GET", "/api/conversations", ""); got != 401 {
		t.Fatalf("old cookie after disable = %d", got)
	}
	if got := mei.status("GET", "/api/auth/me", ""); got != 401 {
		t.Fatalf("old cookie /auth/me after disable = %d", got)
	}
	res, err := http.Post(srv.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"mei","password":"password123"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled login = %d", res.StatusCode)
	}
}

func TestAdminAPIPolicyErrors(t *testing.T) {
	srv, _, _ := setupServer(t)
	boss := login(t, srv, "boss", "boss-password")
	if got := boss.status("POST", "/api/admins", `{"username":"mgr","password":"password123","permissions":["admins","inbox"]}`); got != 201 {
		t.Fatalf("create mgr: %d", got)
	}
	mgr := login(t, srv, "mgr", "password123")
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/admins", `{"username":"x","password":"password123","permissions":["analytics"]}`, 403},
		{"POST", "/api/admins", `{"username":"y","password":"password123","isOwner":true}`, 403},
		{"PATCH", "/api/admins/1", `{"displayName":"改店長"}`, 403},
		{"POST", "/api/admins/1/password", `{"password":"new-password"}`, 403},
		{"PATCH", "/api/admins/2", `{"permissions":["inbox"]}`, 403},
		{"PATCH", "/api/admins/2", `{"disabled":true}`, 403},
		{"PATCH", "/api/admins/999", `{"disabled":true}`, 404},
		{"POST", "/api/admins", `{"username":"mgr","password":"password123"}`, 409},
		{"POST", "/api/admins", `{"username":"z","password":"short"}`, 400},
		{"POST", "/api/admins", `{"username":"z","password":"password123","permissions":["inbox"]}`, 201},
	}
	for _, c := range cases {
		res := mgr.do(c.method, c.path, c.body)
		if res.StatusCode != c.want {
			t.Errorf("%s %s %s = %d, want %d", c.method, c.path, c.body, res.StatusCode, c.want)
			continue
		}
		if c.want == 403 {
			var e map[string]string
			_ = json.NewDecoder(res.Body).Decode(&e)
			if e["error"] == "" {
				t.Errorf("%s %s: 403 without error message", c.method, c.path)
			}
		}
	}
	// 店長不能停用自己（也是最後一位店長）
	if got := boss.status("PATCH", "/api/admins/1", `{"disabled":true}`); got != 403 {
		t.Fatalf("owner disable self = %d", got)
	}
}
