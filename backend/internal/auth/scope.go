package auth

import (
	"context"
	"slices"
)

// IG 帳號可見範圍（安全邊界）：管理員只看得到 IGAccountIDs 內的帳號資料；nil＝全部帳號。
// 店長、超管一律全部。HTTP 層以 ?account= 過濾時，範圍外回 403；單筆資源在範圍外回 404（不洩漏存在與否）。

// AllAccounts 回是否可見全部帳號。
func (u User) AllAccounts() bool { return u.ownerLevel() || len(u.IGAccountIDs) == 0 }

// CanSeeAccount 回是否可見某個帳號。
func (u User) CanSeeAccount(id int64) bool {
	return u.AllAccounts() || slices.Contains(u.IGAccountIDs, id)
}

// AccountScope 回可見帳號清單；nil＝全部。回傳複本。
func (u User) AccountScope() []int64 {
	if u.AllAccounts() {
		return nil
	}
	return slices.Clone(u.IGAccountIDs)
}

// normalizeAccounts 去重、排序；空＝nil（全部）。
func normalizeAccounts(in []int64) []int64 {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	out = slices.Compact(out)
	return out
}

// checkAccountGrant：非全部範圍的操作者只能授予自己範圍內的帳號，也不能授予「全部」。
func checkAccountGrant(actor User, ids []int64) error {
	if actor.AllAccounts() {
		return nil
	}
	if len(ids) == 0 {
		return policy("不能授予「全部帳號」：你自己只能看部分帳號")
	}
	for _, id := range ids {
		if !actor.CanSeeAccount(id) {
			return policy("不能授予自己看不到的 IG 帳號")
		}
	}
	return nil
}

// checkAccountsExist 檢查帳號 id 都存在。
func (s *Service) checkAccountsExist(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ig_accounts WHERE id = ANY($1)`, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return badInput("可見帳號裡有不存在的 IG 帳號")
	}
	return nil
}
