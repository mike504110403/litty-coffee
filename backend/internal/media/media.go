// Package media 管上傳檔案：存到本機目錄並以公開網址提供（IG 發佈時會來抓）。
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // 解 PNG 用
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MaxImageBytes = 8 << 20   // IG 圖片上限 8MB
	MaxVideoBytes = 100 << 20 // MVP 先限制 100MB
)

var ErrNotFound = errors.New("not found")

type Asset struct {
	ID           int64     `json:"id"`
	Filename     string    `json:"filename"`
	OriginalName string    `json:"originalName"`
	ContentType  string    `json:"contentType"`
	Kind         string    `json:"kind"`
	SizeBytes    int64     `json:"sizeBytes"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	URL          string    `json:"url"` // 後台顯示用（相對路徑）
	CreatedAt    time.Time `json:"createdAt"`
}

type Store struct {
	DB         *pgxpool.Pool
	Dir        string
	PublicBase string // 例：https://litty.example.com（IG 要抓得到）
}

func (s *Store) PublicURL(filename string) string {
	return strings.TrimRight(s.PublicBase, "/") + "/media/" + filename
}

func randName(ext string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + ext
}

// Save 驗證並儲存一個上傳檔。PNG 一律轉成 JPEG（IG 圖片只收 JPEG）。
func (s *Store) Save(ctx context.Context, originalName string, r io.Reader) (Asset, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxVideoBytes+1))
	if err != nil {
		return Asset{}, err
	}
	ct := http.DetectContentType(data)
	a := Asset{OriginalName: filepath.Base(originalName)}
	switch {
	case ct == "image/jpeg" || ct == "image/png":
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return a, fmt.Errorf("圖片無法讀取：%w", err)
		}
		if ct == "image/png" {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, flattenOnWhite(img), &jpeg.Options{Quality: 90}); err != nil {
				return a, err
			}
			data = buf.Bytes()
		}
		if len(data) > MaxImageBytes {
			return a, errors.New("圖片超過 8MB")
		}
		a.Kind, a.ContentType = "image", "image/jpeg"
		a.Width, a.Height = img.Bounds().Dx(), img.Bounds().Dy()
		a.Filename = randName(".jpg")
	case hasFtyp(data): // MP4／MOV 都是 ISO BMFF 容器；brand 五花八門（isom、mp42、qt…），只認 ftyp box
		if len(data) > MaxVideoBytes {
			return a, errors.New("影片超過 100MB")
		}
		a.Kind, a.ContentType = "video", "video/mp4"
		ext := ".mp4"
		if isQuickTime(data) {
			a.ContentType, ext = "video/quicktime", ".mov"
		}
		a.Filename = randName(ext)
	default:
		return a, fmt.Errorf("不支援的檔案格式（%s），請上傳 JPEG、PNG、MP4 或 MOV", ct)
	}
	a.SizeBytes = int64(len(data))

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return a, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, a.Filename), data, 0o644); err != nil {
		return a, err
	}
	err = s.DB.QueryRow(ctx, `
INSERT INTO media_assets (filename, original_name, content_type, kind, size_bytes, width, height)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
		a.Filename, a.OriginalName, a.ContentType, a.Kind, a.SizeBytes, a.Width, a.Height).Scan(&a.ID, &a.CreatedAt)
	a.URL = "/media/" + a.Filename
	return a, err
}

func hasFtyp(b []byte) bool { return len(b) >= 12 && string(b[4:8]) == "ftyp" }

// QuickTime（.mov）的 ftyp brand 是 "qt  "
func isQuickTime(b []byte) bool {
	return len(b) >= 12 && string(b[4:8]) == "ftyp" && string(b[8:12]) == "qt  "
}

// flattenOnWhite 把透明背景鋪成白底，避免轉 JPEG 後變黑。
func flattenOnWhite(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			// premultiplied over white
			inv := 0xffff - a
			dst.Set(x, y, rgba16{r + inv, g + inv, bl + inv})
		}
	}
	return dst
}

type rgba16 struct{ r, g, b uint32 }

func (c rgba16) RGBA() (uint32, uint32, uint32, uint32) { return c.r, c.g, c.b, 0xffff }

func (s *Store) Get(ctx context.Context, ids []int64) ([]Asset, error) {
	rows, err := s.DB.Query(ctx, `
SELECT id, filename, original_name, content_type, kind, size_bytes, width, height, created_at
FROM media_assets WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Asset{}
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.Filename, &a.OriginalName, &a.ContentType, &a.Kind, &a.SizeBytes, &a.Width, &a.Height, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.URL = "/media/" + a.Filename
		byID[a.ID] = a
	}
	out := make([]Asset, 0, len(ids))
	for _, id := range ids { // 保持呼叫端順序
		a, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("媒體 %d：%w", id, ErrNotFound)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetOne(ctx context.Context, id int64) (Asset, error) {
	list, err := s.Get(ctx, []int64{id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}
	return list[0], nil
}

// Handler 提供 /media/{filename}（公開；檔名為隨機值，無法列舉）。
func (s *Store) Handler() http.Handler {
	fs := http.StripPrefix("/media/", http.FileServer(http.Dir(s.Dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/media/")
		if name == "" || strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fs.ServeHTTP(w, r)
	})
}
