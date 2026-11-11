package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/appsettings"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

type Server struct {
	DB        *pgxpool.Pool
	Auth      *auth.Service
	Inbox     *inbox.Service
	Hub       *realtime.Hub
	Fake      *ig.Fake // 非 nil＝開發模擬模式
	Auto      *automation.Engine
	AIModel   string // 沒設 key 時自動回覆頁顯示的模型名；有 key 時用 CaptionAI.Model()
	CRM       *crm.Service
	Analytics *analytics.Service
	Media     *media.Store
	Posts     *posts.Service
	CaptionAI *automation.LiveAI // 與 Auto.AI 同一個；Enabled()＝false 表示沒設 key
	Creds     *appsettings.Store // 後台可改的憑證（DB 優先於 .env）
	// AnthropicBaseURL 非空時 test-ai 改打這個網址（測試用 httptest；正式留空＝官方 API）
	AnthropicBaseURL string
	Accounts         *account.Service
	Info             SystemInfo
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Get("/webhooks/instagram", s.webhookVerify)
	r.Post("/webhooks/instagram", s.webhookReceive)
	r.Handle("/media/*", s.Media.Handler()) // 公開：IG 發佈時從這裡抓檔

	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Post("/auth/logout", s.logout)
		r.Group(func(r chi.Router) {
			r.Use(s.Auth.Middleware)
			// 所有登入者都可用
			r.Get("/auth/me", s.me)
			r.Post("/auth/password", s.changePassword)
			r.Get("/ig/accounts", s.listAccounts) // 可見的 IG 帳號（各頁的帳號篩選／選擇器）
			// 各功能依權限把關（權限鍵見 auth.AllPerms）
			perm := func(p string) chi.Router { return r.With(auth.RequirePerm(p)) }
			s.inboxRoutes(perm(auth.PermInbox))
			s.automationRoutes(perm(auth.PermAutomation))
			s.crmRoutes(perm(auth.PermContacts))
			s.analyticsRoutes(perm(auth.PermAnalytics))
			s.postRoutes(perm(auth.PermSchedule))
			s.settingsRoutes(perm(auth.PermIGAccounts))
			s.credentialsRoutes(perm(auth.PermCredentials))
			s.adminRoutes(perm(auth.PermAdmins))
			dev := perm(auth.PermInbox)
			dev.Get("/dev/status", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, map[string]bool{"simulator": s.Fake != nil})
			})
			if s.Fake != nil {
				dev.Post("/dev/simulate", s.simulate)
			}
		})
	})
	return r
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, tok, err := s.Auth.Login(r.Context(), in.Username, in.Password)
	if errors.Is(err, auth.ErrBadCredentials) {
		writeErr(w, http.StatusUnauthorized, "帳號或密碼錯誤")
		return
	}
	if errors.Is(err, auth.ErrDisabled) {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "login failed")
		return
	}
	s.Auth.SetCookie(w, tok, 7*24*3600)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.SetCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u, err := s.Auth.Me(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
