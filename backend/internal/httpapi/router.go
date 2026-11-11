package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/appsettings"
	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/digest"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/monitor"
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
	Audit            *audit.Store // 操作紀錄；nil＝不記
	// SuggestAI 非 nil 時「AI 建議回覆」改用它（測試換假 AI）；nil＝沿用 CaptionAI
	SuggestAI automation.Suggester
	suggestRL suggestLimiter // 建議回覆：每位管理員每分鐘上限
	// DistillAI 非 nil 時「蒸餾自動回覆」改用它（測試換假 AI）；nil＝沿用 CaptionAI
	DistillAI automation.DistillAI
	// 總覽：系統狀態、每日摘要，與它們的即時通知
	Monitor *monitor.Monitor // 系統狀態（總覽頁）；nil＝503
	Digests *digest.Service  // 每日摘要；nil＝503
	SysHub  *realtime.Hub    // system.alert／digest.ready（/api/system/stream）；nil＝串流只送心跳
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
		r.Post("/auth/totp", s.verifyTOTP) // 密碼正確後的第二步（待驗證碼 cookie，見 totp.go）
		r.Group(func(r chi.Router) {
			r.Use(s.Auth.Middleware, s.auditMiddleware, s.Auth.SetupGate) // 異動請求一律記操作紀錄（含權限不足、首次登入鎖定）
			// 所有登入者都可用
			r.Get("/auth/me", s.me)
			r.Post("/auth/password", s.changePassword)
			// 綁定 GA：產生 secret／QR code → 輸入一組碼確認（回備援碼）
			r.Post("/auth/totp/setup", s.totpSetup)
			r.Post("/auth/totp/enable", s.totpEnable)
			r.Get("/ig/accounts", s.listAccounts) // 可見的 IG 帳號（各頁的帳號篩選／選擇器）
			// 總覽的系統狀態與通知串流（所有登入者；內容依可見帳號與權限過濾）
			r.Get("/system/status", s.systemStatus) // 系統狀態：只回可見帳號
			r.Get("/system/stream", s.systemStream) // system.alert／digest.ready 即時通知（依權限與可見帳號過濾）
			// 各功能依權限把關（權限鍵見 auth.AllPerms）
			perm := func(p string) chi.Router { return r.With(auth.RequirePerm(p)) }
			s.inboxRoutes(perm(auth.PermInbox))
			s.reviewInboxRoutes(perm(auth.PermInbox))
			s.automationRoutes(perm(auth.PermAutomation))
			s.reviewRoutes(perm(auth.PermAutomation))
			s.crmRoutes(perm(auth.PermContacts))
			// 顧客標記（黑名單／問題顧客）收件匣也能改；備註與標籤仍限顧客權限（見 patchContact）
			r.With(auth.RequireAnyPerm(auth.PermContacts, auth.PermInbox)).Patch("/contacts/{id}", s.patchContact)
			s.analyticsRoutes(perm(auth.PermAnalytics))
			s.digestRoutes(perm(auth.PermAnalytics))
			s.postRoutes(perm(auth.PermSchedule))
			s.settingsRoutes(r.With(auth.RequireSuper))    // IG 帳號連結：超管專屬
			s.credentialsRoutes(r.With(auth.RequireSuper)) // 串接憑證：超管專屬
			s.adminRoutes(perm(auth.PermAdmins))
			s.auditRoutes(perm(auth.PermAudit))
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
	res, err := s.Auth.Authenticate(r.Context(), in.Username, in.Password)
	u, tok := res.User, res.Session
	if errors.Is(err, auth.ErrBadCredentials) {
		s.recordLoginFailed(r, in.Username, "帳號或密碼錯誤", http.StatusUnauthorized)
		writeErr(w, http.StatusUnauthorized, "帳號或密碼錯誤")
		return
	}
	if errors.Is(err, auth.ErrDisabled) {
		s.recordLoginFailed(r, in.Username, err.Error(), http.StatusForbidden)
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "login failed")
		return
	}
	if res.Pending != "" { // 已綁 GA：先發待驗證碼 cookie，正式 session 等 /auth/totp
		s.Auth.SetPendingCookie(w, res.Pending, auth.PendingMaxAge)
		writeJSON(w, http.StatusOK, map[string]any{"mfaRequired": true})
		return
	}
	s.recordAuth(r, audit.Entry{AdminID: u.ID, AdminName: u.DisplayName, Action: audit.ActionLogin,
		Detail: map[string]any{"username": u.Username}, Status: http.StatusOK, Success: true})
	s.Auth.SetCookie(w, tok, 7*24*3600)
	writeJSON(w, http.StatusOK, u)
}

// recordLoginFailed 記登入失敗：只記嘗試的帳號（不記密碼）；帳號存在時掛上該管理員。
func (s *Server) recordLoginFailed(r *http.Request, username, reason string, status int) {
	if s.Audit == nil {
		return
	}
	e := audit.Entry{Action: audit.ActionLoginFailed, AdminName: audit.Truncate(username),
		Detail: map[string]any{"username": audit.Truncate(username), "reason": reason}, Status: status}
	var name string
	if err := s.DB.QueryRow(r.Context(), `SELECT id, display_name FROM admin_users WHERE username = $1`, username).
		Scan(&e.AdminID, &name); err == nil {
		e.AdminName = name
	}
	s.recordAuth(r, e)
}

// logout 清 cookie；token 仍是目前有效的 session 時一併結束它（session_id 清空）並記紀錄。
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		u, ok, err := s.Auth.Logout(r.Context(), c.Value)
		if err != nil {
			log.Printf("logout: %v", err)
		}
		if ok {
			s.recordAuth(r, audit.Entry{AdminID: u.ID, AdminName: u.DisplayName, Action: audit.ActionLogout,
				Detail: map[string]any{"username": u.Username}, Status: http.StatusNoContent, Success: true})
		}
	}
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
