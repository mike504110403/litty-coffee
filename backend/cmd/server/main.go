package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/config"
	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/httpapi"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatal(err)
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	authSvc := &auth.Service{DB: pool, Secret: []byte(cfg.JWTSecret), Secure: strings.HasPrefix(cfg.WebOrigin, "https://")}
	if err := authSvc.EnsureAdmin(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		log.Fatal(err)
	}

	hub := realtime.NewHub()
	api := &httpapi.Server{DB: pool, Auth: authSvc, Hub: hub, IGAppSecret: cfg.IGAppSecret, IGVerifyToken: cfg.IGVerifyToken}
	var messenger ig.Messenger = ig.NewClient(cfg.IGAPIBase)
	if cfg.DevSimulator {
		api.Fake = ig.NewFake()
		messenger = api.Fake
		log.Print("DEV_SIMULATOR=1: IG 送訊走假 sender，/api/dev/simulate 已開啟")
	}
	api.Inbox = inbox.NewService(pool, messenger, hub)
	api.Inbox.DefaultToken = cfg.IGAccessToken
	engine := &automation.Engine{Store: &automation.Store{DB: pool}, Inbox: api.Inbox, Ctx: ctx}
	if cfg.AnthropicAPIKey != "" {
		engine.AI = automation.NewAI(cfg.AnthropicAPIKey, cfg.AIModel, cfg.AIEffort)
		log.Printf("AI 自動回覆：%s（effort=%s）", cfg.AIModel, cfg.AIEffort)
	} else {
		log.Print("未設 ANTHROPIC_API_KEY：只跑關鍵字規則")
	}
	api.Auto = engine
	api.AIModel = cfg.AIModel
	api.Inbox.OnInbound = engine.OnInbound
	go api.Inbox.RunWorker(ctx)

	srv := &http.Server{Addr: cfg.Addr, Handler: api.Routes(), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }} // 關機時一併結束 SSE 長連線
	go func() {
		log.Printf("listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
