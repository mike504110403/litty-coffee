// Package audit 是後台操作紀錄：誰、何時、從哪、做了什麼、成功與否。
// 寫入點：httpapi 的異動請求 middleware、登入／登出 handler、auth.Middleware（被踢）、IG OAuth callback。
// 背景自動動作（AI 回覆、自動黑名單、降頻）不記；那些紀錄在 messages／contacts。
package audit

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention 是紀錄保留期間。
const Retention = 365 * 24 * time.Hour

type Store struct {
	DB *pgxpool.Pool
}

// Entry 是一筆待寫入的紀錄。
type Entry struct {
	AdminID     int64 // 0＝無（登入失敗找不到帳號）
	AdminName   string
	Action      string
	Summary     string // 空＝用動作中文名稱
	TargetType  string
	TargetID    string
	IGAccountID int64 // 0＝不屬於單一帳號（所有有「操作紀錄」權限的人都看得到）
	Detail      map[string]any
	IP          string
	UserAgent   string
	Status      int
	Success     bool
}

// FromRequest 填入請求的 IP 與 User-Agent。
func (e Entry) FromRequest(r *http.Request) Entry {
	e.IP = ClientIP(r)
	e.UserAgent = UserAgent(r)
	return e
}

func nullID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

func (e Entry) args() []any {
	if e.Summary == "" {
		e.Summary = ActionLabel(e.Action)
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	return []any{nullID(e.AdminID), e.AdminName, e.Action, e.Summary, e.TargetType, e.TargetID,
		nullID(e.IGAccountID), e.Detail, e.IP, e.UserAgent, e.Status, e.Success}
}

const insertCols = `(admin_id, admin_name, action, summary, target_type, target_id, ig_account_id, detail, ip, user_agent, status, success)`

// Record 寫一筆紀錄。nil Store 不做事（測試或未接線時）。寫入失敗只記 log，不影響主流程。
func (s *Store) Record(ctx context.Context, e Entry) {
	if s == nil || s.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.DB.Exec(ctx, `INSERT INTO audit_logs `+insertCols+` VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		e.args()...); err != nil {
		log.Printf("audit: record %s: %v", e.Action, err)
	}
}

// RecordKicked 記「被踢」，同一個舊 session（Detail["session"]）只記一次（DB 唯一索引去重）。
func (s *Store) RecordKicked(ctx context.Context, e Entry) {
	if s == nil || s.DB == nil {
		return
	}
	e.Action = ActionKicked
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.DB.Exec(ctx, `INSERT INTO audit_logs `+insertCols+` VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT ((detail->>'session')) WHERE action = 'auth.kicked' DO NOTHING`, e.args()...); err != nil {
		log.Printf("audit: record kicked: %v", err)
	}
}

// Log 是一筆讀出的紀錄。
type Log struct {
	ID          int64          `json:"id"`
	AdminID     *int64         `json:"adminId"`
	AdminName   string         `json:"adminName"`
	Action      string         `json:"action"`
	ActionName  string         `json:"actionName"`
	Summary     string         `json:"summary"`
	TargetType  string         `json:"targetType"`
	TargetID    string         `json:"targetId"`
	TargetLabel string         `json:"targetLabel"`
	IGAccountID *int64         `json:"igAccountId"`
	Detail      map[string]any `json:"detail"`
	IP          string         `json:"ip"`
	UserAgent   string         `json:"userAgent"`
	Status      int            `json:"status"`
	Success     bool           `json:"success"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// Filter 是清單條件。
type Filter struct {
	AdminID int64
	Action  string
	From    *time.Time // 含
	To      *time.Time // 不含
	// Account 指定單一帳號（只回該帳號的紀錄）；0＝不指定
	Account int64
	// Scope 是登入者可見帳號；nil＝全部。非 nil 時只回「不屬於任何帳號」或屬於這些帳號的紀錄
	Scope    []int64
	Page     int // 從 1 起
	PageSize int
}

// List 依條件分頁列出（新的在前），並回總筆數。
func (s *Store) List(ctx context.Context, f Filter) ([]Log, int, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if f.AdminID > 0 {
		add("admin_id = ?", f.AdminID)
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.From != nil {
		add("created_at >= ?", *f.From)
	}
	if f.To != nil {
		add("created_at < ?", *f.To)
	}
	if f.Account > 0 {
		add("ig_account_id = ?", f.Account)
	}
	if f.Scope != nil {
		add("(ig_account_id IS NULL OR ig_account_id = ANY(?))", f.Scope)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM audit_logs`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.PageSize <= 0 {
		f.PageSize = 50
	}
	if f.PageSize > 200 {
		f.PageSize = 200
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.DB.Query(ctx, `
SELECT id, admin_id, admin_name, action, summary, target_type, target_id, ig_account_id, detail, ip, user_agent,
       status, success, created_at
FROM audit_logs`+cond+` ORDER BY created_at DESC, id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Log, error) {
		var l Log
		var detail []byte
		err := row.Scan(&l.ID, &l.AdminID, &l.AdminName, &l.Action, &l.Summary, &l.TargetType, &l.TargetID,
			&l.IGAccountID, &detail, &l.IP, &l.UserAgent, &l.Status, &l.Success, &l.CreatedAt)
		if err != nil {
			return l, err
		}
		l.ActionName = ActionLabel(l.Action)
		l.TargetLabel = TargetLabel(l.TargetType, l.TargetID)
		l.Detail = map[string]any{}
		_ = json.Unmarshal(detail, &l.Detail)
		return l, nil
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Cleanup 刪掉超過保留期間的紀錄，回刪除筆數。
func (s *Store) Cleanup(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM audit_logs WHERE created_at < $1`, now.Add(-Retention))
	return tag.RowsAffected(), err
}

// RunCleanup 啟動時清一次，之後每天清一次，直到 ctx 結束。
func (s *Store) RunCleanup(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if n, err := s.Cleanup(ctx, time.Now()); err != nil {
			if ctx.Err() == nil {
				log.Printf("audit: cleanup: %v", err)
			}
		} else if n > 0 {
			log.Printf("audit: 清掉 %d 筆超過 365 天的操作紀錄", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
