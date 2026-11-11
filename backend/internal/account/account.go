// Package account 管理連結的 IG 商家帳號：OAuth 連結、token 存 DB、背景自動續期、斷開。
// 同一時間只有一個「已連結」帳號（有 token 的那個）；連新帳號會把舊的斷開，但保留舊帳號的顧客與對話。
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
)

var (
	ErrNotConnected = errors.New("尚未連結 IG 帳號")
	ErrTooSoon      = errors.New("授權取得未滿 24 小時，IG 不允許續期")
)

// 續期門檻：剩不到 RefreshWithin 就續；IG 規定 token 要滿 24 小時才能續。
const (
	RefreshWithin = 20 * 24 * time.Hour
	minTokenAge   = 24 * time.Hour
)

type Account struct {
	ID                int64      `json:"id"`
	IGUserID          string     `json:"igUserId"`
	Username          string     `json:"username"`
	Name              string     `json:"name"`
	ProfilePic        string     `json:"profilePic"`
	AccountType       string     `json:"accountType"`
	TokenExpires      *time.Time `json:"tokenExpires"`
	TokenRefreshedAt  *time.Time `json:"tokenRefreshedAt"`
	ConnectedAt       *time.Time `json:"connectedAt"`
	WebhookSubscribed bool       `json:"webhookSubscribed"`
	LastError         string     `json:"lastError"`
	token             string
}

type Service struct {
	DB            *pgxpool.Pool
	OAuth         ig.OAuth // nil＝未設定 IG_APP_ID／IG_APP_SECRET
	FallbackToken string   // env IG_ACCESS_TOKEN：沒連結帳號時的舊做法兜底
	Now           func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const cols = `id, ig_user_id, username, name, profile_pic, account_type, token_expires, token_refreshed_at,
  connected_at, webhook_subscribed, last_error, access_token`

func scan(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.IGUserID, &a.Username, &a.Name, &a.ProfilePic, &a.AccountType, &a.TokenExpires,
		&a.TokenRefreshedAt, &a.ConnectedAt, &a.WebhookSubscribed, &a.LastError, &a.token)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotConnected
	}
	return a, err
}

// Current 回目前連結中的帳號；沒有回 ErrNotConnected。
func (s *Service) Current(ctx context.Context) (Account, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM ig_accounts WHERE access_token <> ''
ORDER BY connected_at DESC NULLS LAST, id DESC LIMIT 1`))
}

// Token 給發文排程用：已連結帳號的 token，沒有就用 env 兜底。
func (s *Service) Token(ctx context.Context) (string, error) {
	a, err := s.Current(ctx)
	if errors.Is(err, ErrNotConnected) {
		if s.FallbackToken != "" {
			return s.FallbackToken, nil
		}
		return "", errors.New("尚未連結 IG 帳號（到「設定」連結）")
	}
	if err != nil {
		return "", err
	}
	if a.TokenExpires != nil && !a.TokenExpires.After(s.now()) {
		return "", errors.New("IG 授權已過期，請到「設定」重新連結")
	}
	return a.token, nil
}

// Connect 用 OAuth callback 的 code 完成連結。
func (s *Service) Connect(ctx context.Context, code string) (Account, error) {
	if s.OAuth == nil {
		return Account{}, errors.New("未設定 IG_APP_ID／IG_APP_SECRET")
	}
	short, err := s.OAuth.Exchange(ctx, code)
	if err != nil {
		return Account{}, fmt.Errorf("換取授權失敗：%w", err)
	}
	long, err := s.OAuth.LongLived(ctx, short)
	if err != nil {
		return Account{}, fmt.Errorf("換取長效授權失敗：%w", err)
	}
	info, err := s.OAuth.Me(ctx, long.Token)
	if err != nil {
		return Account{}, fmt.Errorf("讀取帳號資料失敗：%w", err)
	}
	if info.AccountType == "PERSONAL" {
		return Account{}, errors.New("這是個人帳號：請先在 IG 切換成商家或創作者帳號")
	}
	subErr := s.OAuth.Subscribe(ctx, long.Token)
	lastErr := ""
	if subErr != nil {
		lastErr = "訂閱私訊通知失敗：" + subErr.Error()
	}

	now := s.now()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx)
	// webhook 先到時已用同一個 ig_user_id 建過列，這裡合併上去，舊顧客對話不會分家
	a, err := scan(tx.QueryRow(ctx, `
INSERT INTO ig_accounts (ig_user_id, username, name, profile_pic, account_type, access_token, token_expires,
  token_refreshed_at, connected_at, webhook_subscribed, last_error)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9, $10)
ON CONFLICT (ig_user_id) DO UPDATE SET username = EXCLUDED.username, name = EXCLUDED.name,
  profile_pic = EXCLUDED.profile_pic, account_type = EXCLUDED.account_type, access_token = EXCLUDED.access_token,
  token_expires = EXCLUDED.token_expires, token_refreshed_at = EXCLUDED.token_refreshed_at,
  connected_at = EXCLUDED.connected_at, webhook_subscribed = EXCLUDED.webhook_subscribed, last_error = EXCLUDED.last_error
RETURNING `+cols, info.UserID, info.Username, info.Name, info.ProfilePic, info.AccountType, long.Token,
		now.Add(long.ExpiresIn), now, subErr == nil, lastErr))
	if err != nil {
		return Account{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ig_accounts SET access_token = '', token_expires = NULL WHERE id <> $1`, a.ID); err != nil {
		return Account{}, err
	}
	return a, tx.Commit(ctx)
}

// Disconnect 清掉 token；帳號列與顧客資料保留。
func (s *Service) Disconnect(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `UPDATE ig_accounts SET access_token = '', token_expires = NULL, webhook_subscribed = false, last_error = ''
WHERE access_token <> ''`)
	return err
}

// Refresh 立即續期目前帳號的 token。
func (s *Service) Refresh(ctx context.Context) (Account, error) {
	a, err := s.Current(ctx)
	if err != nil {
		return a, err
	}
	if err := s.refresh(ctx, a); err != nil {
		return a, err
	}
	return s.Current(ctx)
}

func (s *Service) refresh(ctx context.Context, a Account) error {
	if s.OAuth == nil {
		return errors.New("未設定 IG_APP_ID／IG_APP_SECRET")
	}
	issued := a.TokenRefreshedAt
	if issued == nil {
		issued = a.ConnectedAt
	}
	if issued != nil && s.now().Sub(*issued) < minTokenAge {
		return ErrTooSoon
	}
	long, err := s.OAuth.Refresh(ctx, a.token)
	if err != nil {
		_, _ = s.DB.Exec(ctx, `UPDATE ig_accounts SET last_error = $2 WHERE id = $1`, a.ID, "自動續期失敗："+err.Error())
		return fmt.Errorf("續期失敗：%w", err)
	}
	now := s.now()
	_, err = s.DB.Exec(ctx, `
UPDATE ig_accounts SET access_token = $2, token_expires = $3, token_refreshed_at = $4, last_error = '' WHERE id = $1`,
		a.ID, long.Token, now.Add(long.ExpiresIn), now)
	return err
}

// Resubscribe 重試訂閱私訊 webhook（連結時失敗才需要）。
func (s *Service) Resubscribe(ctx context.Context) (Account, error) {
	a, err := s.Current(ctx)
	if err != nil {
		return a, err
	}
	if s.OAuth == nil {
		return a, errors.New("未設定 IG_APP_ID／IG_APP_SECRET")
	}
	if err := s.OAuth.Subscribe(ctx, a.token); err != nil {
		return a, fmt.Errorf("訂閱私訊通知失敗：%w", err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE ig_accounts SET webhook_subscribed = true, last_error = '' WHERE id = $1`, a.ID); err != nil {
		return a, err
	}
	return s.Current(ctx)
}

// RefreshDue 續期快到期的 token；回傳是否有續。過期的不處理（只能重新連結）。
func (s *Service) RefreshDue(ctx context.Context) (bool, error) {
	a, err := s.Current(ctx)
	if errors.Is(err, ErrNotConnected) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := s.now()
	if a.TokenExpires == nil || !a.TokenExpires.After(now) || a.TokenExpires.Sub(now) > RefreshWithin {
		return false, nil
	}
	if err := s.refresh(ctx, a); err != nil {
		return false, err
	}
	return true, nil
}

// RunRefresher 每 6 小時檢查一次；失敗留在 last_error，設定頁會顯示，下一輪自動再試。
func (s *Service) RunRefresher(ctx context.Context) {
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	for {
		if ok, err := s.RefreshDue(ctx); err != nil {
			log.Printf("ig token refresh: %v", err)
		} else if ok {
			log.Print("ig token refreshed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
