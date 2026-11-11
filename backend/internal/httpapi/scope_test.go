package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

// fullServer 是模擬模式的完整後台（FakeOAuth＋Fake 送訊＋FakePublisher），worker 不跑，由測試手動 ProcessPending。
type fullEnv struct {
	srv  *httptest.Server
	s    *Server
	pool *pgxpool.Pool
	auth *auth.Service
	pub  *ig.FakePublisher
}

func pngData(t *testing.T) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func setupFull(t *testing.T) *fullEnv {
	pool := testdb.Open(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE admin_users, webhook_events, messages, conversations, contacts, ig_accounts, ig_connections,
  scheduled_posts, media_assets, auto_rules, knowledge_items, automation_settings, tags RESTART IDENTITY CASCADE;
INSERT INTO automation_settings (ig_account_id) VALUES (NULL)`); err != nil {
		t.Fatal(err)
	}
	authSvc := &auth.Service{DB: pool, Secret: []byte("test-secret"), Audit: &audit.Store{DB: pool}}
	if err := authSvc.EnsureAdmin(ctx, "boss", "boss-password"); err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.FromKey("scope-test")
	authSvc.Box = box
	hub := realtime.NewHub()
	fake := ig.NewFake()
	inb := inbox.NewService(pool, fake, hub)
	inb.MediaDir = t.TempDir()
	store := &media.Store{DB: pool, Dir: t.TempDir()}
	accounts := &account.Service{DB: pool, Box: box, OAuth: nil}
	inb.Creds = accounts
	s := &Server{
		DB: pool, Auth: authSvc, Hub: hub, Fake: fake, Inbox: inb, Audit: authSvc.Audit,
		Auto:      &automation.Engine{Store: &automation.Store{DB: pool}, Inbox: inb, Ctx: ctx},
		CRM:       &crm.Service{DB: pool},
		Analytics: &analytics.Service{DB: pool, Loc: time.UTC},
		Media:     store,
		Posts:     posts.NewService(pool, store),
		Accounts:  accounts,
	}
	pub := ig.NewFakePublisher()
	s.Posts.Insights = &posts.InsightsRefresher{Svc: s.Posts, IG: pub, Creds: accounts.Credentials}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)
	s.Info = SystemInfo{WebOrigin: srv.URL, PublicBaseURL: srv.URL}
	accounts.OAuth = ig.NewFakeOAuth(srv.URL + "/api/ig/oauth/callback")
	registerAuth(t, srv.URL, authSvc)
	return &fullEnv{srv: srv, s: s, pool: pool, auth: authSvc, pub: pub}
}

func decode[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	var v T
	b, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v (%s)", res.Request.URL.Path, err, b)
	}
	return v
}

func (c *client) json(method, path, body string, want int) *http.Response {
	c.t.Helper()
	res := c.do(method, path, body)
	if res.StatusCode != want {
		b, _ := io.ReadAll(res.Body)
		c.t.Fatalf("%s %s = %d, want %d (%s)", method, path, res.StatusCode, want, b)
	}
	return res
}

// noRedirect 取一個 GET 的 302 回應（不跟隨），可帶額外 cookie。
func (c *client) noRedirect(path string, extra ...*http.Cookie) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+path, nil)
	req.AddCookie(c.cookie)
	for _, ck := range extra {
		req.AddCookie(ck)
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { res.Body.Close() })
	return res
}

// connect 走完整的（模擬）OAuth：start → 授權頁導回 callback → 連上兩個帳號。
// IG 帳號連結只限超管：實際由另建的一位超管操作（owner 只用來取測試伺服器網址）。
func connect(t *testing.T, owner *client) {
	owner = newSuper(t, owner.base).client
	res := owner.noRedirect("/api/ig/oauth/start")
	if res.StatusCode != http.StatusFound {
		t.Fatalf("start: %d", res.StatusCode)
	}
	var state *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == oauthStateCookie {
			state = ck
		}
	}
	loc, _ := url.Parse(res.Header.Get("Location"))
	if state == nil || loc.Query().Get("state") != state.Value {
		t.Fatalf("authorize redirect %s", loc)
	}
	res = owner.noRedirect(loc.Path+"?"+loc.RawQuery, state)
	if got := res.Header.Get("Location"); !strings.HasSuffix(got, "/settings?ig=connected") {
		t.Fatalf("callback redirect %q", got)
	}
}

type acc struct {
	ID       int64  `json:"id"`
	IGUserID string `json:"igUserId"`
	Active   bool   `json:"active"`
}

func simulate(t *testing.T, e *fullEnv, c *client, accountID int64, user, text, image string) {
	t.Helper()
	body := fmt.Sprintf(`{"account":%d,"username":%q,"text":%q,"image":%q}`, accountID, user, text, image)
	c.json("POST", "/api/dev/simulate", body, http.StatusAccepted)
	if _, err := e.s.Inbox.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func idOf(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return id
}

func upload(t *testing.T, c *client, accountID int64, data []byte) (*http.Response, media.Asset) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write(data)
	_ = mw.WriteField("account", fmt.Sprint(accountID))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", c.base+"/api/media", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	var a media.Asset
	if res.StatusCode == http.StatusCreated {
		a = decode[media.Asset](t, res)
	}
	return res, a
}

// 端到端：模擬 OAuth 連上兩個帳號 → 各收一則 DM → 合併收件匣與篩選 → 回覆從對話帳號送出；
// 再以只能看 A 帳號的員工逐一驗證每種資源都看不到、改不到 B 帳號（清單 403／單筆 404）。
func TestAccountScopeAcrossResources(t *testing.T) {
	e := setupFull(t)
	ctx := context.Background()
	owner := login(t, e.srv, "boss", "boss-password")

	// 連結前模擬 → 409
	res := owner.json("POST", "/api/dev/simulate", `{"username":"x","text":"hi"}`, http.StatusConflict)
	if m := decode[map[string]string](t, res); m["error"] != "請先到設定頁連結 IG 帳號（模擬）" {
		t.Fatalf("not connected: %v", m)
	}
	connect(t, owner)
	accs := decode[[]acc](t, owner.json("GET", "/api/ig/accounts", "", 200))
	if len(accs) != 2 || accs[0].IGUserID != ig.FakeAccountCoffee || accs[1].IGUserID != ig.FakeAccountRoastery {
		t.Fatalf("accounts after connect: %+v", accs)
	}
	A, B := accs[0].ID, accs[1].ID

	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pngData(t)) }))
	defer img.Close()
	simulate(t, e, owner, A, "alice", "給 A 店", "")
	simulate(t, e, owner, B, "bob", "給 B 店", img.URL+"/p.png")

	convs := decode[[]inbox.Conversation](t, owner.json("GET", "/api/conversations", "", 200))
	if len(convs) != 2 {
		t.Fatalf("merged inbox: %+v", convs)
	}
	onlyB := decode[[]inbox.Conversation](t, owner.json("GET", fmt.Sprintf("/api/conversations?account=%d", B), "", 200))
	if len(onlyB) != 1 || onlyB[0].IGAccountID != B || onlyB[0].Contact.Username != "bob" {
		t.Fatalf("filter B: %+v", onlyB)
	}
	convA := idOf(t, e.pool, `SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = $1`, A)
	convB := onlyB[0].ID
	contactB := onlyB[0].Contact.ID
	var mediaFile string
	_ = e.pool.QueryRow(ctx, `SELECT media_file FROM messages WHERE conversation_id = $1 AND media_file <> ''`, convB).Scan(&mediaFile)
	if mediaFile == "" {
		t.Fatal("B 的圖片應已下載")
	}

	// 回覆從對話所屬帳號送出，兩個帳號共用同一個（解密後的）token
	owner.json("POST", fmt.Sprintf("/api/conversations/%d/messages", convB), `{"text":"B 店回覆"}`, http.StatusCreated)
	owner.json("POST", fmt.Sprintf("/api/conversations/%d/messages", convA), `{"text":"A 店回覆"}`, http.StatusCreated)
	sent := e.s.Fake.Sent()
	if sent[0].AccountID != ig.FakeAccountRoastery || sent[1].AccountID != ig.FakeAccountCoffee || sent[0].Token != "dev-suat" || sent[1].Token != "dev-suat" {
		t.Fatalf("sent %+v", sent)
	}

	// 貼文／媒體：B 帳號一篇
	_, mediaB := upload(t, owner, B, pngData(t))
	if mediaB.IGAccountID != B {
		t.Fatalf("media %+v", mediaB)
	}
	postB := decode[posts.Post](t, owner.json("POST", "/api/posts",
		fmt.Sprintf(`{"action":"draft","post":{"igAccountId":%d,"kind":"image","mediaIds":[%d]}}`, B, mediaB.ID), 200))
	if postB.IGAccountID != B {
		t.Fatalf("post %+v", postB)
	}
	// 標籤：兩個帳號的顧客各貼一個
	tag := decode[crm.Tag](t, owner.json("POST", "/api/tags", `{"name":"常客"}`, http.StatusCreated))
	contactA := idOf(t, e.pool, `SELECT id FROM contacts WHERE ig_account_id = $1`, A)
	for _, cid := range []int64{contactA, contactB} {
		owner.json("PATCH", fmt.Sprintf("/api/contacts/%d", cid), fmt.Sprintf(`{"tagIds":[%d]}`, tag.ID), 200)
	}
	// B 的規則
	ruleB := decode[automation.Rule](t, owner.json("POST", fmt.Sprintf("/api/automation/rules?account=%d", B),
		`{"name":"B","matchType":"contains","keywords":["x"],"reply":"y","enabled":true}`, http.StatusCreated))

	// 員工：全部功能權限，但只能看 A
	ownerUser, _ := e.auth.Me(ctx, 1)
	if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "staff", Password: "staff-password",
		Permissions: auth.AllPerms, IGAccountIDs: []int64{A}}); err != nil {
		t.Fatal(err)
	}
	staff := login(t, e.srv, "staff", "staff-password")
	me := decode[auth.User](t, staff.json("GET", "/api/auth/me", "", 200))
	if len(me.IGAccountIDs) != 1 || me.IGAccountIDs[0] != A {
		t.Fatalf("me scope %+v", me.IGAccountIDs)
	}

	type probe struct {
		method, path, body string
		want               int
	}
	pB := fmt.Sprintf
	probes := []probe{
		// 收件匣
		{"GET", pB("/api/conversations?account=%d", B), "", 403},
		{"GET", pB("/api/conversations/%d", convB), "", 404},
		{"PATCH", pB("/api/conversations/%d", convB), `{"status":"closed"}`, 404},
		{"POST", pB("/api/conversations/%d/read", convB), "", 404},
		{"GET", pB("/api/conversations/%d/messages", convB), "", 404},
		{"POST", pB("/api/conversations/%d/messages", convB), `{"text":"x"}`, 404},
		{"GET", "/api/inbox/media/" + mediaFile, "", 404},
		// 顧客與標籤
		{"GET", pB("/api/contacts?account=%d", B), "", 403},
		{"GET", pB("/api/contacts/%d", contactB), "", 404},
		{"PATCH", pB("/api/contacts/%d", contactB), `{"note":"x"}`, 404},
		{"GET", pB("/api/tags?account=%d", B), "", 403},
		// 排程與媒體
		{"GET", pB("/api/posts?account=%d", B), "", 403},
		{"GET", pB("/api/posts/upcoming-count?account=%d", B), "", 403},
		{"GET", pB("/api/posts/%d", postB.ID), "", 404},
		{"PUT", pB("/api/posts/%d", postB.ID), `{"action":"draft","post":{"kind":"image"}}`, 404},
		{"DELETE", pB("/api/posts/%d", postB.ID), "", 404},
		{"POST", pB("/api/posts/%d/insights/refresh", postB.ID), "", 404},
		{"POST", "/api/posts", pB(`{"action":"draft","post":{"igAccountId":%d,"kind":"image"}}`, B), 403},
		{"POST", "/api/posts", pB(`{"action":"draft","post":{"igAccountId":%d,"kind":"image","mediaIds":[%d]}}`, A, mediaB.ID), 400},
		{"POST", "/api/posts/caption", pB(`{"brief":"新品","kind":"image","account":%d}`, B), 403},
		// 分析
		{"GET", pB("/api/analytics/summary?account=%d", B), "", 403},
		// 自動回覆（都要指定帳號）
		{"GET", "/api/automation/rules", "", 400},
		{"GET", pB("/api/automation/rules?account=%d", B), "", 403},
		{"GET", pB("/api/automation/settings?account=%d", B), "", 403},
		{"PUT", pB("/api/automation/settings?account=%d", B), `{"debounceSecs":1}`, 403},
		{"GET", pB("/api/automation/knowledge?account=%d", B), "", 403},
		{"POST", pB("/api/automation/knowledge?account=%d", B), `{"title":"x"}`, 403},
		{"POST", pB("/api/automation/test?account=%d", B), `{"text":"x"}`, 403},
		{"PUT", pB("/api/automation/rules/%d?account=%d", ruleB.ID, A), `{"name":"x","matchType":"contains","keywords":["x"],"reply":"x"}`, 404},
		{"DELETE", pB("/api/automation/rules/%d?account=%d", ruleB.ID, A), "", 404},
		// 模擬器
		{"POST", "/api/dev/simulate", pB(`{"account":%d,"username":"x","text":"hi"}`, B), 403},
		// 帳號與連線：超管專屬（全權限的員工也不行）
		{"POST", pB("/api/ig/accounts/%d/active", A), `{"active":false}`, 403},
		{"POST", pB("/api/ig/accounts/%d/subscribe", A), "", 403},
		{"POST", "/api/ig/connections/1/refresh", "", 403},
		{"DELETE", "/api/ig/connections/1", "", 403},
		{"GET", "/api/settings", "", 403},
		{"GET", "/api/ig/oauth/start", "", 403},
	}
	for _, p := range probes {
		if got := staff.status(p.method, p.path, p.body); got != p.want {
			t.Errorf("staff %s %s = %d, want %d", p.method, p.path, got, p.want)
		}
	}
	if code, _ := upload(t, staff, B, pngData(t)); code.StatusCode != 403 {
		t.Errorf("staff upload to B = %d", code.StatusCode)
	}
	if code, _ := upload(t, staff, A, pngData(t)); code.StatusCode != 201 {
		t.Errorf("staff upload to A = %d", code.StatusCode)
	}

	// 看得到的部分只剩 A
	if list := decode[[]inbox.Conversation](t, staff.json("GET", "/api/conversations", "", 200)); len(list) != 1 || list[0].ID != convA {
		t.Errorf("staff inbox %+v", list)
	}
	staff.json("GET", pB("/api/conversations/%d/messages", convA), "", 200)
	if list := decode[[]crm.Contact](t, staff.json("GET", "/api/contacts", "", 200)); len(list) != 1 || list[0].ID != contactA {
		t.Errorf("staff contacts %+v", list)
	}
	if tags := decode[[]crm.Tag](t, staff.json("GET", "/api/tags", "", 200)); tags[0].Count != 1 {
		t.Errorf("staff tag count %+v", tags)
	}
	if tags := decode[[]crm.Tag](t, owner.json("GET", "/api/tags", "", 200)); tags[0].Count != 2 {
		t.Errorf("owner tag count %+v", tags)
	}
	if list := decode[[]posts.Post](t, staff.json("GET", "/api/posts", "", 200)); len(list) != 0 {
		t.Errorf("staff posts %+v", list)
	}
	staffSum := decode[analytics.Summary](t, staff.json("GET", "/api/analytics/summary", "", 200))
	ownerSum := decode[analytics.Summary](t, owner.json("GET", "/api/analytics/summary", "", 200))
	if staffSum.Current.Totals.Inbound != 1 || ownerSum.Current.Totals.Inbound != 2 {
		t.Errorf("analytics staff=%d owner=%d", staffSum.Current.Totals.Inbound, ownerSum.Current.Totals.Inbound)
	}
	if list := decode[[]acc](t, staff.json("GET", "/api/ig/accounts", "", 200)); len(list) != 1 || list[0].ID != A {
		t.Errorf("staff accounts %+v", list)
	}
	staff.json("GET", pB("/api/automation/rules?account=%d", A), "", 200)
	owner.json("GET", "/api/inbox/media/"+mediaFile, "", 200)

	// 帳號停用（超管）：模擬器拒絕、回覆擋下
	owner.json("POST", pB("/api/ig/accounts/%d/active", B), `{"active":false}`, 403)
	newSuper(t, e.srv.URL).json("POST", pB("/api/ig/accounts/%d/active", B), `{"active":false}`, 200)
	owner.json("POST", "/api/dev/simulate", pB(`{"account":%d,"username":"bob","text":"還在嗎"}`, B), 409)
	owner.json("POST", pB("/api/conversations/%d/messages", convB), `{"text":"x"}`, 409)
}

// SSE 只推看得到的帳號；每次心跳重讀登入者，可見範圍變更與停用立即生效。
func TestStreamFiltersByAccountScope(t *testing.T) {
	old := streamPing
	streamPing = 150 * time.Millisecond
	defer func() { streamPing = old }()
	e := setupFull(t)
	ctx := context.Background()
	owner := login(t, e.srv, "boss", "boss-password")
	connect(t, owner)
	accs := decode[[]acc](t, owner.json("GET", "/api/ig/accounts", "", 200))
	A, B := accs[0].ID, accs[1].ID
	ownerUser, _ := e.auth.Me(ctx, 1)
	staffUser, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "staff", Password: "staff-password",
		Permissions: []string{auth.PermInbox}, IGAccountIDs: []int64{A}})
	if err != nil {
		t.Fatal(err)
	}
	staff := login(t, e.srv, "staff", "staff-password")

	req, _ := http.NewRequest("GET", e.srv.URL+"/api/inbox/stream", nil)
	req.AddCookie(staff.cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	events := make(chan int64, 100)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				var d struct {
					IGAccountID int64 `json:"igAccountId"`
				}
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
				events <- d.IGAccountID
			}
		}
	}()
	collect := func() map[int64]int {
		got := map[int64]int{}
		deadline := time.After(400 * time.Millisecond)
		for {
			select {
			case id := <-events:
				got[id]++
			case <-deadline:
				return got
			}
		}
	}

	simulate(t, e, owner, B, "bob", "給 B", "")
	simulate(t, e, owner, A, "alice", "給 A", "")
	if got := collect(); got[A] == 0 || got[B] != 0 {
		t.Fatalf("scope A: events %v", got)
	}

	// 改成只能看 B：下一次心跳後生效
	if _, err := e.pool.Exec(ctx, `UPDATE admin_users SET ig_account_ids = $2 WHERE id = $1`, staffUser.ID, []int64{B}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * streamPing)
	simulate(t, e, owner, A, "alice", "再給 A", "")
	simulate(t, e, owner, B, "bob", "再給 B", "")
	if got := collect(); got[B] == 0 || got[A] != 0 {
		t.Fatalf("scope B: events %v", got)
	}

	// 停用 → 串流結束
	if _, err := e.pool.Exec(ctx, `UPDATE admin_users SET disabled_at = now() WHERE id = $1`, staffUser.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled user's stream should close")
	}
}

// 可見帳號的授予規則：只看部分帳號的人不能授予自己看不到的帳號，也不能授予「全部」。
func TestAccountScopeGrantPolicy(t *testing.T) {
	e := setupFull(t)
	ctx := context.Background()
	owner := login(t, e.srv, "boss", "boss-password")
	connect(t, owner)
	accs := decode[[]acc](t, owner.json("GET", "/api/ig/accounts", "", 200))
	A, B := accs[0].ID, accs[1].ID
	ownerUser, _ := e.auth.Me(ctx, 1)
	if _, err := e.auth.CreateAdmin(ctx, ownerUser, auth.AdminInput{Username: "mgr", Password: "mgr-password",
		Permissions: []string{auth.PermAdmins, auth.PermInbox}, IGAccountIDs: []int64{A}}); err != nil {
		t.Fatal(err)
	}
	mgr := login(t, e.srv, "mgr", "mgr-password")
	mk := func(name, ids string) string {
		return fmt.Sprintf(`{"username":%q,"password":"pw-12345678","permissions":["inbox"]%s}`, name, ids)
	}
	mgr.json("POST", "/api/admins", mk("u1", fmt.Sprintf(`,"igAccountIds":[%d]`, B)), 403)
	mgr.json("POST", "/api/admins", mk("u2", ""), 403) // 全部
	u3 := decode[auth.User](t, mgr.json("POST", "/api/admins", mk("u3", fmt.Sprintf(`,"igAccountIds":[%d]`, A)), 201))
	mgr.json("PATCH", fmt.Sprintf("/api/admins/%d", u3.ID), fmt.Sprintf(`{"igAccountIds":[%d,%d]}`, A, B), 403)
	owner.json("PATCH", fmt.Sprintf("/api/admins/%d", u3.ID), `{"igAccountIds":[99999]}`, 400)
	got := decode[auth.User](t, owner.json("PATCH", fmt.Sprintf("/api/admins/%d", u3.ID), fmt.Sprintf(`{"igAccountIds":[%d,%d]}`, B, A), 200))
	if len(got.IGAccountIDs) != 2 {
		t.Fatalf("owner widen: %+v", got.IGAccountIDs)
	}
	got = decode[auth.User](t, owner.json("PATCH", fmt.Sprintf("/api/admins/%d", u3.ID), `{"igAccountIds":[]}`, 200))
	if got.IGAccountIDs != nil {
		t.Fatalf("[] should mean all: %+v", got.IGAccountIDs)
	}
	// 店長一律全部
	got = decode[auth.User](t, owner.json("PATCH", fmt.Sprintf("/api/admins/%d", u3.ID), fmt.Sprintf(`{"isOwner":true,"igAccountIds":[%d]}`, A), 200))
	if !got.IsOwner || got.IGAccountIDs != nil {
		t.Fatalf("owner scope %+v", got)
	}
}
