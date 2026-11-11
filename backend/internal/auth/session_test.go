package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func callWith(s *Service, tok string) *httptest.ResponseRecorder {
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var e map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e["error"], e["code"]
}

// 單一登入：後登入的踢掉先登入的；被踢的回 401＋session_replaced 並清 cookie。
func TestSecondLoginReplacesFirst(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	mustCreate(t, s, owner, AdminInput{Username: "mei", Permissions: []string{PermInbox}})
	_, tok1, err := s.Login(ctx, "mei", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if rec := callWith(s, tok1); rec.Code != http.StatusOK {
		t.Fatalf("first token before second login: %d", rec.Code)
	}
	_, tok2, err := s.Login(ctx, "mei", "password123")
	if err != nil {
		t.Fatal(err)
	}
	rec := callWith(s, tok1)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("first token after second login: %d", rec.Code)
	}
	if msg, code := errCode(t, rec); code != ErrCodeSessionReplaced || msg != MsgSessionReplaced {
		t.Fatalf("kicked response: %q %q", msg, code)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", c)
	}
	if rec := callWith(s, tok2); rec.Code != http.StatusOK {
		t.Fatalf("second token: %d", rec.Code)
	}

	// 被踢的裝置按登出不影響新裝置
	if _, ok, err := s.Logout(ctx, tok1); err != nil || ok {
		t.Fatalf("logout with stale token: ok=%v err=%v", ok, err)
	}
	if rec := callWith(s, tok2); rec.Code != http.StatusOK {
		t.Fatalf("second token after stale logout: %d", rec.Code)
	}
	// 登出結束目前的 session
	if u, ok, err := s.Logout(ctx, tok2); err != nil || !ok || u.Username != "mei" {
		t.Fatalf("logout: %+v ok=%v err=%v", u, ok, err)
	}
	rec = callWith(s, tok2)
	if _, code := errCode(t, rec); rec.Code != http.StatusUnauthorized || code != "" {
		t.Fatalf("after logout: %d code=%q", rec.Code, code)
	}
}

// 重設密碼撤銷對方 session；改自己的密碼不會踢自己。
func TestResetPasswordRevokesChangePasswordKeeps(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	staff := mustCreate(t, s, owner, AdminInput{Username: "mei", Permissions: []string{PermInbox}})
	_, tok, err := s.Login(ctx, "mei", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, staff.ID, "password123", "password456"); err != nil {
		t.Fatal(err)
	}
	if rec := callWith(s, tok); rec.Code != http.StatusOK {
		t.Fatalf("after own password change: %d", rec.Code)
	}
	if err := s.ResetPassword(ctx, owner, staff.ID, "password789"); err != nil {
		t.Fatal(err)
	}
	rec := callWith(s, tok)
	if _, code := errCode(t, rec); rec.Code != http.StatusUnauthorized || code == ErrCodeSessionReplaced {
		t.Fatalf("after reset: %d code=%q", rec.Code, code)
	}
	if _, tok, err = s.Login(ctx, "mei", "password789"); err != nil {
		t.Fatal(err)
	}
	if rec := callWith(s, tok); rec.Code != http.StatusOK {
		t.Fatalf("login with reset password: %d", rec.Code)
	}
}

// 舊版（沒有 sid）的 token 一律失效。
func TestTokenWithoutSidRejected(t *testing.T) {
	s := setup(t)
	owner := mustOwner(t, s)
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(owner.ID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(s.Secret)
	if err != nil {
		t.Fatal(err)
	}
	// 就算 DB 也沒有 session（剛升級）也不能放行
	if _, err := s.DB.Exec(context.Background(), `UPDATE admin_users SET session_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if rec := callWith(s, tok); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token without sid: %d", rec.Code)
	}
}

// last_seen_at 最多每分鐘寫一次；清單的 online＝2 分鐘內有動靜且有有效 session。
func TestLastSeenAndOnline(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	staff := mustCreate(t, s, owner, AdminInput{Username: "mei", Permissions: []string{PermInbox}})
	onlineOf := func() (bool, *time.Time) {
		list, err := s.ListAdmins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range list {
			if u.ID == staff.ID {
				return u.Online, u.LastSeenAt
			}
		}
		t.Fatal("staff not listed")
		return false, nil
	}
	if on, seen := onlineOf(); on || seen != nil {
		t.Fatalf("never logged in: online=%v seen=%v", on, seen)
	}
	_, tok, err := s.Login(ctx, "mei", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if on, _ := onlineOf(); !on {
		t.Fatal("just logged in should be online")
	}
	// 30 秒前看過：這次請求不寫
	if _, err := s.DB.Exec(ctx, `UPDATE admin_users SET last_seen_at = now() - interval '30 seconds' WHERE id = $1`, staff.ID); err != nil {
		t.Fatal(err)
	}
	_, before := onlineOf()
	callWith(s, tok)
	if _, after := onlineOf(); !after.Equal(*before) {
		t.Fatalf("touched within a minute: %v -> %v", before, after)
	}
	// 5 分鐘沒動靜：離線；下一個請求更新
	if _, err := s.DB.Exec(ctx, `UPDATE admin_users SET last_seen_at = now() - interval '5 minutes' WHERE id = $1`, staff.ID); err != nil {
		t.Fatal(err)
	}
	if on, _ := onlineOf(); on {
		t.Fatal("idle 5 minutes should be offline")
	}
	callWith(s, tok)
	if on, after := onlineOf(); !on || time.Since(*after) > 10*time.Second {
		t.Fatalf("after request: online=%v seen=%v", on, after)
	}
	// 登出後即使剛看過也不算在線
	if _, _, err := s.Logout(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if on, _ := onlineOf(); on {
		t.Fatal("logged out should be offline")
	}
}
