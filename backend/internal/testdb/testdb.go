// Package testdb 是整合測試共用的小工具：連測試庫、建已連結的 IG 帳號。只給 _test.go 用。
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
)

// Open 連 TEST_DATABASE_URL（未設時跳過測試）並跑完 migration。
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ConnectedAccount 建一個掛在連線上、啟用中的 IG 帳號（webhook 才收），回 ig_accounts.id。
// 連線的 token 是假密文：要測 token 解密請走 account.Service.Connect。
func ConnectedAccount(t *testing.T, pool *pgxpool.Pool, igUserID string) int64 {
	t.Helper()
	ctx := context.Background()
	var connID, id int64
	if err := pool.QueryRow(ctx, `
INSERT INTO ig_connections (client_business_id, system_user_id, access_token) VALUES ('biz-' || $1, 'su-' || $1, 'test')
RETURNING id`, igUserID).Scan(&connID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO ig_accounts (ig_user_id, username, connection_id, connected_at) VALUES ($1, $1, $2, now())
ON CONFLICT (ig_user_id) DO UPDATE SET connection_id = EXCLUDED.connection_id, active = true
RETURNING id`, igUserID, connID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
