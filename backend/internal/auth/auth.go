package auth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const CookieName = "litty_session"

var (
	ErrBadCredentials = errors.New("bad credentials")
	ErrDisabled       = errors.New("此帳號已停用")
)

type Service struct {
	DB     *pgxpool.Pool
	Secret []byte
	Secure bool
}

type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	IsOwner     bool       `json:"isOwner"`
	Permissions []string   `json:"permissions"` // 有效權限：店長回完整清單
	Disabled    bool       `json:"disabled"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

const userCols = `id, username, display_name, is_owner, permissions, disabled_at, last_login_at, created_at`

func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	var disabled *time.Time
	err := row.Scan(append([]any{&u.ID, &u.Username, &u.DisplayName, &u.IsOwner, &u.Permissions, &disabled,
		&u.LastLoginAt, &u.CreatedAt}, extra...)...)
	u.Disabled = disabled != nil
	if u.IsOwner {
		u.Permissions = append([]string{}, AllPerms...)
	} else {
		u.Permissions = normalizePerms(u.Permissions)
	}
	return u, err
}

func (s *Service) getUser(ctx context.Context, id int64) (User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM admin_users WHERE id = $1`, id))
}

// EnsureAdmin creates the initial admin (an owner) when the table is empty.
func (s *Service) EnsureAdmin(ctx context.Context, username, password string) error {
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if len(password) < 8 {
		return errors.New("ADMIN_PASSWORD (>=8 chars) required to create the first admin")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx,
		`INSERT INTO admin_users (username, password_hash, display_name, is_owner) VALUES ($1, $2, $1, true)`,
		username, string(hash))
	return err
}

// Login 驗帳密；停用的帳號拒絕登入。成功時更新 last_login_at。
func (s *Service) Login(ctx context.Context, username, password string) (User, string, error) {
	var hash string
	u, err := scanUser(s.DB.QueryRow(ctx,
		`SELECT `+userCols+`, password_hash FROM admin_users WHERE username = $1`, username), &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrBadCredentials
	}
	if err != nil {
		return User{}, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return User{}, "", ErrBadCredentials
	}
	if u.Disabled {
		return User{}, "", ErrDisabled
	}
	if err := s.DB.QueryRow(ctx, `UPDATE admin_users SET last_login_at = now() WHERE id = $1 RETURNING last_login_at`,
		u.ID).Scan(&u.LastLoginAt); err != nil {
		return User{}, "", err
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(u.ID, 10),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
	}).SignedString(s.Secret)
	return u, tok, err
}

func (s *Service) SetCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
}

type ctxKey struct{}

// Middleware 驗 JWT 後每次都從 DB 載入使用者：帳號停用或不存在即 401 並清 cookie，
// 所以停用當下既有 session 就失效。使用者（含權限）放進 context 供 RequirePerm 用。
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		claims := &jwt.RegisteredClaims{}
		_, err = jwt.ParseWithClaims(c.Value, claims, func(*jwt.Token) (any, error) { return s.Secret, nil },
			jwt.WithValidMethods([]string{"HS256"}))
		if err != nil {
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		id, _ := strconv.ParseInt(claims.Subject, 10, 64)
		u, err := s.getUser(r.Context(), id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("auth: load user %d: %v", id, err)
			writeAuthErr(w, http.StatusInternalServerError, "伺服器錯誤")
			return
		}
		if err != nil || u.Disabled {
			s.SetCookie(w, "", -1)
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (s *Service) Me(ctx context.Context, id int64) (User, error) {
	return s.getUser(ctx, id)
}

var ErrWeakPassword = errors.New("新密碼至少 8 個字元")

// ChangePassword 驗舊密碼後換新密碼。既有 session 不撤銷（單一店家、少量管理員）。
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next string) error {
	if len(next) < 8 {
		return ErrWeakPassword
	}
	var hash string
	if err := s.DB.QueryRow(ctx, `SELECT password_hash FROM admin_users WHERE id = $1`, id).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return ErrBadCredentials
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE admin_users SET password_hash = $2 WHERE id = $1`, id, string(nh))
	return err
}
