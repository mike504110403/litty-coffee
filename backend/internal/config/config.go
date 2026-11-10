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

	// Instagram
	IGAppSecret   string // 驗 webhook X-Hub-Signature-256
	IGVerifyToken string // webhook 訂閱驗證
	IGAccessToken string // 長效 token（MVP 先放 env，之後搬到設定頁）
	IGAPIBase     string
	DevSimulator  bool // true＝不打真 IG：送訊走假 sender、開放 /api/dev/simulate

	// AI（Claude API）
	AnthropicAPIKey string
	AIModel         string
	AIEffort        string

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
		IGAppSecret:     os.Getenv("IG_APP_SECRET"),
		IGVerifyToken:   os.Getenv("IG_VERIFY_TOKEN"),
		IGAccessToken:   os.Getenv("IG_ACCESS_TOKEN"),
		IGAPIBase:       env("IG_API_BASE", "https://graph.instagram.com/v23.0"),
		DevSimulator:    os.Getenv("DEV_SIMULATOR") == "1",
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AIModel:         env("AI_MODEL", "claude-opus-5-5"),
		AIEffort:        env("AI_EFFORT", "low"),
		Timezone:        env("TZ_STORE", "Asia/Taipei"),
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
