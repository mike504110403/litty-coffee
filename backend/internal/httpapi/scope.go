package httpapi

import (
	"net/http"
	"strconv"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
)

// IG 帳號可見範圍（安全邊界）的 HTTP 端規則，各 handler 一律經過這裡：
//   - 清單類：?account=<id> 過濾；指定範圍外的帳號回 403；未指定＝登入者可見的全部帳號
//   - 單筆資源（對話、顧客、貼文、附件…）：不在範圍內一律回 404（不洩漏存在與否）
//   - 需要指定單一帳號的操作（自動回覆、上傳、AI 文案、模擬器）：缺帳號 400、範圍外 403
//   - 連線層級的操作（連結、同步、斷開）：只限可見全部帳號的人

const errAccountForbidden = "沒有這個 IG 帳號的權限"

func currentUser(r *http.Request) auth.User {
	u, _ := auth.CurrentUser(r.Context())
	return u
}

// canSee 回登入者能不能看某帳號的資料。
func canSee(r *http.Request, accountID int64) bool { return currentUser(r).CanSeeAccount(accountID) }

// accountsFilter 解析清單類 API 的 ?account=：回要過濾的帳號（nil＝全部）。不合法或範圍外時已寫好回應、回 ok=false。
func accountsFilter(w http.ResponseWriter, r *http.Request) (ids []int64, ok bool) {
	u := currentUser(r)
	v := r.URL.Query().Get("account")
	if v == "" || v == "all" {
		return u.AccountScope(), true
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "account 參數不正確")
		return nil, false
	}
	if !u.CanSeeAccount(id) {
		writeErr(w, http.StatusForbidden, errAccountForbidden)
		return nil, false
	}
	return []int64{id}, true
}

// requireAccount 檢查「必須指定單一帳號」的操作：0＝400、範圍外＝403。
func requireAccount(w http.ResponseWriter, r *http.Request, id int64) bool {
	if id <= 0 {
		writeErr(w, http.StatusBadRequest, "請指定 IG 帳號")
		return false
	}
	if !canSee(r, id) {
		writeErr(w, http.StatusForbidden, errAccountForbidden)
		return false
	}
	return true
}

// accountQuery 取 ?account= 並套 requireAccount。
func accountQuery(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("account"), 10, 64)
	return id, requireAccount(w, r, id)
}

// requireAllAccounts：連線層級的操作只限可見全部帳號的人。
func requireAllAccounts(w http.ResponseWriter, r *http.Request) bool {
	if !currentUser(r).AllAccounts() {
		writeErr(w, http.StatusForbidden, "只有可見全部 IG 帳號的管理員能管理連線")
		return false
	}
	return true
}
