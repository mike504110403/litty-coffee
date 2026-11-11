package appsettings

import (
	"context"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// LiveOAuth 是 ig.OAuth 的「活」實作：每次呼叫都依 Store 目前的 Meta App ID／Secret／配置 ID 取得 client，
// 設定頁一存檔下一次呼叫就用新憑證，不用重啟，也不用換掉 account.Service.OAuth。
//
// 並行安全：憑證從 Store 快照讀（RWMutex）；建好的 client 以 atomic.Pointer 快取，
// 憑證沒變就重用，變了就重建並 CAS 覆蓋（重建是冪等的，多個 goroutine 同時重建只是白做）。
// 已建出的 *ig.OAuthClient 建好後不再修改，所以舊 client 還在飛行中的請求不受影響。
type LiveOAuth struct {
	Store       *Store
	RedirectURI string
	APIBase     string // https://graph.facebook.com/v27.0
	DialogBase  string // 空＝https://www.facebook.com（測試可換成 httptest）
	// SettingsErrorURL 是尚未設定憑證時 AuthorizeURL 回傳的網址（導回設定頁並顯示錯誤），
	// 讓既有的 igOAuthStart 流程在沒憑證時也有合理行為。
	SettingsErrorURL string

	cur atomic.Pointer[liveClient]
}

type liveClient struct {
	id, secret, config string
	c                  *ig.OAuthClient
}

var _ ig.OAuth = (*LiveOAuth)(nil)

// Configured 回目前是否 App ID、App Secret、配置 ID 都有。
func (l *LiveOAuth) Configured() bool { return l.client() != nil }

func (l *LiveOAuth) client() *ig.OAuthClient {
	v := l.Store.Values()
	if v.IGAppID == "" || v.IGAppSecret == "" || v.IGConfigID == "" {
		return nil
	}
	if cur := l.cur.Load(); cur != nil && cur.id == v.IGAppID && cur.secret == v.IGAppSecret && cur.config == v.IGConfigID {
		return cur.c
	}
	c := ig.NewOAuthClient(v.IGAppID, v.IGAppSecret, v.IGConfigID, l.RedirectURI, l.APIBase)
	if l.DialogBase != "" {
		c.DialogBase = l.DialogBase
	}
	n := &liveClient{id: v.IGAppID, secret: v.IGAppSecret, config: v.IGConfigID, c: c}
	l.cur.Store(n)
	return n.c
}

func (l *LiveOAuth) ConfigID() string {
	if c := l.client(); c != nil {
		return c.ConfigID()
	}
	return ""
}

func (l *LiveOAuth) AuthorizeURL(state string) string {
	c := l.client()
	if c == nil {
		if l.SettingsErrorURL != "" {
			return l.SettingsErrorURL + "?" + url.Values{"ig_error": {ErrNotConfigured.Error()}}.Encode()
		}
		return "/"
	}
	return c.AuthorizeURL(state)
}

func (l *LiveOAuth) Exchange(ctx context.Context, code string) (ig.Token, error) {
	c := l.client()
	if c == nil {
		return ig.Token{}, ErrNotConfigured
	}
	return c.Exchange(ctx, code)
}

func (l *LiveOAuth) Business(ctx context.Context, token string) (ig.Business, error) {
	c := l.client()
	if c == nil {
		return ig.Business{}, ErrNotConfigured
	}
	return c.Business(ctx, token)
}

func (l *LiveOAuth) Accounts(ctx context.Context, token string) ([]ig.AccountInfo, error) {
	c := l.client()
	if c == nil {
		return nil, ErrNotConfigured
	}
	return c.Accounts(ctx, token)
}

func (l *LiveOAuth) Subscribe(ctx context.Context, token, igUserID string) error {
	c := l.client()
	if c == nil {
		return ErrNotConfigured
	}
	return c.Subscribe(ctx, token, igUserID)
}

// TokenExpiry 轉給目前的 client（/debug_token）；未設定時回錯誤。
func (l *LiveOAuth) TokenExpiry(ctx context.Context, token string) (*time.Time, error) {
	c := l.client()
	if c == nil {
		return nil, ErrNotConfigured
	}
	return c.TokenExpiry(ctx, token)
}
