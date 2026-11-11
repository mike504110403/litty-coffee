package ig

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ContainerSpec 描述要建立的 IG 媒體容器。
type ContainerSpec struct {
	MediaType      string // "" (單張圖片) | REELS | STORIES | CAROUSEL
	ImageURL       string
	VideoURL       string
	Caption        string
	IsCarouselItem bool
	Children       []string
}

// Publisher 抽象 IG 內容發佈 API（Content Publishing）。
type Publisher interface {
	CreateContainer(ctx context.Context, userID, token string, spec ContainerSpec) (string, error)
	ContainerStatus(ctx context.Context, token, containerID string) (string, error) // IN_PROGRESS | FINISHED | PUBLISHED | ERROR | EXPIRED
	Publish(ctx context.Context, userID, token, containerID string) (mediaID string, err error)
	Permalink(ctx context.Context, token, mediaID string) (string, error)
	// Insights 取單篇已發佈媒體的成效；kind 是貼文類型（image | carousel | reel | story），各類型請求的 metric 不同。
	Insights(ctx context.Context, token, mediaID, kind string) (map[string]int64, error)
}

// InsightMetrics 回傳各貼文類型向 GET /{ig-media-id}/insights 請求的 metric。
// 來源：Instagram Platform「IG Media Insights」參考頁（Unified Login 走 graph.facebook.com，路徑相同）。
//   - FEED（image、carousel）：views、reach、likes、comments、saved、shares（impressions 對 2024-07-02 後的媒體已棄用，不用）
//   - REELS：同上六項（Reels 的 plays 等舊 metric 已由 views 取代）
//   - STORY：views、reach、replies、shares（無 likes／comments／saved；限動只在 24 小時內查得到）
//
// 任一 metric 該類型不支援時整個請求會失敗，所以每種類型只列文件明列支援的。
func InsightMetrics(kind string) []string {
	if kind == "story" {
		return []string{"views", "reach", "replies", "shares"}
	}
	return []string{"views", "reach", "likes", "comments", "saved", "shares"}
}

func (c *Client) CreateContainer(ctx context.Context, userID, token string, s ContainerSpec) (string, error) {
	body := map[string]any{}
	if s.MediaType != "" {
		body["media_type"] = s.MediaType
	}
	if s.ImageURL != "" {
		body["image_url"] = s.ImageURL
	}
	if s.VideoURL != "" {
		body["video_url"] = s.VideoURL
	}
	if s.Caption != "" {
		body["caption"] = s.Caption
	}
	if s.IsCarouselItem {
		body["is_carousel_item"] = true
	}
	if len(s.Children) > 0 {
		body["children"] = strings.Join(s.Children, ",")
	}
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/%s/media", c.Base, url.PathEscape(userID)), token, body, &out)
	return out.ID, err
}

func (c *Client) ContainerStatus(ctx context.Context, token, id string) (string, error) {
	var out struct {
		StatusCode string `json:"status_code"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%s?fields=status_code", c.Base, url.PathEscape(id)), token, nil, &out)
	return out.StatusCode, err
}

func (c *Client) Publish(ctx context.Context, userID, token, containerID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/%s/media_publish", c.Base, url.PathEscape(userID)), token,
		map[string]string{"creation_id": containerID}, &out)
	return out.ID, err
}

func (c *Client) Permalink(ctx context.Context, token, mediaID string) (string, error) {
	var out struct {
		Permalink string `json:"permalink"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%s?fields=permalink", c.Base, url.PathEscape(mediaID)), token, nil, &out)
	return out.Permalink, err
}

func (c *Client) Insights(ctx context.Context, token, mediaID, kind string) (map[string]int64, error) {
	var out struct {
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value float64 `json:"value"`
			} `json:"values"`
			TotalValue *struct {
				Value float64 `json:"value"`
			} `json:"total_value"`
		} `json:"data"`
	}
	q := url.Values{"metric": {strings.Join(InsightMetrics(kind), ",")}}
	u := fmt.Sprintf("%s/%s/insights?%s", c.Base, url.PathEscape(mediaID), q.Encode())
	if err := c.do(ctx, http.MethodGet, u, token, nil, &out); err != nil {
		return nil, err
	}
	res := map[string]int64{}
	for _, d := range out.Data {
		switch {
		case len(d.Values) > 0:
			res[d.Name] = int64(d.Values[0].Value)
		case d.TotalValue != nil:
			res[d.Name] = int64(d.TotalValue.Value)
		}
	}
	return res, nil
}

// FakePublisher 是開發期的假發佈：容器建立後立刻 FINISHED，發佈後狀態變 PUBLISHED。
type FakePublisher struct {
	mu        sync.Mutex
	status    map[string]string
	FailNext  int // 測試用：接下來幾次 Publish 回錯誤
	Published int

	InsightsFail  map[string]bool // 測試用：這些 mediaID 的 Insights 回錯誤
	InsightsCalls map[string]int  // 每個 mediaID 被查詢的次數

	Calls        []PublishCall     // 每次 Publish 用的帳號與 token（測試驗證用對帳號）
	InsightToken map[string]string // mediaID → 最後一次查成效用的 token
}

// PublishCall 是 FakePublisher 收到的一次發佈。
type PublishCall struct{ UserID, Token, MediaID string }

func NewFakePublisher() *FakePublisher {
	return &FakePublisher{status: map[string]string{}, InsightsFail: map[string]bool{}, InsightsCalls: map[string]int{},
		InsightToken: map[string]string{}}
}

func (f *FakePublisher) CreateContainer(_ context.Context, _, _ string, s ContainerSpec) (string, error) {
	if s.ImageURL == "" && s.VideoURL == "" && len(s.Children) == 0 {
		return "", fmt.Errorf("instagram api 400: media url required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "dev-container-" + randHex(6)
	f.status[id] = "FINISHED"
	return id, nil
}

func (f *FakePublisher) ContainerStatus(_ context.Context, _, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.status[id]; ok {
		return s, nil
	}
	return "EXPIRED", nil
}

func (f *FakePublisher) Publish(_ context.Context, userID, token, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailNext > 0 {
		f.FailNext--
		return "", fmt.Errorf("instagram api 500: temporary failure")
	}
	time.Sleep(300 * time.Millisecond)
	f.status[id] = "PUBLISHED"
	f.Published++
	mid := "dev-media-" + randHex(6)
	f.Calls = append(f.Calls, PublishCall{UserID: userID, Token: token, MediaID: mid})
	return mid, nil
}

func (f *FakePublisher) Permalink(_ context.Context, _, mediaID string) (string, error) {
	return "https://www.instagram.com/p/" + mediaID + "/", nil
}

// Insights 回傳依 mediaID 決定的假數據；每多查一次 views 就多一點，方便在畫面上看到「更新」有效。
func (f *FakePublisher) Insights(_ context.Context, token, mediaID, kind string) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.InsightsCalls[mediaID]++
	f.InsightToken[mediaID] = token
	if f.InsightsFail[mediaID] {
		return nil, fmt.Errorf("instagram api 400: insights unavailable (code 10)")
	}
	var h int64
	for _, c := range mediaID {
		h = (h*31 + int64(c)) % 997
	}
	res := map[string]int64{}
	for _, m := range InsightMetrics(kind) {
		switch m {
		case "views":
			res[m] = 200 + h + int64(f.InsightsCalls[mediaID])*7
		case "reach":
			res[m] = 150 + h/2
		case "likes":
			res[m] = 20 + h%40
		case "comments":
			res[m] = 2 + h%5
		case "saved":
			res[m] = 3 + h%9
		case "shares":
			res[m] = 1 + h%6
		case "replies":
			res[m] = h % 4
		}
	}
	return res, nil
}
