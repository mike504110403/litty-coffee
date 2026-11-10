package posts

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
)

func TestCountHashtags(t *testing.T) {
	if n := CountHashtags("今天 #拿鐵 #latte_art 新品 #咖啡廳 # 不算 #台北2026"); n != 4 {
		t.Fatalf("got %d", n)
	}
}

func setup(t *testing.T) (*Service, *pgxpool.Pool) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := db.Migrate(url); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), `TRUNCATE scheduled_posts, media_assets RESTART IDENTITY`); err != nil {
		t.Fatal(err)
	}
	store := &media.Store{DB: pool, Dir: t.TempDir(), PublicBase: "https://example.test"}
	return NewService(pool, store), pool
}

func pngBytes(t *testing.T, w, h int, transparent bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{200, 120, 60, 255}
			if transparent {
				c.A = 0
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func upload(t *testing.T, s *Service, data []byte) media.Asset {
	a, err := s.Media.Save(context.Background(), "x.png", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// 最小 MP4：只要有 ftyp box，DetectContentType 就會判為 video/mp4
func fakeMP4() []byte {
	return append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 'i', 's', 'o', 'm', 'i', 's', 'o', '2'}, make([]byte, 64)...)
}

func TestMediaSave(t *testing.T) {
	s, _ := setup(t)
	a := upload(t, s, pngBytes(t, 40, 50, true))
	if a.Kind != "image" || a.ContentType != "image/jpeg" || a.Width != 40 || a.Height != 50 || !strings.HasSuffix(a.Filename, ".jpg") {
		t.Fatalf("png→jpeg: %+v", a)
	}
	f, err := os.ReadFile(s.Media.Dir + "/" + a.Filename)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(f))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := img.At(5, 5).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Fatalf("transparent area should be white, got %d %d %d", r>>8, g>>8, b>>8)
	}
	if v := upload(t, s, fakeMP4()); v.Kind != "video" || v.ContentType != "video/mp4" {
		t.Fatalf("mp4: %+v", v)
	}
	if _, err := s.Media.Save(context.Background(), "a.txt", strings.NewReader("hello")); err == nil {
		t.Fatal("text file should be rejected")
	}
	if got := s.Media.PublicURL(a.Filename); got != "https://example.test/media/"+a.Filename {
		t.Fatal(got)
	}
}

func TestValidate(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	now := time.Now()
	square := upload(t, s, pngBytes(t, 50, 50, false)).ID
	tall := upload(t, s, pngBytes(t, 50, 100, false)).ID // 1:2 太高
	video := upload(t, s, fakeMP4()).ID
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	cases := []struct {
		name string
		p    Post
		ok   bool
	}{
		{"image ok", Post{Kind: "image", MediaIDs: []int64{square}, ScheduledAt: &future}, true},
		{"image ratio", Post{Kind: "image", MediaIDs: []int64{tall}, ScheduledAt: &future}, false},
		{"image needs one", Post{Kind: "image", MediaIDs: []int64{square, square}, ScheduledAt: &future}, false},
		{"image not video", Post{Kind: "image", MediaIDs: []int64{video}, ScheduledAt: &future}, false},
		{"carousel ok", Post{Kind: "carousel", MediaIDs: []int64{square, video}, ScheduledAt: &future}, true},
		{"carousel too few", Post{Kind: "carousel", MediaIDs: []int64{square}, ScheduledAt: &future}, false},
		{"reel ok", Post{Kind: "reel", MediaIDs: []int64{video}, ScheduledAt: &future}, true},
		{"reel needs video", Post{Kind: "reel", MediaIDs: []int64{square}, ScheduledAt: &future}, false},
		{"story image", Post{Kind: "story", MediaIDs: []int64{tall}, ScheduledAt: &future}, true},
		{"past time", Post{Kind: "image", MediaIDs: []int64{square}, ScheduledAt: &past}, false},
		{"no time", Post{Kind: "image", MediaIDs: []int64{square}}, false},
		{"too many tags", Post{Kind: "image", MediaIDs: []int64{square}, ScheduledAt: &future,
			Caption: strings.Repeat("#tag ", 31)}, false},
		{"missing media", Post{Kind: "image", MediaIDs: []int64{9999}, ScheduledAt: &future}, false},
	}
	for _, c := range cases {
		p := c.p
		err := s.Validate(ctx, &p, true, now)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

func newScheduler(s *Service) (*Scheduler, *ig.FakePublisher) {
	f := ig.NewFakePublisher()
	return &Scheduler{Svc: s, IG: f, Token: "t", PollInterval: time.Millisecond, PollTimeout: time.Second}, f
}

func TestSaveAndPublish(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	now := time.Now()
	img := upload(t, s, pngBytes(t, 50, 50, false)).ID

	// 草稿：媒體可空、不會被發佈
	draft, err := s.Save(ctx, Post{Kind: "image", Caption: "草稿"}, "draft", now)
	if err != nil || draft.Status != "draft" {
		t.Fatalf("draft: %+v %v", draft, err)
	}
	// 未來排程
	later := now.Add(2 * time.Hour)
	future, err := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}, ScheduledAt: &later, Caption: "晚點發"}, "schedule", now)
	if err != nil || future.Status != "scheduled" {
		t.Fatalf("schedule: %+v %v", future, err)
	}
	// 立即發佈
	nowPost, err := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}, Caption: "#新品 上市"}, "now", now)
	if err != nil {
		t.Fatal(err)
	}

	sc, fake := newScheduler(s)
	if done, err := sc.RunOnce(ctx, now.Add(time.Second)); done || err != nil {
		t.Fatalf("first run: done=%v err=%v", done, err)
	}
	if done, _ := sc.RunOnce(ctx, now.Add(time.Second)); !done {
		t.Fatal("only the due post should be picked")
	}
	got, _ := s.Get(ctx, nowPost.ID)
	if got.Status != "published" || got.Permalink == "" || got.PublishedAt == nil || fake.Published != 1 {
		t.Fatalf("published: %+v", got)
	}
	if got, _ := s.Get(ctx, future.ID); got.Status != "scheduled" {
		t.Fatalf("future post touched: %s", got.Status)
	}
	if got, _ := s.Get(ctx, draft.ID); got.Status != "draft" {
		t.Fatalf("draft touched: %s", got.Status)
	}

	// 已發佈不能改、可刪本地紀錄
	if _, err := s.Save(ctx, Post{ID: nowPost.ID, Kind: "image", MediaIDs: []int64{img}}, "draft", now); err != ErrImmutable {
		t.Fatalf("edit published: %v", err)
	}
	if err := s.Delete(ctx, nowPost.ID); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountUpcoming(ctx)
	if n != 1 {
		t.Fatalf("upcoming = %d", n)
	}
	_ = pool
}

func TestRetryAndExhaust(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	now := time.Now()
	img := upload(t, s, pngBytes(t, 50, 50, false)).ID
	p, _ := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}}, "now", now)

	sc, fake := newScheduler(s)
	fake.FailNext = 1
	_, _ = sc.RunOnce(ctx, now.Add(time.Second))
	got, _ := s.Get(ctx, p.ID)
	if got.Status != "scheduled" || got.Attempts != 1 || got.Error == "" || got.NextAttemptAt == nil || !got.NextAttemptAt.After(now.Add(50*time.Second)) {
		t.Fatalf("after 1st failure: %+v", got)
	}
	// 還沒到重試時間 → 不動
	if done, _ := sc.RunOnce(ctx, now.Add(10*time.Second)); !done {
		t.Fatal("retry should wait for backoff")
	}
	// 到點重試成功：沿用同一個容器（不重建）
	_, _ = sc.RunOnce(ctx, now.Add(2*time.Minute))
	if got, _ := s.Get(ctx, p.ID); got.Status != "published" || got.Error != "" || fake.Published != 1 {
		t.Fatalf("retry success: %+v", got)
	}

	// 連續失敗用盡 → failed
	p2, _ := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}}, "now", now)
	fake.FailNext = 10
	at := now
	for i := 0; i < MaxAttempts; i++ {
		at = at.Add(10 * time.Minute)
		_, _ = sc.RunOnce(ctx, at)
	}
	got, _ = s.Get(ctx, p2.ID)
	if got.Status != "failed" || got.Attempts != MaxAttempts {
		t.Fatalf("exhausted: %+v", got)
	}
	// 失敗的可以修改後重新排程，計數歸零
	again, err := s.Save(ctx, Post{ID: p2.ID, Kind: "image", MediaIDs: []int64{img}}, "now", at)
	if err != nil || again.Status != "scheduled" || again.Attempts != 0 || again.Error != "" {
		t.Fatalf("reschedule failed post: %+v %v", again, err)
	}
}

func TestCrashRecoveryDoesNotRepost(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	now := time.Now()
	img := upload(t, s, pngBytes(t, 50, 50, false)).ID
	p, _ := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}}, "now", now)

	sc, fake := newScheduler(s)
	// 模擬：容器建好、IG 已發佈，但程式在寫回 DB 前當掉（狀態卡在 publishing）
	cid, _ := fake.CreateContainer(ctx, "u", "t", ig.ContainerSpec{ImageURL: "x"})
	_, _ = fake.Publish(ctx, "u", "t", cid)
	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET status='publishing', locked_at=$2, attempts=1, ig_container_id=$3 WHERE id=$1`,
		p.ID, now.Add(-time.Hour), cid); err != nil {
		t.Fatal(err)
	}
	before := fake.Published
	if _, err := sc.RunOnce(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, p.ID)
	if got.Status != "published" || fake.Published != before {
		t.Fatalf("recovery: status=%s published %d→%d", got.Status, before, fake.Published)
	}

	// 剛進 publishing（未逾時）不會被別人搶走
	p2, _ := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}}, "now", now)
	_, _ = pool.Exec(ctx, `UPDATE scheduled_posts SET status='publishing', locked_at=$2 WHERE id=$1`, p2.ID, now)
	if done, _ := sc.RunOnce(ctx, now.Add(time.Minute)); !done {
		t.Fatal("fresh publishing post must not be reclaimed")
	}
}

func TestCarousel(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	now := time.Now()
	a := upload(t, s, pngBytes(t, 50, 50, false)).ID
	v := upload(t, s, fakeMP4()).ID
	p, err := s.Save(ctx, Post{Kind: "carousel", MediaIDs: []int64{a, v, a}, Caption: "三張"}, "now", now)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := newScheduler(s)
	if _, err := sc.RunOnce(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, p.ID); got.Status != "published" || len(got.Media) != 3 || got.Media[1].Kind != "video" {
		t.Fatalf("carousel: %+v", got)
	}
}

func TestNoTokenFailsCleanly(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	now := time.Now()
	img := upload(t, s, pngBytes(t, 50, 50, false)).ID
	p, _ := s.Save(ctx, Post{Kind: "image", MediaIDs: []int64{img}}, "now", now)
	sc, _ := newScheduler(s)
	sc.Token = ""
	_, _ = sc.RunOnce(ctx, now.Add(time.Second))
	if got, _ := s.Get(ctx, p.ID); got.Status != "scheduled" || !strings.Contains(got.Error, "IG_ACCESS_TOKEN") {
		t.Fatalf("no token: %+v", got)
	}
}
