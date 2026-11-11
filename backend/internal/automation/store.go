package automation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Rule struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	MatchType string     `json:"matchType"`
	Keywords  []string   `json:"keywords"`
	Reply     string     `json:"reply"`
	Handoff   bool       `json:"handoff"`
	Enabled   bool       `json:"enabled"`
	Priority  int        `json:"priority"`
	HitCount  int64      `json:"hitCount"`
	LastHitAt *time.Time `json:"lastHitAt"`
}

type Knowledge struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Enabled   bool      `json:"enabled"`
	Sort      int       `json:"sort"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Settings struct {
	AIEnabled      bool   `json:"aiEnabled"`
	Persona        string `json:"persona"`
	HandoffMessage string `json:"handoffMessage"`
	DebounceSecs   int    `json:"debounceSecs"`
}

// Store 的資料都依 IG 帳號（ig_accounts.id）區分；ig_account_id IS NULL 的是「範本」，
// 帳號第一次用到時（EnsureAccount）從範本複製一份設定、知識庫、規則。
type Store struct{ DB *pgxpool.Pool }

// EnsureAccount 確保帳號有自己的設定／知識庫／規則；沒有就從範本複製（並行安全：以設定列的 UNIQUE 判定誰複製）。
func (s *Store) EnsureAccount(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return ErrNotFound
	}
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM automation_settings WHERE ig_account_id = $1)`, accountID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var acc int64
	err = tx.QueryRow(ctx, `SELECT id FROM ig_accounts WHERE id = $1`, accountID).Scan(&acc)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO automation_settings (ai_enabled, persona, handoff_message, debounce_secs, ig_account_id)
SELECT ai_enabled, persona, handoff_message, debounce_secs, $1 FROM automation_settings WHERE ig_account_id IS NULL
ON CONFLICT (ig_account_id) DO NOTHING`, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// 範本設定列不在（理論上不會）時用預設值；或別人剛建好了
		tag, err = tx.Exec(ctx, `INSERT INTO automation_settings (ig_account_id) VALUES ($1) ON CONFLICT (ig_account_id) DO NOTHING`, accountID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO knowledge_items (title, content, enabled, sort, ig_account_id)
SELECT title, content, enabled, sort, $1 FROM knowledge_items WHERE ig_account_id IS NULL ORDER BY id`, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO auto_rules (name, match_type, keywords, reply, handoff, enabled, priority, ig_account_id)
SELECT name, match_type, keywords, reply, handoff, enabled, priority, $1 FROM auto_rules WHERE ig_account_id IS NULL ORDER BY id`, accountID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- rules ----

func (r *Rule) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Reply = strings.TrimSpace(r.Reply)
	kws := r.Keywords[:0]
	for _, k := range r.Keywords {
		if k = strings.TrimSpace(k); k != "" {
			kws = append(kws, k)
		}
	}
	r.Keywords = kws
	switch {
	case r.Name == "":
		return errors.New("請填規則名稱")
	case len(r.Keywords) == 0:
		return errors.New("至少要一個關鍵字")
	case r.Reply == "" && !r.Handoff:
		return errors.New("請填回覆內容，或勾選轉真人")
	}
	switch r.MatchType {
	case "exact", "contains":
	case "regex":
		for _, k := range r.Keywords {
			if _, err := regexp.Compile(k); err != nil {
				return fmt.Errorf("正則 %q 不合法：%v", k, err)
			}
		}
	default:
		return errors.New("比對方式必須是 exact、contains 或 regex")
	}
	return nil
}

const ruleCols = `id, name, match_type, keywords, reply, handoff, enabled, priority, hit_count, last_hit_at`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	err := row.Scan(&r.ID, &r.Name, &r.MatchType, &r.Keywords, &r.Reply, &r.Handoff, &r.Enabled, &r.Priority, &r.HitCount, &r.LastHitAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) ListRules(ctx context.Context, accountID int64, onlyEnabled bool) ([]Rule, error) {
	if err := s.EnsureAccount(ctx, accountID); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT `+ruleCols+` FROM auto_rules WHERE ig_account_id = $2 AND ($1 = false OR enabled) ORDER BY priority, id`,
		onlyEnabled, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRule(ctx context.Context, accountID int64, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	if err := s.EnsureAccount(ctx, accountID); err != nil {
		return r, err
	}
	return scanRule(s.DB.QueryRow(ctx, `
INSERT INTO auto_rules (name, match_type, keywords, reply, handoff, enabled, priority, ig_account_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+ruleCols,
		r.Name, r.MatchType, r.Keywords, r.Reply, r.Handoff, r.Enabled, r.Priority, accountID))
}

// UpdateRule 只改得到該帳號自己的規則（別的帳號的回 ErrNotFound）。
func (s *Store) UpdateRule(ctx context.Context, accountID int64, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	return scanRule(s.DB.QueryRow(ctx, `
UPDATE auto_rules SET name=$2, match_type=$3, keywords=$4, reply=$5, handoff=$6, enabled=$7, priority=$8, updated_at=now()
WHERE id=$1 AND ig_account_id=$9 RETURNING `+ruleCols,
		r.ID, r.Name, r.MatchType, r.Keywords, r.Reply, r.Handoff, r.Enabled, r.Priority, accountID))
}

func (s *Store) DeleteRule(ctx context.Context, accountID, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM auto_rules WHERE id=$1 AND ig_account_id=$2`, id, accountID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RecordHit(ctx context.Context, id int64) {
	_, _ = s.DB.Exec(ctx, `UPDATE auto_rules SET hit_count = hit_count + 1, last_hit_at = now() WHERE id=$1`, id)
}

// ---- knowledge ----

func scanKnowledge(row pgx.Row) (Knowledge, error) {
	var k Knowledge
	err := row.Scan(&k.ID, &k.Title, &k.Content, &k.Enabled, &k.Sort, &k.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

func (s *Store) ListKnowledge(ctx context.Context, accountID int64) ([]Knowledge, error) {
	if err := s.EnsureAccount(ctx, accountID); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id, title, content, enabled, sort, updated_at FROM knowledge_items
WHERE ig_account_id = $1 ORDER BY sort, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Knowledge{}
	for rows.Next() {
		k, err := scanKnowledge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) SaveKnowledge(ctx context.Context, accountID int64, k Knowledge) (Knowledge, error) {
	k.Title = strings.TrimSpace(k.Title)
	if k.Title == "" {
		return k, errors.New("請填標題")
	}
	if err := s.EnsureAccount(ctx, accountID); err != nil {
		return k, err
	}
	if k.ID == 0 {
		return scanKnowledge(s.DB.QueryRow(ctx, `
INSERT INTO knowledge_items (title, content, enabled, sort, ig_account_id) VALUES ($1, $2, $3, $4, $5)
RETURNING id, title, content, enabled, sort, updated_at`, k.Title, k.Content, k.Enabled, k.Sort, accountID))
	}
	return scanKnowledge(s.DB.QueryRow(ctx, `
UPDATE knowledge_items SET title=$2, content=$3, enabled=$4, sort=$5, updated_at=now() WHERE id=$1 AND ig_account_id=$6
RETURNING id, title, content, enabled, sort, updated_at`, k.ID, k.Title, k.Content, k.Enabled, k.Sort, accountID))
}

func (s *Store) DeleteKnowledge(ctx context.Context, accountID, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM knowledge_items WHERE id=$1 AND ig_account_id=$2`, id, accountID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- settings ----

func (s *Store) GetSettings(ctx context.Context, accountID int64) (Settings, error) {
	var st Settings
	if err := s.EnsureAccount(ctx, accountID); err != nil {
		return st, err
	}
	err := s.DB.QueryRow(ctx, `SELECT ai_enabled, persona, handoff_message, debounce_secs FROM automation_settings WHERE ig_account_id=$1`,
		accountID).Scan(&st.AIEnabled, &st.Persona, &st.HandoffMessage, &st.DebounceSecs)
	return st, err
}

func (s *Store) SaveSettings(ctx context.Context, accountID int64, st Settings) (Settings, error) {
	if st.DebounceSecs < 0 || st.DebounceSecs > 30 {
		return st, errors.New("合併等待秒數需在 0～30")
	}
	if err := s.EnsureAccount(ctx, accountID); err != nil {
		return st, err
	}
	st.Persona, st.HandoffMessage = strings.TrimSpace(st.Persona), strings.TrimSpace(st.HandoffMessage)
	_, err := s.DB.Exec(ctx, `
UPDATE automation_settings SET ai_enabled=$1, persona=$2, handoff_message=$3, debounce_secs=$4, updated_at=now() WHERE ig_account_id=$5`,
		st.AIEnabled, st.Persona, st.HandoffMessage, st.DebounceSecs, accountID)
	return st, err
}
