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

	"github.com/go-chi/chi/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
)

// SystemInfo 是設定頁「串接資訊」要顯示的唯讀設定（不含任何 secret 本體）。
type SystemInfo struct {
	WebOrigin     string
	PublicBaseURL string
	IGAppIDSet    bool
	IGSecretSet   bool
	AIKeySet      bool
	Timezone      string
}

const oauthStateCookie = "litty_ig_oauth_state"

func (s *Server) settingsRoutes(r chi.Router) {
	r.Get("/settings", s.getSettings)
	r.Get("/ig/oauth/start", s.igOAuthStart)
	r.Get("/ig/oauth/callback", s.igOAuthCallback)
	r.Post("/ig/account/refresh", s.igRefresh)
	r.Post("/ig/account/subscribe", s.igResubscribe)
	r.Delete("/ig/account", s.igDisconnect)
	r.Post("/auth/password", s.changePassword)
}

func (s *Server) redirectURI() string { return s.Info.WebOrigin + "/api/ig/oauth/callback" }

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	var acc *account.Account
	a, err := s.Accounts.Current(r.Context())
	switch {
	case err == nil:
		acc = &a
	case !errors.Is(err, account.ErrNotConnected):
		log.Printf("settings: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": acc,
		"integration": map[string]any{
			"oauthReady":    s.Accounts.OAuth != nil,
			"appIdSet":      s.Info.IGAppIDSet,
			"appSecretSet":  s.Info.IGSecretSet,
			"verifyToken":   s.IGVerifyToken,
			"webhookUrl":    s.Info.PublicBaseURL + "/webhooks/instagram",
			"redirectUri":   s.redirectURI(),
			"publicBaseUrl": s.Info.PublicBaseURL,
			"envTokenSet":   s.Accounts.FallbackToken != "",
			"simulator":     s.Fake != nil,
		},
		"ai":       map[string]any{"enabled": s.Info.AIKeySet, "model": s.AIModel},
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

// igOAuthStart 由前端整頁導向；產生 state 放 cookie 防 CSRF，再轉去 IG 授權頁。
func (s *Server) igOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.Accounts.OAuth == nil {
		s.settingsRedirect(w, r, "ig_error", "尚未設定 IG_APP_ID／IG_APP_SECRET，無法連結")
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
	if q.Get("error") != "" {
		msg := "已取消授權"
		if q.Get("error_reason") != "user_denied" {
			msg = "IG 授權失敗：" + q.Get("error_description")
		}
		s.settingsRedirect(w, r, "ig_error", msg)
		return
	}
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		s.settingsRedirect(w, r, "ig_error", "授權連結已失效，請重新按「連結 IG 帳號」")
		return
	}
	a, err := s.Accounts.Connect(r.Context(), q.Get("code"))
	if err != nil {
		log.Printf("ig oauth: %v", err)
		s.settingsRedirect(w, r, "ig_error", err.Error())
		return
	}
	log.Printf("ig account connected: @%s (%s)", a.Username, a.IGUserID)
	s.settingsRedirect(w, r, "ig", "connected")
}

func (s *Server) accountResult(w http.ResponseWriter, a account.Account, err error) {
	if errors.Is(err, account.ErrNotConnected) || errors.Is(err, account.ErrTooSoon) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) igRefresh(w http.ResponseWriter, r *http.Request) {
	a, err := s.Accounts.Refresh(r.Context())
	s.accountResult(w, a, err)
}

func (s *Server) igResubscribe(w http.ResponseWriter, r *http.Request) {
	a, err := s.Accounts.Resubscribe(r.Context())
	s.accountResult(w, a, err)
}

func (s *Server) igDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.Accounts.Disconnect(r.Context()); err != nil {
		log.Printf("ig disconnect: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
