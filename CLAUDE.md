stage: mvp

# litty-coffee

為咖啡廳做的 IG 經營後台：DM 統一收件匣、關鍵字和 AI 自動回覆、貼文排程、顧客分析。

## 技術選型（MVP 期間一行一條，畢業時補 ADR）

- 後端：Go（chi＋pgx＋goose，migration 嵌入 binary、啟動時自動跑）；`backend/`
- DB：PostgreSQL 16（docker compose，地端 port 54330）
- AI：Claude API（Go 直接呼叫 Anthropic API；回覆用 haiku、分析用 sonnet）
- IG：官方 Instagram API with Instagram Login（商家帳號）；開發期用模擬器假造 webhook
- 前端：Vue 3＋TS＋Vite＋Element Plus＋Pinia＋vue-router；`frontend/`；用 npm（pnpm 在 Node 20 上會壞）
- i18n：只做繁中，不掛 vue-i18n（單一店家）
- 認證：單一或少量管理員，bcrypt＋JWT httpOnly cookie；第一個 admin 由 env 建立
- 處理流程：webhook 先落 `webhook_events`，再由背景處理器依序走規則 → AI → 送出

## 地端起法

```
docker compose up -d db
cd backend && cp .env.example .env   # 填 JWT_SECRET / ADMIN_PASSWORD
set -a; . ./.env; set +a; go run ./cmd/server     # :8790
cd frontend && npm install && npm run dev          # :5190，/api 代理到 :8790
```

## 開發模擬器

`.env` 設 `DEV_SIMULATOR=1`：送訊走假 sender（`internal/ig/fake.go`），後台收件匣出現「模擬訊息」按鈕（`POST /api/dev/simulate`），組出與真 webhook 同形的 payload 走同一條入庫路徑。

- 測試：`TEST_DATABASE_URL=postgres://litty:litty@localhost:54330/litty_test?sslmode=disable go test -p 1 ./...`（`litty_test` 庫需先建；`-p 1` 必要：各套件共用同一個測試庫會互相 TRUNCATE；未設時整合測試跳過）
- webhook 公開路徑：`GET/POST /webhooks/instagram`（驗 `X-Hub-Signature-256`）；串真 IG 時需公開 HTTPS
- 即時推送：`GET /api/inbox/stream`（SSE，行程內 Hub；多實例時換 PG LISTEN/NOTIFY）；事件 `message.created`／`message.updated`（表情、收回）／`conversation.updated`
- 附件：收到當下下載到 `MEDIA_DIR/inbox/`（≤25MB，以內容判型、只收圖片／影片／音訊），只經登入後的 `/api/inbox/media/{name}` 提供；下載失敗保留 IG 原網址並記 `messages.media_error`
- 已讀（messaging_seen）→ `conversations.contact_read_at`；表情（message_reactions）只記顧客按的 → `messages.reaction`；收回（is_deleted）→ 清空內容與本地檔、留 `deleted_at`
- 模擬器另可送「客人已讀／按表情／收回」事件
- Meta App 後台的 webhook 欄位要勾 `messages`、`messaging_seen`、`message_reactions`（連結時程式也會用 subscribed_apps 訂閱這三個）

## 自動回覆

- 流程：顧客訊息入庫 → 等合併秒數（設定頁可調）→ 只處理該對話最新一則 → handler=human 則跳過 → 關鍵字規則（priority 小的先）→ AI（`internal/automation/ai.go`，結構化輸出 reply/handoff＋intent）→ 送出或轉真人
- AI：`ANTHROPIC_API_KEY`、`AI_MODEL`（預設 claude-opus-5-5）、`AI_EFFORT`（預設 low）；開啟 server-side refusal fallback（`fallbacks: default`）
- 意圖寫進顧客訊息的 `messages.meta.intent`，分析頁用

## 顧客與分析

- 標籤 `tags`／`contact_tags`；顧客清單一次查詢帶出標籤（json_agg）與互動數
- 分析 `GET /api/analytics/summary?days=N`：依店家時區（`TZ_STORE`，預設 Asia/Taipei；binary 內嵌 tzdata）切日；同時回前一個等長區間供漲跌
- 回應時間口徑：顧客「開啟回合」（前一則不是顧客訊息）到店家第一則回覆；同回合連傳只算一次
- 圖表色：自動＝`#2a78d6`、真人＝`#eb6834`（dataviz 參考色盤 slot 1/2，已跑 validator；咖啡色系配色 chroma 不足或紅綠色盲分不開，故不用）；單一系列一律 `#2a78d6`
- App 目前只有淺色模式，圖表未做深色版

## 貼文排程

- 媒體：`POST /api/media` 存到 `MEDIA_DIR`（預設 `./data/media`，不進 git），以公開路徑 `/media/{隨機檔名}` 提供；IG 從 `PUBLIC_BASE_URL` + 該路徑抓檔，所以正式環境必須是 IG 抓得到的 HTTPS。PNG 一律轉 JPEG（IG 只收 JPEG），透明處補白
- 狀態：draft → scheduled → publishing → published｜failed。失敗自動重試共 3 次（1 分、5 分後），用盡才 failed；failed 可修改後重新排程（計數歸零）
- 中斷復原：publishing 超過 10 分鐘視為中斷；有 `ig_container_id` 時先查容器狀態，PUBLISHED 即直接標已發佈，不重發
- 發佈 token：設定頁連結帳號的 token（`account.Service.Token`），沒連結才用 `IG_ACCESS_TOKEN` 兜底（DEV_SIMULATOR=1 時走 `ig.FakePublisher`）
- AI 寫文案：`POST /api/posts/caption`，沿用自動回覆的人設與知識庫
- 成效（Insights）：`posts.InsightsRefresher` 每 15 分掃一次，只更新近 30 天已發佈的；未滿 7 天每 6 小時、之後每天；限動只在 24 小時內。指標：貼文／輪播／Reels＝views, reach, likes, comments, saved, shares；限動＝views, reach, replies, shares（`impressions` 已棄用）。手動 `POST /api/posts/{id}/insights/refresh`。需 `instagram_business_manage_insights` 權限（加權限後既有帳號要重新連結）

## 設定頁／IG 帳號連結

- OAuth：Business Login for Instagram（`internal/ig/oauth.go`）。`IG_APP_ID`＋`IG_APP_SECRET` 齊全才開；redirect URI＝`WEB_ORIGIN/api/ig/oauth/callback`（Meta 後台登記同一個，須 HTTPS）
- 流程：`/api/ig/oauth/start` 產 state 放 cookie（10 分、一次性）→ IG 授權 → callback 驗 state → code 換短效 → 長效 60 天 → `/me` 取 `user_id`（＝webhook entry.id）→ 訂閱 messages webhook → 導回 `/settings?ig=connected|ig_error=`
- 同時只有一個已連結帳號（有 token 者）；連新帳號會清掉舊帳號 token，但顧客／對話保留；斷開同理
- 續期：背景每 6 小時，剩 20 天內續 60 天；IG 規定 token 滿 24 小時才能續；失敗寫 `ig_accounts.last_error` 顯示在設定頁，下輪再試；已過期只能重新連結
- 個人帳號（account_type=PERSONAL）拒絕連結
- 模擬模式：`ig.FakeOAuth` 授權頁直接導回 callback，連上模擬器帳號 `dev-litty-coffee`
- 改密碼：`POST /api/auth/password`，既有 session 不撤銷

## TODO（MVP 期間範圍外，畢業時進 ledger）

- AI 真實呼叫尚未實測（目前無 API key）；沙盒可直接驗
- IG 真實發佈（Content Publishing API）尚未實測：端點與參數依官方文件寫成，串上真帳號時需驗；尚未查 `content_publishing_limit`（24h 上限）
- 成效 API 未接真帳號實測；文件寫輪播（album）可能無成效數據，目前照一般貼文請求，失敗會記在 `insights_error`
- 附件下載失敗不重試（IG 網址過期後也抓不到）
- IG OAuth 真實流程尚未實測（需 Meta App＋HTTPS 網址）；token 以明文存 DB，畢業總審時評估加密

## IG API 限制（設計時以此為準）

- 只能在用戶最後一則訊息後 24h 內回覆（`HUMAN_AGENT` tag 可延到 7 天，限真人）
- 不能主動私訊陌生人、不能群發；DM 排程只能落在窗口內
- 收到的媒體 URL 會過期，要即時下載（已做，見開發模擬器段）
