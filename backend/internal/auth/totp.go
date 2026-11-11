package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Google Authenticator（TOTP，RFC 6238）兩步驟登入：
//   - 參數：SHA1、6 位數、30 秒；驗證容許前後各 1 個時間步；同一時間步的碼用過即失效（totp_last_step）
//   - secret 以 secrets.Box 加密存 admin_users.totp_secret（AAD 綁管理員 id）；totp_enabled_at 非 NULL＝已綁定
//   - 登入：密碼正確且已綁定 → 只發「待驗證碼」token（httpOnly cookie litty_mfa，5 分鐘、一次性、另一把簽章鑰匙）
//     → POST /api/auth/totp 驗 6 位數或備援碼 → 換正式 session（沿用單一登入 sid）
//   - 錯誤次數：每個帳號 5 分鐘內最多錯 5 次（綁定確認與登入共用），之後回 ErrTooManyAttempts（429）
//   - 備援碼：綁定時產生 8 組，只存 SHA-256，一次性

const (
	TOTPIssuer        = "Litty Coffee"
	PendingCookieName = "litty_mfa"
	pendingTTL        = 5 * time.Minute
	maxCodeFailures   = 5
	codeFailWindow    = 5 * time.Minute
	recoveryCodeCount = 8
	totpPeriod        = 30
)

var (
	ErrPendingInvalid      = errors.New("驗證已逾時，請重新登入")
	ErrBadCode             = errors.New("驗證碼錯誤")
	ErrTooManyAttempts     = errors.New("驗證碼錯誤次數過多，請 5 分鐘後再試")
	ErrTOTPAlreadyEnabled  = errors.New("已綁定 Google Authenticator")
	ErrTOTPNoPendingSetup  = errors.New("請先產生 QR code 再輸入驗證碼")
	ErrTOTPUnavailable     = errors.New("伺服器未設定加密金鑰，無法使用 Google Authenticator")
	ErrTOTPNotEnabledReset = errors.New("這位管理員尚未綁定 Google Authenticator")
)

var totpOpts = totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

func totpAAD(id int64) string { return "admin_users.totp_secret:" + strconv.FormatInt(id, 10) }

// ---- 待驗證碼 token ----

type pendingClaims struct {
	jwt.RegisteredClaims
	Purpose string `json:"pur"`
	Pid     string `json:"pid"`
}

const pendingPurpose = "mfa_pending"

// pendingKey 是待驗證 token 的簽章鑰匙（與 session 的不同：拿它當 session cookie 會驗章失敗）。
func (s *Service) pendingKey() []byte {
	h := sha256.Sum256(append([]byte("litty-coffee/mfa-pending/v1\x00"), s.Secret...))
	return h[:]
}

// issuePending 產生待驗證碼 token，並把它的 id 存進 admin_users.mfa_pending_id（重新登入會蓋掉舊的）。
func (s *Service) issuePending(ctx context.Context, id int64) (string, error) {
	pid, err := newSessionID()
	if err != nil {
		return "", err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE admin_users SET mfa_pending_id = $2 WHERE id = $1`, id, pid); err != nil {
		return "", err
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, pendingClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(id, 10),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(pendingTTL)),
		},
		Purpose: pendingPurpose,
		Pid:     pid,
	}).SignedString(s.pendingKey())
}

func (s *Service) parsePending(tok string) (int64, string, error) {
	c := &pendingClaims{}
	if _, err := jwt.ParseWithClaims(tok, c, func(*jwt.Token) (any, error) { return s.pendingKey(), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired()); err != nil {
		return 0, "", ErrPendingInvalid
	}
	id, err := strconv.ParseInt(c.Subject, 10, 64)
	if err != nil || c.Purpose != pendingPurpose || c.Pid == "" {
		return 0, "", ErrPendingInvalid
	}
	return id, c.Pid, nil
}

// SetPendingCookie 設定（maxAge<0＝清除）待驗證碼 cookie；只送往 /api/auth。
func (s *Service) SetPendingCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: PendingCookieName, Value: token, Path: "/api/auth", MaxAge: maxAge,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
}

// PendingMaxAge 是待驗證碼 cookie 的秒數。
const PendingMaxAge = int(pendingTTL / time.Second)

// ---- 驗證 ----

type totpRow struct {
	secret      *string
	enabled     bool
	lastStep    int64
	failures    int
	failSince   *time.Time
	pendingID   *string
	disabled    bool
	username    string
	displayName string
}

func (s *Service) lockTOTPRow(ctx context.Context, tx pgx.Tx, id int64) (totpRow, error) {
	var r totpRow
	var disabled *time.Time
	err := tx.QueryRow(ctx, `
SELECT totp_secret, totp_enabled_at IS NOT NULL, totp_last_step, totp_failures, totp_failures_since, mfa_pending_id,
  disabled_at, username, display_name
FROM admin_users WHERE id = $1 FOR UPDATE`, id).Scan(&r.secret, &r.enabled, &r.lastStep, &r.failures, &r.failSince,
		&r.pendingID, &disabled, &r.username, &r.displayName)
	r.disabled = disabled != nil
	return r, err
}

func (r totpRow) limited(now time.Time) bool {
	return r.failures >= maxCodeFailures && r.failSince != nil && now.Sub(*r.failSince) < codeFailWindow
}

// recordFailure 記一次錯誤（固定視窗：視窗過期就從 1 重算）。
func recordFailure(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `
UPDATE admin_users SET
  totp_failures = CASE WHEN totp_failures_since IS NULL OR totp_failures_since < now() - $2 * interval '1 second'
    THEN 1 ELSE totp_failures + 1 END,
  totp_failures_since = CASE WHEN totp_failures_since IS NULL OR totp_failures_since < now() - $2 * interval '1 second'
    THEN now() ELSE totp_failures_since END
WHERE id = $1`, id, int(codeFailWindow/time.Second))
	return err
}

// matchTOTP 驗 6 位數碼（前後各 1 個時間步），回對上的時間步；比 lastStep 舊或相同（用過）的不算。
func matchTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for _, step := range []int64{cur - 1, cur, cur + 1} {
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0), totpOpts)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && step > lastStep {
			return step, true
		}
	}
	return 0, false
}

func (s *Service) decryptSecret(id int64, enc *string) (string, error) {
	if s.Box == nil {
		return "", ErrTOTPUnavailable
	}
	if enc == nil {
		return "", ErrTOTPNoPendingSetup
	}
	return s.Box.Decrypt(*enc, totpAAD(id))
}

// MFAResult 說明 VerifyMFA 用了哪種方式（寫操作紀錄用，不含碼本身）。
type MFAResult struct {
	User     User
	Session  string
	Recovery bool // 用備援碼登入
	Username string
}

// VerifyMFA 用待驗證碼 token＋6 位數碼（或備援碼）換正式 session。錯誤：
//   - ErrPendingInvalid：token 無效、過期、已用過或被新的登入取代；帳號已停用
//   - ErrTooManyAttempts：5 分鐘內錯太多次（碼正確也不放行）
//   - ErrBadCode：碼錯（計入錯誤次數）
//
// 失敗時 MFAResult.Username 仍帶帳號（記操作紀錄用）。
func (s *Service) VerifyMFA(ctx context.Context, pending, code, recovery string) (MFAResult, error) {
	id, pid, err := s.parsePending(pending)
	if err != nil {
		return MFAResult{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return MFAResult{}, err
	}
	defer tx.Rollback(ctx)
	row, err := s.lockTOTPRow(ctx, tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return MFAResult{}, ErrPendingInvalid
	}
	if err != nil {
		return MFAResult{}, err
	}
	res := MFAResult{Username: row.username, Recovery: strings.TrimSpace(recovery) != ""}
	res.User.ID, res.User.DisplayName = id, row.displayName
	if row.disabled || !row.enabled || row.pendingID == nil ||
		subtle.ConstantTimeCompare([]byte(*row.pendingID), []byte(pid)) != 1 {
		return res, ErrPendingInvalid
	}
	now := time.Now()
	if row.limited(now) {
		return res, ErrTooManyAttempts
	}
	ok := false
	step := row.lastStep
	if res.Recovery {
		tag, err := tx.Exec(ctx, `
UPDATE admin_recovery_codes SET used_at = now()
WHERE id = (SELECT id FROM admin_recovery_codes WHERE admin_id = $1 AND code_hash = $2 AND used_at IS NULL LIMIT 1)`,
			id, hashRecovery(recovery))
		if err != nil {
			return res, err
		}
		ok = tag.RowsAffected() == 1
	} else {
		secret, err := s.decryptSecret(id, row.secret)
		if err != nil {
			return res, err
		}
		step, ok = matchTOTP(secret, code, now, row.lastStep)
	}
	if !ok {
		if err := recordFailure(ctx, tx, id); err != nil {
			return res, err
		}
		if err := tx.Commit(ctx); err != nil {
			return res, err
		}
		return res, ErrBadCode
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET totp_failures = 0, totp_failures_since = NULL, totp_last_step = $2
WHERE id = $1`, id, step); err != nil {
		return res, err
	}
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM admin_users WHERE id = $1`, id))
	if err != nil {
		return res, err
	}
	u, tok, err := s.startSession(ctx, tx, u, pid)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	res.User, res.Session = u, tok
	return res, nil
}

// ---- 綁定 ----

// TOTPSetup 是開始綁定的結果：手動輸入用的 secret、otpauth URL 與 QR code（PNG data URL）。
type TOTPSetup struct {
	Secret     string `json:"secret"`
	OtpauthURL string `json:"otpauthUrl"`
	QRCode     string `json:"qrCode"`
}

// StartTOTPSetup 產生新的 secret（加密存起來、尚未啟用）；已綁定的回 ErrTOTPAlreadyEnabled（要先由超管重設）。
func (s *Service) StartTOTPSetup(ctx context.Context, id int64) (TOTPSetup, error) {
	if s.Box == nil {
		return TOTPSetup{}, ErrTOTPUnavailable
	}
	u, err := s.getUser(ctx, id)
	if err != nil {
		return TOTPSetup{}, err
	}
	if u.TOTPEnabled {
		return TOTPSetup{}, ErrTOTPAlreadyEnabled
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: TOTPIssuer, AccountName: u.Username, Period: totpPeriod,
		SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return TOTPSetup{}, err
	}
	enc, err := s.Box.Encrypt(key.Secret(), totpAAD(id))
	if err != nil {
		return TOTPSetup{}, err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE admin_users SET totp_secret = $2 WHERE id = $1 AND totp_enabled_at IS NULL`, id, enc)
	if err != nil {
		return TOTPSetup{}, err
	}
	if tag.RowsAffected() != 1 {
		return TOTPSetup{}, ErrTOTPAlreadyEnabled
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return TOTPSetup{}, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return TOTPSetup{}, err
	}
	return TOTPSetup{Secret: key.Secret(), OtpauthURL: key.URL(),
		QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

// EnableTOTP 用一組驗證碼確認綁定，成功後回 8 組一次性備援碼（只回這一次，DB 只存雜湊）。
func (s *Service) EnableTOTP(ctx context.Context, id int64, code string) ([]string, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row, err := s.lockTOTPRow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if row.enabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	now := time.Now()
	if row.limited(now) {
		return nil, ErrTooManyAttempts
	}
	secret, err := s.decryptSecret(id, row.secret)
	if err != nil {
		return nil, err
	}
	step, ok := matchTOTP(secret, code, now, row.lastStep)
	if !ok {
		if err := recordFailure(ctx, tx, id); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrBadCode
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET totp_enabled_at = now(), totp_last_step = $2, totp_failures = 0,
  totp_failures_since = NULL WHERE id = $1`, id, step); err != nil {
		return nil, err
	}
	codes, err := replaceRecoveryCodes(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit(ctx)
}

// ResetTOTP 由超管清掉別人的 GA 綁定（含備援碼）。被重設的是超管時同時撤銷其 session（立即要重新登入並重綁）；
// 一般管理員維持登入，下次登入不必驗證碼，可再自行綁定。
func (s *Service) ResetTOTP(ctx context.Context, actor User, id int64) error {
	if !actor.IsSuper {
		return policy("只有超級管理員能重設 Google Authenticator")
	}
	if id == actor.ID {
		return policy("不能重設自己的 Google Authenticator")
	}
	target, err := s.getUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if !target.TOTPEnabled {
		return badInput(ErrTOTPNotEnabledReset.Error())
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
UPDATE admin_users SET totp_secret = NULL, totp_enabled_at = NULL, totp_last_step = 0, mfa_pending_id = NULL,
  totp_failures = 0, totp_failures_since = NULL, session_id = CASE WHEN is_super THEN NULL ELSE session_id END
WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- 備援碼 ----

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // 去掉易混淆的 i l o 0 1

// normalizeRecovery 去空白與連字號、轉小寫。
func normalizeRecovery(code string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			return -1
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return r
	}, code)
}

func hashRecovery(code string) string {
	h := sha256.Sum256([]byte("litty-recovery\x00" + normalizeRecovery(code)))
	return hex.EncodeToString(h[:])
}

// newRecoveryCode 產生 xxxxx-xxxxx（10 字、約 49 bits）；拒絕取樣避免取餘偏差。
func newRecoveryCode() (string, error) {
	limit := byte(256 / len(recoveryAlphabet) * len(recoveryAlphabet))
	out := make([]byte, 0, 11)
	b := make([]byte, 1)
	for n := 0; n < 10; {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		if b[0] >= limit {
			continue
		}
		if n == 5 {
			out = append(out, '-')
		}
		out = append(out, recoveryAlphabet[int(b[0])%len(recoveryAlphabet)])
		n++
	}
	return string(out), nil
}

func replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, id int64) ([]string, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, id); err != nil {
		return nil, err
	}
	codes := make([]string, 0, recoveryCodeCount)
	for len(codes) < recoveryCodeCount {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, c)
		if _, err := tx.Exec(ctx, `INSERT INTO admin_recovery_codes (admin_id, code_hash) VALUES ($1, $2)`,
			id, hashRecovery(c)); err != nil {
			return nil, err
		}
	}
	return codes, nil
}
