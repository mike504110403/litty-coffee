package inbox

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 顧客標記（contacts.risk）：
//   - watch：問題顧客，只是提醒，AI 照常回
//   - blocked：黑名單，訊息照收（留在收件匣），但不跑規則與 AI、不發轉真人通知；管理員仍可手動回覆
//
// 來源：manual 管理員、rate 短時間大量訊息（只標 watch＋降頻，不封鎖）、ai AI 判斷騷擾（封鎖）。
// 解除（risk 清空）時保留是誰、何時解除。
const (
	RiskNone    = ""
	RiskWatch   = "watch"
	RiskBlocked = "blocked"

	RiskSourceManual = "manual"
	RiskSourceRate   = "rate"
	RiskSourceAI     = "ai"
)

var ErrBadRisk = errors.New("標記必須是 blocked、watch 或空白（解除）")

const maxRiskReason = 200

// SetRisk 由管理員設定或解除顧客標記（adminID＝操作者）。找不到顧客回 ErrNotFound。
func (s *Service) SetRisk(ctx context.Context, contactID int64, risk, reason string, adminID int64) error {
	if risk != RiskNone && risk != RiskWatch && risk != RiskBlocked {
		return ErrBadRisk
	}
	var admin *int64
	if adminID > 0 {
		admin = &adminID
	}
	tag, err := s.DB.Exec(ctx, `
UPDATE contacts SET risk = $2, risk_reason = $3, risk_source = 'manual', risk_at = now(), risk_by_admin_id = $4
WHERE id = $1`, contactID, risk, clipReason(reason), admin)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.publishContactConversation(ctx, contactID)
	return nil
}

// AutoBlock：AI 判斷騷擾／惡意時自動列入黑名單（來源 ai）；已在黑名單的不覆蓋（保留原本的原因與來源）。
// 回傳是否真的改了。
func (s *Service) AutoBlock(ctx context.Context, contactID int64, reason string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `
UPDATE contacts SET risk = 'blocked', risk_reason = $2, risk_source = 'ai', risk_at = now(), risk_by_admin_id = NULL
WHERE id = $1 AND risk <> 'blocked'`, contactID, clipReason(reason))
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	s.publishContactConversation(ctx, contactID)
	return true, nil
}

// AutoWatch：短時間大量訊息時自動標成問題顧客（來源 rate）。只覆蓋「一般」或「之前也是 rate 標的」，
// 管理員手動標的與黑名單不動。回傳是否真的改了。
func (s *Service) AutoWatch(ctx context.Context, contactID int64, reason string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `
UPDATE contacts SET risk = 'watch', risk_reason = $2, risk_source = 'rate', risk_at = now(), risk_by_admin_id = NULL
WHERE id = $1 AND (risk = '' OR (risk = 'watch' AND risk_source = 'rate'))`, contactID, clipReason(reason))
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	s.publishContactConversation(ctx, contactID)
	return true, nil
}

// SetThrottle 設定降頻等級（0＝正常），有變才推 conversation.updated。
func (s *Service) SetThrottle(ctx context.Context, contactID int64, level int) error {
	tag, err := s.DB.Exec(ctx, `UPDATE contacts SET throttle_level = $2 WHERE id = $1 AND throttle_level <> $2`, contactID, level)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	s.publishContactConversation(ctx, contactID)
	return nil
}

// InboundGap 回某則顧客訊息與它前一則顧客訊息的間隔；沒有前一則回 ok=false。
func (s *Service) InboundGap(ctx context.Context, msgID int64) (gap time.Duration, ok bool, err error) {
	var cur, prev time.Time
	err = s.DB.QueryRow(ctx, `
SELECT m.created_at, p.created_at FROM messages m
JOIN LATERAL (
  SELECT q.created_at FROM messages q
  WHERE q.conversation_id = m.conversation_id AND q.direction = 'in' AND (q.created_at, q.id) < (m.created_at, m.id)
  ORDER BY q.created_at DESC, q.id DESC LIMIT 1
) p ON true
WHERE m.id = $1`, msgID).Scan(&cur, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return cur.Sub(prev), true, nil
}

// LastAutoReplyAt 回對話最後一則自動回覆（AI 或規則）的時間；沒有回 nil。
func (s *Service) LastAutoReplyAt(ctx context.Context, convID int64) (*time.Time, error) {
	var t *time.Time
	err := s.DB.QueryRow(ctx, `
SELECT max(created_at) FROM messages WHERE conversation_id = $1 AND direction = 'out' AND sender_type IN ('ai', 'bot')`,
		convID).Scan(&t)
	return t, err
}

// RecentInbound 數對話在 window 內的顧客訊息數（大量訊息判斷用）。
// 管理員解除過標記的，只從解除當下起算，免得一解除又因同一批舊訊息降頻。
func (s *Service) RecentInbound(ctx context.Context, convID int64, window time.Duration) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `
SELECT count(*) FROM messages m
JOIN conversations c ON c.id = m.conversation_id JOIN contacts ct ON ct.id = c.contact_id
WHERE m.conversation_id = $1 AND m.direction = 'in'
  AND m.created_at > now() - make_interval(secs => $2)
  AND (ct.risk <> '' OR ct.risk_at IS NULL OR m.created_at > ct.risk_at)`, convID, window.Seconds()).Scan(&n)
	return n, err
}

// publishContactConversation 推該顧客對話的 conversation.updated（帶最新的標記），收件匣清單與對話頭即時更新。
func (s *Service) publishContactConversation(ctx context.Context, contactID int64) {
	var convID int64
	if err := s.DB.QueryRow(ctx, `SELECT id FROM conversations WHERE contact_id = $1`, contactID).Scan(&convID); err != nil {
		return
	}
	if c, err := s.GetConversation(ctx, convID); err == nil {
		s.publishConversation(c)
	}
}

func clipReason(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxRiskReason {
		s = string(r[:maxRiskReason])
	}
	return s
}
