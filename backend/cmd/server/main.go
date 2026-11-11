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
	"github.com/mike504110403/litty-coffee/backend/internal/appsettings"
	"github.com/mike504110403/litty-coffee/backend/internal/audit"
	"github.com/mike504110403/litty-coffee/backend/internal/auth"
	"github.com/mike504110403/litty-coffee/backend/internal/automation"
	"github.com/mike504110403/litty-coffee/backend/internal/config"
	"github.com/mike504110403/litty-coffee/backend/internal/crm"
	"github.com/mike504110403/litty-coffee/backend/internal/db"
	"github.com/mike504110403/litty-coffee/backend/internal/digest"
	"github.com/mike504110403/litty-coffee/backend/internal/httpapi"
	"github.com/mike504110403/litty-coffee/backend/internal/ig"
	"github.com/mike504110403/litty-coffee/backend/internal/inbox"
	"github.com/mike504110403/litty-coffee/backend/internal/media"
	"github.com/mike504110403/litty-coffee/backend/internal/monitor"
	"github.com/mike504110403/litty-coffee/backend/internal/posts"
	"github.com/mike504110403/litty-coffee/backend/internal/realtime"
	"github.com/mike504110403/litty-coffee/backend/internal/secrets"
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

	// 操作紀錄：登入／登出／被踢／後台異動；背景每天清掉超過 365 天的
	auditStore := &audit.Store{DB: pool}
	go auditStore.RunCleanup(ctx)
	authSvc := &auth.Service{DB: pool, Secret: []byte(cfg.JWTSecret), Secure: strings.HasPrefix(cfg.WebOrigin, "https://"),
		Audit: auditStore}
	if err := authSvc.EnsureAdmin(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
		log.Fatal(err)
	}

	// 後台可改的憑證：DB（加密）優先於 .env，存檔即生效；啟動時先讀一次
	box, keyFallback, err := secrets.NewBox(cfg.SecretKey, cfg.JWTSecret)
	if err != nil {
		log.Fatal(err)
	}
	authSvc.Box = box // GA（TOTP）secret 加解密
	if keyFallback {
		log.Print("警告：未設 SECRET_KEY，後台憑證加密金鑰改由 JWT_SECRET 衍生（MVP 兜底）；換 JWT_SECRET 會讓已存憑證解不開，正式環境請設獨立的 SECRET_KEY")
	}
	creds := appsettings.New(pool, box, appsettings.Values{
		IGAppID: cfg.IGAppID, IGAppSecret: cfg.IGAppSecret, IGConfigID: cfg.IGConfigID, IGVerifyToken: cfg.IGVerifyToken,
		AnthropicKey: cfg.AnthropicAPIKey, AIModel: cfg.AIModel, AIEffort: cfg.AIEffort,
	})
	if err := creds.Load(ctx); err != nil {
		log.Fatal(err)
	}
	eff := creds.Values()

	hub := realtime.NewHub()
	api := &httpapi.Server{DB: pool, Auth: authSvc, Hub: hub, Creds: creds, Audit: auditStore}
	var messenger ig.Messenger = ig.NewClient(cfg.IGAPIBase)
	if cfg.DevSimulator {
		api.Fake = ig.NewFake()
		messenger = api.Fake
		log.Print("DEV_SIMULATOR=1: IG 送訊走假 sender，/api/dev/simulate 已開啟")
	}
	api.Inbox = inbox.NewService(pool, messenger, hub)
	api.Inbox.MediaDir = cfg.MediaDir
	engine := &automation.Engine{Store: &automation.Store{DB: pool}, Inbox: api.Inbox, Ctx: ctx}
	// AI 永遠掛同一個 LiveAI；沒 key 時 Enabled()＝false（只跑規則、文案回 503）。設定頁存檔時由 OnChange 換 key／模型
	liveAI := &automation.LiveAI{}
	engine.AI = liveAI
	api.CaptionAI = liveAI
	liveAI.Set(eff.AnthropicKey, eff.AIModel, eff.AIEffort)
	creds.OnChange(func(v appsettings.Values) {
		liveAI.Set(v.AnthropicKey, v.AIModel, v.AIEffort)
		if v.AnthropicKey != "" {
			log.Printf("AI 設定已更新：%s（effort=%s）", v.AIModel, v.AIEffort)
		} else {
			log.Print("AI 設定已更新：未設 Claude API Key，只跑關鍵字規則")
		}
	})
	if liveAI.Enabled() {
		log.Printf("AI 自動回覆：%s（effort=%s）", eff.AIModel, eff.AIEffort)
	} else {
		log.Print("未設 Claude API Key（.env 與後台皆無）：只跑關鍵字規則")
	}
	api.Auto = engine
	api.AIModel = eff.AIModel
	api.Inbox.OnInbound = engine.OnInbound

	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("TZ_STORE: %v", err)
	}
	api.CRM = &crm.Service{DB: pool}
	api.Analytics = &analytics.Service{DB: pool, Loc: loc}

	api.Media = &media.Store{DB: pool, Dir: cfg.MediaDir, PublicBase: cfg.PublicBaseURL}
	api.Posts = posts.NewService(pool, api.Media)
	// IG 帳號連結（Unified Login）：設定頁 OAuth；模擬模式下授權頁直接導回，連上兩個模擬商家帳號。
	// 每個帳號用自己所屬連線的 token（加密存 DB）；帳號沒掛連線時才用 env IG_ACCESS_TOKEN 兜底
	api.Info = httpapi.SystemInfo{WebOrigin: cfg.WebOrigin, PublicBaseURL: cfg.PublicBaseURL, Timezone: cfg.Timezone, APIBase: cfg.IGAPIBase}
	api.Accounts = &account.Service{DB: pool, Box: box, FallbackToken: cfg.IGAccessToken}
	api.Inbox.Creds = api.Accounts
	redirectURI := cfg.WebOrigin + "/api/ig/oauth/callback"
	switch {
	case cfg.DevSimulator:
		api.Accounts.OAuth = ig.NewFakeOAuth(redirectURI)
	default:
		// 隨設定頁存檔即時換 Meta App ID／Secret／配置 ID；沒憑證時 account.Service 視為未設定
		api.Accounts.OAuth = &appsettings.LiveOAuth{Store: creds, RedirectURI: redirectURI, APIBase: cfg.IGAPIBase,
			SettingsErrorURL: cfg.WebOrigin + "/settings"}
	}

	var publisher ig.Publisher = ig.NewClient(cfg.IGAPIBase)
	if cfg.DevSimulator {
		publisher = ig.NewFakePublisher()
	}
	scheduler := &posts.Scheduler{Svc: api.Posts, IG: publisher, Creds: api.Accounts.Credentials, OnAPIError: api.Accounts.NoteAPIError}
	go scheduler.Run(ctx)

	// 已發佈貼文的成效數據：背景更新＋手動更新 API 共用同一個 refresher
	api.Posts.Insights = &posts.InsightsRefresher{Svc: api.Posts, IG: publisher, Creds: api.Accounts.Credentials,
		OnAPIError: api.Accounts.NoteAPIError}
	go api.Posts.Insights.Run(ctx)
	go api.Inbox.RunWorker(ctx)

	// 總覽：系統狀態（每分鐘檢查、轉紅／恢復推 system.alert）與每日摘要（到時間產生前一天的摘要、推 digest.ready）。
	// 兩者走獨立的 SysHub（/api/system/stream），不需收件匣權限也收得到
	api.SysHub = realtime.NewHub()
	aiCalls := &automation.CallStats{}
	engine.Calls = aiCalls
	var metaChecker monitor.MetaChecker = ig.NewOAuthClient("", "", "", "", cfg.IGAPIBase) // 只用帳號 token 打 GET /{ig-id}
	if cfg.DevSimulator {
		metaChecker = api.Accounts.OAuth.(*ig.FakeOAuth) // 模擬模式不打真 Meta
	}
	api.Monitor = &monitor.Monitor{DB: pool, Meta: metaChecker, Creds: api.Accounts.Credentials, NoteAPIError: api.Accounts.NoteAPIError,
		AIKey: func() string { return creds.Values().AnthropicKey }, PingAI: httpapi.ClaudeKeyProbe(""),
		Calls: aiCalls, Hub: api.SysHub}
	go api.Monitor.Run(ctx)
	api.Digests = &digest.Service{DB: pool, Analytics: api.Analytics, Loc: loc, AI: liveAI, Calls: aiCalls, Hub: api.SysHub}
	go api.Digests.Run(ctx)

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
