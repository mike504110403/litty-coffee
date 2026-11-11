package account

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

func setup(t *testing.T, o ig.OAuth) (*Service, *time.Time) {
	pool := testdb.Open(t)
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts, ig_connections RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.FromKey("account-test-key")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	return &Service{DB: pool, OAuth: o, Box: box, Now: func() time.Time { return now }}, &now
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// metaServer 模擬 Meta 的 Unified Login 端點（graph.facebook.com）。
type metaServer struct {
	*httptest.Server
	mu         sync.Mutex
	pages      [][]map[string]string // assigned_instagram_accounts 的分頁
	subscribed map[string]string     // ig id → 訂閱時用的 token
	failSub    map[string]bool
	lastCode   string
	expiresIn  int64
	debugExp   int64
	accountsOK bool
}

const metaToken = "suat-123"

func newMeta(t *testing.T) *metaServer {
	m := &metaServer{subscribed: map[string]string{}, failSub: map[string]bool{}, accountsOK: true,
		pages: [][]map[string]string{
			{{"id": "1784001", "username": "shop_a", "name": "Shop A", "profile_picture_url": "https://cdn/a.jpg"}},
			{{"id": "1784002", "username": "shop_b"}}, // 沒有 name／頭像也要接受
		}}
	m.Server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.Close)
	return m
}

func (m *metaServer) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := r.URL.Query()
	authed := r.Header.Get("Authorization") == "Bearer "+metaToken
	switch {
	case r.URL.Path == "/v27.0/oauth/access_token" && r.Method == http.MethodGet:
		m.lastCode = q.Get("code")
		if q.Get("client_id") != "app-1" || q.Get("client_secret") != "app-secret" || q.Get("redirect_uri") != "https://shop.example/api/ig/oauth/callback" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"bad client","code":100}}`))
			return
		}
		if q.Get("code") != "good-code" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid verification code format.","code":100}}`))
			return
		}
		if m.expiresIn > 0 {
			_, _ = w.Write([]byte(`{"access_token":"` + metaToken + `","token_type":"bearer","expires_in":` + itoa(m.expiresIn) + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"` + metaToken + `","token_type":"bearer"}`))
	case r.URL.Path == "/v27.0/debug_token":
		if q.Get("access_token") != "app-1|app-secret" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"is_valid":true,"expires_at":` + itoa(m.debugExp) + `}}`))
	case r.URL.Path == "/v27.0/me" && authed:
		if q.Get("fields") != "id,client_business_id" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"id":"system-user-9","client_business_id":"biz-77"}`))
	case r.URL.Path == "/v27.0/me/assigned_instagram_accounts" && authed:
		if !m.accountsOK {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"Error validating access token","code":190}}`))
			return
		}
		page := 0
		if a := q.Get("after"); a != "" {
			page = int(a[len(a)-1] - '0')
		}
		if page >= len(m.pages) {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		body := `{"data":[` + jsonObjs(m.pages[page]) + `],"paging":{"cursors":{"before":"c` + itoa(int64(page)) + `","after":"c` + itoa(int64(page+1)) + `"}`
		if page+1 < len(m.pages) {
			body += `,"next":"https://graph.facebook.com/next"`
		}
		_, _ = w.Write([]byte(body + `}}`))
	case strings.HasSuffix(r.URL.Path, "/subscribed_apps") && r.Method == http.MethodPost && authed:
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/v27.0/"), "/")[0]
		if q.Get("subscribed_fields") != "messages,messaging_seen,message_reactions" {
			w.WriteHeader(400)
			return
		}
		if m.failSub[id] {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"(#100) cannot subscribe","code":100}}`))
			return
		}
		m.subscribed[id] = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, _ = w.Write([]byte(`{"success":true}`))
	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"unexpected ` + r.Method + ` ` + r.URL.Path + `","code":1}}`))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func jsonObjs(list []map[string]string) string {
	var parts []string
	for _, m := range list {
		var kv []string
		for k, v := range m {
			kv = append(kv, `"`+k+`":"`+v+`"`)
		}
		parts = append(parts, "{"+strings.Join(kv, ",")+"}")
	}
	return strings.Join(parts, ",")
}

func newClient(m *metaServer) *ig.OAuthClient {
	c := ig.NewOAuthClient("app-1", "app-secret", "cfg-55", "https://shop.example/api/ig/oauth/callback", m.URL+"/v27.0")
	c.HTTP = m.Client()
	return c
}

// 授權網址：facebook.com/dialog/oauth，帶 client_id、config_id、redirect_uri、response_type=code、state，不帶 scope。
func TestAuthorizeURL(t *testing.T) {
	c := ig.NewOAuthClient("app-1", "app-secret", "cfg-55", "https://shop.example/api/ig/oauth/callback", "https://graph.facebook.com/v27.0")
	u, err := url.Parse(c.AuthorizeURL("st-1"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "www.facebook.com" || u.Path != "/dialog/oauth" {
		t.Fatalf("authorize url %s", u)
	}
	want := map[string]string{"client_id": "app-1", "config_id": "cfg-55", "redirect_uri": "https://shop.example/api/ig/oauth/callback",
		"response_type": "code", "state": "st-1"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Fatalf("%s=%q want %q (%s)", k, q.Get(k), v, u)
		}
	}
	if q.Has("scope") {
		t.Fatal("Unified Login 不帶 scope（由 config_id 決定權限）")
	}
}

// 完整流程：code 換 token → /me 取業務組合 → 指派帳號翻兩頁 → 兩個帳號都掛在同一個連線、逐一訂閱；token 加密存放。
func TestConnectUnifiedLogin(t *testing.T) {
	m := newMeta(t)
	s, _ := setup(t, newClient(m))
	ctx := context.Background()
	// webhook 時代留下的同 ID 帳號（底下有顧客）要合併，不分家
	var oldID int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO ig_accounts (ig_user_id, username) VALUES ('1784002', 'old_b') RETURNING id`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO contacts (ig_account_id, igsid) VALUES ($1, 'u1')`, oldID); err != nil {
		t.Fatal(err)
	}

	conn, accs, err := s.Connect(ctx, "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if m.lastCode != "good-code" || conn.ClientBusinessID != "biz-77" || conn.SystemUserID != "system-user-9" || conn.ConfigID != "cfg-55" {
		t.Fatalf("connection %+v (code %q)", conn, m.lastCode)
	}
	if conn.TokenExpires != nil || conn.NeedsRelink {
		t.Fatalf("沒有 expires_in 且 debug_token expires_at=0 應為永不過期：%+v", conn)
	}
	if len(accs) != 2 || accs[0].Username != "shop_a" || accs[0].Name != "Shop A" || accs[0].ProfilePic != "https://cdn/a.jpg" {
		t.Fatalf("accounts %+v", accs)
	}
	if accs[1].ID != oldID || accs[1].Username != "shop_b" || accs[1].Name != "" {
		t.Fatalf("同 ID 帳號應合併：%+v", accs[1])
	}
	for _, a := range accs {
		if !a.Active || !a.Connected || *a.ConnectionID != conn.ID || !a.WebhookSubscribed {
			t.Fatalf("account %+v", a)
		}
	}
	if m.subscribed["1784001"] != metaToken || m.subscribed["1784002"] != metaToken {
		t.Fatalf("subscribed %v", m.subscribed)
	}
	var stored string
	_ = s.DB.QueryRow(ctx, `SELECT access_token FROM ig_connections WHERE id = $1`, conn.ID).Scan(&stored)
	if stored == metaToken || !strings.HasPrefix(stored, "v1:") {
		t.Fatalf("token 必須加密存放：%q", stored)
	}
	if count(t, s.DB, `SELECT count(*) FROM contacts WHERE ig_account_id = $1`, oldID) != 1 {
		t.Fatal("舊顧客應保留在合併後的帳號下")
	}

	// 一個 token 驅動兩個帳號：各自回自己的 IG 帳號 ID、同一個 token
	for _, a := range accs {
		igID, tok, err := s.Credentials(ctx, a.ID)
		if err != nil || igID != a.IGUserID || tok != metaToken {
			t.Fatalf("credentials(%d) = %s %s %v", a.ID, igID, tok, err)
		}
	}

	// 重新連結同一個業務組合：更新同一個連線，不會多一個
	if _, _, err := s.Connect(ctx, "good-code"); err != nil {
		t.Fatal(err)
	}
	if count(t, s.DB, `SELECT count(*) FROM ig_connections`) != 1 {
		t.Fatal("同一業務組合重新連結不應新增連線")
	}
}

func TestConnectErrorsAndPartialSubscribe(t *testing.T) {
	m := newMeta(t)
	s, _ := setup(t, newClient(m))
	ctx := context.Background()
	if _, _, err := s.Connect(ctx, "bad"); err == nil || !strings.Contains(err.Error(), "Invalid verification code") {
		t.Fatalf("bad code: %v", err)
	}
	m.pages = nil
	if _, _, err := s.Connect(ctx, "good-code"); !errors.Is(err, ErrNoAccounts) {
		t.Fatalf("no accounts: %v", err)
	}
	// 沒指派帳號：保留連線並記錯誤，之後在 Meta 補指派、按「同步帳號」即可
	if count(t, s.DB, `SELECT count(*) FROM ig_connections WHERE last_error LIKE '%手動加入帳號%'`) != 1 {
		t.Fatal("沒帳號時應保留連線並記錯誤")
	}
	if count(t, s.DB, `SELECT count(*) FROM ig_accounts`) != 0 {
		t.Fatal("沒帳號時不應新增帳號")
	}
	m.pages = [][]map[string]string{{{"id": "1784001", "username": "a"}, {"id": "1784002", "username": "b"}}}
	m.failSub["1784002"] = true
	_, accs, err := s.Connect(ctx, "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if !accs[0].WebhookSubscribed || accs[1].WebhookSubscribed || !strings.Contains(accs[1].LastError, "cannot subscribe") {
		t.Fatalf("partial subscribe: %+v", accs)
	}
	m.failSub["1784002"] = false
	a, err := s.Resubscribe(ctx, accs[1].ID)
	if err != nil || !a.WebhookSubscribed || a.LastError != "" {
		t.Fatalf("resubscribe: %+v %v", a, err)
	}
}

func TestExpiryFromExchangeAndDebugToken(t *testing.T) {
	m := newMeta(t)
	s, now := setup(t, newClient(m))
	ctx := context.Background()
	m.expiresIn = int64(10 * 24 * 3600)
	conn, accs, err := s.Connect(ctx, "good-code")
	if err != nil {
		t.Fatal(err)
	}
	// 10 天後到期（< RelinkWithin）→ 設定頁要提示重新連結
	if conn.TokenExpires == nil || !conn.NeedsRelink {
		t.Fatalf("expiring connection %+v", conn)
	}
	// 過期後不給 token
	*now = conn.TokenExpires.Add(time.Minute)
	if _, _, err := s.Credentials(ctx, accs[0].ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}

	// 同步時用 debug_token 重查到期日：expires_at=0 → 永不過期
	m.debugExp = 0
	*now = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	conn, err = s.RefreshConnection(ctx, conn.ID)
	if err != nil || conn.TokenExpires != nil || conn.NeedsRelink {
		t.Fatalf("refresh: %+v %v", conn, err)
	}
}

// 同步：店家在 Meta 拿掉某帳號的指派 → 帳號脫離連線；token 失效 → 寫在連線 last_error。
func TestRefreshConnectionSyncsAndRecordsErrors(t *testing.T) {
	m := newMeta(t)
	s, _ := setup(t, newClient(m))
	ctx := context.Background()
	conn, accs, err := s.Connect(ctx, "good-code")
	if err != nil {
		t.Fatal(err)
	}
	m.pages = [][]map[string]string{{{"id": "1784001", "username": "shop_a"}}}
	if _, err := s.RefreshConnection(ctx, conn.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Get(ctx, accs[1].ID)
	if b.Connected || b.WebhookSubscribed || b.LastError == "" {
		t.Fatalf("unassigned account should detach: %+v", b)
	}
	if _, _, err := s.Credentials(ctx, b.ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("detached credentials: %v", err)
	}

	m.accountsOK = false
	if _, err := s.RefreshConnection(ctx, conn.ID); err == nil {
		t.Fatal("token invalid should fail")
	}
	c, _ := s.Connection(ctx, conn.ID)
	if !strings.Contains(c.LastError, "已失效") {
		t.Fatalf("last_error %q", c.LastError)
	}

	// API 回 code 190 時（例如送訊失敗）也記在連線上
	if _, err := s.DB.Exec(ctx, `UPDATE ig_connections SET last_error = ''`); err != nil {
		t.Fatal(err)
	}
	s.NoteAPIError(ctx, accs[0].ID, &ig.APIError{Status: 400, Code: 190, Message: "Session has expired"})
	c, _ = s.Connection(ctx, conn.ID)
	if !strings.Contains(c.LastError, "Session has expired") {
		t.Fatalf("note api error: %q", c.LastError)
	}
	s.NoteAPIError(ctx, accs[0].ID, errors.New("network down")) // 非 token 錯誤不記
}

func TestDisableDisconnectAndFallback(t *testing.T) {
	s, _ := setup(t, ig.NewFakeOAuth("http://x/cb"))
	ctx := context.Background()
	conn, accs, err := s.Connect(ctx, "dev-code")
	if err != nil || len(accs) != 2 || accs[0].IGUserID != ig.FakeAccountCoffee || accs[1].IGUserID != ig.FakeAccountRoastery {
		t.Fatalf("fake connect: %+v %v", accs, err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO contacts (ig_account_id, igsid) VALUES ($1, 'u1')`, accs[0].ID); err != nil {
		t.Fatal(err)
	}

	a, err := s.SetActive(ctx, accs[0].ID, false)
	if err != nil || a.Active {
		t.Fatalf("disable: %+v %v", a, err)
	}
	if _, _, err := s.Credentials(ctx, accs[0].ID); !errors.Is(err, ErrInactive) {
		t.Fatalf("disabled credentials: %v", err)
	}
	if _, err := s.SetActive(ctx, accs[0].ID, true); err != nil {
		t.Fatal(err)
	}

	if err := s.Disconnect(ctx, conn.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnect(ctx, conn.ID); !errors.Is(err, ErrConnNotFound) {
		t.Fatalf("second disconnect: %v", err)
	}
	if count(t, s.DB, `SELECT count(*) FROM contacts`) != 1 || count(t, s.DB, `SELECT count(*) FROM ig_accounts WHERE connection_id IS NULL`) != 2 {
		t.Fatal("斷開只拿掉 token，帳號與顧客保留")
	}
	if _, _, err := s.Credentials(ctx, accs[0].ID); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("after disconnect: %v", err)
	}
	s.FallbackToken = "env-token"
	if id, tok, err := s.Credentials(ctx, accs[0].ID); err != nil || tok != "env-token" || id != ig.FakeAccountCoffee {
		t.Fatalf("fallback: %s %s %v", id, tok, err)
	}
	if _, _, err := s.Credentials(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

// 重現實測：Unified Login 的指派清單是空的，但 token 讀得到帳號 → 手動加入，同步帳號時不會被拿掉。
func TestAddAccountManuallyWhenListEmpty(t *testing.T) {
	f := ig.NewFakeOAuth("http://x/cb")
	f.List = nil
	f.Extra = []ig.AccountInfo{{UserID: "17841467271924092", Username: "mikedev666"}}
	s, _ := setup(t, f)
	ctx := context.Background()
	conn, _, err := s.Connect(ctx, "dev-code")
	if !errors.Is(err, ErrNoAccounts) || conn.ID == 0 {
		t.Fatalf("connect: %+v %v", conn, err)
	}
	if _, err := s.AddAccount(ctx, conn.ID, "abc"); !errors.Is(err, ErrBadIGID) {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := s.AddAccount(ctx, conn.ID, "999"); err == nil || !strings.Contains(err.Error(), "讀不到") {
		t.Fatalf("unreadable: %v", err)
	}
	a, err := s.AddAccount(ctx, conn.ID, " 17841467271924092 ")
	if err != nil || a.Username != "mikedev666" || !a.WebhookSubscribed || a.ConnectionID == nil || *a.ConnectionID != conn.ID {
		t.Fatalf("add: %+v %v", a, err)
	}
	if c, _ := s.Connection(ctx, conn.ID); c.LastError != "" {
		t.Fatalf("connection error not cleared: %q", c.LastError)
	}
	// 同步帳號：清單仍空，手動加入的帳號要保留、不報錯
	if _, err := s.RefreshConnection(ctx, conn.ID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, tok, err := s.Credentials(ctx, a.ID); err != nil || tok == "" {
		t.Fatalf("credentials after refresh: %v", err)
	}
}
