// Package posts 管 IG 貼文排程：草稿、排程、自動發佈與重試。
package posts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/media"
)

var (
	ErrNotFound  = errors.New("找不到貼文")
	ErrImmutable = errors.New("貼文發佈中或已發佈，不能修改")
)

const (
	MaxCaption  = 2200
	MaxHashtags = 30
	MaxAttempts = 3
)

type Post struct {
	ID            int64         `json:"id"`
	Kind          string        `json:"kind"`
	Caption       string        `json:"caption"`
	MediaIDs      []int64       `json:"mediaIds"`
	Media         []media.Asset `json:"media"`
	ScheduledAt   *time.Time    `json:"scheduledAt"`
	Status        string        `json:"status"`
	Attempts      int           `json:"attempts"`
	NextAttemptAt *time.Time    `json:"nextAttemptAt"`
	Permalink     string        `json:"permalink"`
	Error         string        `json:"error"`
	PublishedAt   *time.Time    `json:"publishedAt"`
	UpdatedAt     time.Time     `json:"updatedAt"`
	containerID   string
}

type Service struct {
	DB    *pgxpool.Pool
	Media *media.Store
	wake  chan struct{}
}

func NewService(db *pgxpool.Pool, m *media.Store) *Service {
	return &Service{DB: db, Media: m, wake: make(chan struct{}, 1)}
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

var hashtagRe = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

func CountHashtags(caption string) int { return len(hashtagRe.FindAllString(caption, -1)) }

// Validate 依 IG 規格檢查貼文內容；schedule=true 時另檢查時間。
func (s *Service) Validate(ctx context.Context, p *Post, schedule bool, now time.Time) error {
	p.Caption = strings.TrimSpace(p.Caption)
	if utf8.RuneCountInString(p.Caption) > MaxCaption {
		return fmt.Errorf("文案超過 %d 字", MaxCaption)
	}
	if n := CountHashtags(p.Caption); n > MaxHashtags {
		return fmt.Errorf("hashtag 最多 %d 個（目前 %d 個）", MaxHashtags, n)
	}
	assets, err := s.Media.Get(ctx, p.MediaIDs)
	if err != nil {
		return err
	}
	images, videos := 0, 0
	for _, a := range assets {
		if a.Kind == "image" {
			images++
		} else {
			videos++
		}
	}
	n := len(assets)
	switch p.Kind {
	case "image":
		if n != 1 || images != 1 {
			return errors.New("單張貼文需要剛好 1 張圖片")
		}
		a := assets[0]
		if r := float64(a.Width) / float64(a.Height); a.Height > 0 && (r < 0.8-0.005 || r > 1.91+0.005) {
			return fmt.Errorf("圖片比例 %.2f 不符 IG 規格（需介於 4:5 與 1.91:1）", r)
		}
	case "carousel":
		if n < 2 || n > 10 {
			return errors.New("輪播需要 2～10 個圖片或影片")
		}
	case "reel":
		if n != 1 || videos != 1 {
			return errors.New("Reels 需要剛好 1 支影片")
		}
	case "story":
		if n != 1 {
			return errors.New("限動需要剛好 1 張圖片或 1 支影片")
		}
	default:
		return errors.New("類型必須是 image、carousel、reel 或 story")
	}
	if schedule {
		if p.ScheduledAt == nil {
			return errors.New("請選擇發佈時間")
		}
		if p.ScheduledAt.Before(now.Add(-time.Minute)) {
			return errors.New("發佈時間不能早於現在")
		}
	}
	p.Media = assets
	return nil
}

const postCols = `id, kind, caption, media_ids, scheduled_at, status, attempts, next_attempt_at, permalink, error, published_at, updated_at, ig_container_id`

func scanPost(row pgx.Row) (Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.Kind, &p.Caption, &p.MediaIDs, &p.ScheduledAt, &p.Status, &p.Attempts, &p.NextAttemptAt,
		&p.Permalink, &p.Error, &p.PublishedAt, &p.UpdatedAt, &p.containerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Service) withMedia(ctx context.Context, p Post) (Post, error) {
	if len(p.MediaIDs) == 0 {
		p.Media = []media.Asset{}
		return p, nil
	}
	m, err := s.Media.Get(ctx, p.MediaIDs)
	if err != nil {
		return p, err
	}
	p.Media = m
	return p, nil
}

type ListFilter struct {
	From, To *time.Time
	Status   string
}

// List 回傳排程時間落在區間內的貼文；草稿（無時間）一律附上。
func (s *Service) List(ctx context.Context, f ListFilter) ([]Post, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+postCols+` FROM scheduled_posts
WHERE ($3 = '' OR status = $3)
  AND (scheduled_at IS NULL OR (($1::timestamptz IS NULL OR scheduled_at >= $1) AND ($2::timestamptz IS NULL OR scheduled_at < $2)))
ORDER BY scheduled_at NULLS FIRST, id`, f.From, f.To, f.Status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Post, 0, len(list))
	for _, p := range list {
		p, err := s.withMedia(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Post, error) {
	p, err := scanPost(s.DB.QueryRow(ctx, `SELECT `+postCols+` FROM scheduled_posts WHERE id = $1`, id))
	if err != nil {
		return p, err
	}
	return s.withMedia(ctx, p)
}

// Save 建立或更新貼文。action: draft（存草稿）| schedule（排程）| now（立即發佈）。
func (s *Service) Save(ctx context.Context, p Post, action string, now time.Time) (Post, error) {
	switch action {
	case "draft", "schedule", "now":
	default:
		return p, errors.New("action 必須是 draft、schedule 或 now")
	}
	if action == "now" {
		p.ScheduledAt = &now
	}
	if action == "draft" {
		// 草稿只檢查基本格式；媒體可以先不齊
		p.Caption = strings.TrimSpace(p.Caption)
		if utf8.RuneCountInString(p.Caption) > MaxCaption {
			return p, fmt.Errorf("文案超過 %d 字", MaxCaption)
		}
		if p.MediaIDs == nil {
			p.MediaIDs = []int64{}
		}
		if _, err := s.Media.Get(ctx, p.MediaIDs); err != nil {
			return p, err
		}
	} else if err := s.Validate(ctx, &p, true, now); err != nil {
		return p, err
	}

	status := "draft"
	var next *time.Time
	if action != "draft" {
		status, next = "scheduled", p.ScheduledAt
	}

	var saved Post
	var err error
	if p.ID == 0 {
		saved, err = scanPost(s.DB.QueryRow(ctx, `
INSERT INTO scheduled_posts (kind, caption, media_ids, scheduled_at, status, next_attempt_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+postCols, p.Kind, p.Caption, p.MediaIDs, p.ScheduledAt, status, next))
	} else {
		// 只有 draft / scheduled / failed 能改；重新排程會清掉重試狀態與舊容器
		saved, err = scanPost(s.DB.QueryRow(ctx, `
UPDATE scheduled_posts SET kind=$2, caption=$3, media_ids=$4, scheduled_at=$5, status=$6, next_attempt_at=$7,
  attempts=0, error='', ig_container_id='', updated_at=now()
WHERE id=$1 AND status IN ('draft','scheduled','failed') RETURNING `+postCols,
			p.ID, p.Kind, p.Caption, p.MediaIDs, p.ScheduledAt, status, next))
		if errors.Is(err, ErrNotFound) {
			if _, gerr := s.Get(ctx, p.ID); gerr == nil {
				return p, ErrImmutable
			}
		}
	}
	if err != nil {
		return p, err
	}
	if status == "scheduled" {
		s.Wake()
	}
	return s.withMedia(ctx, saved)
}

// Delete 刪除本地紀錄；發佈中不可刪。已發佈的只刪本地，不會刪 IG 上的貼文。
func (s *Service) Delete(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM scheduled_posts WHERE id = $1 AND status <> 'publishing'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, gerr := s.Get(ctx, id); gerr == nil {
			return ErrImmutable
		}
		return ErrNotFound
	}
	return nil
}

// CountUpcoming 是總覽「待發排程」的數字。
func (s *Service) CountUpcoming(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM scheduled_posts WHERE status IN ('scheduled','publishing')`).Scan(&n)
	return n, err
}
