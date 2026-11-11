package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// SystemInfo 是設定頁「串接資訊」要顯示的唯讀設定（不含任何 secret 本體）。
type SystemInfo struct {
	WebOrigin     string
	PublicBaseURL string
	Timezone      string
	APIBase       string // Graph API 位址（Unified Login：graph.facebook.com）
}

const oauthStateCookie = "litty_ig_oauth_state"

func (s *Server) settingsRoutes(r chi.Router) {
	r.Get("/settings", s.getSettings)
	// 連線層級（只限可見全部帳號的人）
	r.Get("/ig/oauth/start", s.igOAuthStart)
	r.Get("/ig/oauth/callback", s.igOAuthCallback)
	r.Post("/ig/connections/{id}/refresh", s.igConnRefresh)
	r.Post("/ig/connections/{id}/accounts", s.igConnAddAccount)
	r.Delete("/ig/connections/{id}", s.igConnDisconnect)
	// 帳號層級（須在可見範圍內）
	r.Post("/ig/accounts/{id}/active", s.igAccountActive)
	r.Post("/ig/accounts/{id}/subscribe", s.igAccountResubscribe)
	// /auth/password（所有登入者可用）、GET /ig/accounts（所有登入者可用）在 router.go 註冊
}

func (s *Server) redirectURI() string { return s.Info.WebOrigin + "/api/ig/oauth/callback" }

// visibleAccounts 回登入者看得到的帳號。
func (s *Server) visibleAccounts(r *http.Request) ([]account.Account, error) {
	all, err := s.Accounts.List(r.Context())
	if err != nil {
		return nil, err
	}
	out := []account.Account{}
	for _, a := range all {
		if canSee(r, a.ID) {
			out = append(out, a)
		}
	}
	return out, nil
}

// listAccounts：所有登入者可用（收件匣篩選、自動回覆／排程的帳號選擇器），只回可見帳號。
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.visibleAccounts(r)
	if err != nil {
		log.Printf("accounts: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.visibleAccounts(r)
	if err != nil {
		log.Printf("settings: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	manage := currentUser(r).AllAccounts()
	conns := []account.Connection{}
	if manage {
		if conns, err = s.Accounts.Connections(r.Context()); err != nil {
			log.Printf("settings: %v", err)
			writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts":          accounts,
		"connections":       conns,
		"manageConnections": manage,
		"integration": map[string]any{
			"oauthReady":    s.Accounts.OAuthReady(),
			"webhookUrl":    s.Info.PublicBaseURL + "/webhooks/instagram",
			"redirectUri":   s.redirectURI(),
			"publicBaseUrl": s.Info.PublicBaseURL,
			"envTokenSet":   s.Accounts.FallbackToken != "",
			"simulator":     s.Fake != nil,
			"permissions":   ig.Permissions,
			"apiBase":       s.Info.APIBase,
		},
		"timezone": s.Info.Timezone,
	})
}

func (s *Server) settingsRedirect(w http.ResponseWriter, r *http.Request, key, val string) {
	http.Redirect(w, r, s.Info.WebOrigin+"/settings?"+url.Values{key: {val}}.Encode(), http.StatusFound)
}

func (s *Server) setStateCookie(w http.ResponseWriter, val string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: val, Path: "/api/ig/oauth", MaxAge: maxAge,
		HttpOnly: true, Secure: s.Auth.Secure, SameSite: http.SameSiteLaxMode})
}

// igOAuthStart 由前端整頁導向；產生 state 放 cookie 防 CSRF，再轉去 Facebook 登入（Unified Login）授權頁。
func (s *Server) igOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !currentUser(r).AllAccounts() {
		s.settingsRedirect(w, r, "ig_error", "只有可見全部 IG 帳號的管理員能連結帳號")
		return
	}
	if s.Accounts.OAuth == nil {
		s.settingsRedirect(w, r, "ig_error", account.ErrNotConfigured.Error())
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)
	s.setStateCookie(w, state, 600)
	http.Redirect(w, r, s.Accounts.OAuth.AuthorizeURL(state), http.StatusFound)
}

func (s *Server) igOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := r.Cookie(oauthStateCookie)
	s.setStateCookie(w, "", -1)
	fail := func(msg string) {
		s.recordIGConnect(r, audit.ActionIGConnectError, "", map[string]any{"error": audit.Truncate(msg)})
		s.settingsRedirect(w, r, "ig_error", msg)
	}
	if !currentUser(r).AllAccounts() {
		fail("只有可見全部 IG 帳號的管理員能連結帳號")
		return
	}
	if q.Get("error") != "" {
		msg := "已取消授權"
		if q.Get("error_reason") != "user_denied" {
			msg = "授權失敗：" + q.Get("error_description")
		}
		fail(msg)
		return
	}
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		fail("授權連結已失效，請重新按「連結 IG 帳號」")
		return
	}
	conn, accs, err := s.Accounts.Connect(r.Context(), q.Get("code"))
	if err != nil {
		log.Printf("ig oauth: %v", err)
		fail(err.Error())
		return
	}
	names := make([]string, 0, len(accs))
	for _, a := range accs {
		log.Printf("ig account connected: @%s (%s) via connection %d", a.Username, a.IGUserID, conn.ID)
		names = append(names, "@"+a.Username)
	}
	s.recordIGConnect(r, audit.ActionIGConnect, strconv.FormatInt(conn.ID, 10), map[string]any{"accounts": names})
	s.settingsRedirect(w, r, "ig", "connected")
}

// recordIGConnect 記 OAuth callback 的連結結果（GET 不經異動 middleware）。
func (s *Server) recordIGConnect(r *http.Request, action, connID string, detail map[string]any) {
	u := currentUser(r)
	detail["username"] = u.Username
	status := http.StatusFound
	s.recordAuth(r, audit.Entry{AdminID: u.ID, AdminName: u.DisplayName, Action: action, TargetType: "connection",
		TargetID: connID, Detail: detail, Status: status, Success: action == audit.ActionIGConnect})
}

func (s *Server) accountErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrNotFound), errors.Is(err, account.ErrConnNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, account.ErrNotConnected), errors.Is(err, account.ErrNotConfigured):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) igConnRefresh(w http.ResponseWriter, r *http.Request) {
	if !requireAllAccounts(w, r) {
		return
	}
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	c, err := s.Accounts.RefreshConnection(r.Context(), id)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// igConnAddAccount：Meta 沒列出帳號時，店長手動輸入 IG 帳號編號加入連線。
func (s *Server) igConnAddAccount(w http.ResponseWriter, r *http.Request) {
	if !requireAllAccounts(w, r) {
		return
	}
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		IGUserID string `json:"igUserId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	a, err := s.Accounts.AddAccount(r.Context(), id, in.IGUserID)
	switch {
	case errors.Is(err, account.ErrBadIGID):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.accountErr(w, err)
	default:
		log.Printf("ig account added manually: @%s (%s) via connection %d", a.Username, a.IGUserID, id)
		writeJSON(w, http.StatusOK, a)
	}
}

func (s *Server) igConnDisconnect(w http.ResponseWriter, r *http.Request) {
	if !requireAllAccounts(w, r) {
		return
	}
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.Accounts.Disconnect(r.Context(), id); err != nil {
		if errors.Is(err, account.ErrConnNotFound) {
			s.accountErr(w, err)
			return
		}
		log.Printf("ig disconnect: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scopedAccountID 解析 {id}；不在可見範圍內回 404。
func scopedAccountID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return 0, false
	}
	if !canSee(r, id) {
		writeErr(w, http.StatusNotFound, account.ErrNotFound.Error())
		return 0, false
	}
	return id, true
}

func (s *Server) igAccountActive(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedAccountID(w, r)
	if !ok {
		return
	}
	var in struct {
		Active *bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Active == nil {
		writeErr(w, http.StatusBadRequest, "請指定 active")
		return
	}
	a, err := s.Accounts.SetActive(r.Context(), id, *in.Active)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) igAccountResubscribe(w http.ResponseWriter, r *http.Request) {
	id, ok := scopedAccountID(w, r)
	if !ok {
		return
	}
	a, err := s.Accounts.Resubscribe(r.Context(), id)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct{ Current, Next string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	err := s.Auth.ChangePassword(r.Context(), auth.UserID(r.Context()), in.Current, in.Next)
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		writeErr(w, http.StatusBadRequest, "目前密碼不正確")
	case errors.Is(err, auth.ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		log.Printf("change password: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
