package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

// PolicyError 是權限規則擋下的操作（HTTP 403）。
type PolicyError struct{ Msg string }

func (e *PolicyError) Error() string { return e.Msg }

// InputError 是欄位不合法（HTTP 400）。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

var (
	ErrUserNotFound  = errors.New("找不到這個管理員")
	ErrUsernameTaken = errors.New("帳號已被使用")
)

func policy(msg string) error   { return &PolicyError{msg} }
func badInput(msg string) error { return &InputError{msg} }

type AdminInput struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName"`
	Password    string   `json:"password"`
	IsOwner     bool     `json:"isOwner"`
	IsSuper     bool     `json:"isSuper"`
	Permissions []string `json:"permissions"`
	// IGAccountIDs 可見 IG 帳號；省略或空＝全部
	IGAccountIDs []int64 `json:"igAccountIds"`
}

// AdminPatch 只更新非 nil 的欄位。
type AdminPatch struct {
	DisplayName *string   `json:"displayName"`
	IsOwner     *bool     `json:"isOwner"`
	IsSuper     *bool     `json:"isSuper"`
	Permissions *[]string `json:"permissions"`
	Disabled    *bool     `json:"disabled"`
	// IGAccountIDs 可見 IG 帳號；nil＝不動，[]＝全部
	IGAccountIDs *[]int64 `json:"igAccountIds"`
}

func (s *Service) ListAdmins(ctx context.Context) ([]User, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+userCols+` FROM admin_users ORDER BY is_super DESC, is_owner DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func checkPerms(perms []string) error {
	for _, p := range perms {
		if !validPerm(p) {
			return badInput("未知的權限：" + p)
		}
	}
	return nil
}

// checkGrant：非店長只能授予自己也有的權限。
func checkGrant(actor User, added []string) error {
	for _, p := range added {
		if !actor.Can(p) {
			return policy("不能授予自己沒有的權限：" + permLabels[p])
		}
	}
	return nil
}

func checkPassword(pw string) error {
	if len(pw) < 8 {
		return badInput("密碼至少 8 個字元")
	}
	return nil
}

// CreateAdmin 新增管理員。只有超管能新增超管；只有店長、超管能新增店長；其他人只能授予自己有的權限。
func (s *Service) CreateAdmin(ctx context.Context, actor User, in AdminInput) (User, error) {
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || len([]rune(in.Username)) > 50 || strings.IndexFunc(in.Username, unicode.IsSpace) >= 0 {
		return User{}, badInput("帳號必填，最多 50 字且不能有空白")
	}
	if err := checkPassword(in.Password); err != nil {
		return User{}, err
	}
	if err := checkPerms(in.Permissions); err != nil {
		return User{}, err
	}
	perms := normalizePerms(in.Permissions)
	if in.IsSuper && !actor.IsSuper {
		return User{}, policy("只有超級管理員能新增超級管理員")
	}
	if in.IsOwner && !actor.ownerLevel() {
		return User{}, policy("只有店長能新增店長")
	}
	if !actor.ownerLevel() {
		if err := checkGrant(actor, perms); err != nil {
			return User{}, err
		}
	}
	accounts := normalizeAccounts(in.IGAccountIDs)
	if in.IsOwner || in.IsSuper {
		accounts = nil
	} else {
		if err := checkAccountGrant(actor, accounts); err != nil {
			return User{}, err
		}
	}
	if err := s.checkAccountsExist(ctx, accounts); err != nil {
		return User{}, err
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = in.Username
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	u, err := scanUser(s.DB.QueryRow(ctx, `
INSERT INTO admin_users (username, password_hash, display_name, is_owner, permissions, ig_account_ids, is_super)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+userCols, in.Username, string(hash), name, in.IsOwner, perms, accounts, in.IsSuper))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrUsernameTaken
	}
	return u, err
}

// UpdateAdmin 改顯示名稱／超管、店長身分／權限／停用。規則：
//   - 只有超管能動超管帳號、變更任何人的超管身分；超管可管理所有人（含店長）
//   - 非店長（且非超管）不能動店長帳號、不能變更任何人的店長身分、不能授予自己沒有的權限
//   - 不能停用自己、不能拿掉自己的「管理員」權限
//   - 最後一位啟用中的店長、最後一位啟用中的超管不能被停用或降級
//   - 只能看部分 IG 帳號的人只能授予自己看得到的帳號，不能授予「全部」
func (s *Service) UpdateAdmin(ctx context.Context, actor User, id int64, p AdminPatch) (User, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)

	// 先鎖住所有啟用中的店長與超管，避免兩人同時互相降級／停用而變成零位店長（超管）
	type head struct {
		ID             int64
		Owner, IsSuper bool
	}
	rows, err := tx.Query(ctx, `SELECT id, is_owner, is_super FROM admin_users WHERE (is_owner OR is_super) AND disabled_at IS NULL
ORDER BY id FOR UPDATE`)
	if err != nil {
		return User{}, err
	}
	heads, err := pgx.CollectRows(rows, pgx.RowToStructByPos[head])
	if err != nil {
		return User{}, err
	}

	var stored []string        // 原始存的權限（店長的有效權限是全部，降級時以此為基礎）
	var storedAccounts []int64 // 原始存的可見帳號（同上）
	target, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+`, permissions, ig_account_ids FROM admin_users WHERE id = $1 FOR UPDATE`, id),
		&stored, &storedAccounts)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}

	if target.IsSuper && !actor.IsSuper {
		return User{}, policy("只有超級管理員能修改超級管理員帳號")
	}
	if target.IsOwner && !actor.ownerLevel() {
		return User{}, policy("只有店長能修改店長帳號")
	}
	newSuper := target.IsSuper
	if p.IsSuper != nil && *p.IsSuper != target.IsSuper {
		if !actor.IsSuper {
			return User{}, policy("只有超級管理員能變更超級管理員身分")
		}
		newSuper = *p.IsSuper
	}
	newOwner := target.IsOwner
	if p.IsOwner != nil && *p.IsOwner != target.IsOwner {
		if !actor.ownerLevel() {
			return User{}, policy("只有店長能變更店長身分")
		}
		newOwner = *p.IsOwner
	}
	newPerms := normalizePerms(stored)
	if p.Permissions != nil {
		if err := checkPerms(*p.Permissions); err != nil {
			return User{}, err
		}
		newPerms = normalizePerms(*p.Permissions)
		if !actor.ownerLevel() {
			var added []string
			for _, perm := range newPerms {
				if !target.Can(perm) {
					added = append(added, perm)
				}
			}
			if err := checkGrant(actor, added); err != nil {
				return User{}, err
			}
		}
	}
	newAccounts := normalizeAccounts(storedAccounts)
	if p.IGAccountIDs != nil {
		newAccounts = normalizeAccounts(*p.IGAccountIDs)
		if !slices.Equal(newAccounts, target.IGAccountIDs) || target.ownerLevel() {
			if err := checkAccountGrant(actor, newAccounts); err != nil {
				return User{}, err
			}
		}
		if err := s.checkAccountsExist(ctx, newAccounts); err != nil {
			return User{}, err
		}
	}
	if newOwner || newSuper {
		newAccounts = nil
	}
	newDisabled := target.Disabled
	if p.Disabled != nil {
		newDisabled = *p.Disabled
	}
	newName := target.DisplayName
	if p.DisplayName != nil {
		newName = strings.TrimSpace(*p.DisplayName)
		if newName == "" {
			newName = target.Username
		}
	}

	if target.ID == actor.ID {
		if newDisabled {
			return User{}, policy("不能停用自己")
		}
		if !newOwner && !newSuper && !slices.Contains(newPerms, PermAdmins) {
			return User{}, policy("不能移除自己的「管理員」權限")
		}
	}
	others := func(super bool) int {
		n := 0
		for _, h := range heads {
			if h.ID != target.ID && ((super && h.IsSuper) || (!super && h.Owner)) {
				n++
			}
		}
		return n
	}
	if target.IsOwner && !target.Disabled && (!newOwner || newDisabled) && others(false) == 0 {
		return User{}, policy("至少要保留一位啟用中的店長")
	}
	if target.IsSuper && !target.Disabled && (!newSuper || newDisabled) && others(true) == 0 {
		return User{}, policy("至少要保留一位啟用中的超級管理員")
	}

	u, err := scanUser(tx.QueryRow(ctx, `
UPDATE admin_users SET display_name = $2, is_owner = $3, permissions = $4,
  disabled_at = CASE WHEN NOT $5 THEN NULL ELSE COALESCE(disabled_at, now()) END, ig_account_ids = $6,
  session_id = CASE WHEN $5 THEN NULL ELSE session_id END, is_super = $7
WHERE id = $1 RETURNING `+userCols, id, newName, newOwner, newPerms, newDisabled, newAccounts, newSuper))
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit(ctx)
}

// ResetPassword 由管理員直接重設別人的密碼（自己的密碼請用 ChangePassword，需驗舊密碼）。
// 同時撤銷對方目前的 session（被重設的人要用新密碼重新登入）。
func (s *Service) ResetPassword(ctx context.Context, actor User, id int64, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	if id == actor.ID {
		return policy("請用「管理員密碼」修改自己的密碼")
	}
	target, err := s.getUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if err := checkManage(actor, target); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE admin_users SET password_hash = $2, session_id = NULL, mfa_pending_id = NULL WHERE id = $1`,
		id, string(hash))
	return err
}

// checkManage：超管帳號只有超管能動；店長帳號只有店長、超管能動。
func checkManage(actor, target User) error {
	if target.IsSuper && !actor.IsSuper {
		return policy("只有超級管理員能修改超級管理員帳號")
	}
	if target.IsOwner && !actor.ownerLevel() {
		return policy("只有店長能修改店長帳號")
	}
	return nil
}
