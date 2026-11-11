package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/audit"
)

// 操作紀錄（見 internal/audit）：
//   - auditMiddleware 掛在登入後的路由群組，記所有異動請求（POST/PUT/PATCH/DELETE），含權限不足等失敗
//   - 登入／登出在 login／logout handler 記；被踢在 auth.Middleware 記；IG 連結結果在 OAuth callback 記
//   - 查詢 GET /api/audit-logs（「操作紀錄」權限；依可見帳號過濾）、GET /api/audit-logs/actions

func (s *Server) auditRoutes(r chi.Router) {
	r.Get("/audit-logs", s.listAuditLogs)
	r.Get("/audit-logs/actions", s.auditActions)
	r.Get("/audit-logs/admins", s.auditAdmins)
}

const maxAuditBody = 1 << 20 // 超過不解析，只記大小

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// auditMiddleware 須掛在 auth.Middleware 之後、RequirePerm 之前（權限不足的 403 也要記）。
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Audit == nil || !isMutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		rctx := chi.RouteContext(r.Context())
		// 群組內的路由此時已比對完成（樣式與 URL 參數可用）；掛在子路由（/automation）的要等 handler 跑完才完整
		pre, _ := audit.LookupRoute(r.Method, routePattern(rctx))

		var body any
		if !pre.NoBody {
			body = readAuditBody(r)
		}
		account := s.auditAccount(r.Context(), pre, chi.URLParam(r, idParamName(pre)))

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if audit.Skipped(r.Method, routePattern(rctx)) {
			return
		}

		route, _ := audit.LookupRoute(r.Method, routePattern(rctx))
		params := map[string]any{}
		if rctx != nil {
			for i, k := range rctx.URLParams.Keys {
				if k != "*" && i < len(rctx.URLParams.Values) {
					params[k] = rctx.URLParams.Values[i]
				}
			}
		}
		targetID := ""
		if route.Target != "" {
			targetID, _ = params[idParamName(route)].(string)
		}
		if account == 0 {
			account = s.auditAccount(r.Context(), route, targetID)
		}
		if account == 0 {
			account = queryAccount(r)
		}
		if account == 0 {
			account = bodyAccount(body)
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		detail := map[string]any{"method": r.Method, "path": r.URL.Path, "route": "/api" + route.Pattern}
		if len(params) > 0 {
			detail["params"] = params
		}
		if q := sanitizedQuery(r); len(q) > 0 {
			detail["query"] = q
		}
		if body != nil && !route.NoBody {
			detail["body"] = body
		}
		u := currentUser(r)
		detail["username"] = u.Username
		s.Audit.Record(r.Context(), audit.Entry{
			AdminID: u.ID, AdminName: u.DisplayName, Action: route.Action, Summary: route.Name,
			TargetType: route.Target, TargetID: targetID, IGAccountID: account, Detail: detail,
			Status: status, Success: status < 400,
		}.FromRequest(r))
	})
}

func routePattern(rctx *chi.Context) string {
	if rctx == nil {
		return ""
	}
	return rctx.RoutePattern()
}

func idParamName(rt audit.Route) string {
	if strings.Contains(rt.Pattern, "{name}") {
		return "name"
	}
	return "id"
}

// readAuditBody 讀出 JSON body 的遮蔽摘要，並把 body 還給 handler。非 JSON（上傳檔案）或太大的只記大小。
func readAuditBody(r *http.Request) any {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "json") {
		return map[string]any{"_note": "非 JSON 內容未記錄", "contentType": ct}
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBody+1))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
	if err != nil || len(buf) == 0 {
		return nil
	}
	if len(buf) > maxAuditBody {
		return map[string]any{"_note": "內容過大未記錄"}
	}
	v := audit.SanitizeJSON(buf)
	if _, isText := v.(string); isText {
		// 不是合法 JSON：原文可能夾帶機密，不記內容
		return map[string]any{"_note": "內容無法解析未記錄", "bytes": len(buf)}
	}
	return v
}

func sanitizedQuery(r *http.Request) map[string]any {
	q := r.URL.Query()
	if len(q) == 0 {
		return nil
	}
	out := map[string]any{}
	for k, v := range q {
		val := strings.Join(v, ",")
		if audit.IsSecretKey(k) && val != "" {
			out[k] = audit.Redacted
		} else {
			out[k] = audit.Truncate(val)
		}
	}
	return out
}

// auditAccount 依目標資源查異動所屬的 IG 帳號（刪除類在 handler 前查）；0＝查不到。
// 查不到時 middleware 再依序用 ?account=、body 的 account／igAccountId；都沒有＝不屬於單一帳號。
func (s *Server) auditAccount(ctx context.Context, rt audit.Route, targetID string) int64 {
	id, _ := strconv.ParseInt(targetID, 10, 64)
	var q string
	switch rt.Target {
	case "ig_account":
		return id
	case "conversation":
		q = `SELECT ct.ig_account_id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE c.id = $1`
	case "message":
		q = `SELECT ct.ig_account_id FROM messages m JOIN conversations c ON c.id = m.conversation_id
  JOIN contacts ct ON ct.id = c.contact_id WHERE m.id = $1`
	case "review":
		q = `SELECT ig_account_id FROM message_reviews WHERE id = $1`
	case "contact":
		q = `SELECT ig_account_id FROM contacts WHERE id = $1`
	case "post":
		q = `SELECT ig_account_id FROM scheduled_posts WHERE id = $1`
	case "rule":
		q = `SELECT ig_account_id FROM auto_rules WHERE id = $1`
	case "knowledge":
		q = `SELECT ig_account_id FROM knowledge_items WHERE id = $1`
	}
	if q != "" && id > 0 {
		var acc *int64
		err := s.DB.QueryRow(ctx, q, id).Scan(&acc)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("audit: resolve account %s %d: %v", rt.Target, id, err)
		}
		if acc != nil {
			return *acc
		}
	}
	return 0
}

// queryAccount 取 ?account=（自動回覆等 API 用）。
func queryAccount(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.URL.Query().Get("account"), 10, 64)
	return id
}

// bodyAccount 從 body 摘要找帳號欄位（account／igAccountId，或第一層物件裡的同名欄位，例如貼文的 post.igAccountId）。
func bodyAccount(body any) int64 {
	m, ok := body.(map[string]any)
	if !ok {
		return 0
	}
	if id := accountField(m); id > 0 {
		return id
	}
	for _, v := range m {
		if sub, ok := v.(map[string]any); ok {
			if id := accountField(sub); id > 0 {
				return id
			}
		}
	}
	return 0
}

func accountField(m map[string]any) int64 {
	for _, k := range []string{"account", "igAccountId"} {
		if f, ok := m[k].(float64); ok && f > 0 {
			return int64(f)
		}
	}
	return 0
}

// recordAuth 記不經 middleware 的動作（登入、登出、IG 連結）。
func (s *Server) recordAuth(r *http.Request, e audit.Entry) {
	s.Audit.Record(r.Context(), e.FromRequest(r))
}

func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	u := currentUser(r)
	f := audit.Filter{Action: q.Get("action"), Scope: u.AccountScope()}
	f.AdminID, _ = strconv.ParseInt(q.Get("admin"), 10, 64)
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.PageSize, _ = strconv.Atoi(q.Get("pageSize"))
	if v := q.Get("account"); v != "" && v != "all" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "account 參數不正確")
			return
		}
		if !u.CanSeeAccount(id) {
			writeErr(w, http.StatusForbidden, errAccountForbidden)
			return
		}
		f.Account = id
	}
	for _, p := range []struct {
		key string
		dst **time.Time
		end bool
	}{{"from", &f.From, false}, {"to", &f.To, true}} {
		v := q.Get(p.key)
		if v == "" {
			continue
		}
		t, err := parseAuditTime(v, p.end, s.storeLoc())
		if err != nil {
			writeErr(w, http.StatusBadRequest, p.key+" 參數不正確")
			return
		}
		*p.dst = &t
	}
	if s.Audit == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []audit.Log{}, "total": 0})
		return
	}
	items, total, err := s.Audit.List(r.Context(), f)
	if err != nil {
		log.Printf("audit list: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// parseAuditTime 收 RFC3339，或 YYYY-MM-DD（店家時區；當作 to 時是該日結束＝隔天 0 點）。
func parseAuditTime(v string, end bool, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, loc)
	if err != nil {
		return t, err
	}
	if end {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

func (s *Server) auditActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, audit.Actions())
}

func (s *Server) storeLoc() *time.Location {
	if s.Analytics != nil && s.Analytics.Loc != nil {
		return s.Analytics.Loc
	}
	return time.Local
}

// auditAdmins 給「管理員」篩選下拉用（有操作紀錄權限但沒有管理員權限的人也要能選）；只回名稱。
func (s *Server) auditAdmins(w http.ResponseWriter, r *http.Request) {
	list, err := s.Auth.ListAdmins(r.Context())
	if err != nil {
		log.Printf("audit admins: %v", err)
		writeErr(w, http.StatusInternalServerError, "伺服器錯誤")
		return
	}
	type item struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Disabled    bool   `json:"disabled"`
	}
	out := make([]item, 0, len(list))
	for _, u := range list {
		out = append(out, item{u.ID, u.Username, u.DisplayName, u.Disabled})
	}
	writeJSON(w, http.StatusOK, out)
}
