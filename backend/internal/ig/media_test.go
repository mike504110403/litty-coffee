package ig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ListMedia：帶 fields／limit、Bearer token；跟著 cursors.after 翻頁（不打 paging.next 的網址），遇到早於 since 就停。
func TestClientListMediaPaging(t *testing.T) {
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.URL.String())
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/ig-1/media" || r.URL.Query().Get("fields") != mediaFields || r.URL.Query().Get("limit") != "50" {
			t.Errorf("unexpected request %s", r.URL)
		}
		page := map[string]any{}
		switch r.URL.Query().Get("after") {
		case "":
			page["data"] = []map[string]string{
				{"id": "1", "caption": "新", "media_type": "IMAGE", "media_product_type": "FEED", "timestamp": "2026-11-10T08:00:00+0000", "media_url": "https://cdn/1.jpg"},
				{"id": "2", "media_type": "VIDEO", "media_product_type": "REELS", "timestamp": "2026-11-09T08:00:00+0000", "thumbnail_url": "https://cdn/2.jpg", "media_url": "https://cdn/2.mp4"},
			}
			page["paging"] = map[string]any{"cursors": map[string]string{"after": "c2"}, "next": "https://evil.example/next"}
		case "c2":
			page["data"] = []map[string]string{
				{"id": "3", "caption": "舊一點", "media_type": "CAROUSEL_ALBUM", "timestamp": "2026-11-05T08:00:00+0000"},
				{"id": "4", "caption": "太舊", "media_type": "IMAGE", "timestamp": "2026-10-01T08:00:00+0000"},
			}
			page["paging"] = map[string]any{"cursors": map[string]string{"after": "c3"}, "next": "https://evil.example/next2"}
		default:
			t.Errorf("should stop before page %s", r.URL.Query().Get("after"))
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	items, err := c.ListMedia(context.Background(), "ig-1", "tok", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].ID != "1" || items[2].ID != "3" || len(reqs) != 2 {
		t.Fatalf("items=%+v reqs=%v", items, reqs)
	}
	if !items[0].Timestamp.Equal(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)) || items[0].Caption != "新" {
		t.Fatalf("parse: %+v", items[0])
	}
	if items[0].ThumbURL() != "https://cdn/1.jpg" || items[1].ThumbURL() != "https://cdn/2.jpg" || (MediaItem{MediaType: "VIDEO", MediaURL: "x.mp4"}).ThumbURL() != "" {
		t.Fatal("ThumbURL：圖片用 media_url、影片用 thumbnail_url、影片沒縮圖不下載影片檔")
	}
}

// 第一次同步（since 零值）最多 200 篇。
func TestClientListMediaCap(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data []map[string]string
		for i := 0; i < 50; i++ {
			n++
			data = append(data, map[string]string{"id": fmt.Sprint(n), "media_type": "IMAGE", "timestamp": "2026-11-10T08:00:00+0000"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "paging": map[string]any{"cursors": map[string]string{"after": fmt.Sprint(n)}, "next": "x"}})
	}))
	defer srv.Close()
	items, err := NewClient(srv.URL).ListMedia(context.Background(), "ig-1", "tok", time.Time{})
	if err != nil || len(items) != MaxListMedia {
		t.Fatalf("len=%d err=%v", len(items), err)
	}
}

func TestClientListStoriesAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stories") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
				{"id": "s1", "media_type": "IMAGE", "media_product_type": "STORY", "timestamp": "2026-11-10T08:00:00+0000"}}})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"expired","code":190}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	st, err := c.ListStories(context.Background(), "ig-1", "tok")
	if err != nil || len(st) != 1 || st[0].MediaProductType != "STORY" {
		t.Fatalf("stories: %+v %v", st, err)
	}
	if _, err := c.ListMedia(context.Background(), "ig-1", "tok", time.Time{}); !IsTokenInvalid(err) {
		t.Fatalf("token 失效要能判斷：%v", err)
	}
	if _, err := c.FetchThumb(context.Background(), srv.URL+"/x.jpg"); err == nil {
		t.Fatal("縮圖只接受 https")
	}
}
