package auth

import (
	"context"
	"testing"
)

// migration 00016 建了 superAdmin：表不再是空的，EnsureAdmin 仍要建立 env 的店長（且不是超管）；已有店長就不再建。
func TestEnsureAdminIgnoresSuperAdmins(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO admin_users (username, password_hash, is_super, must_change_password) VALUES ('superAdmin', 'x', true, true)`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAdmin(ctx, "admin", "admin-password"); err != nil {
		t.Fatal(err)
	}
	u, _, err := s.Login(ctx, "admin", "admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsOwner || u.IsSuper || u.SetupRequired {
		t.Fatalf("env admin: %+v", u)
	}
	if err := s.EnsureAdmin(ctx, "other", "other-password"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&n)
	if n != 2 {
		t.Fatalf("admins: %d", n)
	}
}

// 超管：全部權限、全部帳號；首次鎖定＝必須改密碼或未綁 GA。
func TestSuperUserFlags(t *testing.T) {
	sup := User{IsSuper: true, IGAccountIDs: nil}
	if !sup.Can(PermAdmins) || !sup.AllAccounts() || !sup.CanSeeAccount(42) || !sup.ownerLevel() {
		t.Fatal("super should have everything")
	}
	staff := User{Permissions: []string{PermInbox}, IGAccountIDs: []int64{1}}
	if staff.Can(PermIGAccounts) || staff.Can(PermCredentials) || staff.AllAccounts() {
		t.Fatal("staff")
	}
	for _, p := range AllPerms {
		if p == PermIGAccounts || p == PermCredentials {
			t.Fatalf("%s must not be grantable", p)
		}
	}
	if normalizePerms([]string{PermIGAccounts, PermInbox, PermCredentials})[0] != PermInbox {
		t.Fatal("leftover super-only keys should be dropped")
	}
	if checkPerms([]string{PermIGAccounts}) == nil {
		t.Fatal("granting ig_accounts should be rejected")
	}
}
