package appsettings

import (
	"context"
	"net/url"
	"sync/atomic"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// LiveOAuth 是 ig.OAuth 的「活」實作：每次呼叫都依 Store 目前的 App ID／Secret 取得 client，
// 設定頁一存檔下一次呼叫就用新憑證，不用重啟，也不用換掉 account.Service.OAuth。
//
// 並行安全：憑證從 Store 快照讀（RWMutex）；建好的 client 以 atomic.Pointer 快取，
// 憑證沒變就重用，變了就重建並 CAS 覆蓋（重建是冪等的，多個 goroutine 同時重建只是白做）。
// 已建出的 *ig.OAuthClient 建好後不再修改，所以舊 client 還在飛行中的請求不受影響。
type LiveOAuth struct {
	Store       *Store
	RedirectURI string
	APIBase     string
	// SettingsErrorURL 是尚未設定憑證時 AuthorizeURL 回傳的網址（導回設定頁並顯示錯誤），
	// 讓既有的 igOAuthStart 流程在沒憑證時也有合理行為。
	SettingsErrorURL string

	cur atomic.Pointer[liveClient]
}

type liveClient struct {
	id, secret string
	c          *ig.OAuthClient
}

var _ ig.OAuth = (*LiveOAuth)(nil)

// Configured 回目前是否同時有 App ID 與 App Secret。
func (l *LiveOAuth) Configured() bool { return l.client() != nil }

func (l *LiveOAuth) client() *ig.OAuthClient {
	v := l.Store.Values()
	if v.IGAppID == "" || v.IGAppSecret == "" {
		return nil
	}
	if cur := l.cur.Load(); cur != nil && cur.id == v.IGAppID && cur.secret == v.IGAppSecret {
		return cur.c
	}
	n := &liveClient{id: v.IGAppID, secret: v.IGAppSecret,
		c: ig.NewOAuthClient(v.IGAppID, v.IGAppSecret, l.RedirectURI, l.APIBase)}
	l.cur.Store(n)
	return n.c
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

func (l *LiveOAuth) Exchange(ctx context.Context, code string) (string, error) {
	c := l.client()
	if c == nil {
		return "", ErrNotConfigured
	}
	return c.Exchange(ctx, code)
}

func (l *LiveOAuth) LongLived(ctx context.Context, short string) (ig.LongToken, error) {
	c := l.client()
	if c == nil {
		return ig.LongToken{}, ErrNotConfigured
	}
	return c.LongLived(ctx, short)
}

func (l *LiveOAuth) Refresh(ctx context.Context, token string) (ig.LongToken, error) {
	c := l.client()
	if c == nil {
		return ig.LongToken{}, ErrNotConfigured
	}
	return c.Refresh(ctx, token)
}

func (l *LiveOAuth) Me(ctx context.Context, token string) (ig.AccountInfo, error) {
	c := l.client()
	if c == nil {
		return ig.AccountInfo{}, ErrNotConfigured
	}
	return c.Me(ctx, token)
}

func (l *LiveOAuth) Subscribe(ctx context.Context, token string) error {
	c := l.client()
	if c == nil {
		return ErrNotConfigured
	}
	return c.Subscribe(ctx, token)
}
