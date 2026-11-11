package posts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

const (
	insightsWindow    = 30 * 24 * time.Hour // 只更新最近 30 天內發佈的
	insightsFreshAge  = 7 * 24 * time.Hour  // 未滿 7 天：每 6 小時；之後每天
	insightsFreshEach = 6 * time.Hour
	insightsOldEach   = 24 * time.Hour
	storyLifetime     = 24 * time.Hour // 限動成效只查得到 24 小時
)

var (
	ErrInsightsUnavailable = errors.New("這篇貼文目前無法取得成效數據")
	ErrNoToken             = errors.New("這個 IG 帳號目前無法取得授權")
)

// canRefreshInsights 判斷一篇貼文現在能不能向 IG 查成效：已發佈、有 media id，限動另限 24 小時內。
func canRefreshInsights(p Post, now time.Time) bool {
	if p.Status != "published" || p.igMediaID == "" || p.PublishedAt == nil {
		return false
	}
	return !(p.Kind == "story" && now.Sub(*p.PublishedAt) > storyLifetime)
}

// InsightsRefresher 背景更新已發佈貼文的成效數據；單篇失敗只記 log 與 insights_error，不影響其他貼文。
// 每篇用該貼文帳號所屬連線的 token。
type InsightsRefresher struct {
	Svc        *Service
	IG         ig.Publisher
	Creds      CredentialsFunc
	OnAPIError func(ctx context.Context, accountID int64, err error)
	Every      time.Duration // 掃描間隔，預設 15 分鐘（實際更新節奏由貼文年齡決定）
}

func (r *InsightsRefresher) Run(ctx context.Context) {
	every := r.Every
	if every <= 0 {
		every = 15 * time.Minute
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if n, err := r.RunOnce(ctx, time.Now()); err != nil {
			log.Printf("insights: %v", err)
		} else if n > 0 {
			log.Printf("insights: refreshed %d posts", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// RunOnce 找出到期的貼文逐篇更新，回傳成功篇數。帳號沒 token 的貼文略過（記 log），不影響其他帳號。
func (r *InsightsRefresher) RunOnce(ctx context.Context, now time.Time) (int, error) {
	rows, err := r.Svc.DB.Query(ctx, `
SELECT id FROM scheduled_posts
WHERE status = 'published' AND ig_media_id <> '' AND published_at > $1::timestamptz - $2::interval
  AND (kind <> 'story' OR published_at > $1::timestamptz - $3::interval)
  AND (insights_checked_at IS NULL
       OR insights_checked_at <= $1::timestamptz - CASE WHEN published_at > $1::timestamptz - $4::interval THEN $5::interval ELSE $6::interval END)
ORDER BY published_at DESC`,
		now, secs(insightsWindow), secs(storyLifetime), secs(insightsFreshAge), secs(insightsFreshEach), secs(insightsOldEach))
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	ok := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		if _, err := r.refresh(ctx, id, now); err != nil {
			log.Printf("insights: post %d: %v", id, err)
			continue
		}
		ok++
	}
	return ok, nil
}

func secs(d time.Duration) string { return fmt.Sprintf("%d seconds", int(d.Seconds())) }

// RefreshPost 手動更新單篇；回傳更新後的貼文。
func (r *InsightsRefresher) RefreshPost(ctx context.Context, id int64) (Post, error) {
	return r.refresh(ctx, id, time.Now())
}

func (r *InsightsRefresher) refresh(ctx context.Context, id int64, now time.Time) (Post, error) {
	p, err := r.Svc.Get(ctx, id)
	if err != nil {
		return p, err
	}
	if !canRefreshInsights(p, now) {
		return p, ErrInsightsUnavailable
	}
	_, tok, err := r.Creds.get(ctx, p.IGAccountID)
	if err != nil {
		return p, fmt.Errorf("%w：%v", ErrNoToken, err)
	}
	m, err := r.IG.Insights(ctx, tok, p.igMediaID, p.Kind)
	if err != nil {
		if r.OnAPIError != nil {
			r.OnAPIError(ctx, p.IGAccountID, err)
		}
		_, _ = r.Svc.DB.Exec(ctx, `UPDATE scheduled_posts SET insights_checked_at = $2, insights_error = $3 WHERE id = $1`,
			id, now, err.Error())
		return p, err
	}
	b, _ := json.Marshal(m)
	if _, err := r.Svc.DB.Exec(ctx, `
UPDATE scheduled_posts SET insights = $2, insights_fetched_at = $3, insights_checked_at = $3, insights_error = '' WHERE id = $1`,
		id, b, now); err != nil {
		return p, err
	}
	return r.Svc.Get(ctx, id)
}
