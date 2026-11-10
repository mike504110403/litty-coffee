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
	UserID(ctx context.Context, token string) (string, error)
	CreateContainer(ctx context.Context, userID, token string, spec ContainerSpec) (string, error)
	ContainerStatus(ctx context.Context, token, containerID string) (string, error) // IN_PROGRESS | FINISHED | PUBLISHED | ERROR | EXPIRED
	Publish(ctx context.Context, userID, token, containerID string) (mediaID string, err error)
	Permalink(ctx context.Context, token, mediaID string) (string, error)
}

func (c *Client) UserID(ctx context.Context, token string) (string, error) {
	var out struct {
		UserID string `json:"user_id"`
		ID     string `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, c.Base+"/me?fields=user_id,username", token, nil, &out); err != nil {
		return "", err
	}
	if out.UserID != "" {
		return out.UserID, nil
	}
	return out.ID, nil
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

// FakePublisher 是開發期的假發佈：容器建立後立刻 FINISHED，發佈後狀態變 PUBLISHED。
type FakePublisher struct {
	mu        sync.Mutex
	status    map[string]string
	FailNext  int // 測試用：接下來幾次 Publish 回錯誤
	Published int
}

func NewFakePublisher() *FakePublisher { return &FakePublisher{status: map[string]string{}} }

func (f *FakePublisher) UserID(context.Context, string) (string, error) {
	return "dev-litty-coffee", nil
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

func (f *FakePublisher) Publish(_ context.Context, _, _, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailNext > 0 {
		f.FailNext--
		return "", fmt.Errorf("instagram api 500: temporary failure")
	}
	time.Sleep(300 * time.Millisecond)
	f.status[id] = "PUBLISHED"
	f.Published++
	return "dev-media-" + randHex(6), nil
}

func (f *FakePublisher) Permalink(_ context.Context, _, mediaID string) (string, error) {
	return "https://www.instagram.com/p/" + mediaID + "/", nil
}
