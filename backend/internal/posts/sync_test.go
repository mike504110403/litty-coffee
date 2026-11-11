package posts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

func TestSyncKind(t *testing.T) {
	for _, c := range []struct{ mt, pt, want string }{
		{"IMAGE", "STORY", "story"},
		{"VIDEO", "STORY", "story"},
		{"VIDEO", "REELS", "reel"},
		{"CAROUSEL_ALBUM", "FEED", "carousel"},
		{"IMAGE", "FEED", "image"},
		{"VIDEO", "FEED", "image"},
		{"IMAGE", "", "image"},
	} {
		if got := SyncKind(c.mt, c.pt); got != c.want {
			t.Errorf("SyncKind(%s,%s)=%s want %s", c.mt, c.pt, got, c.want)
		}
	}
}

// fakeClock 讓冷卻與排程可控。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newSyncer(s *Service) (*MediaSyncer, *ig.FakePublisher, *fakeClock) {
	f := ig.NewFakePublisher()
	clk := &fakeClock{t: time.Now()}
	ins := &InsightsRefresher{Svc: s, IG: f, Creds: testCreds(s.DB)}
	return &MediaSyncer{Svc: s, IG: f, Creds: testCreds(s.DB), Insights: ins, Now: clk.now}, f, clk
}

func countPosts(t *testing.T, s *Service, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM scheduled_posts WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func postByMedia(t *testing.T, s *Service, mediaID string) Post {
	t.Helper()
	var id int64
	if err := s.DB.QueryRow(context.Background(), `SELECT id FROM scheduled_posts WHERE ig_media_id = $1`, mediaID).Scan(&id); err != nil {
		t.Fatalf("media %s: %v", mediaID, err)
	}
	p, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 同步：新貼文建成 source='ig'（類型映射、時間、縮圖存本地）；後台發的（已有 media id）只更新文案、不重複建；
// 限動同步進來當下就抓一次成效；再同步時 IG 上改過的文案會更新、縮圖不重抓。
func TestSyncCreatesUpdatesAndDedupes(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	m, f, clk := newSyncer(s)
	ts := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	appID := seedPost(t, pool, "image", "published", "m-app", 5*24*time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET caption = '後台原文', media_ids = '{}' WHERE id = $1`, appID); err != nil {
		t.Fatal(err)
	}
	f.SetMedia("acct", []ig.MediaItem{
		{ID: "m-app", MediaType: "IMAGE", MediaProductType: "FEED", Caption: "在 IG 改過的文案", Timestamp: ts.Add(-time.Hour), Permalink: "https://ig/p/app"},
		{ID: "m-car", MediaType: "CAROUSEL_ALBUM", MediaProductType: "FEED", Caption: " 輪播 ", Timestamp: ts, Permalink: "https://ig/p/car",
			MediaURL: "https://cdn.test/car.jpg"},
		{ID: "m-reel", MediaType: "VIDEO", MediaProductType: "REELS", Caption: "Reels", Timestamp: ts.Add(-2 * time.Hour),
			ThumbnailURL: "https://cdn.test/reel.jpg", MediaURL: "https://cdn.test/reel.mp4"},
	})
	f.SetStories("acct", []ig.MediaItem{{ID: "m-story", MediaType: "IMAGE", Caption: "今日限定", Timestamp: time.Now().Add(-time.Hour),
		MediaURL: "https://cdn.test/story.jpg"}})

	res, err := m.SyncNow(ctx, testAcc)
	if err != nil || res.Posts != 3 || res.Stories != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if n := countPosts(t, s, `true`); n != 4 {
		t.Fatalf("後台發的不應重複建：共 %d 筆", n)
	}
	app, _ := s.Get(ctx, appID)
	if app.Source != SourceApp || app.Caption != "在 IG 改過的文案" || app.Permalink != "https://ig/p/app" || app.IGSyncedAt == nil || app.IGThumb != "" {
		t.Fatalf("後台貼文應只更新文案／連結：%+v", app)
	}
	car := postByMedia(t, s, "m-car")
	if car.Source != SourceIG || car.Kind != "carousel" || car.Status != "published" || car.Caption != "輪播" ||
		!car.PublishedAt.Equal(ts) || car.ScheduledAt == nil || !car.ScheduledAt.Equal(ts) || car.Permalink != "https://ig/p/car" {
		t.Fatalf("輪播：%+v", car)
	}
	if len(car.IGThumb) < 8 || car.IGThumb[:7] != "/media/" {
		t.Fatalf("縮圖路徑：%q", car.IGThumb)
	}
	if _, err := os.Stat(filepath.Join(s.Media.Dir, filepath.Base(car.IGThumb))); err != nil {
		t.Fatalf("縮圖應存在 MEDIA_DIR：%v", err)
	}
	if reel := postByMedia(t, s, "m-reel"); reel.Kind != "reel" || reel.IGThumb == "" {
		t.Fatalf("reel：%+v", reel)
	}
	story := postByMedia(t, s, "m-story")
	if story.Kind != "story" || story.Source != SourceIG || story.InsightsAt == nil || story.Insights["views"] == 0 {
		t.Fatalf("限動同步時應順手抓成效：%+v", story)
	}
	if f.InsightsCalls["m-car"] != 0 {
		t.Fatal("一般貼文成效留給 InsightsRefresher，不在同步時抓")
	}
	thumbs := f.FakeThumbCalls()

	// 再同步：IG 上改了輪播文案 → 更新；不重複建、縮圖不重抓、限動成效不重抓
	f.SetMedia("acct", []ig.MediaItem{
		{ID: "m-car", MediaType: "CAROUSEL_ALBUM", MediaProductType: "FEED", Caption: "輪播（改）", Timestamp: ts, MediaURL: "https://cdn.test/car2.jpg"},
	})
	clk.t = clk.t.Add(ManualSyncCool)
	if res, err := m.SyncNow(ctx, testAcc); err != nil || res.Posts != 1 || res.Stories != 1 {
		t.Fatalf("second sync: %+v %v", res, err)
	}
	car2 := postByMedia(t, s, "m-car")
	if car2.Caption != "輪播（改）" || car2.ID != car.ID || car2.IGThumb != car.IGThumb || car2.Permalink != "https://ig/p/car" {
		t.Fatalf("文案應更新、其他保留：%+v", car2)
	}
	if n := countPosts(t, s, `true`); n != 4 || f.FakeThumbCalls() != thumbs || f.InsightsCalls["m-story"] != 1 {
		t.Fatalf("不應重複建或重抓：rows=%d thumbs=%d→%d story insights=%d", n, thumbs, f.FakeThumbCalls(), f.InsightsCalls["m-story"])
	}
}

// 第一次同步不帶 since（抓最近 200 篇）；之後從上次同步往回 30 天。
func TestSyncSince(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	m, f, clk := newSyncer(s)
	f.SetMedia("acct", []ig.MediaItem{{ID: "m1", MediaType: "IMAGE", Caption: "x", Timestamp: time.Now().Add(-time.Hour)}})
	if _, err := m.SyncNow(ctx, testAcc); err != nil {
		t.Fatal(err)
	}
	first := postByMedia(t, s, "m1").IGSyncedAt
	clk.t = clk.t.Add(ManualSyncCool)
	if _, err := m.SyncNow(ctx, testAcc); err != nil {
		t.Fatal(err)
	}
	var media []ig.ListCall
	for _, c := range f.MediaListCalls() {
		if c.Kind == "media" {
			media = append(media, c)
		}
	}
	if len(media) != 2 || !media[0].Since.IsZero() || media[0].Token != "tok-acct" || media[0].UserID != "acct" {
		t.Fatalf("first call: %+v", media)
	}
	if want := first.Add(-syncOverlap); first == nil || !media[1].Since.Equal(want) {
		t.Fatalf("second since=%v want %v", media[1].Since, want)
	}
}

// 後台發的貼文在中斷復原時沒有 media id：同類型、同文案、時間相近的由同步認領，不另建一筆。
func TestSyncAdoptsAppPostWithoutMediaID(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	m, f, _ := newSyncer(s)
	id := seedPost(t, pool, "image", "published", "", time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET caption = '週末新品' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	app, _ := s.Get(ctx, id)
	f.SetMedia("acct", []ig.MediaItem{
		{ID: "m-x", MediaType: "IMAGE", Caption: "週末新品", Timestamp: app.PublishedAt.Add(2 * time.Minute), Permalink: "https://ig/p/x"},
	})
	f.SetStories("acct", nil)
	if _, err := m.SyncNow(ctx, testAcc); err != nil {
		t.Fatal(err)
	}
	if n := countPosts(t, s, `true`); n != 1 {
		t.Fatalf("應認領而非新建：%d 筆", n)
	}
	if got := postByMedia(t, s, "m-x"); got.ID != id || got.Source != SourceApp || got.Permalink != "https://ig/p/x" {
		t.Fatalf("adopt: %+v", got)
	}
}

// source='ig' 的貼文唯讀：不能改、不能重新排程、不能刪；可以切 ai_exclude、更新成效。
func TestSyncedPostReadOnly(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	m, f, _ := newSyncer(s)
	f.SetMedia("acct", []ig.MediaItem{{ID: "m-ro", MediaType: "IMAGE", Caption: "IG 發的", Timestamp: time.Now().Add(-time.Hour)}})
	f.SetStories("acct", nil)
	if _, err := m.SyncNow(ctx, testAcc); err != nil {
		t.Fatal(err)
	}
	p := postByMedia(t, s, "m-ro")
	for _, action := range []string{"draft", "schedule", "now"} {
		in := p
		in.Caption = "偷改"
		at := time.Now().Add(time.Hour)
		in.ScheduledAt = &at
		if _, err := s.Save(ctx, in, action, time.Now()); !errors.Is(err, ErrSynced) {
			t.Fatalf("%s: err=%v", action, err)
		}
	}
	if err := s.Delete(ctx, p.ID); !errors.Is(err, ErrSynced) {
		t.Fatalf("delete: %v", err)
	}
	got, err := s.SetAIExclude(ctx, p.ID, true)
	if err != nil || !got.AIExclude || got.Caption != "IG 發的" {
		t.Fatalf("ai exclude: %+v %v", got, err)
	}
	if _, err := m.Insights.RefreshPost(ctx, p.ID); err != nil {
		t.Fatalf("成效應可手動更新：%v", err)
	}
}

// 手動同步每帳號 1 分鐘冷卻（失敗也算）。
func TestSyncNowCooldown(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	m, f, clk := newSyncer(s)
	f.SetMediaErr(errors.New("instagram api 500: boom"))
	if _, err := m.SyncNow(ctx, testAcc); err == nil {
		t.Fatal("want error")
	}
	f.SetMediaErr(nil)
	clk.t = clk.t.Add(30 * time.Second)
	_, err := m.SyncNow(ctx, testAcc)
	var cool *CooldownError
	if !errors.As(err, &cool) || cool.RetryAfter != 30*time.Second {
		t.Fatalf("cooldown: %v", err)
	}
	clk.t = clk.t.Add(30 * time.Second)
	if _, err := m.SyncNow(ctx, testAcc); err != nil {
		t.Fatal(err)
	}
}

// 背景排程：貼文每 6 小時、限動每 30 分鐘；單一帳號失敗（token 失效）不影響其他帳號，token 失效走 OnAPIError。
func TestSyncRunOnceScheduleAndIsolation(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	acc2 := testdb.ConnectedAccount(t, pool, "acct-2")
	acc3 := testdb.ConnectedAccount(t, pool, "acct-3")
	m, f, clk := newSyncer(s)
	base := m.Creds
	m.Creds = func(ctx context.Context, id int64) (string, string, error) {
		if id == acc3 {
			return "", "", errors.New("帳號已停用")
		}
		if id == testAcc {
			return "acct", "bad-token", nil
		}
		return base(ctx, id)
	}
	var noted []int64
	m.OnAPIError = func(_ context.Context, id int64, err error) {
		if ig.IsTokenInvalid(err) {
			noted = append(noted, id)
		}
	}
	tokenErr := &ig.APIError{Status: 400, Code: 190, Message: "expired"}
	// testAcc 的 token 失效：只讓它的呼叫失敗
	m.IG = &failFor{FakePublisher: f, token: "bad-token", err: tokenErr}
	f.SetMedia("acct-2", []ig.MediaItem{{ID: "m2", MediaType: "IMAGE", Caption: "acct2", Timestamp: time.Now().Add(-time.Hour)}})
	f.SetStories("acct-2", nil)

	if _, err := m.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if countPosts(t, s, `ig_account_id = $1`, acc2) != 1 {
		t.Fatal("其他帳號照常同步")
	}
	if len(noted) == 0 || noted[0] != testAcc {
		t.Fatalf("token 失效應走 OnAPIError：%v", noted)
	}
	calls := func(kind string) int {
		n := 0
		for _, c := range f.MediaListCalls() {
			if c.Kind == kind && c.UserID == "acct-2" {
				n++
			}
		}
		return n
	}
	if calls("media") != 1 || calls("stories") != 1 {
		t.Fatalf("first run: media=%d stories=%d", calls("media"), calls("stories"))
	}
	m.RunOnce(ctx) // 還沒到期
	clk.t = clk.t.Add(31 * time.Minute)
	m.RunOnce(ctx) // 只有限動到期
	if calls("media") != 1 || calls("stories") != 2 {
		t.Fatalf("after 31m: media=%d stories=%d", calls("media"), calls("stories"))
	}
	clk.t = clk.t.Add(6 * time.Hour)
	m.RunOnce(ctx)
	if calls("media") != 2 || calls("stories") != 3 {
		t.Fatalf("after 6h: media=%d stories=%d", calls("media"), calls("stories"))
	}
}

// failFor 讓特定 token 的 List 呼叫失敗，其他照 FakePublisher。
type failFor struct {
	*ig.FakePublisher
	token string
	err   error
}

func (f *failFor) ListMedia(ctx context.Context, userID, token string, since time.Time) ([]ig.MediaItem, error) {
	if token == f.token {
		return nil, f.err
	}
	return f.FakePublisher.ListMedia(ctx, userID, token, since)
}

func (f *failFor) ListStories(ctx context.Context, userID, token string) ([]ig.MediaItem, error) {
	if token == f.token {
		return nil, f.err
	}
	return f.FakePublisher.ListStories(ctx, userID, token)
}

// 發佈與同步競速：同步先把同一則 media 建成 source='ig'，發佈寫入時以後台這筆為準（同步那筆刪掉），不撞唯一索引。
func TestMarkPublishedReplacesSyncedDuplicate(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	m, f, _ := newSyncer(s)
	f.SetMedia("acct", []ig.MediaItem{{ID: "m-race", MediaType: "IMAGE", Caption: "x", Timestamp: time.Now()}})
	f.SetStories("acct", nil)
	if _, err := m.SyncNow(ctx, testAcc); err != nil {
		t.Fatal(err)
	}
	app := seedPost(t, pool, "image", "publishing", "", 0)
	p, _ := s.Get(ctx, app)
	sc, _ := newScheduler(s)
	if err := sc.markPublished(ctx, p, "m-race", "https://ig/p/race"); err != nil {
		t.Fatal(err)
	}
	if got := postByMedia(t, s, "m-race"); got.ID != app || got.Source != SourceApp || got.Status != "published" {
		t.Fatalf("got %+v", got)
	}
	if countPosts(t, s, `source = 'ig'`) != 0 {
		t.Fatal("同步的那筆應刪除")
	}
}
