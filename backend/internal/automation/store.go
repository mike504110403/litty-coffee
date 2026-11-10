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

type Store struct{ DB *pgxpool.Pool }

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

func (s *Store) ListRules(ctx context.Context, onlyEnabled bool) ([]Rule, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+ruleCols+` FROM auto_rules WHERE ($1 = false OR enabled) ORDER BY priority, id`, onlyEnabled)
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

func (s *Store) CreateRule(ctx context.Context, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	return scanRule(s.DB.QueryRow(ctx, `
INSERT INTO auto_rules (name, match_type, keywords, reply, handoff, enabled, priority)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+ruleCols,
		r.Name, r.MatchType, r.Keywords, r.Reply, r.Handoff, r.Enabled, r.Priority))
}

func (s *Store) UpdateRule(ctx context.Context, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return r, err
	}
	return scanRule(s.DB.QueryRow(ctx, `
UPDATE auto_rules SET name=$2, match_type=$3, keywords=$4, reply=$5, handoff=$6, enabled=$7, priority=$8, updated_at=now()
WHERE id=$1 RETURNING `+ruleCols,
		r.ID, r.Name, r.MatchType, r.Keywords, r.Reply, r.Handoff, r.Enabled, r.Priority))
}

func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM auto_rules WHERE id=$1`, id)
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

func (s *Store) ListKnowledge(ctx context.Context) ([]Knowledge, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, title, content, enabled, sort, updated_at FROM knowledge_items ORDER BY sort, id`)
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

func (s *Store) SaveKnowledge(ctx context.Context, k Knowledge) (Knowledge, error) {
	k.Title = strings.TrimSpace(k.Title)
	if k.Title == "" {
		return k, errors.New("請填標題")
	}
	if k.ID == 0 {
		return scanKnowledge(s.DB.QueryRow(ctx, `
INSERT INTO knowledge_items (title, content, enabled, sort) VALUES ($1, $2, $3, $4)
RETURNING id, title, content, enabled, sort, updated_at`, k.Title, k.Content, k.Enabled, k.Sort))
	}
	return scanKnowledge(s.DB.QueryRow(ctx, `
UPDATE knowledge_items SET title=$2, content=$3, enabled=$4, sort=$5, updated_at=now() WHERE id=$1
RETURNING id, title, content, enabled, sort, updated_at`, k.ID, k.Title, k.Content, k.Enabled, k.Sort))
}

func (s *Store) DeleteKnowledge(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM knowledge_items WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- settings ----

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var st Settings
	err := s.DB.QueryRow(ctx, `SELECT ai_enabled, persona, handoff_message, debounce_secs FROM automation_settings WHERE id=1`).
		Scan(&st.AIEnabled, &st.Persona, &st.HandoffMessage, &st.DebounceSecs)
	return st, err
}

func (s *Store) SaveSettings(ctx context.Context, st Settings) (Settings, error) {
	if st.DebounceSecs < 0 || st.DebounceSecs > 30 {
		return st, errors.New("合併等待秒數需在 0～30")
	}
	_, err := s.DB.Exec(ctx, `
UPDATE automation_settings SET ai_enabled=$1, persona=$2, handoff_message=$3, debounce_secs=$4, updated_at=now() WHERE id=1`,
		st.AIEnabled, strings.TrimSpace(st.Persona), strings.TrimSpace(st.HandoffMessage), st.DebounceSecs)
	return st, err
}
