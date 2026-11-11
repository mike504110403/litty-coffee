package ig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MediaItem 是 IG 帳號上的一則貼文或限動（GET /{ig-user-id}/media、/stories 的一筆）。
type MediaItem struct {
	ID               string
	Caption          string
	MediaType        string // IMAGE | VIDEO | CAROUSEL_ALBUM
	MediaProductType string // FEED | REELS | STORY | AD
	Timestamp        time.Time
	Permalink        string
	ThumbnailURL     string // 影片才有
	MediaURL         string // 影片是影片檔網址
}

// ThumbURL 回適合當縮圖下載的網址：影片用 thumbnail_url；圖片／輪播用 media_url；影片沒縮圖時回空（不下載影片檔）。
func (m MediaItem) ThumbURL() string {
	if m.ThumbnailURL != "" {
		return m.ThumbnailURL
	}
	if m.MediaType == "VIDEO" {
		return ""
	}
	return m.MediaURL
}

// MediaLister 抽象「讀 IG 帳號上的貼文與限動」；正式環境是 Client（graph.facebook.com），模擬模式是 FakePublisher。
type MediaLister interface {
	// ListMedia 由新到舊翻頁，遇到早於 since 的就停（since 為零值＝不限），最多 MaxListMedia 篇。
	ListMedia(ctx context.Context, igUserID, token string, since time.Time) ([]MediaItem, error)
	// ListStories 回目前還在的限動（IG 只給 24 小時內的）。
	ListStories(ctx context.Context, igUserID, token string) ([]MediaItem, error)
	// FetchThumb 下載縮圖（IG CDN 網址，不帶 token）。
	FetchThumb(ctx context.Context, rawURL string) ([]byte, error)
}

const (
	MaxListMedia   = 200
	MaxListStories = 100
	MaxThumbBytes  = 8 << 20
	mediaPageSize  = 50
	mediaFields    = "id,caption,media_type,media_product_type,timestamp,permalink,thumbnail_url,media_url"
)

type mediaJSON struct {
	ID               string `json:"id"`
	Caption          string `json:"caption"`
	MediaType        string `json:"media_type"`
	MediaProductType string `json:"media_product_type"`
	Timestamp        string `json:"timestamp"`
	Permalink        string `json:"permalink"`
	ThumbnailURL     string `json:"thumbnail_url"`
	MediaURL         string `json:"media_url"`
}

type mediaPage struct {
	Data   []mediaJSON `json:"data"`
	Paging struct {
		Cursors struct {
			After string `json:"after"`
		} `json:"cursors"`
		Next string `json:"next"`
	} `json:"paging"`
}

// parseIGTime：Graph API 的時間是 2017-08-31T18:10:00+0000（時區沒有冒號），也接受 RFC3339。
func parseIGTime(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02T15:04:05-0700", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func (m mediaJSON) item() (MediaItem, error) {
	ts, err := parseIGTime(m.Timestamp)
	if err != nil {
		return MediaItem{}, fmt.Errorf("media %s: 時間格式不正確 %q", m.ID, m.Timestamp)
	}
	return MediaItem{ID: m.ID, Caption: m.Caption, MediaType: m.MediaType, MediaProductType: m.MediaProductType,
		Timestamp: ts, Permalink: m.Permalink, ThumbnailURL: m.ThumbnailURL, MediaURL: m.MediaURL}, nil
}

// listPaged 跟著 paging.cursors.after 翻頁（不用 paging.next 的完整網址，只打自己組的 Base 路徑）。
// stop 回 true 時停止（該筆不收）；收滿 max 篇也停。
func (c *Client) listPaged(ctx context.Context, path, token string, max int, stop func(MediaItem) bool) ([]MediaItem, error) {
	var out []MediaItem
	after := ""
	for len(out) < max {
		q := url.Values{"fields": {mediaFields}, "limit": {fmt.Sprint(mediaPageSize)}}
		if after != "" {
			q.Set("after", after)
		}
		var page mediaPage
		if err := c.do(ctx, http.MethodGet, c.Base+path+"?"+q.Encode(), token, nil, &page); err != nil {
			return out, err
		}
		for _, d := range page.Data {
			it, err := d.item()
			if err != nil {
				return out, err
			}
			if stop != nil && stop(it) {
				return out, nil
			}
			out = append(out, it)
			if len(out) >= max {
				return out, nil
			}
		}
		if page.Paging.Next == "" || page.Paging.Cursors.After == "" || len(page.Data) == 0 {
			break
		}
		after = page.Paging.Cursors.After
	}
	return out, nil
}

func (c *Client) ListMedia(ctx context.Context, igUserID, token string, since time.Time) ([]MediaItem, error) {
	return c.listPaged(ctx, "/"+url.PathEscape(igUserID)+"/media", token, MaxListMedia, func(it MediaItem) bool {
		return !since.IsZero() && it.Timestamp.Before(since)
	})
}

func (c *Client) ListStories(ctx context.Context, igUserID, token string) ([]MediaItem, error) {
	return c.listPaged(ctx, "/"+url.PathEscape(igUserID)+"/stories", token, MaxListStories, nil)
}

// FetchThumb 下載 IG CDN 上的縮圖：只接受 https、不帶 token、上限 MaxThumbBytes。
func (c *Client) FetchThumb(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("縮圖網址不正確")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下載縮圖失敗（HTTP %d）", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxThumbBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxThumbBytes {
		return nil, errors.New("縮圖超過 8MB")
	}
	return data, nil
}

// ---- 模擬模式 ----

// fakeMedia 是 FakePublisher 給模擬帳號的假貼文／限動（第一次查詢時依當下時間產生，之後固定，ID 穩定才能測去重）。
type fakeMedia struct {
	posts, stories []MediaItem
}

type fakeMediaState struct {
	mu        sync.Mutex
	byAccount map[string]*fakeMedia
	// mediaErr 非 nil 時 ListMedia／ListStories 回這個錯（測試 token 失效等）
	mediaErr   error
	listCalls  []ListCall
	thumbCalls int
}

// ListCall 是 FakePublisher 收到的一次 ListMedia／ListStories。
type ListCall struct {
	Kind, UserID, Token string // Kind：media | stories
	Since               time.Time
}

func (f *FakePublisher) media(userID string) *fakeMedia {
	if f.mediaState.byAccount == nil {
		f.mediaState.byAccount = map[string]*fakeMedia{}
	}
	m := f.mediaState.byAccount[userID]
	if m == nil {
		now := time.Now().Truncate(time.Second)
		id := func(s string) string { return "dev-ig-" + userID + "-" + s }
		thumb := func(s string) string { return "https://fake-cdn.invalid/" + userID + "/" + s + ".jpg" }
		m = &fakeMedia{
			posts: []MediaItem{
				{ID: id("p1"), MediaType: "IMAGE", MediaProductType: "FEED", Timestamp: now.Add(-3 * 24 * time.Hour),
					Caption: "本週新品：焦糖海鹽拿鐵 ☕️ 鹹甜平衡、奶香濃郁，冷熱都有。\n#littycoffee #新品", MediaURL: thumb("p1")},
				{ID: id("p2"), MediaType: "CAROUSEL_ALBUM", MediaProductType: "FEED", Timestamp: now.Add(-10 * 24 * time.Hour),
					Caption: "會員日活動：本週六日全品項 9 折，出示會員卡即可。\n#會員日", MediaURL: thumb("p2")},
				{ID: id("p3"), MediaType: "VIDEO", MediaProductType: "REELS", Timestamp: now.Add(-20 * 24 * time.Hour),
					Caption: "30 秒看懂拉花：愛心篇 💛", ThumbnailURL: thumb("p3"), MediaURL: "https://fake-cdn.invalid/" + userID + "/p3.mp4"},
			},
			stories: []MediaItem{
				{ID: id("s1"), MediaType: "IMAGE", MediaProductType: "STORY", Timestamp: now.Add(-2 * time.Hour),
					Caption: "今天下午三點後外帶第二杯半價（限今日）", MediaURL: thumb("s1")},
			},
		}
		for i := range m.posts {
			m.posts[i].Permalink = "https://www.instagram.com/p/" + m.posts[i].ID + "/"
		}
		m.stories[0].Permalink = "https://www.instagram.com/stories/" + userID + "/" + m.stories[0].ID + "/"
		f.mediaState.byAccount[userID] = m
	}
	return m
}

// SetMedia／SetStories 換掉某帳號的假貼文／限動（測試用；依傳入順序回，應由新到舊）。
func (f *FakePublisher) SetMedia(userID string, items []MediaItem) {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	f.media(userID).posts = append([]MediaItem(nil), items...)
}

func (f *FakePublisher) SetStories(userID string, items []MediaItem) {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	f.media(userID).stories = append([]MediaItem(nil), items...)
}

// SetMediaErr 讓之後的 ListMedia／ListStories 回這個錯（nil＝恢復）。
func (f *FakePublisher) SetMediaErr(err error) {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	f.mediaState.mediaErr = err
}

// MediaListCalls 回目前為止的 ListMedia／ListStories 呼叫紀錄複本。
func (f *FakePublisher) MediaListCalls() []ListCall {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	return append([]ListCall(nil), f.mediaState.listCalls...)
}

func (f *FakePublisher) ListMedia(_ context.Context, userID, token string, since time.Time) ([]MediaItem, error) {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	f.mediaState.listCalls = append(f.mediaState.listCalls, ListCall{Kind: "media", UserID: userID, Token: token, Since: since})
	if f.mediaState.mediaErr != nil {
		return nil, f.mediaState.mediaErr
	}
	var out []MediaItem
	for _, it := range f.media(userID).posts {
		if !since.IsZero() && it.Timestamp.Before(since) {
			break
		}
		out = append(out, it)
		if len(out) >= MaxListMedia {
			break
		}
	}
	return out, nil
}

func (f *FakePublisher) ListStories(_ context.Context, userID, token string) ([]MediaItem, error) {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	f.mediaState.listCalls = append(f.mediaState.listCalls, ListCall{Kind: "stories", UserID: userID, Token: token})
	if f.mediaState.mediaErr != nil {
		return nil, f.mediaState.mediaErr
	}
	var out []MediaItem
	for _, it := range f.media(userID).stories {
		if time.Since(it.Timestamp) <= 24*time.Hour {
			out = append(out, it)
		}
	}
	return out, nil
}

// FetchThumb 不連網：依網址產生一張純色 JPEG，讓地端畫面看得到縮圖。
func (f *FakePublisher) FetchThumb(_ context.Context, rawURL string) ([]byte, error) {
	f.mediaState.mu.Lock()
	f.mediaState.thumbCalls++
	f.mediaState.mu.Unlock()
	if !strings.HasPrefix(rawURL, "https://") {
		return nil, errors.New("縮圖網址不正確")
	}
	var h uint32
	for _, c := range rawURL {
		h = h*31 + uint32(c)
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	col := color.RGBA{R: uint8(120 + h%100), G: uint8(80 + (h/7)%80), B: uint8(50 + (h/13)%60), A: 255}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, col)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FakeThumbCalls 回 FetchThumb 被呼叫的次數（測試驗證不重複下載）。
func (f *FakePublisher) FakeThumbCalls() int {
	f.mediaState.mu.Lock()
	defer f.mediaState.mu.Unlock()
	return f.mediaState.thumbCalls
}
