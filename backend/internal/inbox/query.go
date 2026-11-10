package inbox

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const convSelect = `
SELECT c.id, c.status, c.handler, c.unread_count, c.last_message_at, c.last_inbound_at, c.last_message_text,
       ct.id, ct.igsid, ct.username, ct.name, ct.avatar_url
FROM conversations c JOIN contacts ct ON ct.id = c.contact_id`

func scanConv(row pgx.Row) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.Status, &c.Handler, &c.UnreadCount, &c.LastMessageAt, &c.LastInboundAt, &c.LastMessageText,
		&c.Contact.ID, &c.Contact.IGSID, &c.Contact.Username, &c.Contact.Name, &c.Contact.AvatarURL)
	if c.LastInboundAt != nil {
		t := c.LastInboundAt.Add(ReplyWindow)
		c.WindowExpiresAt = &t
	}
	return c, err
}

type ListFilter struct {
	Status string // open | closed | ""（全部）
	Q      string
	Limit  int
}

func (s *Service) ListConversations(ctx context.Context, f ListFilter) ([]Conversation, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	rows, err := s.DB.Query(ctx, convSelect+`
WHERE ($1 = '' OR c.status = $1)
  AND ($2 = '' OR ct.username ILIKE '%' || $2 || '%' OR ct.name ILIKE '%' || $2 || '%' OR c.last_message_text ILIKE '%' || $2 || '%')
ORDER BY c.last_message_at DESC NULLS LAST
LIMIT $3`, f.Status, f.Q, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	c, err := scanConv(s.DB.QueryRow(ctx, convSelect+` WHERE c.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListMessages 回傳時間正序；beforeID>0 時往前翻頁。
func (s *Service) ListMessages(ctx context.Context, convID, beforeID int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `
SELECT id, conversation_id, direction, sender_type, kind, text, media_url, created_at FROM (
  SELECT * FROM messages WHERE conversation_id = $1 AND ($2 = 0 OR id < $2)
  ORDER BY created_at DESC, id DESC LIMIT $3
) m ORDER BY created_at, id`, convID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Direction, &m.SenderType, &m.Kind, &m.Text, &m.MediaURL, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) MarkRead(ctx context.Context, id int64) error {
	return s.updateConv(ctx, id, `UPDATE conversations SET unread_count = 0 WHERE id = $1`)
}

func (s *Service) SetHandler(ctx context.Context, id int64, handler string) error {
	if handler != "bot" && handler != "human" {
		return errors.New("handler must be bot or human")
	}
	return s.updateConv(ctx, id, `UPDATE conversations SET handler = $2 WHERE id = $1`, handler)
}

func (s *Service) SetStatus(ctx context.Context, id int64, status string) error {
	if status != "open" && status != "closed" {
		return errors.New("status must be open or closed")
	}
	return s.updateConv(ctx, id, `UPDATE conversations SET status = $2 WHERE id = $1`, status)
}

func (s *Service) updateConv(ctx context.Context, id int64, sql string, args ...any) error {
	tag, err := s.DB.Exec(ctx, sql, append([]any{id}, args...)...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if c, err := s.GetConversation(ctx, id); err == nil {
		s.publishConversation(c)
	}
	return nil
}
