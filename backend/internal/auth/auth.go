package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/mike504110403/litty-coffee/backend/internal/audit"
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
	Audit  *audit.Store // 被踢時記操作紀錄；nil＝不記
}

// 單一登入：每次登入產生新的 session_id（存 admin_users、放 JWT 的 sid）。請求的 sid 與 DB 不符＝已在別處登入。
const (
	// ErrCodeSessionReplaced 是被新登入踢掉時 401 回應的 code（前端據此跳提示）
	ErrCodeSessionReplaced = "session_replaced"
	MsgSessionReplaced     = "此帳號已在其他裝置登入，你已被登出"
	// lastSeenEvery：last_seen_at 最多這麼久寫一次；onlineWithin：多久內有動靜算在線
	lastSeenEvery = time.Minute
	onlineWithin  = 2 * time.Minute
)

type sessionClaims struct {
	jwt.RegisteredClaims
	Sid string `json:"sid"`
}

type User struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	IsOwner     bool     `json:"isOwner"`
	Permissions []string `json:"permissions"` // 有效權限：店長回完整清單
	// IGAccountIDs 是可見的 IG 帳號（ig_accounts.id）；nil＝全部（店長一律 nil）。見 scope.go
	IGAccountIDs []int64    `json:"igAccountIds"`
	Disabled     bool       `json:"disabled"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	// LastSeenAt 最後有動靜的時間（最多每分鐘更新）；Online＝2 分鐘內有動靜且有有效 session
	LastSeenAt *time.Time `json:"lastSeenAt"`
	Online     bool       `json:"online"`
	sessionID  string     // 目前有效的 session（不輸出）
}

// SameSession 回報兩份使用者資料是否屬於同一個有效 session（SSE 心跳重讀時比對）。
func (u User) SameSession(o User) bool { return u.sessionID != "" && u.sessionID == o.sessionID }

const userCols = `id, username, display_name, is_owner, permissions, disabled_at, last_login_at, created_at, ig_account_ids,
  last_seen_at, session_id`

func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	var disabled *time.Time
	var sid *string
	err := row.Scan(append([]any{&u.ID, &u.Username, &u.DisplayName, &u.IsOwner, &u.Permissions, &disabled,
		&u.LastLoginAt, &u.CreatedAt, &u.IGAccountIDs, &u.LastSeenAt, &sid}, extra...)...)
	u.Disabled = disabled != nil
	if sid != nil {
		u.sessionID = *sid
	}
	u.Online = u.sessionID != "" && !u.Disabled && u.LastSeenAt != nil && time.Since(*u.LastSeenAt) < onlineWithin
	if u.IsOwner {
		u.Permissions = append([]string{}, AllPerms...)
		u.IGAccountIDs = nil
	} else {
		u.Permissions = normalizePerms(u.Permissions)
		u.IGAccountIDs = normalizeAccounts(u.IGAccountIDs)
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

// Login 驗帳密；停用的帳號拒絕登入。成功時更新 last_login_at，並產生新的 session（同帳號其他裝置隨即失效）。
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
	sid, err := newSessionID()
	if err != nil {
		return User{}, "", err
	}
	if err := s.DB.QueryRow(ctx, `
UPDATE admin_users SET last_login_at = now(), last_seen_at = now(), session_id = $2 WHERE id = $1
RETURNING last_login_at, last_seen_at`, u.ID, sid).Scan(&u.LastLoginAt, &u.LastSeenAt); err != nil {
		return User{}, "", err
	}
	u.sessionID = sid
	u.Online = true
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(u.ID, 10),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		},
		Sid: sid,
	}).SignedString(s.Secret)
	return u, tok, err
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sessionHash 是寫進操作紀錄的 session 代號（不存原值）。
func sessionHash(sid string) string {
	h := sha256.Sum256([]byte(sid))
	return hex.EncodeToString(h[:8])
}

func (s *Service) parseToken(token string) (int64, string, error) {
	claims := &sessionClaims{}
	if _, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return s.Secret, nil },
		jwt.WithValidMethods([]string{"HS256"})); err != nil {
		return 0, "", err
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	return id, claims.Sid, err
}

// Logout 結束 token 所屬的 session：只有它仍是目前有效的 session 才清掉 session_id
// （已被別處登入踢掉的舊裝置登出，不影響新裝置）。ok＝確實結束了一個有效 session。
func (s *Service) Logout(ctx context.Context, token string) (User, bool, error) {
	id, sid, err := s.parseToken(token)
	if err != nil || sid == "" {
		return User{}, false, nil
	}
	u, err := scanUser(s.DB.QueryRow(ctx, `UPDATE admin_users SET session_id = NULL WHERE id = $1 AND session_id = $2
RETURNING `+userCols, id, sid))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

func (s *Service) SetCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
}

type ctxKey struct{}

// Middleware 驗 JWT 後每次都從 DB 載入使用者：帳號停用或不存在即 401 並清 cookie，
// 所以停用當下既有 session 就失效。JWT 的 sid 必須等於 DB 的 session_id：
//   - DB 有別的 session＝已在其他裝置登入 → 401＋code session_replaced（記一次「被踢」）
//   - DB 沒有 session（登出、被停用、被重設密碼）或舊版 token 沒有 sid → 401
//
// 使用者（含權限）放進 context 供 RequirePerm 用。last_seen_at 最多每分鐘更新一次。
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil {
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		id, sid, err := s.parseToken(c.Value)
		if err != nil {
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
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
		if sid == "" || sid != u.sessionID {
			s.SetCookie(w, "", -1)
			if sid != "" && u.sessionID != "" {
				s.Audit.RecordKicked(r.Context(), audit.Entry{AdminID: u.ID, AdminName: u.DisplayName,
					Detail: map[string]any{"session": sessionHash(sid), "path": r.URL.Path}, Status: http.StatusUnauthorized,
					Success: true}.FromRequest(r))
				writeAuthErrCode(w, http.StatusUnauthorized, MsgSessionReplaced, ErrCodeSessionReplaced)
				return
			}
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if u.LastSeenAt == nil || time.Since(*u.LastSeenAt) >= lastSeenEvery {
			if err := s.DB.QueryRow(r.Context(), `UPDATE admin_users SET last_seen_at = now() WHERE id = $1 AND session_id = $2
RETURNING last_seen_at`, u.ID, sid).Scan(&u.LastSeenAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("auth: touch last_seen %d: %v", u.ID, err)
			}
			u.Online = true
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (s *Service) Me(ctx context.Context, id int64) (User, error) {
	return s.getUser(ctx, id)
}

var ErrWeakPassword = errors.New("新密碼至少 8 個字元")

// ChangePassword 驗舊密碼後換新密碼。自己目前的 session 不撤銷（單一登入下也不會有其他有效 session）。
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

func writeAuthErrCode(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}
