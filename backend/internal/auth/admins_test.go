package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
)

// 整合測試：需要地端 PG，TEST_DATABASE_URL 未設時跳過。
func setup(t *testing.T) *Service {
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
	return &Service{DB: pool, Secret: []byte("test-secret")}
}

func mustOwner(t *testing.T, s *Service) User {
	t.Helper()
	if err := s.EnsureAdmin(context.Background(), "boss", "boss-password"); err != nil {
		t.Fatal(err)
	}
	u, _, err := s.Login(context.Background(), "boss", "boss-password")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func mustCreate(t *testing.T, s *Service, actor User, in AdminInput) User {
	t.Helper()
	if in.Password == "" {
		in.Password = "password123"
	}
	u, err := s.CreateAdmin(context.Background(), actor, in)
	if err != nil {
		t.Fatalf("create %s: %v", in.Username, err)
	}
	return u
}

func isPolicy(err error) bool {
	var pe *PolicyError
	return errors.As(err, &pe)
}

func ptr[T any](v T) *T { return &v }

func TestEnsureAdminCreatesOwnerWithAllPerms(t *testing.T) {
	s := setup(t)
	u := mustOwner(t, s)
	if !u.IsOwner || !slices.Equal(u.Permissions, AllPerms) || u.LastLoginAt == nil {
		t.Fatalf("owner = %+v", u)
	}
	for _, p := range AllPerms {
		if !u.Can(p) {
			t.Fatalf("owner lacks %s", p)
		}
	}
}

func TestLoginRejectsDisabled(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	staff := mustCreate(t, s, owner, AdminInput{Username: "mei", Permissions: []string{PermInbox}})
	if _, err := s.UpdateAdmin(ctx, owner, staff.ID, AdminPatch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "mei", "password123"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled login err = %v", err)
	}
	if _, _, err := s.Login(ctx, "mei", "wrong-password"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password must not reveal disabled state: %v", err)
	}
}

// 停用當下，既有 token 立即失效（Middleware 每次都查 DB）。
func TestMiddlewareRejectsDisabledUsersOldToken(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	staff := mustCreate(t, s, owner, AdminInput{Username: "mei", Permissions: []string{PermInbox}})
	_, tok, err := s.Login(ctx, "mei", "password123")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Middleware(RequirePerm(PermInbox)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserID(r.Context()) != staff.ID {
			t.Errorf("ctx user = %d", UserID(r.Context()))
		}
		w.WriteHeader(http.StatusOK)
	})))
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("before disable: %d", rec.Code)
	}
	if _, err := s.UpdateAdmin(ctx, owner, staff.ID, AdminPatch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	rec := call()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after disable: %d", rec.Code)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Name != CookieName || c[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", c)
	}
	// 停用會撤銷 session：重新啟用後舊 token 仍無效，要重新登入
	if _, err := s.UpdateAdmin(ctx, owner, staff.ID, AdminPatch{Disabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if rec := call(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after re-enable: %d", rec.Code)
	}
	if _, tok, err = s.Login(ctx, "mei", "password123"); err != nil {
		t.Fatal(err)
	}
	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("new login after re-enable: %d", rec.Code)
	}
}

func TestRequirePerm(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	call := func(u *User, perm string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if u != nil {
			req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, *u))
		}
		rec := httptest.NewRecorder()
		RequirePerm(perm)(ok).ServeHTTP(rec, req)
		return rec.Code
	}
	staff := &User{ID: 2, Permissions: []string{PermInbox}}
	owner := &User{ID: 1, IsOwner: true}
	for _, p := range AllPerms {
		want := http.StatusForbidden
		if p == PermInbox {
			want = http.StatusOK
		}
		if got := call(staff, p); got != want {
			t.Errorf("staff %s = %d, want %d", p, got, want)
		}
		if got := call(owner, p); got != http.StatusOK {
			t.Errorf("owner %s = %d", p, got)
		}
	}
	if got := call(nil, PermInbox); got != http.StatusUnauthorized {
		t.Errorf("no user = %d", got)
	}
}

func TestNonOwnerAdminLimits(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	mgr := mustCreate(t, s, owner, AdminInput{Username: "mgr", Permissions: []string{PermAdmins, PermInbox}})

	if _, err := s.CreateAdmin(ctx, mgr, AdminInput{Username: "a", Password: "password123", Permissions: []string{PermContacts}}); !isPolicy(err) {
		t.Fatalf("grant perm mgr lacks: %v", err)
	}
	if _, err := s.CreateAdmin(ctx, mgr, AdminInput{Username: "b", Password: "password123", IsOwner: true}); !isPolicy(err) {
		t.Fatalf("non-owner creating owner: %v", err)
	}
	staff, err := s.CreateAdmin(ctx, mgr, AdminInput{Username: "c", Password: "password123", Permissions: []string{PermInbox}})
	if err != nil {
		t.Fatalf("grant own perm: %v", err)
	}
	if _, err := s.UpdateAdmin(ctx, mgr, staff.ID, AdminPatch{Permissions: &[]string{PermInbox, PermAnalytics}}); !isPolicy(err) {
		t.Fatalf("update granting perm mgr lacks: %v", err)
	}
	if _, err := s.UpdateAdmin(ctx, mgr, staff.ID, AdminPatch{IsOwner: ptr(true)}); !isPolicy(err) {
		t.Fatalf("non-owner promoting: %v", err)
	}
	// 對方原本就有、自己沒有的權限可以保留（只擋「新授予」）
	rich := mustCreate(t, s, owner, AdminInput{Username: "rich", Permissions: []string{PermAnalytics}})
	if _, err := s.UpdateAdmin(ctx, mgr, rich.ID, AdminPatch{Permissions: &[]string{PermAnalytics, PermInbox}}); err != nil {
		t.Fatalf("keeping existing perm: %v", err)
	}
	// 店長帳號一律不能動
	for name, err := range map[string]error{
		"rename":  func() error { _, e := s.UpdateAdmin(ctx, mgr, owner.ID, AdminPatch{DisplayName: ptr("x")}); return e }(),
		"disable": func() error { _, e := s.UpdateAdmin(ctx, mgr, owner.ID, AdminPatch{Disabled: ptr(true)}); return e }(),
		"demote":  func() error { _, e := s.UpdateAdmin(ctx, mgr, owner.ID, AdminPatch{IsOwner: ptr(false)}); return e }(),
		"reset":   s.ResetPassword(ctx, mgr, owner.ID, "new-password"),
	} {
		if !isPolicy(err) {
			t.Fatalf("mgr %s owner: %v", name, err)
		}
	}
	// 自己：不能停用、不能拿掉 admins
	if _, err := s.UpdateAdmin(ctx, mgr, mgr.ID, AdminPatch{Disabled: ptr(true)}); !isPolicy(err) {
		t.Fatalf("disable self: %v", err)
	}
	if _, err := s.UpdateAdmin(ctx, mgr, mgr.ID, AdminPatch{Permissions: &[]string{PermInbox}}); !isPolicy(err) {
		t.Fatalf("remove own admins: %v", err)
	}
	// 一般操作可以
	if _, err := s.UpdateAdmin(ctx, mgr, staff.ID, AdminPatch{DisplayName: ptr("小美"), Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetPassword(ctx, mgr, staff.ID, "another-pass"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAdmin(ctx, mgr, AdminInput{Username: "c", Password: "password123"}); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username: %v", err)
	}
	var ie *InputError
	if _, err := s.CreateAdmin(ctx, owner, AdminInput{Username: "d", Password: "short"}); !errors.As(err, &ie) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := s.CreateAdmin(ctx, owner, AdminInput{Username: "e", Password: "password123", Permissions: []string{"nope"}}); !errors.As(err, &ie) {
		t.Fatalf("unknown perm: %v", err)
	}
}

func TestLastEnabledOwnerProtected(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	owner := mustOwner(t, s)
	keepAdmins := &[]string{PermAdmins}

	if _, err := s.UpdateAdmin(ctx, owner, owner.ID, AdminPatch{IsOwner: ptr(false), Permissions: keepAdmins}); !isPolicy(err) {
		t.Fatalf("demote last owner: %v", err)
	}
	if _, err := s.UpdateAdmin(ctx, owner, owner.ID, AdminPatch{Disabled: ptr(true)}); !isPolicy(err) {
		t.Fatalf("disable last owner: %v", err)
	}
	// 第二位店長停用中 → 仍是最後一位啟用中的店長
	second := mustCreate(t, s, owner, AdminInput{Username: "owner2", IsOwner: true})
	if _, err := s.UpdateAdmin(ctx, owner, second.ID, AdminPatch{Disabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAdmin(ctx, owner, owner.ID, AdminPatch{IsOwner: ptr(false), Permissions: keepAdmins}); !isPolicy(err) {
		t.Fatalf("demote while other owner disabled: %v", err)
	}
	// 第二位啟用後：可以降級自己，但對方變成最後一位
	if _, err := s.UpdateAdmin(ctx, owner, second.ID, AdminPatch{Disabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	demoted, err := s.UpdateAdmin(ctx, owner, owner.ID, AdminPatch{IsOwner: ptr(false), Permissions: keepAdmins})
	if err != nil {
		t.Fatalf("demote with another owner: %v", err)
	}
	if demoted.IsOwner || !slices.Equal(demoted.Permissions, []string{PermAdmins}) {
		t.Fatalf("demoted = %+v", demoted)
	}
	second, _ = s.Me(ctx, second.ID)
	if _, err := s.UpdateAdmin(ctx, second, second.ID, AdminPatch{IsOwner: ptr(false), Permissions: keepAdmins}); !isPolicy(err) {
		t.Fatalf("second is now the last owner: %v", err)
	}
	// 沒有刪除 API：管理員只能停用（停用的仍保留在列表）
	list, err := s.ListAdmins(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d err=%v", len(list), err)
	}
}
