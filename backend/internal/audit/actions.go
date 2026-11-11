package audit

import "strings"

// 操作紀錄的動作鍵（machine key）與中文名稱。
//   - 登入後的異動請求（POST/PUT/PATCH/DELETE）一律由 httpapi 的 middleware 依「方法＋chi 路由樣式」查 Routes 記錄；
//     查不到的退回「METHOD 樣式」當鍵與名稱（新增路由忘了登記時至少不漏記）
//   - 登入／登出／被踢／IG 連結結果不走 middleware，由各 handler 直接記（下列 Action* 常數）
const (
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
	ActionKicked         = "auth.kicked"
	ActionIGConnect      = "ig.connect"
	ActionIGConnectError = "ig.connect_failed"
	ActionTOTPFailed     = "auth.totp_failed" // 登入第二步驗證碼／備援碼錯誤或錯太多次（不記碼）
)

// Route 是一條異動路由的登記。Pattern 不含 /api 前綴。
type Route struct {
	Method, Pattern string
	Action, Name    string
	// Target 是 URL 參數 {id}／{name} 指的資源種類（見 targetLabels）；空＝沒有單一目標
	Target string
	// NoBody＝不記 request body（密碼類；上傳檔案）
	NoBody bool
}

// Routes 是 router.go 所有登入後異動路由的中文名稱。新增路由時一併登記（TestRouteNamesCoverRouter 會抓漏）。
var Routes = []Route{
	{"POST", "/auth/password", "auth.change_password", "修改自己的密碼", "", true},
	{"POST", "/auth/totp/setup", "auth.totp_setup", "開始綁定 Google Authenticator", "", true},
	{"POST", "/auth/totp/enable", "auth.totp_enable", "確認綁定 Google Authenticator", "", true},

	{"PATCH", "/conversations/{id}", "inbox.update_conversation", "變更對話狀態／接手方式", "conversation", false},
	{"POST", "/conversations/{id}/read", "inbox.mark_read", "標記對話已讀", "conversation", false},
	{"POST", "/conversations/{id}/messages", "inbox.reply", "回覆私訊", "conversation", false},
	{"POST", "/conversations/{id}/suggest", "inbox.suggest", "AI 建議回覆", "conversation", true},
	{"POST", "/messages/{id}/reviews", "review.mark_wrong", "標記回答有誤", "message", false},
	{"POST", "/reviews/{id}/resolve", "review.resolve", "處理待改進問答", "review", false},

	{"PUT", "/automation/settings", "automation.update_settings", "修改 AI 設定", "", false},
	{"POST", "/automation/rules", "automation.create_rule", "新增關鍵字規則", "", false},
	{"PUT", "/automation/rules/{id}", "automation.update_rule", "修改關鍵字規則", "rule", false},
	{"DELETE", "/automation/rules/{id}", "automation.delete_rule", "刪除關鍵字規則", "rule", false},
	{"POST", "/automation/knowledge", "automation.create_knowledge", "新增知識庫", "", false},
	{"PUT", "/automation/knowledge/{id}", "automation.update_knowledge", "修改知識庫", "knowledge", false},
	{"DELETE", "/automation/knowledge/{id}", "automation.delete_knowledge", "刪除知識庫", "knowledge", false},
	{"POST", "/automation/test", "automation.sandbox", "自動回覆沙盒測試", "", false},
	{"POST", "/automation/distill", "automation.distill", "蒸餾自動回覆", "", false},
	{"POST", "/automation/distill/suggestions/{id}/accept", "automation.distill_accept", "採用蒸餾建議", "distill_suggestion", false},
	{"POST", "/automation/distill/suggestions/{id}/dismiss", "automation.distill_dismiss", "略過蒸餾建議", "distill_suggestion", false},

	{"PATCH", "/contacts/{id}", "contact.update", "修改顧客（備註／標籤／標記）", "contact", false},
	{"POST", "/tags", "tag.create", "新增標籤", "", false},
	{"PUT", "/tags/{id}", "tag.update", "修改標籤", "tag", false},
	{"DELETE", "/tags/{id}", "tag.delete", "刪除標籤", "tag", false},

	{"POST", "/media", "media.upload", "上傳媒體", "", true},
	{"POST", "/posts", "post.create", "新增貼文", "", false},
	{"PUT", "/posts/{id}", "post.update", "修改貼文", "post", false},
	{"DELETE", "/posts/{id}", "post.delete", "刪除貼文", "post", false},
	{"POST", "/posts/caption", "post.caption", "AI 產生貼文文案", "", false},
	{"POST", "/posts/{id}/insights/refresh", "post.refresh_insights", "更新貼文成效", "post", false},

	{"POST", "/ig/connections/{id}/refresh", "ig.sync_connection", "同步 IG 帳號", "connection", false},
	{"POST", "/ig/connections/{id}/accounts", "ig.add_account", "手動加入 IG 帳號", "connection", false},
	{"DELETE", "/ig/connections/{id}", "ig.disconnect", "斷開 IG 連線", "connection", false},
	{"POST", "/ig/accounts/{id}/active", "ig.set_active", "停用／啟用 IG 帳號", "ig_account", false},
	{"POST", "/ig/accounts/{id}/subscribe", "ig.resubscribe", "重試私訊訂閱", "ig_account", false},

	{"PUT", "/settings/credentials", "credentials.update", "修改串接憑證", "", false},
	{"POST", "/settings/credentials/test-ai", "credentials.test_ai", "測試 Claude 連線", "", false},

	{"POST", "/admins", "admin.create", "新增管理員", "", false},
	{"PATCH", "/admins/{id}", "admin.update", "修改管理員", "admin", false},
	{"POST", "/admins/{id}/password", "admin.reset_password", "重設管理員密碼", "admin", true},
	{"POST", "/admins/{id}/totp/reset", "admin.reset_totp", "重設管理員 Google Authenticator", "admin", true},

	{"POST", "/dev/simulate", "dev.simulate", "模擬訊息（開發）", "", false},

	{"POST", "/digests/generate", "digest.generate", "重新產生每日摘要", "", false},
}

// 不走 middleware 的動作
var specialActions = []ActionName{
	{ActionLogin, "登入"},
	{ActionLoginFailed, "登入失敗"},
	{ActionLogout, "登出"},
	{ActionKicked, "被其他裝置登入踢出"},
	{ActionIGConnect, "連結 IG 帳號"},
	{ActionIGConnectError, "連結 IG 帳號失敗"},
	{ActionTOTPFailed, "登入驗證碼錯誤"},
}

// ActionName 是篩選下拉用的「動作鍵 → 中文名稱」。
type ActionName struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Actions 回全部已登記的動作（登入類在前，其餘照 Routes 順序）。
func Actions() []ActionName {
	out := append([]ActionName{}, specialActions...)
	for _, r := range Routes {
		out = append(out, ActionName{r.Action, r.Name})
	}
	return out
}

// ActionLabel 回動作鍵的中文名稱；未登記的回鍵本身。
func ActionLabel(key string) string {
	for _, a := range specialActions {
		if a.Key == key {
			return a.Name
		}
	}
	for _, r := range Routes {
		if r.Action == key {
			return r.Name
		}
	}
	return key
}

// LookupRoute 依方法與 chi 路由樣式（可含 /api 前綴）找登記；找不到時回退「METHOD 樣式」，ok=false。
// skipped 是有登記名稱、但不寫進操作紀錄的路由：每開一次對話就打一次，記了只會洗版（Mike 2026-11-11）。
var skipped = map[string]bool{
	"POST /conversations/{id}/read": true,
}

// Skipped 回這條路由是否不記操作紀錄。
func Skipped(method, pattern string) bool { return skipped[method+" "+pattern] }

func LookupRoute(method, pattern string) (Route, bool) {
	pattern = strings.TrimPrefix(pattern, "/api")
	for _, r := range Routes {
		if r.Method == method && r.Pattern == pattern {
			return r, true
		}
	}
	key := method + " " + pattern
	return Route{Method: method, Pattern: pattern, Action: key, Name: key}, false
}

var targetLabels = map[string]string{
	"conversation":       "對話",
	"message":            "訊息",
	"review":             "待改進問答",
	"rule":               "關鍵字規則",
	"knowledge":          "知識庫",
	"distill_suggestion": "蒸餾建議",
	"contact":            "顧客",
	"tag":                "標籤",
	"post":               "貼文",
	"connection":         "IG 連線",
	"ig_account":         "IG 帳號",
	"admin":              "管理員",
}

// TargetLabel 回「種類 #id」的中文說明；沒有目標回空字串。
func TargetLabel(typ, id string) string {
	if typ == "" && id == "" {
		return ""
	}
	l := targetLabels[typ]
	if l == "" {
		l = typ
	}
	if id == "" {
		return l
	}
	return l + " #" + id
}
