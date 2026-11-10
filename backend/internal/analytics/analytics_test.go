package analytics

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/db"
)

func setup(t *testing.T) *pgxpool.Pool {
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
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE webhook_events, messages, conversations, contacts, ig_accounts, auto_rules, tags RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool
}

type msg struct {
	dir, sender, intent string
	at                  time.Time
}

func seedConv(t *testing.T, pool *pgxpool.Pool, igsid string, firstSeen time.Time, msgs []msg) (contactID, convID int64) {
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO ig_accounts (ig_user_id) VALUES ('acct') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO contacts (ig_account_id, igsid, username, first_seen_at, last_seen_at)
VALUES ((SELECT id FROM ig_accounts WHERE ig_user_id='acct'), $1, $1, $2, $2) RETURNING id`, igsid, firstSeen).Scan(&contactID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO conversations (contact_id) VALUES ($1) RETURNING id`, contactID).Scan(&convID); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		meta := `{}`
		if m.intent != "" {
			meta = `{"intent":"` + m.intent + `"}`
		}
		if _, err := pool.Exec(ctx, `INSERT INTO messages (conversation_id, direction, sender_type, text, meta, created_at)
VALUES ($1, $2, $3, 'x', $4::jsonb, $5)`, convID, m.dir, m.sender, meta, m.at); err != nil {
			t.Fatal(err)
		}
	}
	return contactID, convID
}

func TestSummary(t *testing.T) {
	pool := setup(t)
	loc, _ := time.LoadLocation("Asia/Taipei")
	// 「現在」＝台北 11/11 10:00；7 天區間＝11/05 00:00 ～ 11/12 00:00（台北）
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, loc)
	at := func(day, h, m, s int) time.Time { return time.Date(2026, 11, day, h, m, s, 0, loc) }

	// A：今天 09:00 問營業時間，AI 10 秒回；09:30 再問，真人 5 分鐘後回
	seedConv(t, pool, "a", at(11, 9, 0, 0), []msg{
		{"in", "contact", "hours", at(11, 9, 0, 0)},
		{"in", "contact", "", at(11, 9, 0, 5)}, // 同一回合，不另算
		{"out", "ai", "", at(11, 9, 0, 10)},
		{"in", "contact", "reservation", at(11, 9, 30, 0)},
		{"out", "human", "", at(11, 9, 35, 0)},
	})
	// B：11/06 00:30（台北，UTC 是 11/05）問菜單，規則 2 秒回 → 落在 11/06
	seedConv(t, pool, "b", at(6, 0, 30, 0), []msg{
		{"in", "contact", "menu", at(6, 0, 30, 0)},
		{"out", "bot", "", at(6, 0, 30, 2)},
	})
	// C：上一個區間（11/01）的訊息，只進 previous；今天的未回覆訊息
	seedConv(t, pool, "c", at(1, 12, 0, 0), []msg{
		{"in", "contact", "menu", at(1, 12, 0, 0)},
		{"out", "human", "", at(1, 12, 1, 0)},
		{"in", "contact", "complaint", at(11, 8, 0, 0)},
	})

	svc := &Service{DB: pool, Loc: loc}
	sum, err := svc.Summary(context.Background(), 7, now)
	if err != nil {
		t.Fatal(err)
	}
	c := sum.Current
	if c.Totals.Inbound != 5 || c.Totals.OutboundAuto != 2 || c.Totals.OutboundHuman != 1 {
		t.Fatalf("totals: %+v", c.Totals)
	}
	if c.Totals.NewContacts != 2 || c.Totals.ActiveContacts != 3 || c.Totals.HumanConvs != 1 {
		t.Fatalf("contacts: %+v", c.Totals)
	}
	if c.AutoShare == nil || math.Abs(*c.AutoShare-2.0/3) > 1e-9 {
		t.Fatalf("autoShare: %v", c.AutoShare)
	}
	// 回合：A 09:00（10s）、A 09:30（300s）、B（2s）、C 今天（未回）
	r := c.Response
	if r.Turns != 4 || r.Answered != 3 {
		t.Fatalf("response turns: %+v", r)
	}
	if math.Abs(*r.AvgSecs-104) > 1e-6 || math.Abs(*r.MedianSecs-10) > 1e-6 ||
		math.Abs(*r.AutoAvg-6) > 1e-6 || math.Abs(*r.HumanAvg-300) > 1e-6 {
		t.Fatalf("response secs: avg=%v med=%v auto=%v human=%v", *r.AvgSecs, *r.MedianSecs, *r.AutoAvg, *r.HumanAvg)
	}
	if sum.Previous.Totals.Inbound != 1 || sum.Previous.Totals.OutboundHuman != 1 {
		t.Fatalf("previous: %+v", sum.Previous.Totals)
	}

	if len(sum.Daily) != 7 || sum.Daily[0].Date != "2026-11-05" || sum.Daily[6].Date != "2026-11-11" {
		t.Fatalf("daily range: %+v", sum.Daily)
	}
	if d := sum.Daily[1]; d.Date != "2026-11-06" || d.Inbound != 1 || d.Auto != 1 {
		t.Fatalf("11/06 bucket (tz boundary): %+v", d)
	}
	if d := sum.Daily[6]; d.Inbound != 4 || d.Auto != 1 || d.Human != 1 {
		t.Fatalf("today bucket: %+v", d)
	}
	if sum.Hours[9] != 3 || sum.Hours[0] != 1 || sum.Hours[8] != 1 {
		t.Fatalf("hours: %v", sum.Hours)
	}
	got := map[string]int{}
	for _, it := range sum.Intents {
		got[it.Key] = it.Count
	}
	if got["hours"] != 1 || got["reservation"] != 1 || got["menu"] != 1 || got["complaint"] != 1 || len(got) != 4 {
		t.Fatalf("intents: %v", sum.Intents)
	}
}

func TestEmptySummary(t *testing.T) {
	pool := setup(t)
	loc, _ := time.LoadLocation("Asia/Taipei")
	sum, err := (&Service{DB: pool, Loc: loc}).Summary(context.Background(), 30, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Current.AutoShare != nil || sum.Current.Response.AvgSecs != nil || len(sum.Daily) != 30 {
		t.Fatalf("empty summary: %+v", sum.Current)
	}
}

func TestContactsAndTags(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	now := time.Now()
	cid, _ := seedConv(t, pool, "latte", now, []msg{
		{"in", "contact", "menu", now.Add(-time.Hour)},
		{"out", "bot", "", now.Add(-time.Hour + time.Second)},
		{"in", "contact", "reservation", now},
	})
	svc := &crm.Service{DB: pool}

	vip, err := svc.CreateTag(ctx, "常客", "")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := svc.CreateTag(ctx, " 常客 ", "")
	if again.ID != vip.ID {
		t.Fatal("same-name tag should be reused")
	}
	dog, _ := svc.CreateTag(ctx, "帶狗", "")

	note := "喜歡燕麥拿鐵"
	if err := svc.Update(ctx, cid, &note, []int64{vip.ID, dog.ID}); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Get(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	if d.Note != note || len(d.Tags) != 2 || d.InboundCount != 2 || d.LastIntent != "reservation" || d.OutboundByAI != 1 {
		t.Fatalf("detail: %+v", d)
	}
	if len(d.Intents) != 2 {
		t.Fatalf("intents: %+v", d.Intents)
	}

	// 只留一個標籤、備註不動
	if err := svc.Update(ctx, cid, nil, []int64{dog.ID}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, crm.ListFilter{TagID: dog.ID})
	if err != nil || len(list) != 1 || len(list[0].Tags) != 1 || list[0].Note != note {
		t.Fatalf("filter by tag: %+v err=%v", list, err)
	}
	if list, _ := svc.List(ctx, crm.ListFilter{TagID: vip.ID}); len(list) != 0 {
		t.Fatalf("removed tag still matches: %+v", list)
	}
	if list, _ := svc.List(ctx, crm.ListFilter{Q: "燕麥"}); len(list) != 1 {
		t.Fatal("search by note failed")
	}
	if err := svc.Update(ctx, 9999, &note, nil); err != crm.ErrNotFound {
		t.Fatalf("missing contact: %v", err)
	}
	tags, _ := svc.Tags(ctx)
	if len(tags) != 2 {
		t.Fatalf("tags: %+v", tags)
	}
}
