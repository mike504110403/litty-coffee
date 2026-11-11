package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
)

// rawLogin 打 /api/auth/login，回回應與 cookie（不要求 200）。
func rawLogin(t *testing.T, base, user, pw string) (*http.Response, map[string]*http.Cookie) {
	t.Helper()
	res, err := http.Post(base+"/api/auth/login", "application/json",
		strings.NewReader(fmt.Sprintf(`{"username":%q,"password":%q}`, user, pw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	cs := map[string]*http.Cookie{}
	for _, c := range res.Cookies() {
		cs[c.Name] = c
	}
	return res, cs
}

// pendingLogin 密碼登入（已綁 GA），回待驗證碼 cookie。
func pendingLogin(t *testing.T, base, user, pw string) *http.Cookie {
	t.Helper()
	res, cs := rawLogin(t, base, user, pw)
	body := bodyOf(res)
	if res.StatusCode != 200 || !strings.Contains(body, `"mfaRequired":true`) {
		t.Fatalf("login %s: %d %s", user, res.StatusCode, body)
	}
	if cs[auth.CookieName] != nil && cs[auth.CookieName].MaxAge >= 0 {
		t.Fatal("mfa login must not set a session cookie")
	}
	p := cs[auth.PendingCookieName]
	if p == nil || !p.HttpOnly || p.MaxAge != 300 || p.Path != "/api/auth" {
		t.Fatalf("pending cookie: %+v", p)
	}
	return p
}

// verify 打 /api/auth/totp（帶待驗證碼 cookie），回回應與新的 session client（成功時）。
func verify(t *testing.T, base string, pending *http.Cookie, body string) (*http.Response, *client) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/auth/totp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if pending != nil {
		req.AddCookie(pending)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName && c.MaxAge > 0 {
			return res, &client{t: t, base: base, cookie: c}
		}
	}
	return res, nil
}

func bodyOf(res *http.Response) string {
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func errCodeOf(t *testing.T, res *http.Response) string {
	t.Helper()
	var e map[string]string
	_ = json.NewDecoder(res.Body).Decode(&e)
	return e["code"]
}

// IG 帳號連結與串接憑證只限超管：店長、全權限員工 403；GET /api/ig/accounts 所有人可讀。
func TestSuperOnlyRoutes(t *testing.T) {
	srv, authSvc, _ := setupServer(t)
	ctx := context.Background()
	owner, _ := authSvc.Me(ctx, 1)
	if _, err := authSvc.CreateAdmin(ctx, owner, auth.AdminInput{Username: "all", Password: "password123", Permissions: auth.AllPerms}); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.CreateAdmin(ctx, owner, auth.AdminInput{Username: "ib", Password: "password123", Permissions: []string{auth.PermInbox}}); err != nil {
		t.Fatal(err)
	}
	boss := login(t, srv, "boss", "boss-password")
	all := login(t, srv, "all", "password123")
	ib := login(t, srv, "ib", "password123")
	root := newSuper(t, srv.URL)
	for _, p := range probes {
		if p.perm != superOnly {
			continue
		}
		for name, c := range map[string]*client{"owner": boss, "all-perms": all} {
			if got := c.status(p.method, p.path, p.body); got != 403 {
				t.Errorf("%s %s %s = %d, want 403", name, p.method, p.path, got)
			}
		}
		got := root.status(p.method, p.path, p.body)
		if (p.want != 0 && got != p.want) || got == 403 {
			t.Errorf("super %s %s = %d, want %d", p.method, p.path, got, p.want)
		}
	}
	for name, c := range map[string]*client{"owner": boss, "all-perms": all, "inbox": ib, "super": root.client} {
		if got := c.status("GET", "/api/ig/accounts", ""); got != 200 {
			t.Errorf("%s GET /api/ig/accounts = %d", name, got)
		}
	}
	// 超管可見全部帳號、有全部權限
	var me auth.User
	if err := json.NewDecoder(root.do("GET", "/api/auth/me", "").Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if !me.IsSuper || me.IsOwner || me.IGAccountIDs != nil || len(me.Permissions) != len(auth.AllPerms) || me.SetupRequired || !me.TOTPEnabled {
		t.Fatalf("super /auth/me = %+v", me)
	}
	// ig_accounts／credentials 不能再授予
	if got := boss.status("POST", "/api/admins", `{"username":"x","password":"password123","permissions":["ig_accounts"]}`); got != 400 {
		t.Errorf("grant ig_accounts = %d, want 400", got)
	}
	if got := root.status("POST", "/api/admins", `{"username":"y","password":"password123","permissions":["credentials"]}`); got != 400 {
		t.Errorf("grant credentials = %d, want 400", got)
	}
}

// 首次登入鎖定：必須改密碼或超管未綁 GA 時，只能 /auth/me、改密碼、綁 GA、登出；其餘（含 SSE）403 setup_required。
func TestSetupGateAndForcedPasswordChange(t *testing.T) {
	srv, authSvc, pool := setupServer(t)
	ctx := context.Background()
	owner, _ := authSvc.Me(ctx, 1)
	if _, err := authSvc.CreateAdmin(ctx, owner, auth.AdminInput{Username: "mei", Password: "password123", Permissions: []string{auth.PermInbox}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET must_change_password = true WHERE username = 'mei';
INSERT INTO admin_users (username, password_hash, display_name, is_super, must_change_password)
SELECT 'sa', password_hash, '超管', true, true FROM admin_users WHERE username = 'mei'`); err != nil {
		t.Fatal(err)
	}

	blocked := []struct{ method, path, body string }{
		{"GET", "/api/conversations", ""},
		{"GET", "/api/inbox/stream", ""},
		{"GET", "/api/system/stream", ""},
		{"GET", "/api/system/status", ""},
		{"GET", "/api/ig/accounts", ""},
		{"POST", "/api/tags", `{"name":"x"}`},
		{"GET", "/api/admins", ""},
		{"GET", "/api/settings", ""},
	}
	check := func(who string, c *client) {
		t.Helper()
		for _, b := range blocked {
			res := c.do(b.method, b.path, b.body)
			if res.StatusCode != 403 || errCodeOf(t, res) != auth.ErrCodeSetupRequired {
				t.Errorf("%s setup-only %s %s = %d, want 403 setup_required", who, b.method, b.path, res.StatusCode)
			}
		}
		var me auth.User
		if err := json.NewDecoder(c.json("GET", "/api/auth/me", "", 200).Body).Decode(&me); err != nil || !me.SetupRequired {
			t.Errorf("%s /auth/me setupRequired: %+v", who, me)
		}
	}

	// 一般管理員：必須改密碼
	mei := login(t, srv, "mei", "password123")
	check("mei", mei)
	mei.json("POST", "/api/auth/password", `{"current":"password123","next":"password123"}`, 400) // 不能沿用
	mei.json("POST", "/api/auth/password", `{"current":"wrong-pass","next":"brand-new-1"}`, 400)
	mei.json("POST", "/api/auth/password", `{"current":"password123","next":"brand-new-1"}`, 204)
	mei.json("GET", "/api/conversations", "", 200) // 同一個 session 立即解鎖

	// 超管：改密碼後仍要綁 GA
	sa := login(t, srv, "sa", "password123")
	check("sa", sa)
	sa.json("POST", "/api/auth/totp/enable", `{"code":"123456"}`, 400) // 還沒產生 secret
	sa.json("POST", "/api/auth/password", `{"current":"password123","next":"super-pass-1"}`, 204)
	check("sa after password", sa)
	setup := decode[auth.TOTPSetup](t, sa.json("POST", "/api/auth/totp/setup", "", 200))
	if !strings.HasPrefix(setup.OtpauthURL, "otpauth://totp/Litty%20Coffee:sa?") || !strings.Contains(setup.OtpauthURL, "issuer=Litty%20Coffee") ||
		!strings.HasPrefix(setup.QRCode, "data:image/png;base64,") || len(setup.Secret) < 32 {
		t.Fatalf("setup: %+v", setup)
	}
	sa.json("POST", "/api/auth/totp/enable", `{"code":"000000"}`, 400)
	en := decode[struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}](t, sa.json("POST", "/api/auth/totp/enable", fmt.Sprintf(`{"code":%q}`, totpCode(t, setup.Secret, time.Now())), 200))
	if len(en.RecoveryCodes) != 8 {
		t.Fatalf("recovery codes: %v", en.RecoveryCodes)
	}
	var stored int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes rc JOIN admin_users u ON u.id = rc.admin_id
WHERE u.username = 'sa' AND rc.code_hash <> ALL($1)`, en.RecoveryCodes).Scan(&stored)
	if stored != 8 {
		t.Fatalf("recovery codes must be stored hashed: %d", stored)
	}
	var secretCol string
	_ = pool.QueryRow(ctx, `SELECT totp_secret FROM admin_users WHERE username = 'sa'`).Scan(&secretCol)
	if !strings.HasPrefix(secretCol, "v1:") || strings.Contains(secretCol, setup.Secret) {
		t.Fatalf("totp secret must be encrypted: %q", secretCol)
	}
	sa.json("GET", "/api/settings", "", 200)
	sa.json("GET", "/api/conversations", "", 200)
	sa.json("POST", "/api/auth/totp/setup", "", 409) // 已綁定
	// 登出不受鎖定影響
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET must_change_password = true WHERE username = 'mei'`); err != nil {
		t.Fatal(err)
	}
	mei = login(t, srv, "mei", "brand-new-1")
	mei.json("POST", "/api/auth/logout", "", 204)
	mei.json("GET", "/api/auth/me", "", 401)
}

// GA 兩步驟登入：待驗證 token 不能當 session、一次性；錯碼、重用碼、限流 429、備援碼一次性；登入後單一登入踢人照舊。
func TestTOTPLoginFlow(t *testing.T) {
	srv, _, pool := setupServer(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE audit_logs`); err != nil {
		t.Fatal(err)
	}
	root := newSuper(t, srv.URL)
	first := root.client // newSuper 建立的 session

	// 密碼錯：照舊 401，不發待驗證 token
	if res, cs := rawLogin(t, srv.URL, root.Username, "nope"); res.StatusCode != 401 || cs[auth.PendingCookieName] != nil {
		t.Fatalf("bad password: %d", res.StatusCode)
	}
	pending := pendingLogin(t, srv.URL, root.Username, root.Password)
	first.json("GET", "/api/auth/me", "", 200) // 密碼正確但還沒過驗證碼：既有 session 不受影響

	// 待驗證 token 不能當 session
	fake := &client{t: t, base: srv.URL, cookie: &http.Cookie{Name: auth.CookieName, Value: pending.Value}}
	fake.json("GET", "/api/auth/me", "", 401)
	fake.json("GET", "/api/conversations", "", 401)
	// 沒有／亂寫的待驗證 token
	if res, _ := verify(t, srv.URL, nil, `{"code":"123456"}`); res.StatusCode != 401 {
		t.Fatalf("no pending: %d", res.StatusCode)
	}
	if res, _ := verify(t, srv.URL, &http.Cookie{Name: auth.PendingCookieName, Value: first.cookie.Value}, `{"code":"123456"}`); res.StatusCode != 401 {
		t.Fatalf("session token as pending: %d", res.StatusCode)
	}

	now := time.Now()
	if res, _ := verify(t, srv.URL, pending, `{"code":"000000"}`); res.StatusCode != 400 {
		t.Fatalf("wrong code: %d", res.StatusCode)
	}
	// 綁定時用過的時間步不能重用
	if res, _ := verify(t, srv.URL, pending, fmt.Sprintf(`{"code":%q}`, totpCode(t, root.Secret, now))); res.StatusCode != 400 {
		t.Fatalf("replayed code: %d", res.StatusCode)
	}
	// 容許下一個時間步（±1）
	res, second := verify(t, srv.URL, pending, fmt.Sprintf(`{"code":%q}`, totpCode(t, root.Secret, now.Add(30*time.Second))))
	if res.StatusCode != 200 || second == nil {
		t.Fatalf("good code: %d %s", res.StatusCode, bodyOf(res))
	}
	second.json("GET", "/api/settings", "", 200)
	// 單一登入：先前的 session 被踢
	if r := first.do("GET", "/api/auth/me", ""); r.StatusCode != 401 || errCodeOf(t, r) != auth.ErrCodeSessionReplaced {
		t.Fatalf("old session after mfa login: %d", r.StatusCode)
	}
	// 待驗證 token 一次性
	if res, c := verify(t, srv.URL, pending, fmt.Sprintf(`{"code":%q}`, totpCode(t, root.Secret, now.Add(30*time.Second)))); res.StatusCode != 401 || c != nil {
		t.Fatalf("pending reuse: %d", res.StatusCode)
	}

	// 備援碼：可用一次（大小寫、連字號不拘），再用就錯
	p2 := pendingLogin(t, srv.URL, root.Username, root.Password)
	rc := strings.ToUpper(strings.ReplaceAll(root.Recovery[0], "-", ""))
	res, third := verify(t, srv.URL, p2, fmt.Sprintf(`{"recoveryCode":%q}`, rc))
	if res.StatusCode != 200 || third == nil {
		t.Fatalf("recovery code: %d", res.StatusCode)
	}
	second.json("GET", "/api/auth/me", "", 401)
	third.json("GET", "/api/auth/me", "", 200)
	p3 := pendingLogin(t, srv.URL, root.Username, root.Password)
	if res, _ := verify(t, srv.URL, p3, fmt.Sprintf(`{"recoveryCode":%q}`, root.Recovery[0])); res.StatusCode != 400 {
		t.Fatalf("recovery reuse: %d", res.StatusCode)
	}
	// 新的登入取代舊的待驗證 token
	p4 := pendingLogin(t, srv.URL, root.Username, root.Password)
	if res, _ := verify(t, srv.URL, p3, `{"code":"000000"}`); res.StatusCode != 401 {
		t.Fatalf("superseded pending: %d", res.StatusCode)
	}

	// 限流：5 分鐘內錯 5 次後 429（碼正確也擋）
	for i := 0; i < 4; i++ { // 前面備援碼重用已錯 1 次
		if res, _ := verify(t, srv.URL, p4, `{"code":"000000"}`); res.StatusCode != 400 {
			t.Fatalf("wrong #%d: %d", i, res.StatusCode)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET totp_last_step = 0 WHERE id = $1`, root.ID); err != nil {
		t.Fatal(err)
	}
	good := fmt.Sprintf(`{"code":%q}`, totpCode(t, root.Secret, time.Now()))
	if res, _ := verify(t, srv.URL, p4, good); res.StatusCode != 429 {
		t.Fatalf("rate limited: %d", res.StatusCode)
	}
	// 視窗過了就放行
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET totp_failures_since = now() - interval '6 minutes' WHERE id = $1`, root.ID); err != nil {
		t.Fatal(err)
	}
	if res, c := verify(t, srv.URL, p4, good); res.StatusCode != 200 || c == nil {
		t.Fatalf("after window: %d", res.StatusCode)
	}

	// 待驗證 token 過期（DB 端被清）
	p5 := pendingLogin(t, srv.URL, root.Username, root.Password)
	if _, err := pool.Exec(ctx, `UPDATE admin_users SET mfa_pending_id = NULL WHERE id = $1`, root.ID); err != nil {
		t.Fatal(err)
	}
	if res, _ := verify(t, srv.URL, p5, good); res.StatusCode != 401 {
		t.Fatalf("cleared pending: %d", res.StatusCode)
	}

	// 操作紀錄：錯碼有記、成功記登入（mfa），任何紀錄都不含碼、secret、備援碼
	var failed, ok429, logins int
	_ = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action = $1 AND status = 400 AND NOT success),
  count(*) FILTER (WHERE action = $1 AND status = 429), count(*) FILTER (WHERE action = $2 AND detail->>'mfa' IN ('totp','recovery'))
FROM audit_logs WHERE admin_id = $3`, audit.ActionTOTPFailed, audit.ActionLogin, root.ID).Scan(&failed, &ok429, &logins)
	if failed != 7 || ok429 != 1 || logins != 3 {
		t.Fatalf("audit rows: failed=%d 429=%d logins=%d", failed, ok429, logins)
	}
	assertNoSecretsInAudit(t, pool, append([]string{root.Secret, rc, "000000"}, root.Recovery...))
}

// assertNoSecretsInAudit：操作紀錄任何欄位都不含這些字串。
func assertNoSecretsInAudit(t *testing.T, pool *pgxpool.Pool, secrets []string) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT action, admin_name || summary || target_id || detail::text || user_agent FROM audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var action, all string
		if err := rows.Scan(&action, &all); err != nil {
			t.Fatal(err)
		}
		n++
		for _, s := range secrets {
			if s != "" && strings.Contains(strings.ToLower(all), strings.ToLower(s)) {
				t.Errorf("audit %s leaks %q: %s", action, s, all)
			}
		}
	}
	if n == 0 {
		t.Fatal("no audit rows")
	}
}

// 重設 GA：只有超管、不能重設自己；被重設的超管立即被登出、重新登入要重綁；一般管理員可自選綁定，被重設後維持登入、下次登入不用碼。
func TestResetTOTP(t *testing.T) {
	srv, authSvc, pool := setupServer(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE audit_logs`); err != nil {
		t.Fatal(err)
	}
	owner, _ := authSvc.Me(ctx, 1)
	staff, err := authSvc.CreateAdmin(ctx, owner, auth.AdminInput{Username: "mei", Password: "password123", Permissions: []string{auth.PermInbox}})
	if err != nil {
		t.Fatal(err)
	}
	boss := login(t, srv, "boss", "boss-password")
	rootA := newSuper(t, srv.URL)
	rootB := newSuper(t, srv.URL)

	// 一般管理員自選綁定（店長也能綁）
	mei := login(t, srv, "mei", "password123")
	setup := decode[auth.TOTPSetup](t, mei.json("POST", "/api/auth/totp/setup", "", 200))
	codes := decode[struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}](t, mei.json("POST", "/api/auth/totp/enable", fmt.Sprintf(`{"code":%q}`, totpCode(t, setup.Secret, time.Now())), 200)).RecoveryCodes
	mei.json("GET", "/api/conversations", "", 200) // 綁定不影響目前 session
	pendingLogin(t, srv.URL, "mei", "password123") // 之後登入要碼

	// 權限：店長不能重設；超管不能重設自己；沒綁的回 400
	boss.json("POST", fmt.Sprintf("/api/admins/%d/totp/reset", staff.ID), "", 403)
	rootA.json("POST", fmt.Sprintf("/api/admins/%d/totp/reset", rootA.ID), "", 403)
	rootA.json("POST", "/api/admins/1/totp/reset", "", 400)
	rootA.json("POST", "/api/admins/999/totp/reset", "", 404)

	// 重設一般管理員：維持登入、下次登入不用碼、備援碼作廢
	rootA.json("POST", fmt.Sprintf("/api/admins/%d/totp/reset", staff.ID), "", 204)
	mei.json("GET", "/api/conversations", "", 200)
	if res, cs := rawLogin(t, srv.URL, "mei", "password123"); res.StatusCode != 200 || cs[auth.CookieName] == nil {
		t.Fatalf("login after reset: %d", res.StatusCode)
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE admin_id = $1`, staff.ID).Scan(&left)
	if left != 0 {
		t.Fatalf("recovery codes left: %d", left)
	}

	// 重設超管：立即登出；重新登入不用碼但進入首次鎖定，必須重綁
	rootA.json("POST", fmt.Sprintf("/api/admins/%d/totp/reset", rootB.ID), "", 204)
	rootB.json("GET", "/api/auth/me", "", 401)
	res, cs := rawLogin(t, srv.URL, rootB.Username, rootB.Password)
	if res.StatusCode != 200 || cs[auth.CookieName] == nil {
		t.Fatalf("super login after reset: %d", res.StatusCode)
	}
	b2 := &client{t: t, base: srv.URL, cookie: cs[auth.CookieName]}
	if r := b2.do("GET", "/api/settings", ""); r.StatusCode != 403 || errCodeOf(t, r) != auth.ErrCodeSetupRequired {
		t.Fatalf("reset super must rebind: %d", r.StatusCode)
	}
	if r := oneAudit(t, pool, "admin.reset_totp"); r != 2 {
		t.Fatalf("reset audit rows: %d", r)
	}
	assertNoSecretsInAudit(t, pool, append([]string{setup.Secret, rootA.Secret, rootB.Secret}, codes...))
}

func oneAudit(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action = $1 AND success`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 角色規則：店長不能動超管、不能建立／指定超管；超管可管理店長；最後一位啟用中的超管不能停用或降級。
func TestSuperRoleRules(t *testing.T) {
	srv, _, _ := setupServer(t)
	boss := login(t, srv, "boss", "boss-password")
	root := newSuper(t, srv.URL)
	p := func(f string, a ...any) string { return fmt.Sprintf(f, a...) }

	boss.json("POST", "/api/admins", `{"username":"s2","password":"password123","isSuper":true}`, 403)
	boss.json("PATCH", p("/api/admins/%d", root.ID), `{"displayName":"x"}`, 403)
	boss.json("PATCH", p("/api/admins/%d", root.ID), `{"disabled":true}`, 403)
	boss.json("POST", p("/api/admins/%d/password", root.ID), `{"password":"password999"}`, 403)
	if got := boss.status("POST", "/api/admins", `{"username":"mgr","password":"password123","permissions":["admins"]}`); got != 201 {
		t.Fatalf("create mgr: %d", got)
	}
	mgr := login(t, srv, "mgr", "password123")
	var mgrID int64
	for _, a := range decode[struct {
		Admins []auth.User `json:"admins"`
	}](t, boss.json("GET", "/api/admins", "", 200)).Admins {
		if a.Username == "mgr" {
			mgrID = a.ID
		}
		if a.Username == root.Username && (!a.IsSuper || !a.TOTPEnabled) {
			t.Fatalf("admin list super: %+v", a)
		}
	}
	boss.json("PATCH", p("/api/admins/%d", mgrID), `{"isSuper":true}`, 403)
	mgr.json("PATCH", p("/api/admins/%d", root.ID), `{"displayName":"x"}`, 403)

	// 最後一位超管
	root.json("PATCH", p("/api/admins/%d", root.ID), `{"isSuper":false,"isOwner":true}`, 403)
	// 超管可管理店長（含停用、降級，只要還有別的店長）
	root.json("PATCH", "/api/admins/1", `{"displayName":"老闆"}`, 200)
	root.json("POST", "/api/admins/1/password", `{"password":"boss-password2"}`, 204)
	root.json("PATCH", "/api/admins/1", `{"isOwner":false}`, 403) // 最後一位店長
	// 超管能建立／指定超管；兩位以上時可降級另一位
	s2 := decode[auth.User](t, root.json("POST", "/api/admins", `{"username":"s2","password":"password123","isSuper":true,"igAccountIds":[]}`, 201))
	if !s2.IsSuper || !s2.SetupRequired || s2.IGAccountIDs != nil {
		t.Fatalf("new super: %+v", s2)
	}
	root.json("PATCH", p("/api/admins/%d", mgrID), `{"isSuper":true}`, 200)
	root.json("PATCH", p("/api/admins/%d", mgrID), `{"isSuper":false,"permissions":["admins"]}`, 200)
	root.json("PATCH", p("/api/admins/%d", s2.ID), `{"disabled":true}`, 200)
	root.json("PATCH", p("/api/admins/%d", root.ID), `{"isSuper":false,"isOwner":true}`, 403) // s2 已停用，root 仍是最後一位
	root.json("PATCH", p("/api/admins/%d", s2.ID), `{"disabled":false}`, 200)
	root.json("PATCH", p("/api/admins/%d", root.ID), `{"isSuper":false,"isOwner":true}`, 200)
}
