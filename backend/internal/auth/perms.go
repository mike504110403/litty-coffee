package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
)

// 功能權限鍵。後端是唯一來源；前端由 /auth/me 取得，管理員頁的勾選清單也照這份。
const (
	PermInbox       = "inbox"       // 收件匣＋即時推送＋回覆
	PermContacts    = "contacts"    // 顧客與標籤
	PermAutomation  = "automation"  // 自動回覆規則／知識庫／設定／沙盒
	PermSchedule    = "schedule"    // 排程、媒體上傳、AI 文案
	PermAnalytics   = "analytics"   // 分析
	PermIGAccounts  = "ig_accounts" // 連結／斷開／續期 IG 帳號、串接資訊
	PermCredentials = "credentials" // IG App／Claude 憑證
	PermAdmins      = "admins"      // 管理員帳號
)

// AllPerms 是完整權限清單（順序即前端顯示順序）。
var AllPerms = []string{PermInbox, PermContacts, PermAutomation, PermSchedule, PermAnalytics,
	PermIGAccounts, PermCredentials, PermAdmins}

var permLabels = map[string]string{
	PermInbox:       "收件匣",
	PermContacts:    "顧客",
	PermAutomation:  "自動回覆",
	PermSchedule:    "排程",
	PermAnalytics:   "分析",
	PermIGAccounts:  "IG 帳號",
	PermCredentials: "串接憑證",
	PermAdmins:      "管理員",
}

func validPerm(p string) bool { return slices.Contains(AllPerms, p) }

// Can 回報使用者是否有某功能權限；店長一律有。
func (u User) Can(perm string) bool { return u.IsOwner || slices.Contains(u.Permissions, perm) }

// normalizePerms 去重、去掉未知鍵，並依 AllPerms 排序。
func normalizePerms(in []string) []string {
	out := []string{}
	for _, p := range AllPerms {
		if slices.Contains(in, p) {
			out = append(out, p)
		}
	}
	return out
}

// CurrentUser 取出 Middleware 放進 context 的登入者。
func CurrentUser(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

// UserID 是登入者 id；沒有登入者時為 0。
func UserID(ctx context.Context) int64 {
	u, _ := CurrentUser(ctx)
	return u.ID
}

// RequirePerm 只放行有指定功能權限的登入者（須掛在 Middleware 之後）。
func RequirePerm(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := CurrentUser(r.Context())
			if !ok {
				writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if !u.Can(perm) {
				writeAuthErr(w, http.StatusForbidden, "沒有「"+permLabels[perm]+"」的權限")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeAuthErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
