package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
)

const superOnly = "super" // perms_test 探針：超管專屬路由

// 測試用超管：IG 帳號連結與憑證只限超管，connect 等 helper 需要一位已綁 GA 的超管。
// setupFull／setupServer 依測試伺服器網址登記 auth.Service，helper 據此直接在 DB 建超管（不經 HTTP，不留操作紀錄）。
var (
	testAuths sync.Map // base URL → *auth.Service
	superSeq  atomic.Int64
)

func registerAuth(t *testing.T, base string, a *auth.Service) {
	testAuths.Store(base, a)
	t.Cleanup(func() { testAuths.Delete(base) })
}

type superUser struct {
	*client
	ID       int64
	Username string
	Password string
	Secret   string   // GA secret（產生驗證碼用）
	Recovery []string // 備援碼
}

// newSuper 建一位超管（已改密碼、已綁 GA）並回已登入的 client。
func newSuper(t *testing.T, base string) superUser {
	t.Helper()
	v, ok := testAuths.Load(base)
	if !ok {
		t.Fatal("newSuper: test server not registered (registerAuth)")
	}
	a := v.(*auth.Service)
	ctx := context.Background()
	su := superUser{Username: fmt.Sprintf("root%d", superSeq.Add(1)), Password: "root-password"}
	hash, _ := bcrypt.GenerateFromPassword([]byte(su.Password), bcrypt.MinCost)
	if err := a.DB.QueryRow(ctx, `INSERT INTO admin_users (username, password_hash, display_name, is_super) VALUES ($1, $2, $1, true)
RETURNING id`, su.Username, string(hash)).Scan(&su.ID); err != nil {
		t.Fatal(err)
	}
	_, tok, err := a.Login(ctx, su.Username, su.Password)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := a.StartTOTPSetup(ctx, su.ID)
	if err != nil {
		t.Fatal(err)
	}
	su.Secret = setup.Secret
	if su.Recovery, err = a.EnableTOTP(ctx, su.ID, totpCode(t, su.Secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	su.client = &client{t: t, base: base, cookie: &http.Cookie{Name: auth.CookieName, Value: tok}}
	return su
}

func totpCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := totp.GenerateCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
