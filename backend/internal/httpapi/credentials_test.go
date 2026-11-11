package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/appsettings"
	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
)

func credServer(t *testing.T, env appsettings.Values) (*Server, http.Handler) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE app_settings, webhook_events`); err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.FromKey("test-key")
	creds := appsettings.New(pool, box, env)
	if err := creds.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: pool, Creds: creds, Inbox: inbox.NewService(pool, ig.NewFake(), realtime.NewHub())}
	r := chi.NewRouter()
	s.credentialsRoutes(r)
	r.Get("/webhooks/instagram", s.webhookVerify)
	r.Post("/webhooks/instagram", s.webhookReceive)
	return s, r
}

func do(h http.Handler, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type view struct {
	Set    bool   `json:"set"`
	Source string `json:"source"`
	Hint   string `json:"hint"`
	Value  string `json:"value"`
}

func getCreds(t *testing.T, h http.Handler) map[string]view {
	t.Helper()
	w := do(h, "GET", "/settings/credentials", "")
	if w.Code != 200 {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	var m map[string]view
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCredentialsGetPutClear(t *testing.T) {
	_, h := credServer(t, appsettings.Values{IGAppID: "111", AIModel: "env-model", AIEffort: "low"})

	m := getCreds(t, h)
	if v := m["igAppId"]; !v.Set || v.Source != "env" || v.Value != "111" {
		t.Fatalf("igAppId: %+v", v)
	}
	if v := m["anthropicApiKey"]; v.Set || v.Source != "none" {
		t.Fatalf("anthropicApiKey: %+v", v)
	}

	w := do(h, "PUT", "/settings/credentials",
		`{"igAppId":"222","igAppSecret":"super-secret-value-9876","anthropicApiKey":"sk-ant-api03-zzzz5555","aiModel":""}`)
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	// 回應與 GET 都不得含 secret 全文
	for _, body := range []string{w.Body.String(), do(h, "GET", "/settings/credentials", "").Body.String()} {
		if strings.Contains(body, "super-secret-value") || strings.Contains(body, "sk-ant-api03") {
			t.Fatalf("洩漏 secret 全文: %s", body)
		}
	}
	m = getCreds(t, h)
	if v := m["igAppSecret"]; !v.Set || v.Source != "db" || v.Hint != "9876" || v.Value != "" {
		t.Fatalf("igAppSecret: %+v", v)
	}
	if v := m["igAppId"]; v.Source != "db" || v.Value != "222" {
		t.Fatalf("igAppId: %+v", v)
	}
	if v := m["aiModel"]; v.Source != "env" || v.Value != "env-model" {
		t.Fatalf("空字串應不變: %+v", v)
	}

	// clear → 退回 env / none
	if w := do(h, "PUT", "/settings/credentials", `{"clear":["igAppId","anthropicApiKey"]}`); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	m = getCreds(t, h)
	if v := m["igAppId"]; v.Source != "env" || v.Value != "111" {
		t.Fatalf("clear 後 igAppId: %+v", v)
	}
	if v := m["anthropicApiKey"]; v.Set {
		t.Fatalf("clear 後 key: %+v", v)
	}
	if v := m["igAppSecret"]; v.Source != "db" {
		t.Fatal("沒被 clear 的欄位應保留")
	}
}

func TestCredentialsPutValidation(t *testing.T) {
	_, h := credServer(t, appsettings.Values{})
	for _, body := range []string{
		`{"igAppId":"abc"}`, `{"aiEffort":"max"}`, `{"nope":"x"}`, `{"clear":["nope"]}`,
		`{"igAppId":123}`, `{"clear":"igAppId"}`, `not json`, `{"igAppId":"5","clear":["igAppId"]}`,
	} {
		if w := do(h, "PUT", "/settings/credentials", body); w.Code != 400 {
			t.Errorf("%s: 應 400, got %d %s", body, w.Code, w.Body)
		}
	}
	if w := do(h, "PUT", "/settings/credentials", `{"aiEffort":"high","aiModel":"claude-x"}`); w.Code != 200 {
		t.Fatalf("合法: %d %s", w.Code, w.Body)
	}
}

// 存檔後 webhook 驗證立即用新值，舊值失效。
func TestWebhookUsesLiveCredentials(t *testing.T) {
	_, h := credServer(t, appsettings.Values{IGAppSecret: "old-secret-1234", IGVerifyToken: "old-token"})
	sign := func(secret string, body string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	payload := `{"object":"instagram","entry":[]}`
	post := func(secret string) int {
		return do(h, "POST", "/webhooks/instagram", payload, "X-Hub-Signature-256", sign(secret, payload)).Code
	}
	verify := func(tok string) int {
		return do(h, "GET", "/webhooks/instagram?hub.mode=subscribe&hub.challenge=abc&hub.verify_token="+tok, "").Code
	}
	if post("old-secret-1234") != 200 || verify("old-token") != 200 {
		t.Fatal("env 值應可用")
	}
	if w := do(h, "PUT", "/settings/credentials", `{"igAppSecret":"new-secret-5678","igVerifyToken":"new-token"}`); w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if post("old-secret-1234") != 401 || verify("old-token") != 403 {
		t.Fatal("存檔後舊值應失效")
	}
	if post("new-secret-5678") != 200 || verify("new-token") != 200 {
		t.Fatal("存檔後新值應立即生效")
	}
	// 清掉 DB 值 → 退回 env
	_ = do(h, "PUT", "/settings/credentials", `{"clear":["igAppSecret","igVerifyToken"]}`)
	if post("old-secret-1234") != 200 || verify("old-token") != 200 {
		t.Fatal("clear 後應退回 env")
	}
}

func TestTestAICredential(t *testing.T) {
	var gotKey, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 此版 SDK 的 WithAPIKey 以 Authorization: Bearer 送出；兩種都接受
		gotKey, gotPath = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.URL.Path
		if gotKey == "" {
			gotKey = r.Header.Get("X-Api-Key")
		}
		w.Header().Set("Content-Type", "application/json")
		if gotKey != "good-key-0000" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"has_more":false,"first_id":null,"last_id":null}`))
	}))
	defer upstream.Close()

	s, h := credServer(t, appsettings.Values{})
	s.AnthropicBaseURL = upstream.URL

	post := func(body string) map[string]any {
		w := do(h, "POST", "/settings/credentials/test-ai", body)
		if w.Code != 200 {
			t.Fatalf("code %d %s", w.Code, w.Body)
		}
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	if m := post(""); m["ok"] != false || !strings.Contains(m["error"].(string), "尚未設定") {
		t.Fatalf("沒 key: %v", m)
	}
	if m := post(`{"apiKey":"bad-key"}`); m["ok"] != false || !strings.Contains(m["error"].(string), "401") ||
		!strings.Contains(m["error"].(string), "invalid x-api-key") {
		t.Fatalf("壞 key: %v", m)
	}
	if m := post(`{"apiKey":"good-key-0000"}`); m["ok"] != true || gotPath != "/v1/models" {
		t.Fatalf("好 key: %v path=%s", m, gotPath)
	}
	// 不帶 body：測目前生效的 key
	_ = do(h, "PUT", "/settings/credentials", `{"anthropicApiKey":"good-key-0000"}`)
	if m := post(""); m["ok"] != true || gotKey != "good-key-0000" {
		t.Fatalf("用已存 key: %v", m)
	}
	// 連不上
	upstream.Close()
	if m := post(`{"apiKey":"good-key-0000"}`); m["ok"] != false {
		t.Fatalf("連不上應失敗: %v", m)
	}
}
