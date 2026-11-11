package ig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Business Login for Instagram（Instagram API with Instagram Login）。
// 流程：authorize → code 換短效 token → 換 60 天長效 token → 每次續期再 60 天（需 token 已滿 24 小時）。

// Scopes 是後台需要的權限：基本資料、私訊、發文、成效數據。
var Scopes = []string{
	"instagram_business_basic",
	"instagram_business_manage_messages",
	"instagram_business_content_publish",
	"instagram_business_manage_insights",
}

// AccountInfo 是 /me 回傳的商家帳號資料。UserID 是 webhook entry.id 用的那個 ID。
type AccountInfo struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	ProfilePic  string `json:"profile_picture_url"`
	AccountType string `json:"account_type"`
}

type LongToken struct {
	Token     string
	ExpiresIn time.Duration
}

type OAuth interface {
	AuthorizeURL(state string) string
	Exchange(ctx context.Context, code string) (shortToken string, err error)
	LongLived(ctx context.Context, shortToken string) (LongToken, error)
	Refresh(ctx context.Context, token string) (LongToken, error)
	Me(ctx context.Context, token string) (AccountInfo, error)
	Subscribe(ctx context.Context, token string) error // 讓本 app 收到該帳號的私訊、已讀、表情 webhook
}

type OAuthClient struct {
	AppID, AppSecret, RedirectURI string
	AuthBase                      string // https://www.instagram.com
	TokenBase                     string // https://api.instagram.com
	GraphBase                     string // https://graph.instagram.com（不帶版本，換長效／續期用）
	APIBase                       string // https://graph.instagram.com/v23.0
	HTTP                          *http.Client
}

func NewOAuthClient(appID, appSecret, redirectURI, apiBase string) *OAuthClient {
	return &OAuthClient{
		AppID: appID, AppSecret: appSecret, RedirectURI: redirectURI,
		AuthBase: "https://www.instagram.com", TokenBase: "https://api.instagram.com",
		GraphBase: "https://graph.instagram.com", APIBase: apiBase,
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *OAuthClient) AuthorizeURL(state string) string {
	q := url.Values{
		"client_id":     {c.AppID},
		"redirect_uri":  {c.RedirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(Scopes, ",")},
		"state":         {state},
	}
	return c.AuthBase + "/oauth/authorize?" + q.Encode()
}

func (c *OAuthClient) Exchange(ctx context.Context, code string) (string, error) {
	code = strings.TrimSuffix(code, "#_") // IG 會在 redirect 的 code 後面黏 #_
	form := url.Values{
		"client_id":     {c.AppID},
		"client_secret": {c.AppSecret},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {c.RedirectURI},
		"code":          {code},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenBase+"/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.send(req)
	if err != nil {
		return "", err
	}
	return parseShortToken(body)
}

// parseShortToken 同時接受 {"data":[{access_token}]} 與扁平 {access_token} 兩種回應形狀（官方文件兩種都出現過）。
func parseShortToken(body []byte) (string, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		Data        []struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken != "" {
		return out.AccessToken, nil
	}
	if len(out.Data) > 0 && out.Data[0].AccessToken != "" {
		return out.Data[0].AccessToken, nil
	}
	return "", errors.New("instagram oauth: 回應沒有 access_token")
}

func (c *OAuthClient) LongLived(ctx context.Context, short string) (LongToken, error) {
	q := url.Values{"grant_type": {"ig_exchange_token"}, "client_secret": {c.AppSecret}, "access_token": {short}}
	return c.longToken(ctx, c.GraphBase+"/access_token?"+q.Encode())
}

func (c *OAuthClient) Refresh(ctx context.Context, token string) (LongToken, error) {
	q := url.Values{"grant_type": {"ig_refresh_token"}, "access_token": {token}}
	return c.longToken(ctx, c.GraphBase+"/refresh_access_token?"+q.Encode())
}

func (c *OAuthClient) longToken(ctx context.Context, u string) (LongToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return LongToken{}, err
	}
	body, err := c.send(req)
	if err != nil {
		return LongToken{}, err
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return LongToken{}, err
	}
	if out.AccessToken == "" {
		return LongToken{}, errors.New("instagram oauth: 回應沒有 access_token")
	}
	return LongToken{Token: out.AccessToken, ExpiresIn: time.Duration(out.ExpiresIn) * time.Second}, nil
}

func (c *OAuthClient) Me(ctx context.Context, token string) (AccountInfo, error) {
	var a AccountInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.APIBase+"/me?fields=user_id,username,name,profile_picture_url,account_type", nil)
	if err != nil {
		return a, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	body, err := c.send(req)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return a, err
	}
	if a.UserID == "" {
		return a, errors.New("instagram /me 沒有 user_id")
	}
	return a, nil
}

func (c *OAuthClient) Subscribe(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.APIBase+"/me/subscribed_apps?subscribed_fields=messages,messaging_seen,message_reactions", nil)
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
		return errors.New("instagram 訂閱 webhook 回傳 success=false")
	}
	return nil
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
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
			ErrorMessage string `json:"error_message"` // api.instagram.com 的錯誤形狀
		}
		_ = json.Unmarshal(body, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = e.ErrorMessage
		}
		return nil, fmt.Errorf("instagram api %d: %s", res.StatusCode, msg)
	}
	return body, nil
}

// FakeOAuth 給開發模擬器用：授權頁直接導回 callback，連上的帳號就是模擬器的假商家帳號。
type FakeOAuth struct {
	RedirectURI string
	Account     AccountInfo
	FailRefresh bool
	Refreshed   int
}

func NewFakeOAuth(redirectURI, accountID string) *FakeOAuth {
	return &FakeOAuth{RedirectURI: redirectURI, Account: AccountInfo{
		UserID: accountID, Username: "littycoffee", Name: "Litty Coffee（模擬）", AccountType: "BUSINESS",
	}}
}

func (f *FakeOAuth) AuthorizeURL(state string) string {
	return f.RedirectURI + "?" + url.Values{"code": {"dev-code#_"}, "state": {state}}.Encode()
}

func (f *FakeOAuth) Exchange(_ context.Context, code string) (string, error) {
	if strings.TrimSuffix(code, "#_") != "dev-code" {
		return "", errors.New("instagram api 400: invalid code")
	}
	return "dev-short", nil
}

func (f *FakeOAuth) LongLived(context.Context, string) (LongToken, error) {
	return LongToken{Token: "dev-long", ExpiresIn: 60 * 24 * time.Hour}, nil
}

func (f *FakeOAuth) Refresh(context.Context, string) (LongToken, error) {
	if f.FailRefresh {
		return LongToken{}, errors.New("instagram api 400: token expired")
	}
	f.Refreshed++
	return LongToken{Token: fmt.Sprintf("dev-long-r%d", f.Refreshed), ExpiresIn: 60 * 24 * time.Hour}, nil
}

func (f *FakeOAuth) Me(context.Context, string) (AccountInfo, error) { return f.Account, nil }
func (f *FakeOAuth) Subscribe(context.Context, string) error         { return nil }
