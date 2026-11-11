package config

import (
	"fmt"
	"os"
)

type Config struct {
	Addr          string
	DatabaseURL   string
	JWTSecret     string
	AdminUser     string
	AdminPassword string
	WebOrigin     string
	SecretKey     string // 加密 DB 內 secret 用的金鑰；空＝由 JWT_SECRET 衍生（MVP 兜底，main 會記警告）

	// Instagram
	IGAppID       string // Meta App ID（應用程式設定 → 基本資料；設定頁 Unified Login 連結用）
	IGAppSecret   string // Meta App Secret：驗 webhook X-Hub-Signature-256；也用於 OAuth 換 token
	IGConfigID    string // Facebook 登入的配置 ID（Unified Login 授權網址的 config_id）
	IGVerifyToken string // webhook 訂閱驗證
	IGAccessToken string // 兜底用：帳號沒掛連線時才用
	IGAPIBase     string
	DevSimulator  bool // true＝不打真 IG：送訊走假 sender、開放 /api/dev/simulate

	// AI（Claude API）
	AnthropicAPIKey string
	AIModel         string
	AIEffort        string

	MediaDir      string // 上傳檔存放目錄
	PublicBaseURL string // 對外網址（IG 抓媒體用），例：https://litty.example.com

	Timezone string // 店家時區（分析切日用）
}

func Load() (Config, error) {
	c := Config{
		Addr:            env("ADDR", ":8790"),
		DatabaseURL:     env("DATABASE_URL", "postgres://litty:litty@localhost:54330/litty?sslmode=disable"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		AdminUser:       env("ADMIN_USER", "admin"),
		AdminPassword:   os.Getenv("ADMIN_PASSWORD"),
		WebOrigin:       env("WEB_ORIGIN", "http://localhost:5190"),
		SecretKey:       os.Getenv("SECRET_KEY"),
		IGAppID:         os.Getenv("IG_APP_ID"),
		IGAppSecret:     os.Getenv("IG_APP_SECRET"),
		IGConfigID:      os.Getenv("IG_CONFIG_ID"),
		IGVerifyToken:   os.Getenv("IG_VERIFY_TOKEN"),
		IGAccessToken:   os.Getenv("IG_ACCESS_TOKEN"),
		IGAPIBase:       env("IG_API_BASE", "https://graph.facebook.com/v27.0"),
		DevSimulator:    os.Getenv("DEV_SIMULATOR") == "1",
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AIModel:         env("AI_MODEL", "claude-opus-5-5"),
		AIEffort:        env("AI_EFFORT", "low"),
		Timezone:        env("TZ_STORE", "Asia/Taipei"),
		MediaDir:        env("MEDIA_DIR", "./data/media"),
		PublicBaseURL:   env("PUBLIC_BASE_URL", "http://localhost:8790"),
	}
	if len(c.JWTSecret) < 16 {
		return c, fmt.Errorf("JWT_SECRET must be at least 16 chars")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
