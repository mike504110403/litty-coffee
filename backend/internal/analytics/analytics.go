// Package analytics 產出後台分析頁與總覽的統計數字。
package analytics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	DB  *pgxpool.Pool
	Loc *time.Location // 店家時區，日界線與時段分布以此計
}

type Totals struct {
	Inbound        int `json:"inbound"`
	OutboundAuto   int `json:"outboundAuto"` // ai + bot
	OutboundHuman  int `json:"outboundHuman"`
	NewContacts    int `json:"newContacts"`
	ActiveContacts int `json:"activeContacts"` // 期間內有傳訊的顧客
	HumanConvs     int `json:"humanConvs"`     // 期間內需要真人回覆的對話
}

// AutoShare 是店家送出的訊息中由自動回覆（規則＋AI）負責的比例；沒有送出時為 nil。
func (t Totals) AutoShare() *float64 {
	n := t.OutboundAuto + t.OutboundHuman
	if n == 0 {
		return nil
	}
	v := float64(t.OutboundAuto) / float64(n)
	return &v
}

type Response struct {
	Turns      int      `json:"turns"` // 顧客開啟的回合數
	Answered   int      `json:"answered"`
	AvgSecs    *float64 `json:"avgSecs"`
	MedianSecs *float64 `json:"medianSecs"`
	AutoAvg    *float64 `json:"autoAvgSecs"`
	HumanAvg   *float64 `json:"humanAvgSecs"`
}

type Period struct {
	Totals    Totals   `json:"totals"`
	AutoShare *float64 `json:"autoShare"`
	Response  Response `json:"response"`
}

type DayPoint struct {
	Date    string `json:"date"` // YYYY-MM-DD（店家時區）
	Inbound int    `json:"inbound"`
	Auto    int    `json:"auto"`
	Human   int    `json:"human"`
}

type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type RuleHit struct {
	Name     string `json:"name"`
	HitCount int64  `json:"hitCount"`
}

type Summary struct {
	From     time.Time  `json:"from"`
	To       time.Time  `json:"to"`
	Days     int        `json:"days"`
	Current  Period     `json:"current"`
	Previous Period     `json:"previous"` // 前一個等長區間，用來算漲跌
	Daily    []DayPoint `json:"daily"`
	Intents  []Count    `json:"intents"`
	Hours    []int      `json:"hours"` // 0～23 時的顧客訊息數
	TopRules []RuleHit  `json:"topRules"`
}

// convIn 回「對話屬於 accounts 參數（第 n 個佔位符）內帳號」的條件；參數為 NULL＝全部帳號。
func convIn(col, n string) string {
	return `(` + n + `::bigint[] IS NULL OR ` + col + ` IN (SELECT c.id FROM conversations c JOIN contacts ct ON ct.id = c.contact_id WHERE ct.ig_account_id = ANY(` + n + `)))`
}

// Summary 統計最近 days 天（含今天，依店家時區切日）；accounts 為 nil＝全部帳號，否則只算這些 IG 帳號。
func (s *Service) Summary(ctx context.Context, days int, now time.Time, accounts []int64) (Summary, error) {
	if days < 1 {
		days = 1
	}
	local := now.In(s.Loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.Loc)
	from := today.AddDate(0, 0, -(days - 1))
	to := today.AddDate(0, 0, 1)
	prevFrom := from.AddDate(0, 0, -days)

	sum := Summary{From: from, To: to, Days: days}
	var err error
	if sum.Current, err = s.period(ctx, from, to, accounts); err != nil {
		return sum, err
	}
	if sum.Previous, err = s.period(ctx, prevFrom, from, accounts); err != nil {
		return sum, err
	}
	if sum.Daily, err = s.daily(ctx, from, days, accounts); err != nil {
		return sum, err
	}
	if sum.Intents, err = s.intents(ctx, from, to, accounts); err != nil {
		return sum, err
	}
	if sum.Hours, err = s.hours(ctx, from, to, accounts); err != nil {
		return sum, err
	}
	sum.TopRules, err = s.topRules(ctx, accounts)
	return sum, err
}

func (s *Service) period(ctx context.Context, from, to time.Time, accounts []int64) (Period, error) {
	var p Period
	t := &p.Totals
	err := s.DB.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE direction = 'in'),
  count(*) FILTER (WHERE direction = 'out' AND sender_type IN ('ai','bot')),
  count(*) FILTER (WHERE direction = 'out' AND sender_type = 'human'),
  count(DISTINCT conversation_id) FILTER (WHERE direction = 'in'),
  count(DISTINCT conversation_id) FILTER (WHERE direction = 'out' AND sender_type = 'human')
FROM messages WHERE created_at >= $1 AND created_at < $2 AND `+convIn("conversation_id", "$3"), from, to, accounts).
		Scan(&t.Inbound, &t.OutboundAuto, &t.OutboundHuman, &t.ActiveContacts, &t.HumanConvs)
	if err != nil {
		return p, err
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE first_seen_at >= $1 AND first_seen_at < $2
  AND ($3::bigint[] IS NULL OR ig_account_id = ANY($3))`, from, to, accounts).
		Scan(&t.NewContacts); err != nil {
		return p, err
	}
	p.AutoShare = t.AutoShare()

	// 回應時間：顧客「開啟一個回合」（前一則不是顧客訊息）到店家第一則回覆的時間；系統事件訊息（sender_type=system）不算回覆、也不切回合
	r := &p.Response
	err = s.DB.QueryRow(ctx, `
WITH m AS (
  SELECT id, conversation_id, direction, created_at,
         lag(direction) OVER (PARTITION BY conversation_id ORDER BY created_at, id) AS prev_dir
  FROM messages WHERE sender_type <> 'system' AND `+convIn("conversation_id", "$3")+`
), starts AS (
  SELECT id, conversation_id, created_at FROM m
  WHERE direction = 'in' AND (prev_dir IS NULL OR prev_dir = 'out') AND created_at >= $1 AND created_at < $2
), resp AS (
  SELECT s.id, r.sender_type, EXTRACT(EPOCH FROM r.created_at - s.created_at)::float8 AS secs
  FROM starts s
  LEFT JOIN LATERAL (
    SELECT o.sender_type, o.created_at FROM messages o
    WHERE o.conversation_id = s.conversation_id AND o.direction = 'out' AND o.sender_type <> 'system' AND (o.created_at, o.id) > (s.created_at, s.id)
    ORDER BY o.created_at, o.id LIMIT 1
  ) r ON true
)
SELECT count(*), count(secs),
       avg(secs), percentile_cont(0.5) WITHIN GROUP (ORDER BY secs),
       avg(secs) FILTER (WHERE sender_type IN ('ai','bot')),
       avg(secs) FILTER (WHERE sender_type = 'human')
FROM resp`, from, to, accounts).Scan(&r.Turns, &r.Answered, &r.AvgSecs, &r.MedianSecs, &r.AutoAvg, &r.HumanAvg)
	return p, err
}

func (s *Service) daily(ctx context.Context, from time.Time, days int, accounts []int64) ([]DayPoint, error) {
	rows, err := s.DB.Query(ctx, `
WITH d AS (
  SELECT generate_series(0, $2 - 1) AS i
)
SELECT to_char(($1::timestamptz AT TIME ZONE $3)::date + d.i, 'YYYY-MM-DD'),
       count(m.id) FILTER (WHERE m.direction = 'in'),
       count(m.id) FILTER (WHERE m.direction = 'out' AND m.sender_type IN ('ai','bot')),
       count(m.id) FILTER (WHERE m.direction = 'out' AND m.sender_type = 'human')
FROM d LEFT JOIN messages m
  ON (m.created_at AT TIME ZONE $3)::date = ($1::timestamptz AT TIME ZONE $3)::date + d.i AND `+convIn("m.conversation_id", "$4")+`
GROUP BY d.i ORDER BY d.i`, from, days, s.Loc.String(), accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayPoint{}
	for rows.Next() {
		var p DayPoint
		if err := rows.Scan(&p.Date, &p.Inbound, &p.Auto, &p.Human); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) intents(ctx context.Context, from, to time.Time, accounts []int64) ([]Count, error) {
	rows, err := s.DB.Query(ctx, `
SELECT meta->>'intent', count(*) FROM messages
WHERE direction = 'in' AND meta ? 'intent' AND created_at >= $1 AND created_at < $2 AND `+convIn("conversation_id", "$3")+`
GROUP BY 1 ORDER BY 2 DESC`, from, to, accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Key, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) hours(ctx context.Context, from, to time.Time, accounts []int64) ([]int, error) {
	out := make([]int, 24)
	rows, err := s.DB.Query(ctx, `
SELECT extract(hour FROM created_at AT TIME ZONE $3)::int, count(*) FROM messages
WHERE direction = 'in' AND created_at >= $1 AND created_at < $2 AND `+convIn("conversation_id", "$4")+` GROUP BY 1`,
		from, to, s.Loc.String(), accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h, n int
		if err := rows.Scan(&h, &n); err != nil {
			return nil, err
		}
		out[h] = n
	}
	return out, rows.Err()
}

// topRules 只算帳號自己的規則（範本 ig_account_id IS NULL 不會被命中）。
func (s *Service) topRules(ctx context.Context, accounts []int64) ([]RuleHit, error) {
	// 多個帳號的同名規則（多半是同一份範本複製出來的）合併計算
	rows, err := s.DB.Query(ctx, `SELECT name, sum(hit_count)::bigint FROM auto_rules
WHERE hit_count > 0 AND ig_account_id IS NOT NULL AND ($1::bigint[] IS NULL OR ig_account_id = ANY($1))
GROUP BY name ORDER BY 2 DESC, name LIMIT 5`, accounts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuleHit{}
	for rows.Next() {
		var r RuleHit
		if err := rows.Scan(&r.Name, &r.HitCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
