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
}

func Load() (Config, error) {
	c := Config{
		Addr:          env("ADDR", ":8790"),
		DatabaseURL:   env("DATABASE_URL", "postgres://litty:litty@localhost:54330/litty?sslmode=disable"),
		JWTSecret:     os.Getenv("JWT_SECRET"),
		AdminUser:     env("ADMIN_USER", "admin"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		WebOrigin:     env("WEB_ORIGIN", "http://localhost:5190"),
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
