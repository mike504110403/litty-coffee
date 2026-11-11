package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

// 功能權限鍵。後端是唯一來源；前端由 /auth/me 取得，管理員頁的勾選清單也照這份。
const (
	PermInbox       = "inbox"       // 收件匣＋即時推送＋回覆
	PermContacts    = "contacts"    // 顧客與標籤
	PermAutomation  = "automation"  // 自動回覆規則／知識庫／設定／沙盒
	PermSchedule    = "schedule"    // 排程、媒體上傳、AI 文案
	PermAnalytics   = "analytics"   // 分析
	PermIGAccounts  = "ig_accounts" // 超管專屬（不可授予）：連結／斷開／續期 IG 帳號、串接資訊
	PermCredentials = "credentials" // 超管專屬（不可授予）：IG App／Claude 憑證
	PermAdmins      = "admins"      // 管理員帳號
	PermAudit       = "audit"       // 操作紀錄（看所有管理員的登入與異動紀錄）
)

// AllPerms 是可授予的完整權限清單（順序即前端顯示順序）。IG 帳號與串接憑證自 00016 起只限超管，不在清單內
// （既有勾選由 migration 拿掉；送來會回「未知的權限」）。
var AllPerms = []string{PermInbox, PermContacts, PermAutomation, PermSchedule, PermAnalytics,
	PermAdmins, PermAudit}

var permLabels = map[string]string{
	PermInbox:       "收件匣",
	PermContacts:    "顧客",
	PermAutomation:  "自動回覆",
	PermSchedule:    "排程",
	PermAnalytics:   "分析",
	PermIGAccounts:  "IG 帳號",
	PermCredentials: "串接憑證",
	PermAdmins:      "管理員",
	PermAudit:       "操作紀錄",
}

func validPerm(p string) bool { return slices.Contains(AllPerms, p) }

// Can 回報使用者是否有某功能權限；店長、超管一律有。
// 超管專屬功能（IG 帳號連結、串接憑證）不要用 Can／RequirePerm 把關，一律用 IsSuper／RequireSuper。
func (u User) Can(perm string) bool { return u.ownerLevel() || slices.Contains(u.Permissions, perm) }

// ownerLevel：店長或超管（可管理店長、不受權限勾選限制）。
func (u User) ownerLevel() bool { return u.IsOwner || u.IsSuper }

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

// RequireAnyPerm 放行有其中任一功能權限的登入者（須掛在 Middleware 之後）。
func RequireAnyPerm(perms ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := CurrentUser(r.Context())
			if !ok {
				writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			labels := make([]string, 0, len(perms))
			for _, p := range perms {
				if u.Can(p) {
					next.ServeHTTP(w, r)
					return
				}
				labels = append(labels, "「"+permLabels[p]+"」")
			}
			writeAuthErr(w, http.StatusForbidden, "沒有"+strings.Join(labels, "或")+"的權限")
		})
	}
}

// RequireSuper 只放行超級管理員（須掛在 Middleware 之後）。
func RequireSuper(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(r.Context())
		if !ok {
			writeAuthErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !u.IsSuper {
			writeAuthErr(w, http.StatusForbidden, "只有超級管理員能使用這個功能")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ErrCodeSetupRequired 是首次登入鎖定（必須改密碼／超管未綁 GA）時 403 回應的 code。
const ErrCodeSetupRequired = "setup_required"

// setupAllowed 是首次登入鎖定中仍可用的 API（登出不經 Middleware，本來就可用）。
var setupAllowed = map[string]bool{
	"GET /api/auth/me":           true,
	"POST /api/auth/password":    true,
	"POST /api/auth/totp/setup":  true,
	"POST /api/auth/totp/enable": true,
}

// SetupGate：登入者 SetupRequired 時，除了 setupAllowed 一律 403＋code setup_required（含 SSE）。須掛在 Middleware 之後。
func (s *Service) SetupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(r.Context())
		if ok && u.SetupRequired && !setupAllowed[r.Method+" "+r.URL.Path] {
			writeAuthErrCode(w, http.StatusForbidden, "請先完成帳號設定（修改密碼、綁定 Google Authenticator）", ErrCodeSetupRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeAuthErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
