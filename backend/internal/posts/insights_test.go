package posts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/testdb"
)

// seedPost 直接寫一篇指定狀態的貼文（繞過發佈流程）。
func seedPost(t *testing.T, pool *pgxpool.Pool, kind, status, mediaID string, publishedAgo time.Duration) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
INSERT INTO scheduled_posts (kind, status, ig_media_id, published_at, scheduled_at, ig_account_id)
VALUES ($1, $2, $3, now() - $4::interval, now() - $4::interval, $5) RETURNING id`,
		kind, status, mediaID, secs(publishedAgo), testAcc).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newRefresher(s *Service) (*InsightsRefresher, *ig.FakePublisher) {
	f := ig.NewFakePublisher()
	return &InsightsRefresher{Svc: s, IG: f, Creds: testCreds(s.DB)}, f
}

func TestInsightsWrittenForPublishedOnly(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	pub := seedPost(t, pool, "image", "published", "m-pub", time.Hour)
	reel := seedPost(t, pool, "reel", "published", "m-reel", 2*24*time.Hour)
	draft := seedPost(t, pool, "image", "draft", "", time.Hour)
	failed := seedPost(t, pool, "image", "failed", "", time.Hour)
	sched := seedPost(t, pool, "image", "scheduled", "", -time.Hour)

	r, f := newRefresher(s)
	n, err := r.RunOnce(ctx, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := s.Get(ctx, pub)
	if got.InsightsAt == nil || got.Insights["views"] == 0 || got.Insights["reach"] == 0 || got.InsightsError != "" {
		t.Fatalf("published post: %+v", got)
	}
	if _, ok := got.Insights["likes"]; !ok {
		t.Fatalf("feed post should carry likes: %v", got.Insights)
	}
	if got, _ := s.Get(ctx, reel); got.InsightsAt == nil {
		t.Fatal("reel not refreshed")
	}
	for _, id := range []int64{draft, failed, sched} {
		p, _ := s.Get(ctx, id)
		if p.InsightsAt != nil || len(p.Insights) != 0 {
			t.Fatalf("post %d (%s) should be skipped: %+v", id, p.Status, p.Insights)
		}
	}
	if len(f.InsightsCalls) != 2 {
		t.Fatalf("calls: %v", f.InsightsCalls)
	}
	// 剛更新過：同一輪再跑不會重查
	if n, _ := r.RunOnce(ctx, time.Now()); n != 0 {
		t.Fatalf("second run refreshed %d", n)
	}
}

func TestInsightsCadenceAndWindow(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	young := seedPost(t, pool, "image", "published", "m-young", 2*24*time.Hour)      // 未滿 7 天：每 6 小時
	old := seedPost(t, pool, "image", "published", "m-old", 10*24*time.Hour)         // 7～30 天：每天
	ancient := seedPost(t, pool, "image", "published", "m-ancient", 31*24*time.Hour) // 超過 30 天：不更新

	r, f := newRefresher(s)
	now := time.Now()
	if n, _ := r.RunOnce(ctx, now); n != 2 {
		t.Fatalf("first: %d", n)
	}
	if n, _ := r.RunOnce(ctx, now.Add(7*time.Hour)); n != 1 || f.InsightsCalls["m-young"] != 2 || f.InsightsCalls["m-old"] != 1 {
		t.Fatalf("after 7h: n=%d calls=%v", n, f.InsightsCalls)
	}
	if n, _ := r.RunOnce(ctx, now.Add(25*time.Hour)); n != 2 {
		t.Fatalf("after 25h: %d", n)
	}
	if f.InsightsCalls["m-ancient"] != 0 {
		t.Fatal("posts older than 30 days must not be refreshed")
	}
	_, _, _ = young, old, ancient
}

func TestInsightsStoryOnlyWithin24h(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	fresh := seedPost(t, pool, "story", "published", "m-story-new", 3*time.Hour)
	stale := seedPost(t, pool, "story", "published", "m-story-old", 25*time.Hour)

	r, f := newRefresher(s)
	if n, _ := r.RunOnce(ctx, time.Now()); n != 1 {
		t.Fatalf("n=%d", n)
	}
	if f.InsightsCalls["m-story-old"] != 0 {
		t.Fatal("story older than 24h was fetched")
	}
	got, _ := s.Get(ctx, fresh)
	if _, ok := got.Insights["replies"]; !ok || got.Insights["likes"] != 0 {
		t.Fatalf("story metrics: %v", got.Insights)
	}
	if !got.InsightsRefresh {
		t.Fatal("fresh story should be refreshable")
	}
	// 手動更新也不能繞過 24 小時限制
	if _, err := r.RefreshPost(ctx, stale); err != ErrInsightsUnavailable {
		t.Fatalf("manual refresh of expired story: %v", err)
	}
	if p, _ := s.Get(ctx, stale); p.InsightsRefresh {
		t.Fatal("expired story must not be marked refreshable")
	}
}

func TestInsightsOneFailureDoesNotBlockOthers(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	bad := seedPost(t, pool, "image", "published", "m-bad", 5*time.Hour)
	good1 := seedPost(t, pool, "image", "published", "m-good1", 4*time.Hour)
	good2 := seedPost(t, pool, "reel", "published", "m-good2", 6*time.Hour)

	r, f := newRefresher(s)
	f.InsightsFail["m-bad"] = true
	n, err := r.RunOnce(ctx, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if p, _ := s.Get(ctx, bad); p.InsightsAt != nil || p.InsightsError == "" {
		t.Fatalf("bad post: %+v", p)
	}
	for _, id := range []int64{good1, good2} {
		if p, _ := s.Get(ctx, id); p.InsightsAt == nil || p.Insights["views"] == 0 {
			t.Fatalf("good post %d not refreshed: %+v", id, p)
		}
	}
	// 手動更新失敗會把錯誤回給呼叫端；恢復後成功並清掉錯誤
	if _, err := r.RefreshPost(ctx, bad); err == nil {
		t.Fatal("expected error")
	}
	f.InsightsFail["m-bad"] = false
	p, err := r.RefreshPost(ctx, bad)
	if err != nil || p.InsightsError != "" || p.InsightsAt == nil {
		t.Fatalf("recovered: %+v %v", p, err)
	}
}

func TestInsightsNoTokenAndNotPublished(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	pub := seedPost(t, pool, "image", "published", "m1", time.Hour)
	draft := seedPost(t, pool, "image", "draft", "", time.Hour)
	r, _ := newRefresher(s)
	if _, err := r.RefreshPost(ctx, draft); err != ErrInsightsUnavailable {
		t.Fatalf("draft: %v", err)
	}
	r.Creds = nil
	if n, err := r.RunOnce(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("no token: n=%d err=%v", n, err)
	}
	if _, err := r.RefreshPost(ctx, pub); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no token refresh: %v", err)
	}
}

// 成效用貼文帳號的 token 查。
func TestInsightsUseAccountToken(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	acc2 := testdb.ConnectedAccount(t, pool, "acct-2")
	id := seedPost(t, pool, "image", "published", "m-two", time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE scheduled_posts SET ig_account_id = $2 WHERE id = $1`, id, acc2); err != nil {
		t.Fatal(err)
	}
	r, f := newRefresher(s)
	if _, err := r.RefreshPost(ctx, id); err != nil {
		t.Fatal(err)
	}
	if f.InsightToken["m-two"] != "tok-acct-2" {
		t.Fatalf("insights token %q", f.InsightToken["m-two"])
	}
}
