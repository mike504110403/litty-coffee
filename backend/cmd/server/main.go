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
	_ "time/tzdata" // 容器內沒有系統時區資料時也能載入店家時區

	"github.com/mike504110403/litty-coffee/backend/internal/account"
	"github.com/mike504110403/litty-coffee/backend/internal/analytics"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/config"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/httpapi"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
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
		ai := automation.NewAI(cfg.AnthropicAPIKey, cfg.AIModel, cfg.AIEffort)
		engine.AI = ai
		api.CaptionAI = ai
		log.Printf("AI 自動回覆：%s（effort=%s）", cfg.AIModel, cfg.AIEffort)
	} else {
		log.Print("未設 ANTHROPIC_API_KEY：只跑關鍵字規則")
	}
	api.Auto = engine
	api.AIModel = cfg.AIModel
	api.Inbox.OnInbound = engine.OnInbound

	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("TZ_STORE: %v", err)
	}
	api.CRM = &crm.Service{DB: pool}
	api.Analytics = &analytics.Service{DB: pool, Loc: loc}

	api.Media = &media.Store{DB: pool, Dir: cfg.MediaDir, PublicBase: cfg.PublicBaseURL}
	api.Posts = posts.NewService(pool, api.Media)
	// IG 帳號連結：設定頁 OAuth；模擬模式下授權頁直接導回，連上模擬器的假商家帳號
	api.Info = httpapi.SystemInfo{WebOrigin: cfg.WebOrigin, PublicBaseURL: cfg.PublicBaseURL, IGAppIDSet: cfg.IGAppID != "",
		IGSecretSet: cfg.IGAppSecret != "", AIKeySet: cfg.AnthropicAPIKey != "", Timezone: cfg.Timezone}
	api.Accounts = &account.Service{DB: pool, FallbackToken: cfg.IGAccessToken}
	redirectURI := cfg.WebOrigin + "/api/ig/oauth/callback"
	switch {
	case cfg.DevSimulator:
		api.Accounts.OAuth = ig.NewFakeOAuth(redirectURI, httpapi.DevAccountID)
	case cfg.IGAppID != "" && cfg.IGAppSecret != "":
		api.Accounts.OAuth = ig.NewOAuthClient(cfg.IGAppID, cfg.IGAppSecret, redirectURI, cfg.IGAPIBase)
	}
	go api.Accounts.RunRefresher(ctx)

	scheduler := &posts.Scheduler{Svc: api.Posts, IG: ig.NewClient(cfg.IGAPIBase), TokenFunc: api.Accounts.Token}
	if cfg.DevSimulator {
		scheduler.IG, scheduler.TokenFunc = ig.NewFakePublisher(), nil
		scheduler.Token = "dev-token"
	}
	go scheduler.Run(ctx)
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
