// Package account 管連結的 IG 專業帳號（Instagram API with Unified Login）。
//
// 一次授權＝一個「連線」（ig_connections）：業務組合擁有的 system user access token（加密存 DB），
// 底下是這個 token 被指派到的所有 IG 帳號（ig_accounts.connection_id）。帳號可個別停用（不收不送、資料保留）；
// 斷開連線只拿掉 token，帳號與顧客／對話保留。各功能（收件匣、排程、成效）都用「該帳號所屬連線」的 token。
package account

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
)

var (
	ErrNotFound      = errors.New("找不到這個 IG 帳號")
	ErrConnNotFound  = errors.New("找不到這個連線")
	ErrNotConnected  = errors.New("這個 IG 帳號尚未連結（到「設定」連結）")
	ErrInactive      = errors.New("這個 IG 帳號已停用（到「設定」啟用）")
	ErrExpired       = errors.New("IG 授權已過期，請到「設定」重新連結")
	ErrNoAccounts    = errors.New("這次授權沒有指派任何 IG 專業帳號：請在授權畫面勾選要連結的帳號")
	ErrNotConfigured = errors.New("尚未設定 Meta App ID／App Secret／配置 ID（到「設定 → 憑證」填寫，或設 IG_APP_ID／IG_APP_SECRET／IG_CONFIG_ID）")
)

// RelinkWithin：token 剩不到這段時間就在設定頁提示「需重新連結」（配置預設永不過期，通常不會出現）。
const RelinkWithin = 14 * 24 * time.Hour

// tokenAAD 是連線 token 加密用的 AAD。
const tokenAAD = "ig_connection_token"

type Account struct {
	ID                int64      `json:"id"`
	IGUserID          string     `json:"igUserId"`
	Username          string     `json:"username"`
	Name              string     `json:"name"`
	ProfilePic        string     `json:"profilePic"`
	Active            bool       `json:"active"`
	ConnectionID      *int64     `json:"connectionId"` // nil＝沒有 token（需重新連結）
	Connected         bool       `json:"connected"`
	WebhookSubscribed bool       `json:"webhookSubscribed"`
	LastError         string     `json:"lastError"`
	ConnectedAt       *time.Time `json:"connectedAt"`
}

type Connection struct {
	ID               int64      `json:"id"`
	ClientBusinessID string     `json:"clientBusinessId"`
	SystemUserID     string     `json:"systemUserId"`
	ConfigID         string     `json:"configId"`
	TokenExpires     *time.Time `json:"tokenExpires"` // nil＝永不過期
	NeedsRelink      bool       `json:"needsRelink"`  // 已過期或快過期
	LastError        string     `json:"lastError"`
	ConnectedAt      time.Time  `json:"connectedAt"`
}

type Service struct {
	DB            *pgxpool.Pool
	OAuth         ig.OAuth     // nil＝未設定；正式環境是 appsettings.LiveOAuth，模擬模式是 ig.FakeOAuth
	Box           *secrets.Box // 連線 token 加解密
	FallbackToken string       // env IG_ACCESS_TOKEN：帳號沒掛連線時的兜底
	Now           func() time.Time
}

// OAuthReady 回 OAuth 是否可用：nil 或實作了 Configured() 且回 false（憑證尚未設定）都算未設定。
func (s *Service) OAuthReady() bool {
	if s.OAuth == nil {
		return false
	}
	if c, ok := s.OAuth.(interface{ Configured() bool }); ok {
		return c.Configured()
	}
	return true
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const accountCols = `id, ig_user_id, username, name, profile_pic, active, connection_id, webhook_subscribed, last_error, connected_at`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.IGUserID, &a.Username, &a.Name, &a.ProfilePic, &a.Active, &a.ConnectionID,
		&a.WebhookSubscribed, &a.LastError, &a.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	a.Connected = a.ConnectionID != nil
	return a, err
}

// List 回全部帳號（依 id）。
func (s *Service) List(ctx context.Context) ([]Account, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+accountCols+` FROM ig_accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.DB.QueryRow(ctx, `SELECT `+accountCols+` FROM ig_accounts WHERE id = $1`, id))
}

const connCols = `id, client_business_id, system_user_id, config_id, token_expires, last_error, connected_at`

func (s *Service) scanConn(row pgx.Row) (Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.ClientBusinessID, &c.SystemUserID, &c.ConfigID, &c.TokenExpires, &c.LastError, &c.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrConnNotFound
	}
	c.NeedsRelink = c.TokenExpires != nil && c.TokenExpires.Sub(s.now()) < RelinkWithin
	return c, err
}

// Connections 回全部連線（依 id）。
func (s *Service) Connections(ctx context.Context) ([]Connection, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+connCols+` FROM ig_connections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := s.scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) Connection(ctx context.Context, id int64) (Connection, error) {
	return s.scanConn(s.DB.QueryRow(ctx, `SELECT `+connCols+` FROM ig_connections WHERE id = $1`, id))
}

// Credentials 回某帳號發 API 用的 (IG 帳號 ID, token)：該帳號所屬連線的 token；
// 帳號沒掛連線時才用 env 兜底。停用、過期、找不到都回錯誤。
func (s *Service) Credentials(ctx context.Context, accountID int64) (igUserID, token string, err error) {
	var active bool
	var enc *string
	var expires *time.Time
	err = s.DB.QueryRow(ctx, `
SELECT a.ig_user_id, a.active, c.access_token, c.token_expires
FROM ig_accounts a LEFT JOIN ig_connections c ON c.id = a.connection_id WHERE a.id = $1`, accountID).
		Scan(&igUserID, &active, &enc, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if !active {
		return igUserID, "", ErrInactive
	}
	if enc == nil {
		if s.FallbackToken != "" {
			return igUserID, s.FallbackToken, nil
		}
		return igUserID, "", ErrNotConnected
	}
	if expires != nil && !expires.After(s.now()) {
		return igUserID, "", ErrExpired
	}
	token, err = s.decrypt(*enc)
	return igUserID, token, err
}

func (s *Service) decrypt(enc string) (string, error) {
	if s.Box == nil {
		return "", errors.New("account: 未設定加密金鑰")
	}
	tok, err := s.Box.Decrypt(enc, tokenAAD)
	if err != nil {
		return "", fmt.Errorf("IG 授權解不開（SECRET_KEY 是否換過？請重新連結）：%w", err)
	}
	return tok, nil
}

// NoteAPIError：IG API 回 token 失效（店家移除 app、帳號移到別的業務組合、過期）時記在該連線的 last_error，設定頁會顯示。
func (s *Service) NoteAPIError(ctx context.Context, accountID int64, err error) {
	if !ig.IsTokenInvalid(err) {
		return
	}
	if _, e := s.DB.Exec(ctx, `
UPDATE ig_connections SET last_error = $2 WHERE id = (SELECT connection_id FROM ig_accounts WHERE id = $1)`,
		accountID, "IG 授權已失效，請重新連結："+err.Error()); e != nil {
		log.Printf("account: note api error: %v", e)
	}
}

// Connect 用 OAuth callback 的 code 完成連結：建立／更新連線，並同步這個 token 看得到的全部 IG 帳號。
func (s *Service) Connect(ctx context.Context, code string) (Connection, []Account, error) {
	if !s.OAuthReady() {
		return Connection{}, nil, ErrNotConfigured
	}
	tok, err := s.OAuth.Exchange(ctx, code)
	if err != nil {
		return Connection{}, nil, fmt.Errorf("換取授權失敗：%w", err)
	}
	biz, err := s.OAuth.Business(ctx, tok.AccessToken)
	if err != nil {
		return Connection{}, nil, fmt.Errorf("讀取業務組合資料失敗：%w", err)
	}
	list, err := s.OAuth.Accounts(ctx, tok.AccessToken)
	if err != nil {
		return Connection{}, nil, fmt.Errorf("讀取 IG 帳號清單失敗：%w", err)
	}
	if len(list) == 0 {
		return Connection{}, nil, ErrNoAccounts
	}
	if s.Box == nil {
		return Connection{}, nil, errors.New("account: 未設定加密金鑰")
	}
	enc, err := s.Box.Encrypt(tok.AccessToken, tokenAAD)
	if err != nil {
		return Connection{}, nil, err
	}
	var connID int64
	if err := s.DB.QueryRow(ctx, `
INSERT INTO ig_connections (client_business_id, system_user_id, config_id, access_token, token_expires, last_error, connected_at)
VALUES ($1, $2, $3, $4, $5, '', $6)
ON CONFLICT (client_business_id, system_user_id) DO UPDATE SET config_id = EXCLUDED.config_id,
  access_token = EXCLUDED.access_token, token_expires = EXCLUDED.token_expires, last_error = '', connected_at = EXCLUDED.connected_at
RETURNING id`, biz.ClientBusinessID, biz.SystemUserID, s.OAuth.ConfigID(), enc, tok.ExpiresAt, s.now()).Scan(&connID); err != nil {
		return Connection{}, nil, err
	}
	accs, err := s.syncAccounts(ctx, connID, tok.AccessToken, list)
	if err != nil {
		return Connection{}, nil, err
	}
	c, err := s.Connection(ctx, connID)
	return c, accs, err
}

// syncAccounts 把清單上的帳號掛到連線（webhook 先到過的同 ID 帳號會合併，舊顧客對話不分家），
// 不在清單上的舊帳號從這個連線拿掉，再逐一訂閱 webhook。
func (s *Service) syncAccounts(ctx context.Context, connID int64, token string, list []ig.AccountInfo) ([]Account, error) {
	now := s.now()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	ids := make([]string, 0, len(list))
	var accs []Account
	for _, info := range list {
		ids = append(ids, info.UserID)
		a, err := scanAccount(tx.QueryRow(ctx, `
INSERT INTO ig_accounts (ig_user_id, username, name, profile_pic, connection_id, connected_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (ig_user_id) DO UPDATE SET
  username = CASE WHEN EXCLUDED.username <> '' THEN EXCLUDED.username ELSE ig_accounts.username END,
  name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE ig_accounts.name END,
  profile_pic = CASE WHEN EXCLUDED.profile_pic <> '' THEN EXCLUDED.profile_pic ELSE ig_accounts.profile_pic END,
  connection_id = EXCLUDED.connection_id, connected_at = EXCLUDED.connected_at, last_error = ''
RETURNING `+accountCols, info.UserID, info.Username, info.Name, info.ProfilePic, connID, now))
		if err != nil {
			return nil, err
		}
		accs = append(accs, a)
	}
	if _, err := tx.Exec(ctx, `
UPDATE ig_accounts SET connection_id = NULL, webhook_subscribed = false, last_error = '這次授權沒有包含此帳號，請重新連結'
WHERE connection_id = $1 AND NOT (ig_user_id = ANY($2))`, connID, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(accs))
	for _, a := range accs {
		a, err := s.subscribe(ctx, a, token)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// subscribe 訂閱單一帳號的 webhook；失敗寫在帳號的 last_error（不算整體失敗）。
func (s *Service) subscribe(ctx context.Context, a Account, token string) (Account, error) {
	subErr := s.OAuth.Subscribe(ctx, token, a.IGUserID)
	lastErr := ""
	if subErr != nil {
		lastErr = "訂閱私訊通知失敗：" + subErr.Error()
		log.Printf("ig subscribe %s: %v", a.IGUserID, subErr)
	}
	return scanAccount(s.DB.QueryRow(ctx, `
UPDATE ig_accounts SET webhook_subscribed = $2, last_error = $3 WHERE id = $1 RETURNING `+accountCols,
		a.ID, subErr == nil, lastErr))
}

func (s *Service) connToken(ctx context.Context, connID int64) (string, error) {
	var enc string
	err := s.DB.QueryRow(ctx, `SELECT access_token FROM ig_connections WHERE id = $1`, connID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConnNotFound
	}
	if err != nil {
		return "", err
	}
	return s.decrypt(enc)
}

// RefreshConnection 用已存的 token 重新同步帳號清單（店家在 Meta 後台加減指派的帳號）並重查到期日。
// 失敗寫在連線的 last_error。
func (s *Service) RefreshConnection(ctx context.Context, connID int64) (Connection, error) {
	if !s.OAuthReady() {
		return Connection{}, ErrNotConfigured
	}
	tok, err := s.connToken(ctx, connID)
	if err != nil {
		return Connection{}, err
	}
	fail := func(err error) (Connection, error) {
		msg := "同步帳號失敗：" + err.Error()
		if ig.IsTokenInvalid(err) {
			msg = "IG 授權已失效，請重新連結：" + err.Error()
		}
		_, _ = s.DB.Exec(ctx, `UPDATE ig_connections SET last_error = $2 WHERE id = $1`, connID, msg)
		return Connection{}, errors.New(msg)
	}
	list, err := s.OAuth.Accounts(ctx, tok)
	if err != nil {
		return fail(err)
	}
	if len(list) == 0 {
		return fail(ErrNoAccounts)
	}
	if ex, ok := s.OAuth.(interface {
		TokenExpiry(context.Context, string) (*time.Time, error)
	}); ok {
		// 未驗證：SUAT 是否能用 debug_token 查到期日；查不到就維持原值
		if exp, err := ex.TokenExpiry(ctx, tok); err == nil {
			if _, err := s.DB.Exec(ctx, `UPDATE ig_connections SET token_expires = $2 WHERE id = $1`, connID, exp); err != nil {
				return Connection{}, err
			}
		}
	}
	if _, err := s.syncAccounts(ctx, connID, tok, list); err != nil {
		return Connection{}, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE ig_connections SET last_error = '' WHERE id = $1`, connID); err != nil {
		return Connection{}, err
	}
	return s.Connection(ctx, connID)
}

// Disconnect 刪掉連線（token）；底下帳號、顧客、對話保留，帳號變成「需重新連結」。
// TODO：未呼叫 DELETE subscribed_apps 取消訂閱；未連結帳號的 webhook 會被收件匣拒收。
func (s *Service) Disconnect(ctx context.Context, connID int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE ig_accounts SET connection_id = NULL, webhook_subscribed = false, last_error = ''
WHERE connection_id = $1`, connID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM ig_connections WHERE id = $1`, connID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConnNotFound
	}
	return tx.Commit(ctx)
}

// SetActive 停用／啟用單一帳號：停用後不收（webhook 拒收）不送（送訊、發文、成效都擋），資料保留。
func (s *Service) SetActive(ctx context.Context, id int64, active bool) (Account, error) {
	return scanAccount(s.DB.QueryRow(ctx, `UPDATE ig_accounts SET active = $2 WHERE id = $1 RETURNING `+accountCols, id, active))
}

// Resubscribe 重試訂閱單一帳號的私訊 webhook（連結時失敗才需要）。
func (s *Service) Resubscribe(ctx context.Context, id int64) (Account, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return a, err
	}
	if a.ConnectionID == nil {
		return a, ErrNotConnected
	}
	if !s.OAuthReady() {
		return a, ErrNotConfigured
	}
	tok, err := s.connToken(ctx, *a.ConnectionID)
	if err != nil {
		return a, err
	}
	a, err = s.subscribe(ctx, a, tok)
	if err != nil {
		return a, err
	}
	if !a.WebhookSubscribed {
		return a, errors.New(a.LastError)
	}
	return a, nil
}
