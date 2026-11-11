package inbox

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const convSelect = `
SELECT c.id, c.status, c.handler, c.unread_count, c.last_message_at, c.last_inbound_at, c.last_message_text, c.contact_read_at,
       ct.ig_account_id, ct.id, ct.igsid, ct.username, ct.name, ct.avatar_url
FROM conversations c JOIN contacts ct ON ct.id = c.contact_id`

func scanConv(row pgx.Row) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.Status, &c.Handler, &c.UnreadCount, &c.LastMessageAt, &c.LastInboundAt, &c.LastMessageText, &c.ContactReadAt,
		&c.IGAccountID, &c.Contact.ID, &c.Contact.IGSID, &c.Contact.Username, &c.Contact.Name, &c.Contact.AvatarURL)
	if c.LastInboundAt != nil {
		t := c.LastInboundAt.Add(ReplyWindow)
		c.WindowExpiresAt = &t
	}
	return c, err
}

type ListFilter struct {
	Status   string // open | closed | ""（全部）
	Q        string
	Limit    int
	Accounts []int64 // 只列這些 IG 帳號（ig_accounts.id）；nil＝全部
}

func (s *Service) ListConversations(ctx context.Context, f ListFilter) ([]Conversation, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	rows, err := s.DB.Query(ctx, convSelect+`
WHERE ($1 = '' OR c.status = $1)
  AND ($2 = '' OR ct.username ILIKE '%' || $2 || '%' OR ct.name ILIKE '%' || $2 || '%' OR c.last_message_text ILIKE '%' || $2 || '%')
  AND ($4::bigint[] IS NULL OR ct.ig_account_id = ANY($4))
ORDER BY c.last_message_at DESC NULLS LAST
LIMIT $3`, f.Status, f.Q, f.Limit, f.Accounts)
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
SELECT `+msgCols+` FROM (
  SELECT * FROM messages WHERE conversation_id = $1 AND ($2 = 0 OR id < $2)
  ORDER BY created_at DESC, id DESC LIMIT $3
) m `+msgJoin+` ORDER BY m.created_at, m.id`, convID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const msgCols = `m.id, m.conversation_id, mct.ig_account_id, m.direction, m.sender_type, m.kind, m.text, m.media_url, m.media_file, m.reaction,
  m.deleted_at, m.created_at, m.sent_by_admin_id, COALESCE(NULLIF(au.display_name, ''), au.username, '')`

// msgJoin 帶出送出者（管理員）名稱與所屬 IG 帳號；查詢須以 m 為 messages 別名。
const msgJoin = `LEFT JOIN admin_users au ON au.id = m.sent_by_admin_id
  JOIN conversations mc ON mc.id = m.conversation_id JOIN contacts mct ON mct.id = mc.contact_id`

// MediaRoute 是收件匣附件的後台路徑（需登入）。
const MediaRoute = "/api/inbox/media/"

func scanMsg(row pgx.Row) (Message, error) {
	var m Message
	var file string
	var deleted *time.Time
	var sentByID *int64
	var sentByName string
	err := row.Scan(&m.ID, &m.ConversationID, &m.IGAccountID, &m.Direction, &m.SenderType, &m.Kind, &m.Text, &m.MediaURL, &file,
		&m.Reaction, &deleted, &m.CreatedAt, &sentByID, &sentByName)
	if sentByID != nil {
		m.SentBy = &Sender{ID: *sentByID, DisplayName: sentByName}
	}
	if file != "" {
		m.MediaURL = MediaRoute + file
	}
	m.Deleted = deleted != nil
	return m, err
}

// MediaAccount 回某個收件匣附件檔所屬的 IG 帳號（權限檢查用）；找不到回 ErrNotFound。
func (s *Service) MediaAccount(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(ctx, `
SELECT ct.ig_account_id FROM messages m JOIN conversations c ON c.id = m.conversation_id JOIN contacts ct ON ct.id = c.contact_id
WHERE m.media_file = $1 LIMIT 1`, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Service) GetMessage(ctx context.Context, id int64) (Message, error) {
	return scanMsg(s.DB.QueryRow(ctx, `SELECT `+msgCols+` FROM messages m `+msgJoin+` WHERE m.id = $1`, id))
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
