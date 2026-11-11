package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/appsettings"
)

// 後台「憑證」設定：IG Meta App 與 Claude AI。DB 值（加密）優先於 .env，存檔即生效。
// 權限由外層另行包裝；這裡只做資料面。

// credentialNames 是 API 的欄位名稱 ↔ appsettings key。
var credentialNames = map[string]string{
	"igAppId":         appsettings.KeyIGAppID,
	"igAppSecret":     appsettings.KeyIGAppSecret,
	"igConfigId":      appsettings.KeyIGConfigID,
	"igVerifyToken":   appsettings.KeyIGVerifyToken,
	"anthropicApiKey": appsettings.KeyAnthropicKey,
	"aiModel":         appsettings.KeyAIModel,
	"aiEffort":        appsettings.KeyAIEffort,
}

func credentialName(key string) string {
	for n, k := range credentialNames {
		if k == key {
			return n
		}
	}
	return key
}

func (s *Server) credentialsRoutes(r chi.Router) {
	r.Get("/settings/credentials", s.getCredentials)
	r.Put("/settings/credentials", s.putCredentials)
	r.Post("/settings/credentials/test-ai", s.testAICredential)
}

type credentialView struct {
	Set    bool   `json:"set"`
	Source string `json:"source"`          // db | env | none
	Hint   string `json:"hint"`            // 尾 4 碼（值不足 8 碼時為空）
	Value  string `json:"value,omitempty"` // 只有非 secret 欄位才帶全文
}

func (s *Server) credentialsPayload() map[string]credentialView {
	out := map[string]credentialView{}
	for _, e := range s.Creds.Entries() {
		v := credentialView{Set: e.Set(), Source: e.Source, Hint: e.Hint()}
		if !e.Secret {
			v.Value = e.Value
		}
		out[credentialName(e.Key)] = v
	}
	return out
}

func (s *Server) getCredentials(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.credentialsPayload())
}

// putCredentials：只更新有送的欄位；空字串＝不變；clear 列出要刪除 DB 值（退回 .env）的欄位。
func (s *Server) putCredentials(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	set := map[string]string{}
	var clear []string
	for name, msg := range raw {
		if name == "clear" {
			var names []string
			if err := json.Unmarshal(msg, &names); err != nil {
				writeErr(w, http.StatusBadRequest, "clear 應為欄位名稱陣列")
				return
			}
			for _, n := range names {
				key, ok := credentialNames[n]
				if !ok {
					writeErr(w, http.StatusBadRequest, "未知欄位："+n)
					return
				}
				clear = append(clear, key)
			}
			continue
		}
		key, ok := credentialNames[name]
		if !ok {
			writeErr(w, http.StatusBadRequest, "未知欄位："+name)
			return
		}
		var v string
		if err := json.Unmarshal(msg, &v); err != nil {
			writeErr(w, http.StatusBadRequest, name+" 應為字串")
			return
		}
		set[key] = v
	}
	if err := s.Creds.Update(r.Context(), set, clear); err != nil {
		var inv *appsettings.ErrInvalid
		if errors.As(err, &inv) {
			writeErr(w, http.StatusBadRequest, inv.Msg)
			return
		}
		log.Printf("credentials update: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	writeJSON(w, http.StatusOK, s.credentialsPayload())
}

// testAICredential 用 Models List API 驗證 Claude API key（唯讀、不計費）。
// body 可帶 {"apiKey": "..."} 先測尚未儲存的 key；省略則測目前生效的 key。
// 不論測試成功與否都回 200，結果在 {ok, error}；只有 body 壞掉才 400。
func (s *Server) testAICredential(w http.ResponseWriter, r *http.Request) {
	var in struct {
		APIKey string `json:"apiKey"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	key := strings.TrimSpace(in.APIKey)
	if key == "" {
		key = s.Creds.Values().AnthropicKey
	}
	if key == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "尚未設定 Claude API Key"})
		return
	}
	if err := pingAnthropic(r.Context(), key, s.AnthropicBaseURL); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pingAnthropic 呼叫 client.Models.List（GET /v1/models，limit=1）。baseURL 空＝官方 API。
func pingAnthropic(ctx context.Context, apiKey, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithMaxRetries(0)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := anthropic.NewClient(opts...)
	_, err := client.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1)})
	if err == nil {
		return nil
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		msg := apiErrMessage(apiErr)
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("API Key 無效或已停用（401）%s", msg)
		case http.StatusForbidden:
			return fmt.Errorf("API Key 沒有權限（403）%s", msg)
		}
		return fmt.Errorf("Claude API 回應 %d%s", apiErr.StatusCode, msg)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("連線逾時：連不上 Claude API")
	}
	return fmt.Errorf("連線失敗：%v", err)
}

// apiErrMessage 取出 Anthropic 錯誤本文的 error.message，格式化成「：訊息」；拿不到回空字串。
func apiErrMessage(e *anthropic.Error) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(e.RawJSON()), &body) == nil && body.Error.Message != "" {
		return "：" + body.Error.Message
	}
	return ""
}
