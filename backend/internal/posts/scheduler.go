package posts

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
)

// Scheduler 到點發佈貼文。失敗自動重試（共 MaxAttempts 次，間隔遞增），用盡才標 failed。
// 發佈中途中斷（重啟、當機）時，以容器狀態判斷 IG 是否已發佈，避免重複貼文。
type Scheduler struct {
	Svc          *Service
	IG           ig.Publisher
	Token        string                                    // 固定 token（測試、模擬器）
	TokenFunc    func(ctx context.Context) (string, error) // 非 nil 時每次發文前取（設定頁連結的帳號）
	PollInterval time.Duration                             // 容器處理中時的輪詢間隔
	PollTimeout  time.Duration                             // 影片處理最久等多久
	StaleAfter   time.Duration                             // publishing 超過這段時間視為中斷

	userID, userTok string
}

var retryBackoff = []time.Duration{time.Minute, 5 * time.Minute}

func (sc *Scheduler) Run(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		for {
			done, err := sc.RunOnce(ctx, time.Now())
			if err != nil {
				log.Printf("scheduler: %v", err)
			}
			if done || err != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-sc.Svc.wake:
		case <-tick.C:
		}
	}
}

// RunOnce 認領並處理一篇到期貼文；沒有到期的回 done=true。
func (sc *Scheduler) RunOnce(ctx context.Context, now time.Time) (done bool, err error) {
	p, err := scanPost(sc.Svc.DB.QueryRow(ctx, `
UPDATE scheduled_posts SET status = 'publishing', locked_at = $1, attempts = attempts + 1, updated_at = now()
WHERE id = (
  SELECT id FROM scheduled_posts
  WHERE (status = 'scheduled' AND next_attempt_at <= $1)
     OR (status = 'publishing' AND locked_at < $1::timestamptz - $2::interval)
  ORDER BY next_attempt_at NULLS FIRST, id LIMIT 1 FOR UPDATE SKIP LOCKED
) RETURNING `+postCols, now, fmt.Sprintf("%d seconds", int(sc.staleAfter().Seconds()))))
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	mediaID, permalink, perr := sc.publish(ctx, p)
	if errors.Is(perr, errAlreadyPublished) {
		log.Printf("scheduler: post %d was already published before interruption", p.ID)
		return false, nil
	}
	if perr != nil {
		return false, sc.fail(ctx, p, perr, now)
	}
	_, err = sc.Svc.DB.Exec(ctx, `
UPDATE scheduled_posts SET status = 'published', ig_media_id = $2, permalink = $3, error = '', published_at = $4,
  next_attempt_at = NULL, updated_at = now()
WHERE id = $1`, p.ID, mediaID, permalink, time.Now())
	log.Printf("scheduler: post %d published (%s)", p.ID, mediaID)
	return false, err
}

func (sc *Scheduler) staleAfter() time.Duration {
	if sc.StaleAfter > 0 {
		return sc.StaleAfter
	}
	return 10 * time.Minute
}

func (sc *Scheduler) fail(ctx context.Context, p Post, cause error, now time.Time) error {
	log.Printf("scheduler: post %d attempt %d failed: %v", p.ID, p.Attempts, cause)
	if p.Attempts >= MaxAttempts {
		_, err := sc.Svc.DB.Exec(ctx, `
UPDATE scheduled_posts SET status = 'failed', error = $2, next_attempt_at = NULL, updated_at = now() WHERE id = $1`,
			p.ID, cause.Error())
		return err
	}
	wait := retryBackoff[min(p.Attempts-1, len(retryBackoff)-1)]
	_, err := sc.Svc.DB.Exec(ctx, `
UPDATE scheduled_posts SET status = 'scheduled', error = $2, next_attempt_at = $3, updated_at = now() WHERE id = $1`,
		p.ID, cause.Error(), now.Add(wait))
	return err
}

func (sc *Scheduler) uid(ctx context.Context, tok string) (string, error) {
	if sc.userID != "" && sc.userTok == tok {
		return sc.userID, nil
	}
	id, err := sc.IG.UserID(ctx, tok)
	if err != nil {
		return "", fmt.Errorf("查 IG 帳號失敗：%w", err)
	}
	sc.userID, sc.userTok = id, tok
	return id, nil
}

func (sc *Scheduler) token(ctx context.Context) (string, error) {
	if sc.TokenFunc != nil {
		return sc.TokenFunc(ctx)
	}
	if sc.Token == "" {
		return "", errors.New("尚未連結 IG 帳號（到「設定」連結）")
	}
	return sc.Token, nil
}

func (sc *Scheduler) publish(ctx context.Context, p Post) (mediaID, permalink string, err error) {
	tok, err := sc.token(ctx)
	if err != nil {
		return "", "", err
	}
	userID, err := sc.uid(ctx, tok)
	if err != nil {
		return "", "", err
	}

	// 中斷復原：已有容器時先看 IG 端狀態
	container := p.containerID
	if container != "" {
		st, err := sc.IG.ContainerStatus(ctx, tok, container)
		switch {
		case err != nil:
			return "", "", err
		case st == "PUBLISHED":
			return "", "", sc.markPublishedUnknownMedia(ctx, p)
		case st == "ERROR" || st == "EXPIRED":
			container = ""
		}
	}

	if container == "" {
		p, err = sc.Svc.withMedia(ctx, p)
		if err != nil {
			return "", "", err
		}
		container, err = sc.buildContainer(ctx, userID, tok, p)
		if err != nil {
			return "", "", err
		}
		if _, err := sc.Svc.DB.Exec(ctx, `UPDATE scheduled_posts SET ig_container_id = $2 WHERE id = $1`, p.ID, container); err != nil {
			return "", "", err
		}
	}
	if err := sc.waitFinished(ctx, tok, container); err != nil {
		return "", "", err
	}
	mediaID, err = sc.IG.Publish(ctx, userID, tok, container)
	if err != nil {
		return "", "", err
	}
	permalink, _ = sc.IG.Permalink(ctx, tok, mediaID) // 拿不到不影響發佈結果
	return mediaID, permalink, nil
}

// errAlreadyPublished 讓 RunOnce 不要再覆寫 markPublishedUnknownMedia 已寫好的狀態。
var errAlreadyPublished = errors.New("already published")

func (sc *Scheduler) markPublishedUnknownMedia(ctx context.Context, p Post) error {
	_, err := sc.Svc.DB.Exec(ctx, `
UPDATE scheduled_posts SET status = 'published', error = '', published_at = COALESCE(published_at, now()),
  next_attempt_at = NULL, updated_at = now() WHERE id = $1`, p.ID)
	if err != nil {
		return err
	}
	return errAlreadyPublished
}

func (sc *Scheduler) buildContainer(ctx context.Context, userID, tok string, p Post) (string, error) {
	spec := func(a media.Asset) ig.ContainerSpec {
		u := sc.Svc.Media.PublicURL(a.Filename)
		if a.Kind == "video" {
			return ig.ContainerSpec{VideoURL: u}
		}
		return ig.ContainerSpec{ImageURL: u}
	}
	switch p.Kind {
	case "image":
		s := spec(p.Media[0])
		s.Caption = p.Caption
		return sc.IG.CreateContainer(ctx, userID, tok, s)
	case "reel":
		s := spec(p.Media[0])
		s.MediaType, s.Caption = "REELS", p.Caption
		return sc.IG.CreateContainer(ctx, userID, tok, s)
	case "story":
		s := spec(p.Media[0])
		s.MediaType = "STORIES"
		return sc.IG.CreateContainer(ctx, userID, tok, s)
	case "carousel":
		var children []string
		for _, a := range p.Media {
			s := spec(a)
			s.IsCarouselItem = true
			if a.Kind == "video" {
				s.MediaType = "VIDEO"
			}
			id, err := sc.IG.CreateContainer(ctx, userID, tok, s)
			if err != nil {
				return "", fmt.Errorf("輪播第 %d 個項目：%w", len(children)+1, err)
			}
			if err := sc.waitFinished(ctx, tok, id); err != nil {
				return "", err
			}
			children = append(children, id)
		}
		return sc.IG.CreateContainer(ctx, userID, tok, ig.ContainerSpec{MediaType: "CAROUSEL", Children: children, Caption: p.Caption})
	}
	return "", fmt.Errorf("unknown kind %q", p.Kind)
}

func (sc *Scheduler) waitFinished(ctx context.Context, tok, container string) error {
	interval, timeout := sc.PollInterval, sc.PollTimeout
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		st, err := sc.IG.ContainerStatus(ctx, tok, container)
		if err != nil {
			return err
		}
		switch st {
		case "FINISHED", "":
			return nil
		case "ERROR", "EXPIRED":
			if _, err := sc.Svc.DB.Exec(ctx, `UPDATE scheduled_posts SET ig_container_id = '' WHERE ig_container_id = $1`, container); err != nil {
				return err
			}
			return fmt.Errorf("IG 處理媒體失敗（%s）", st)
		}
		if time.Now().After(deadline) {
			return errors.New("IG 處理媒體逾時")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
