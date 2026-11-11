package account

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

func setup(t *testing.T) (*Service, *ig.FakeOAuth, *time.Time) {
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
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	f := ig.NewFakeOAuth("http://x/cb", "acc-1")
	return &Service{DB: pool, OAuth: f, Now: func() time.Time { return now }}, f, &now
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConnectMergesWithWebhookAccount(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	// webhook 先到：ingest 只建了 ig_user_id 的空列，底下已有顧客
	var id int64
	if err := s.DB.QueryRow(ctx, `INSERT INTO ig_accounts (ig_user_id) VALUES ('acc-1') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO contacts (ig_account_id, igsid) VALUES ($1, 'u1')`, id); err != nil {
		t.Fatal(err)
	}
	a, err := s.Connect(ctx, "dev-code#_")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != id || a.Username != "littycoffee" || !a.WebhookSubscribed || a.token != "dev-long" {
		t.Fatalf("connect: %+v", a)
	}
	if want := s.now().Add(60 * 24 * time.Hour); !a.TokenExpires.Equal(want) {
		t.Fatalf("expires %v want %v", a.TokenExpires, want)
	}
	if n := count(t, s.DB, `SELECT count(*) FROM ig_accounts`); n != 1 {
		t.Fatalf("accounts %d", n)
	}
	if tok, _ := s.Token(ctx); tok != "dev-long" {
		t.Fatalf("token %q", tok)
	}
}

func TestConnectBadCodeAndPersonal(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Connect(ctx, "nope"); err == nil {
		t.Fatal("bad code accepted")
	}
	f.Account.AccountType = "PERSONAL"
	if _, err := s.Connect(ctx, "dev-code"); err == nil || !strings.Contains(err.Error(), "個人帳號") {
		t.Fatalf("personal: %v", err)
	}
	if _, err := s.Current(ctx); err != ErrNotConnected {
		t.Fatalf("should stay disconnected: %v", err)
	}
}

func TestSingleActiveAccount(t *testing.T) {
	s, f, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Connect(ctx, "dev-code"); err != nil {
		t.Fatal(err)
	}
	f.Account.UserID, f.Account.Username = "acc-2", "other"
	if _, err := s.Connect(ctx, "dev-code"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s.DB, `SELECT count(*) FROM ig_accounts WHERE access_token <> ''`); n != 1 {
		t.Fatalf("active %d", n)
	}
	if a, _ := s.Current(ctx); a.Username != "other" {
		t.Fatalf("current %+v", a)
	}
}

func TestTokenFallbackAndExpiry(t *testing.T) {
	s, _, now := setup(t)
	ctx := context.Background()
	if _, err := s.Token(ctx); err == nil {
		t.Fatal("no account, no fallback should error")
	}
	s.FallbackToken = "env-tok"
	if tok, _ := s.Token(ctx); tok != "env-tok" {
		t.Fatalf("fallback %q", tok)
	}
	if _, err := s.Connect(ctx, "dev-code"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(61 * 24 * time.Hour)
	if _, err := s.Token(ctx); err == nil || !strings.Contains(err.Error(), "過期") {
		t.Fatalf("expired: %v", err)
	}
}

func TestRefreshDue(t *testing.T) {
	s, f, now := setup(t)
	ctx := context.Background()
	if ok, err := s.RefreshDue(ctx); ok || err != nil {
		t.Fatalf("no account: %v %v", ok, err)
	}
	if _, err := s.Connect(ctx, "dev-code"); err != nil {
		t.Fatal(err)
	}
	// 剩 50 天：不續
	*now = now.Add(10 * 24 * time.Hour)
	if ok, _ := s.RefreshDue(ctx); ok {
		t.Fatal("refreshed too early")
	}
	// 剩 15 天：續，到期日往後推、token 換新
	*now = now.Add(35 * 24 * time.Hour)
	if ok, err := s.RefreshDue(ctx); !ok || err != nil {
		t.Fatalf("refresh: %v %v", ok, err)
	}
	a, _ := s.Current(ctx)
	if a.token != "dev-long-r1" || !a.TokenExpires.Equal(now.Add(60*24*time.Hour)) || !a.TokenRefreshedAt.Equal(*now) {
		t.Fatalf("after refresh %+v", a)
	}
	// 失敗：留在 last_error，token 不變
	*now = now.Add(45 * 24 * time.Hour)
	f.FailRefresh = true
	if _, err := s.RefreshDue(ctx); err == nil {
		t.Fatal("want error")
	}
	a, _ = s.Current(ctx)
	if a.token != "dev-long-r1" || !strings.Contains(a.LastError, "自動續期失敗") {
		t.Fatalf("after fail %+v", a)
	}
	// 已過期：不再嘗試（只能重新連結）
	*now = now.Add(30 * 24 * time.Hour)
	f.FailRefresh = false
	if ok, err := s.RefreshDue(ctx); ok || err != nil {
		t.Fatalf("expired should skip: %v %v", ok, err)
	}
}

func TestManualRefreshTooSoon(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Connect(ctx, "dev-code"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx); err == nil || !strings.Contains(err.Error(), "24 小時") {
		t.Fatalf("too soon: %v", err)
	}
}

func TestDisconnectKeepsContacts(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	a, err := s.Connect(ctx, "dev-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO contacts (ig_account_id, igsid) VALUES ($1, 'u1')`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(ctx); err != ErrNotConnected {
		t.Fatalf("still connected: %v", err)
	}
	if n := count(t, s.DB, `SELECT count(*) FROM contacts`); n != 1 {
		t.Fatalf("contacts %d", n)
	}
}

// 真 client 的回應解析：短效 token 兩種形狀、錯誤訊息、#_ 尾巴。
func TestOAuthClientExchange(t *testing.T) {
	var gotCode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/oauth/access_token":
			gotCode = r.PostForm.Get("code")
			if gotCode == "flat" {
				_, _ = w.Write([]byte(`{"access_token":"s1","user_id":1,"permissions":["a"]}`))
				return
			}
			if gotCode == "bad" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error_type":"OAuthException","code":400,"error_message":"Invalid code"}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"access_token":"s2","user_id":"1","permissions":"a,b"}]}`))
		case "/access_token":
			if r.URL.Query().Get("grant_type") != "ig_exchange_token" || r.URL.Query().Get("client_secret") != "sec" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"long","token_type":"bearer","expires_in":5183944}`))
		}
	}))
	defer srv.Close()
	c := ig.NewOAuthClient("app", "sec", "https://x/cb", srv.URL+"/v23.0")
	c.TokenBase, c.GraphBase = srv.URL, srv.URL
	ctx := context.Background()
	if tok, err := c.Exchange(ctx, "nested#_"); tok != "s2" || err != nil || gotCode != "nested" {
		t.Fatalf("nested: %q %v code=%q", tok, err, gotCode)
	}
	if tok, err := c.Exchange(ctx, "flat"); tok != "s1" || err != nil {
		t.Fatalf("flat: %q %v", tok, err)
	}
	if _, err := c.Exchange(ctx, "bad"); err == nil || !strings.Contains(err.Error(), "Invalid code") {
		t.Fatalf("bad: %v", err)
	}
	lt, err := c.LongLived(ctx, "s2")
	if err != nil || lt.Token != "long" || lt.ExpiresIn < 59*24*time.Hour {
		t.Fatalf("long: %+v %v", lt, err)
	}
	if u := c.AuthorizeURL("st"); !strings.Contains(u, "instagram_business_content_publish") || !strings.Contains(u, "state=st") {
		t.Fatalf("authorize url %s", u)
	}
}
