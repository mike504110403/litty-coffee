package inbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// 收到的附件：IG 給的 CDN 網址會過期，收到當下就下載到 MediaDir/inbox/，
// 只經登入後的 /api/inbox/media/{name} 提供（顧客私訊內容不公開）。

const maxInboxMedia = 25 << 20

func downloadable(kind string) bool {
	switch kind {
	case "image", "video", "audio", "story_mention", "story_reply":
		return true
	}
	return false
}

func (s *Service) inboxDir() string { return filepath.Join(s.MediaDir, "inbox") }

// saveMedia 下載附件並更新訊息；失敗只記錯誤，保留原始網址（過期前仍看得到）。
func (s *Service) saveMedia(ctx context.Context, m Message, src string) Message {
	if s.MediaDir == "" {
		return m
	}
	name, err := s.download(ctx, src)
	if err != nil {
		log.Printf("inbox: media %d: %v", m.ID, err)
		_, _ = s.DB.Exec(ctx, `UPDATE messages SET media_error = $2 WHERE id = $1`, m.ID, err.Error())
		return m
	}
	if _, err := s.DB.Exec(ctx, `UPDATE messages SET media_file = $2, media_error = '' WHERE id = $1`, m.ID, name); err != nil {
		log.Printf("inbox: media %d: %v", m.ID, err)
		s.removeMediaFile(name)
		return m
	}
	m.MediaURL = MediaRoute + name
	return m
}

func (s *Service) download(ctx context.Context, src string) (string, error) {
	if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
		return "", errors.New("附件網址無效")
	}
	ctx, cancel := context.WithTimeout(ctx, s.Download.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	res, err := s.Download.Do(req)
	if err != nil {
		return "", fmt.Errorf("下載附件失敗：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下載附件失敗：HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxInboxMedia+1))
	if err != nil {
		return "", fmt.Errorf("下載附件失敗：%w", err)
	}
	if len(body) > maxInboxMedia {
		return "", errors.New("附件超過 25MB，未保存")
	}
	ext, err := mediaExt(body, res.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.inboxDir(), 0o755); err != nil {
		return "", err
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	name := hex.EncodeToString(b) + ext
	if err := os.WriteFile(filepath.Join(s.inboxDir(), name), body, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// mediaExt 以檔案內容判斷型別（不信任對方的 Content-Type），只收圖片／影片／音訊。
func mediaExt(body []byte, header string) (string, error) {
	ct := http.DetectContentType(body)
	if ct == "application/octet-stream" && len(body) >= 12 && string(body[4:8]) == "ftyp" {
		ct = "video/mp4"
	}
	if ct == "application/octet-stream" { // 例如 aac／m4a 偵測不出來時才參考 header
		if mt, _, err := mime.ParseMediaType(header); err == nil && strings.HasPrefix(mt, "audio/") {
			ct = mt
		}
	}
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case mt == "image/jpeg":
		return ".jpg", nil
	case mt == "image/png":
		return ".png", nil
	case mt == "image/gif":
		return ".gif", nil
	case mt == "image/webp":
		return ".webp", nil
	case mt == "video/mp4":
		return ".mp4", nil
	case mt == "video/webm":
		return ".webm", nil
	case mt == "audio/mpeg":
		return ".mp3", nil
	case mt == "audio/wave", mt == "audio/wav":
		return ".wav", nil
	case mt == "audio/ogg", mt == "application/ogg":
		return ".ogg", nil
	case mt == "audio/aac", mt == "audio/mp4", mt == "audio/x-m4a":
		return ".m4a", nil
	}
	return "", fmt.Errorf("不支援的附件格式（%s）", mt)
}

func (s *Service) removeMediaFile(name string) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return
	}
	if err := os.Remove(filepath.Join(s.inboxDir(), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("inbox: remove media %s: %v", name, err)
	}
}

// MediaHandler 提供已下載的附件（掛在需登入的路由下）。
func (s *Service) MediaHandler(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(s.inboxDir(), name))
}
