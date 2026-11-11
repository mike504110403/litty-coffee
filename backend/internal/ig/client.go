package ig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Profile 是 IG 用戶的公開資料（需對方先傳過訊息）。
type Profile struct {
	Name       string `json:"name"`
	Username   string `json:"username"`
	ProfilePic string `json:"profile_pic"`
}

// Messenger 抽象 IG 送訊／查人；正式環境用 Client（graph.facebook.com，Unified Login 的 SUAT），開發期用 Fake。
// TODO：{ig-id}/messages 與用戶 profile 在 graph.facebook.com 上的行為未以真帳號驗證。
type Messenger interface {
	SendText(ctx context.Context, accountID, token, igsid, text string, humanAgent bool) (mid string, err error)
	Profile(ctx context.Context, token, igsid string) (Profile, error)
}

type Client struct {
	Base string
	HTTP *http.Client
}

func NewClient(base string) *Client {
	return &Client{Base: base, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// APIError 是 Graph API 的錯誤回應。
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("instagram api %d: %s (code %d)", e.Status, e.Message, e.Code)
	}
	return fmt.Sprintf("instagram api %d: %s", e.Status, e.Message)
}

// TokenInvalid 回這個錯誤是否表示 token 失效（code 190：過期、被撤銷、店家移除 app、帳號移到別的業務組合等）。
func (e *APIError) TokenInvalid() bool { return e.Code == 190 || e.Status == http.StatusUnauthorized }

// IsTokenInvalid 判斷任意錯誤是否為 token 失效。
func IsTokenInvalid(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.TokenInvalid()
}

func (c *Client) SendText(ctx context.Context, accountID, token, igsid, text string, humanAgent bool) (string, error) {
	body := map[string]any{
		"recipient": map[string]string{"id": igsid},
		"message":   map[string]string{"text": text},
	}
	if humanAgent {
		body["messaging_type"] = "MESSAGE_TAG"
		body["tag"] = "HUMAN_AGENT"
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/%s/messages", c.Base, url.PathEscape(accountID)), token, body, &out); err != nil {
		return "", err
	}
	return out.MessageID, nil
}

func (c *Client) Profile(ctx context.Context, token, igsid string) (Profile, error) {
	var p Profile
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%s?fields=name,username,profile_pic", c.Base, url.PathEscape(igsid)), token, nil, &p)
	return p, err
}

func (c *Client) do(ctx context.Context, method, u, token string, in, out any) error {
	var rd *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e apiError
		_ = json.NewDecoder(res.Body).Decode(&e)
		return &APIError{Status: res.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	return json.NewDecoder(res.Body).Decode(out)
}
