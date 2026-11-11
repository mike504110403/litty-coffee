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
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
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
	// Box 加解密 GA（TOTP）secret；nil＝不能綁定 GA（已綁定的人也無法通過驗證碼）
	Box *secrets.Box
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
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	IsOwner     bool   `json:"isOwner"`
	// IsSuper 超級管理員：位階在店長之上，IG 帳號連結與串接憑證只限超管；一律可見全部帳號
	IsSuper     bool     `json:"isSuper"`
	Permissions []string `json:"permissions"` // 有效權限：店長、超管回完整清單
	// IGAccountIDs 是可見的 IG 帳號（ig_accounts.id）；nil＝全部（店長一律 nil）。見 scope.go
	IGAccountIDs []int64    `json:"igAccountIds"`
	Disabled     bool       `json:"disabled"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	// LastSeenAt 最後有動靜的時間（最多每分鐘更新）；Online＝2 分鐘內有動靜且有有效 session
	LastSeenAt *time.Time `json:"lastSeenAt"`
	Online     bool       `json:"online"`
	// MustChangePassword 必須先改密碼；TOTPEnabled 已綁定 GA（登入要驗證碼）
	MustChangePassword bool `json:"mustChangePassword"`
	TOTPEnabled        bool `json:"totpEnabled"`
	// SetupRequired＝只能改密碼／綁 GA／登出（必須改密碼，或超管尚未綁 GA），見 SetupGate
	SetupRequired bool   `json:"setupRequired"`
	sessionID     string // 目前有效的 session（不輸出）
}

// SameSession 回報兩份使用者資料是否屬於同一個有效 session（SSE 心跳重讀時比對）。
func (u User) SameSession(o User) bool { return u.sessionID != "" && u.sessionID == o.sessionID }

const userCols = `id, username, display_name, is_owner, permissions, disabled_at, last_login_at, created_at, ig_account_ids,
  last_seen_at, session_id, is_super, must_change_password, totp_enabled_at IS NOT NULL`

func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	var disabled *time.Time
	var sid *string
	err := row.Scan(append([]any{&u.ID, &u.Username, &u.DisplayName, &u.IsOwner, &u.Permissions, &disabled,
		&u.LastLoginAt, &u.CreatedAt, &u.IGAccountIDs, &u.LastSeenAt, &sid, &u.IsSuper, &u.MustChangePassword,
		&u.TOTPEnabled}, extra...)...)
	u.Disabled = disabled != nil
	u.SetupRequired = u.MustChangePassword || (u.IsSuper && !u.TOTPEnabled)
	if sid != nil {
		u.sessionID = *sid
	}
	u.Online = u.sessionID != "" && !u.Disabled && u.LastSeenAt != nil && time.Since(*u.LastSeenAt) < onlineWithin
	if u.IsOwner || u.IsSuper {
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

// EnsureAdmin creates the initial admin (an owner) when there is no admin besides super admins
// (migration 00016 seeds superAdmin, so the table is never empty on a fresh install).
func (s *Service) EnsureAdmin(ctx context.Context, username, password string) error {
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM admin_users WHERE NOT is_super`).Scan(&n); err != nil {
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

// ErrMFARequired：密碼正確但已綁 GA，要再走 VerifyMFA（Login 便利包裝用）。
var ErrMFARequired = errors.New("需要驗證碼")

// LoginResult 是密碼驗證的結果：Session 與 Pending 恰有一個非空。
//   - Session：正式 session token（沒綁 GA）
//   - Pending：已綁 GA，待驗證碼的一次性 token（5 分鐘；不能當 session 用），見 totp.go
type LoginResult struct {
	User    User
	Session string
	Pending string
}

// Authenticate 驗帳密；停用的帳號拒絕登入。沒綁 GA：更新 last_login_at 並產生新的 session（同帳號其他裝置隨即失效）；
// 已綁 GA：只發待驗證碼 token，既有 session 不動（驗證碼通過才換 session）。
func (s *Service) Authenticate(ctx context.Context, username, password string) (LoginResult, error) {
	var hash string
	u, err := scanUser(s.DB.QueryRow(ctx,
		`SELECT `+userCols+`, password_hash FROM admin_users WHERE username = $1`, username), &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginResult{}, ErrBadCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return LoginResult{}, ErrBadCredentials
	}
	if u.Disabled {
		return LoginResult{}, ErrDisabled
	}
	if u.TOTPEnabled {
		pending, err := s.issuePending(ctx, u.ID)
		return LoginResult{User: u, Pending: pending}, err
	}
	u, tok, err := s.startSession(ctx, s.DB, u, "")
	return LoginResult{User: u, Session: tok}, err
}

// Login 是 Authenticate 的便利包裝：已綁 GA 的帳號回 ErrMFARequired。
func (s *Service) Login(ctx context.Context, username, password string) (User, string, error) {
	res, err := s.Authenticate(ctx, username, password)
	if err != nil {
		return User{}, "", err
	}
	if res.Pending != "" {
		return res.User, "", ErrMFARequired
	}
	return res.User, res.Session, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// startSession 產生新的 session（寫 session_id、last_login_at）並簽 JWT。pendingID 非空時只在待驗證 token 仍有效時才換
// （同時清掉它，一次性），否則回 ErrPendingInvalid。
func (s *Service) startSession(ctx context.Context, q querier, u User, pendingID string) (User, string, error) {
	sid, err := newSessionID()
	if err != nil {
		return User{}, "", err
	}
	err = q.QueryRow(ctx, `
UPDATE admin_users SET last_login_at = now(), last_seen_at = now(), session_id = $2, mfa_pending_id = NULL
WHERE id = $1 AND ($3 = '' OR mfa_pending_id = $3)
RETURNING last_login_at, last_seen_at`, u.ID, sid, pendingID).Scan(&u.LastLoginAt, &u.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrPendingInvalid
	}
	if err != nil {
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

var (
	ErrWeakPassword = errors.New("新密碼至少 8 個字元")
	ErrSamePassword = errors.New("新密碼不能與目前的密碼相同")
)

// ChangePassword 驗舊密碼後換新密碼，並清掉「必須改密碼」。自己目前的 session 不撤銷（單一登入下也不會有其他有效 session）。
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next string) error {
	if len(next) < 8 {
		return ErrWeakPassword
	}
	if next == current {
		return ErrSamePassword
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
	_, err = s.DB.Exec(ctx, `UPDATE admin_users SET password_hash = $2, must_change_password = false WHERE id = $1`, id, string(nh))
	return err
}

func writeAuthErrCode(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}
