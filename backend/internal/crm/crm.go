// Package crm 管顧客資料：標籤、備註、互動統計。
package crm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

type Tag struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	Count int    `json:"count"`
}

type Contact struct {
	ID             int64     `json:"id"`
	IGAccountID    int64     `json:"igAccountId"` // 傳訊給哪個我方 IG 帳號
	IGSID          string    `json:"igsid"`
	Username       string    `json:"username"`
	Name           string    `json:"name"`
	AvatarURL      string    `json:"avatarUrl"`
	Note           string    `json:"note"`
	FirstSeenAt    time.Time `json:"firstSeenAt"`
	LastSeenAt     time.Time `json:"lastSeenAt"`
	ConversationID *int64    `json:"conversationId"`
	Handler        string    `json:"handler"`
	InboundCount   int       `json:"inboundCount"`
	LastIntent     string    `json:"lastIntent"`
	Tags           []Tag     `json:"tags"`
}

type IntentCount struct {
	Intent string `json:"intent"`
	Count  int    `json:"count"`
}

type ContactDetail struct {
	Contact
	Intents       []IntentCount `json:"intents"`
	OutboundByAI  int           `json:"outboundByAuto"`
	OutboundHuman int           `json:"outboundByHuman"`
}

type Service struct{ DB *pgxpool.Pool }

// 以 tag 聚合成 JSON，避免 N+1
const contactSelect = `
SELECT ct.id, ct.ig_account_id, ct.igsid, ct.username, ct.name, ct.avatar_url, ct.note, ct.first_seen_at, ct.last_seen_at,
       cv.id, COALESCE(cv.handler, ''),
       (SELECT count(*) FROM messages m WHERE m.conversation_id = cv.id AND m.direction = 'in'),
       COALESCE((SELECT m.meta->>'intent' FROM messages m
                 WHERE m.conversation_id = cv.id AND m.direction = 'in' AND m.meta ? 'intent'
                 ORDER BY m.created_at DESC LIMIT 1), ''),
       COALESCE((SELECT json_agg(json_build_object('id', t.id, 'name', t.name, 'color', t.color) ORDER BY t.name)
                 FROM contact_tags x JOIN tags t ON t.id = x.tag_id WHERE x.contact_id = ct.id), '[]')
FROM contacts ct LEFT JOIN conversations cv ON cv.contact_id = ct.id`

func scanContact(row pgx.Row) (Contact, error) {
	var c Contact
	err := row.Scan(&c.ID, &c.IGAccountID, &c.IGSID, &c.Username, &c.Name, &c.AvatarURL, &c.Note, &c.FirstSeenAt, &c.LastSeenAt,
		&c.ConversationID, &c.Handler, &c.InboundCount, &c.LastIntent, &c.Tags)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

type ListFilter struct {
	Q        string
	TagID    int64
	Sort     string  // recent | active | new
	Accounts []int64 // 只列這些 IG 帳號的顧客；nil＝全部
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]Contact, error) {
	order := "ct.last_seen_at DESC"
	switch f.Sort {
	case "new":
		order = "ct.first_seen_at DESC"
	case "active":
		order = "12 DESC, ct.last_seen_at DESC" // inbound count
	}
	rows, err := s.DB.Query(ctx, contactSelect+`
WHERE ($1 = '' OR ct.username ILIKE '%' || $1 || '%' OR ct.name ILIKE '%' || $1 || '%' OR ct.note ILIKE '%' || $1 || '%')
  AND ($2 = 0 OR EXISTS (SELECT 1 FROM contact_tags x WHERE x.contact_id = ct.id AND x.tag_id = $2))
  AND ($3::bigint[] IS NULL OR ct.ig_account_id = ANY($3))
ORDER BY `+order+` LIMIT 500`, f.Q, f.TagID, f.Accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (ContactDetail, error) {
	c, err := scanContact(s.DB.QueryRow(ctx, contactSelect+` WHERE ct.id = $1`, id))
	if err != nil {
		return ContactDetail{}, err
	}
	d := ContactDetail{Contact: c, Intents: []IntentCount{}}
	if c.ConversationID == nil {
		return d, nil
	}
	rows, err := s.DB.Query(ctx, `
SELECT meta->>'intent', count(*) FROM messages
WHERE conversation_id = $1 AND direction = 'in' AND meta ? 'intent'
GROUP BY 1 ORDER BY 2 DESC`, *c.ConversationID)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var ic IntentCount
		if err := rows.Scan(&ic.Intent, &ic.Count); err != nil {
			return d, err
		}
		d.Intents = append(d.Intents, ic)
	}
	err = s.DB.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE sender_type IN ('ai','bot')), count(*) FILTER (WHERE sender_type = 'human')
FROM messages WHERE conversation_id = $1 AND direction = 'out'`, *c.ConversationID).Scan(&d.OutboundByAI, &d.OutboundHuman)
	return d, err
}

// Update 改備註與整組標籤（tagIDs 為 nil 表示不動標籤）。
func (s *Service) Update(ctx context.Context, id int64, note *string, tagIDs []int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contacts WHERE id = $1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if note != nil {
		if _, err := tx.Exec(ctx, `UPDATE contacts SET note = $2 WHERE id = $1`, id, strings.TrimSpace(*note)); err != nil {
			return err
		}
	}
	if tagIDs != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM contact_tags WHERE contact_id = $1 AND NOT (tag_id = ANY($2))`, id, tagIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO contact_tags (contact_id, tag_id) SELECT $1, t.id FROM tags t WHERE t.id = ANY($2)
ON CONFLICT DO NOTHING`, id, tagIDs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- tags ----

var palette = []string{"#8b5e3c", "#c8915a", "#a2504a", "#5f7f5a", "#6b6fa8", "#b07a9b", "#3f7f86", "#b7791f"}

// Tags 回全部標籤（標籤本身全店共用）；Count 只算 accounts 內的顧客（nil＝全部帳號）。
func (s *Service) Tags(ctx context.Context, accounts []int64) ([]Tag, error) {
	rows, err := s.DB.Query(ctx, `
SELECT t.id, t.name, t.color,
       count(x.contact_id) FILTER (WHERE $1::bigint[] IS NULL OR ct.ig_account_id = ANY($1))
FROM tags t LEFT JOIN contact_tags x ON x.tag_id = t.id LEFT JOIN contacts ct ON ct.id = x.contact_id
GROUP BY t.id ORDER BY t.name`, accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateTag 建立標籤；同名已存在時直接回傳既有的。
func (s *Service) CreateTag(ctx context.Context, name, color string) (Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 20 {
		return Tag{}, errors.New("標籤名稱需為 1～20 字")
	}
	if color == "" {
		var n int
		_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM tags`).Scan(&n)
		color = palette[n%len(palette)]
	}
	var t Tag
	err := s.DB.QueryRow(ctx, `
INSERT INTO tags (name, color) VALUES ($1, $2)
ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
RETURNING id, name, color`, name, color).Scan(&t.ID, &t.Name, &t.Color)
	return t, err
}

func (s *Service) UpdateTag(ctx context.Context, id int64, name, color string) (Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 20 {
		return Tag{}, errors.New("標籤名稱需為 1～20 字")
	}
	var t Tag
	err := s.DB.QueryRow(ctx, `UPDATE tags SET name = $2, color = $3 WHERE id = $1 RETURNING id, name, color`, id, name, color).
		Scan(&t.ID, &t.Name, &t.Color)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return t, errors.New("已有同名標籤")
	}
	return t, err
}

func (s *Service) DeleteTag(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM tags WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
