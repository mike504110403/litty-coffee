package ig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Instagram API with Unified Login（Facebook 登入的「配置」流程，官方建議新整合採用的設定）。
// 流程：facebook.com/dialog/oauth（帶 config_id，不帶 scope）→ code 換業務組合擁有的
// system user access token（SUAT）→ /me 取 client_business_id → /me/assigned_instagram_accounts 列出
// 這個 token 能用的 IG 專業帳號 → 逐一訂閱 webhook。
//
// SUAT 只能打 graph.facebook.com（graph.instagram.com 不行）。token 壽命在配置裡設定（預設永不過期）。
// TODO：未實作 Token Management API（POST /{client_business_id}/system_user_access_tokens）；
// 配置若設了到期日，到期前只能請店家重新連結。

// Permissions 是配置裡要勾的權限（Unified Login 不在授權網址帶 scope，這份只給設定頁／文件參考）。
var Permissions = []string{
	"instagram_basic",
	"instagram_manage_messages",
	"instagram_content_publish",
	"instagram_manage_insights",
}

// Token 是換到的存取權杖；ExpiresAt nil＝永不過期（或查不到）。
type Token struct {
	AccessToken string
	ExpiresAt   *time.Time
}

// Business 是 token 所屬的業務組合與系統用戶。
type Business struct {
	ClientBusinessID string `json:"client_business_id"`
	SystemUserID     string `json:"id"`
}

// AccountInfo 是 token 指派到的 IG 專業帳號。UserID 就是 webhook entry.id 用的那個 ID。
// name／profile_picture_url 文件沒寫，有就用、沒有也不算錯。
// TODO：Unified Login 沒有 account_type，無法擋個人帳號（個人帳號本來也指派不到業務組合）。
type AccountInfo struct {
	UserID     string `json:"id"`
	Username   string `json:"username"`
	Name       string `json:"name"`
	ProfilePic string `json:"profile_picture_url"`
}

type OAuth interface {
	AuthorizeURL(state string) string
	Exchange(ctx context.Context, code string) (Token, error)
	Business(ctx context.Context, token string) (Business, error)
	Accounts(ctx context.Context, token string) ([]AccountInfo, error)
	Account(ctx context.Context, token, igUserID string) (AccountInfo, error) // 用 token 直接讀單一帳號（手動加入時驗證）
	Subscribe(ctx context.Context, token, igUserID string) error              // 讓本 app 收到該帳號的私訊、已讀、表情 webhook
	ConfigID() string
}

type OAuthClient struct {
	AppID, AppSecret, ConfigurationID, RedirectURI string
	DialogBase                                     string // https://www.facebook.com
	APIBase                                        string // https://graph.facebook.com/v27.0
	HTTP                                           *http.Client
}

func NewOAuthClient(appID, appSecret, configID, redirectURI, apiBase string) *OAuthClient {
	return &OAuthClient{
		AppID: appID, AppSecret: appSecret, ConfigurationID: configID, RedirectURI: redirectURI,
		DialogBase: "https://www.facebook.com", APIBase: strings.TrimRight(apiBase, "/"),
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *OAuthClient) ConfigID() string { return c.ConfigurationID }

func (c *OAuthClient) AuthorizeURL(state string) string {
	q := url.Values{
		"client_id":     {c.AppID},
		"config_id":     {c.ConfigurationID},
		"redirect_uri":  {c.RedirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	return c.DialogBase + "/dialog/oauth?" + q.Encode()
}

// Exchange 用 code 換 token；回應沒有 expires_in 時再用 /debug_token 查到期日（查不到視為永不過期）。
func (c *OAuthClient) Exchange(ctx context.Context, code string) (Token, error) {
	q := url.Values{
		"client_id":     {c.AppID},
		"client_secret": {c.AppSecret},
		"redirect_uri":  {c.RedirectURI},
		"code":          {code},
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := c.get(ctx, c.APIBase+"/oauth/access_token?"+q.Encode(), "", &out); err != nil {
		return Token{}, err
	}
	if out.AccessToken == "" {
		return Token{}, errors.New("meta oauth: 回應沒有 access_token")
	}
	t := Token{AccessToken: out.AccessToken}
	if out.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
		t.ExpiresAt = &exp
		return t, nil
	}
	// 未驗證：SUAT 是否能用 debug_token 查到期日；失敗不影響連結
	if exp, err := c.TokenExpiry(ctx, out.AccessToken); err == nil {
		t.ExpiresAt = exp
	}
	return t, nil
}

// TokenExpiry 用 /debug_token（app token＝AppID|AppSecret）查到期日；expires_at=0 表示永不過期，回 nil。
func (c *OAuthClient) TokenExpiry(ctx context.Context, token string) (*time.Time, error) {
	q := url.Values{"input_token": {token}, "access_token": {c.AppID + "|" + c.AppSecret}}
	var out struct {
		Data struct {
			ExpiresAt int64 `json:"expires_at"`
			IsValid   *bool `json:"is_valid"`
		} `json:"data"`
	}
	if err := c.get(ctx, c.APIBase+"/debug_token?"+q.Encode(), "", &out); err != nil {
		return nil, err
	}
	if out.Data.IsValid != nil && !*out.Data.IsValid {
		return nil, errors.New("meta: token 已失效，請重新連結")
	}
	if out.Data.ExpiresAt <= 0 {
		return nil, nil
	}
	t := time.Unix(out.Data.ExpiresAt, 0)
	return &t, nil
}

func (c *OAuthClient) Business(ctx context.Context, token string) (Business, error) {
	var b Business
	if err := c.get(ctx, c.APIBase+"/me?fields=id,client_business_id", token, &b); err != nil {
		return b, err
	}
	log.Printf("ig oauth debug: /me → system_user=%s business=%s", b.SystemUserID, b.ClientBusinessID)
	if b.SystemUserID == "" {
		return b, errors.New("meta /me 沒有 id")
	}
	return b, nil
}

// maxAccountPages 防止分頁回應有誤時無限迴圈。
const maxAccountPages = 50

// Accounts 列出 token 指派到的全部 IG 帳號（跟著 paging.cursors.after 翻頁）。
func (c *OAuthClient) Accounts(ctx context.Context, token string) ([]AccountInfo, error) {
	out := []AccountInfo{}
	after := ""
	for page := 0; page < maxAccountPages; page++ {
		q := url.Values{"fields": {"id,username,name,profile_picture_url"}}
		if after != "" {
			q.Set("after", after)
		}
		var res struct {
			Data   []AccountInfo `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		raw, err := c.getRaw(ctx, c.APIBase+"/me/assigned_instagram_accounts?"+q.Encode(), token)
		if err != nil {
			log.Printf("ig oauth debug: assigned_instagram_accounts error: %v", err)
			return nil, err
		}
		log.Printf("ig oauth debug: assigned_instagram_accounts page %d → %s", page, raw) // 暫時除錯：不含 token
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		for _, a := range res.Data {
			if a.UserID != "" {
				out = append(out, a)
			}
		}
		next := res.Paging.Cursors.After
		if len(res.Data) == 0 || next == "" || next == after || res.Paging.Next == "" {
			return out, nil
		}
		after = next
	}
	return out, nil
}

// Account 用 token 直接讀某個 IG 帳號；讀得到＝這個 token 有權操作它。
func (c *OAuthClient) Account(ctx context.Context, token, igUserID string) (AccountInfo, error) {
	var a AccountInfo
	err := c.get(ctx, c.APIBase+"/"+url.PathEscape(igUserID)+"?fields=id,username,name,profile_picture_url", token, &a)
	if err == nil && a.UserID == "" {
		err = errors.New("meta 沒有回傳帳號 id")
	}
	return a, err
}

func (c *OAuthClient) Subscribe(ctx context.Context, token, igUserID string) error {
	u := fmt.Sprintf("%s/%s/subscribed_apps?subscribed_fields=messages,messaging_seen,message_reactions",
		c.APIBase, url.PathEscape(igUserID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	body, err := c.send(req)
	if err != nil {
		return err
	}
	var out struct {
		Success bool `json:"success"`
	}
	if json.Unmarshal(body, &out) == nil && !out.Success {
		return errors.New("meta 訂閱 webhook 回傳 success=false")
	}
	return nil
}

func (c *OAuthClient) getRaw(ctx context.Context, u, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.send(req)
}

func (c *OAuthClient) get(ctx context.Context, u, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	body, err := c.send(req)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *OAuthClient) send(req *http.Request) ([]byte, error) {
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		var e apiError
		_ = json.Unmarshal(body, &e)
		return nil, &APIError{Status: res.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	return body, nil
}

// FakeOAuth 給開發模擬器用：授權頁直接導回 callback，連上兩個模擬商家帳號。
type FakeOAuth struct {
	RedirectURI  string
	Biz          Business
	List         []AccountInfo
	Extra        []AccountInfo // 測試用：token 讀得到、但不在指派清單上的帳號（重現 Unified Login 實測行為）
	Token        Token
	SubscribeErr map[string]error // 測試用：這些帳號訂閱失敗
	Subscribed   []string
}

// 模擬器的兩個假商家帳號（webhook entry.id）。
const (
	FakeAccountCoffee   = "dev-litty-coffee"
	FakeAccountRoastery = "dev-litty-roastery"
)

func NewFakeOAuth(redirectURI string) *FakeOAuth {
	return &FakeOAuth{
		RedirectURI: redirectURI,
		Biz:         Business{ClientBusinessID: "dev-business", SystemUserID: "dev-system-user"},
		List: []AccountInfo{
			{UserID: FakeAccountCoffee, Username: "littycoffee", Name: "Litty Coffee（模擬）"},
			{UserID: FakeAccountRoastery, Username: "littyroastery", Name: "Litty Roastery（模擬）"},
		},
		Token: Token{AccessToken: "dev-suat"},
	}
}

func (f *FakeOAuth) ConfigID() string { return "dev-config" }

func (f *FakeOAuth) AuthorizeURL(state string) string {
	return f.RedirectURI + "?" + url.Values{"code": {"dev-code"}, "state": {state}}.Encode()
}

func (f *FakeOAuth) Exchange(_ context.Context, code string) (Token, error) {
	if code != "dev-code" {
		return Token{}, &APIError{Status: 400, Message: "invalid code"}
	}
	return f.Token, nil
}

func (f *FakeOAuth) Business(context.Context, string) (Business, error) { return f.Biz, nil }

func (f *FakeOAuth) Accounts(context.Context, string) ([]AccountInfo, error) {
	return append([]AccountInfo(nil), f.List...), nil
}

func (f *FakeOAuth) Account(_ context.Context, _, igUserID string) (AccountInfo, error) {
	for _, a := range append(append([]AccountInfo(nil), f.List...), f.Extra...) {
		if a.UserID == igUserID {
			return a, nil
		}
	}
	return AccountInfo{}, &APIError{Status: 400, Code: 100, Message: "Unsupported get request"}
}

func (f *FakeOAuth) Subscribe(_ context.Context, _, igUserID string) error {
	if err := f.SubscribeErr[igUserID]; err != nil {
		return err
	}
	f.Subscribed = append(f.Subscribed, igUserID)
	return nil
}
