package posts

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
)

// IG 貼文同步：把 IG 帳號上「不是從後台發的」貼文、輪播、Reels、限動寫進 scheduled_posts（source='ig'），
// 讓 InsightsRefresher 撿得到成效、AI 拿得到近期文案。後台自己發的（已有 ig_media_id）同步到時只更新文案與連結。

const (
	syncPostsEvery   = 6 * time.Hour    // 貼文每 6 小時同步一次
	syncStoriesEvery = 30 * time.Minute // 限動每 30 分鐘（IG 只給 24 小時內的）
	syncTick         = time.Minute
	syncOverlap      = 30 * 24 * time.Hour // 之後的同步往回多抓 30 天，IG 上改過的文案才更新得到
	ManualSyncCool   = time.Minute         // 手動同步每帳號冷卻
	adoptWindow      = 2 * time.Hour       // 後台發的貼文不知道 media id 時（中斷復原），以同類型、同文案、時間相近認領
)

// SyncKind 把 IG 的 media_type／media_product_type 對應到貼文類型：STORY→story、REELS→reel、CAROUSEL_ALBUM→carousel、其他→image。
func SyncKind(mediaType, productType string) string {
	switch {
	case productType == "STORY":
		return "story"
	case productType == "REELS":
		return "reel"
	case mediaType == "CAROUSEL_ALBUM":
		return "carousel"
	}
	return "image"
}

// SyncResult 是一次同步處理的篇數（含新建與更新）。
type SyncResult struct {
	Posts   int `json:"posts"`
	Stories int `json:"stories"`
}

// CooldownError：手動同步太頻繁。
type CooldownError struct{ RetryAfter time.Duration }

func (e *CooldownError) Error() string {
	return fmt.Sprintf("剛同步過，請 %d 秒後再試", int((e.RetryAfter+time.Second-1)/time.Second))
}

// MediaSyncer 背景同步各帳號的 IG 貼文與限動；單一帳號失敗只記 log，不影響其他帳號。
type MediaSyncer struct {
	Svc        *Service
	IG         ig.MediaLister
	Creds      CredentialsFunc
	OnAPIError func(ctx context.Context, accountID int64, err error) // token 失效時寫連線 last_error
	// Insights 非 nil 時，新同步進來的限動當下就抓一次成效（限動過 24 小時就拿不到）
	Insights *InsightsRefresher
	Now      func() time.Time // 測試用；nil＝time.Now

	mu          sync.Mutex
	lastPosts   map[int64]time.Time // 背景排程：上次嘗試（成功或失敗）
	lastStories map[int64]time.Time
	lastManual  map[int64]time.Time // 手動冷卻
	locks       map[int64]*sync.Mutex
}

func (m *MediaSyncer) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MediaSyncer) accountLock(id int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[int64]*sync.Mutex{}
	}
	l := m.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	return l
}

// Run 每分鐘看一次哪些帳號到期；啟動時全部帳號先同步一次（貼文從 DB 推得上次同步時間，不會每次重抓 200 篇）。
func (m *MediaSyncer) Run(ctx context.Context) {
	tick := time.NewTicker(syncTick)
	defer tick.Stop()
	for {
		if res, err := m.RunOnce(ctx); err != nil {
			log.Printf("ig sync: %v", err)
		} else if res.Posts+res.Stories > 0 {
			log.Printf("ig sync: %d posts, %d stories", res.Posts, res.Stories)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// RunOnce 同步所有到期的啟用中帳號，回處理篇數合計。
func (m *MediaSyncer) RunOnce(ctx context.Context) (SyncResult, error) {
	rows, err := m.Svc.DB.Query(ctx, `SELECT id FROM ig_accounts WHERE active ORDER BY id`)
	if err != nil {
		return SyncResult{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return SyncResult{}, err
	}
	var total SyncResult
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		now := m.now()
		m.mu.Lock()
		if m.lastPosts == nil {
			m.lastPosts, m.lastStories = map[int64]time.Time{}, map[int64]time.Time{}
		}
		doPosts := now.Sub(m.lastPosts[id]) >= syncPostsEvery
		doStories := now.Sub(m.lastStories[id]) >= syncStoriesEvery
		if doPosts {
			m.lastPosts[id] = now
		}
		if doStories {
			m.lastStories[id] = now
		}
		m.mu.Unlock()
		if !doPosts && !doStories {
			continue
		}
		res, err := m.syncAccount(ctx, id, doPosts, doStories)
		total.Posts += res.Posts
		total.Stories += res.Stories
		if err != nil {
			log.Printf("ig sync: account %d: %v", id, err)
		}
	}
	return total, nil
}

// SyncNow 是手動同步（貼文＋限動）：每帳號 ManualSyncCool 冷卻（成功失敗都算），太頻繁回 *CooldownError。
// 認不到 token 回包了 ErrNoToken 的錯；IG API 失敗回原錯誤。
func (m *MediaSyncer) SyncNow(ctx context.Context, accountID int64) (SyncResult, error) {
	now := m.now()
	m.mu.Lock()
	if m.lastManual == nil {
		m.lastManual = map[int64]time.Time{}
	}
	if last, ok := m.lastManual[accountID]; ok && now.Sub(last) < ManualSyncCool {
		m.mu.Unlock()
		return SyncResult{}, &CooldownError{RetryAfter: ManualSyncCool - now.Sub(last)}
	}
	m.lastManual[accountID] = now
	// 手動同步也算背景排程的一次，避免緊接著又自動跑
	if m.lastPosts == nil {
		m.lastPosts, m.lastStories = map[int64]time.Time{}, map[int64]time.Time{}
	}
	m.lastPosts[accountID], m.lastStories[accountID] = now, now
	m.mu.Unlock()
	return m.syncAccount(ctx, accountID, true, true)
}

func (m *MediaSyncer) syncAccount(ctx context.Context, accountID int64, doPosts, doStories bool) (SyncResult, error) {
	l := m.accountLock(accountID)
	l.Lock()
	defer l.Unlock()
	var res SyncResult
	userID, tok, err := m.Creds.get(ctx, accountID)
	if err != nil {
		return res, fmt.Errorf("%w：%v", ErrNoToken, err)
	}
	if doPosts {
		since, err := m.postsSince(ctx, accountID)
		if err != nil {
			return res, err
		}
		items, err := m.IG.ListMedia(ctx, userID, tok, since)
		if err != nil {
			m.noteAPIError(ctx, accountID, err)
			return res, err
		}
		for _, it := range items {
			if _, _, err := m.upsert(ctx, accountID, it); err != nil {
				return res, fmt.Errorf("media %s: %w", it.ID, err)
			}
			res.Posts++
		}
	}
	if doStories {
		items, err := m.IG.ListStories(ctx, userID, tok)
		if err != nil {
			m.noteAPIError(ctx, accountID, err)
			return res, err
		}
		for _, it := range items {
			it.MediaProductType = "STORY" // /stories 回來的一律當限動
			id, created, err := m.upsert(ctx, accountID, it)
			if err != nil {
				return res, fmt.Errorf("story %s: %w", it.ID, err)
			}
			res.Stories++
			if created && m.Insights != nil {
				if _, err := m.Insights.refresh(ctx, id, m.now()); err != nil {
					log.Printf("ig sync: story %d insights: %v", id, err)
				}
			}
		}
	}
	return res, nil
}

func (m *MediaSyncer) noteAPIError(ctx context.Context, accountID int64, err error) {
	if m.OnAPIError != nil {
		m.OnAPIError(ctx, accountID, err)
	}
}

// postsSince：帳號還沒有任何同步過的貼文＝第一次（零值，抓最近 MaxListMedia 篇）；否則從上次同步往回 syncOverlap。
func (m *MediaSyncer) postsSince(ctx context.Context, accountID int64) (time.Time, error) {
	var last *time.Time
	if err := m.Svc.DB.QueryRow(ctx, `SELECT max(ig_synced_at) FROM scheduled_posts WHERE ig_account_id = $1 AND kind <> 'story'`,
		accountID).Scan(&last); err != nil {
		return time.Time{}, err
	}
	if last == nil {
		return time.Time{}, nil
	}
	return last.Add(-syncOverlap), nil
}

// upsert 寫入一則 IG 媒體，回貼文 id 與是否新建：
//   - 同帳號已有同 media id（後台發的或之前同步的）→ 只更新文案（以 IG 為準）、連結、同步時間；縮圖缺才補
//   - 後台發的貼文在中斷復原時不知道 media id → 同類型、同文案、發佈時間相近的認領（寫入 media id），不另建
//   - 其他 → 新建 source='ig'、status='published'（scheduled_at＝published_at＝IG 時間，月曆才放得對）
func (m *MediaSyncer) upsert(ctx context.Context, accountID int64, it ig.MediaItem) (int64, bool, error) {
	if it.ID == "" {
		return 0, false, errors.New("缺 media id")
	}
	kind := SyncKind(it.MediaType, it.MediaProductType)
	caption := strings.TrimSpace(it.Caption)
	now := m.now()

	var id int64
	var source, thumb string
	err := m.Svc.DB.QueryRow(ctx, `SELECT id, source, ig_thumb FROM scheduled_posts WHERE ig_account_id = $1 AND ig_media_id = $2`,
		accountID, it.ID).Scan(&id, &source, &thumb)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		adopted, aerr := m.adopt(ctx, accountID, it, kind, caption, now)
		if aerr != nil {
			return 0, false, aerr
		}
		if adopted > 0 {
			return adopted, false, nil
		}
	case err != nil:
		return 0, false, err
	}

	newThumb := ""
	if id == 0 || (source == SourceIG && thumb == "") {
		newThumb = m.saveThumb(ctx, it)
	}
	var created bool
	err = m.Svc.DB.QueryRow(ctx, `
INSERT INTO scheduled_posts (ig_account_id, kind, caption, status, ig_media_id, permalink, published_at, scheduled_at,
  source, ig_thumb, ig_synced_at)
VALUES ($1, $2, $3, 'published', $4, $5, $6, $6, 'ig', $7, $8)
ON CONFLICT (ig_account_id, ig_media_id) WHERE ig_media_id <> '' DO UPDATE SET
  caption = EXCLUDED.caption,
  permalink = CASE WHEN EXCLUDED.permalink <> '' THEN EXCLUDED.permalink ELSE scheduled_posts.permalink END,
  ig_thumb = CASE WHEN scheduled_posts.ig_thumb = '' THEN EXCLUDED.ig_thumb ELSE scheduled_posts.ig_thumb END,
  ig_synced_at = EXCLUDED.ig_synced_at,
  updated_at = CASE WHEN scheduled_posts.caption IS DISTINCT FROM EXCLUDED.caption THEN now() ELSE scheduled_posts.updated_at END
RETURNING id, (xmax = 0)`,
		accountID, kind, caption, it.ID, it.Permalink, it.Timestamp, newThumb, now).Scan(&id, &created)
	return id, created, err
}

// adopt 認領「後台發了、但不知道 media id」的貼文（見 Scheduler.markPublishedUnknownMedia），回認領到的 id（0＝沒有）。
func (m *MediaSyncer) adopt(ctx context.Context, accountID int64, it ig.MediaItem, kind, caption string, now time.Time) (int64, error) {
	var id int64
	err := m.Svc.DB.QueryRow(ctx, `
UPDATE scheduled_posts SET ig_media_id = $2, permalink = CASE WHEN $3 <> '' THEN $3 ELSE permalink END, ig_synced_at = $4
WHERE id = (
  SELECT id FROM scheduled_posts
  WHERE ig_account_id = $1 AND source = 'app' AND status = 'published' AND ig_media_id = '' AND kind = $5
    AND btrim(caption) = $6 AND published_at BETWEEN $7::timestamptz - $8::interval AND $7::timestamptz + $8::interval
  ORDER BY abs(extract(epoch FROM published_at - $7::timestamptz)), id LIMIT 1
) RETURNING id`, accountID, it.ID, it.Permalink, now, kind, caption, it.Timestamp, secs(adoptWindow)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// saveThumb 下載縮圖存到 MEDIA_DIR；失敗只記 log（之後同步會再補），回檔名或空字串。
func (m *MediaSyncer) saveThumb(ctx context.Context, it ig.MediaItem) string {
	u := it.ThumbURL()
	if u == "" || m.Svc.Media == nil {
		return ""
	}
	data, err := m.IG.FetchThumb(ctx, u)
	if err != nil {
		log.Printf("ig sync: media %s thumb: %v", it.ID, err)
		return ""
	}
	name, err := m.Svc.Media.SaveThumb(data)
	if err != nil {
		log.Printf("ig sync: media %s thumb: %v", it.ID, err)
		return ""
	}
	return name
}
