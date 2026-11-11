stage: mvp

# litty-coffee

為咖啡廳做的 IG 經營後台：DM 統一收件匣、關鍵字和 AI 自動回覆、貼文排程、顧客分析。

## 技術選型（MVP 期間一行一條，畢業時補 ADR）

- 後端：Go（chi＋pgx＋goose，migration 嵌入 binary、啟動時自動跑）；`backend/`
- DB：PostgreSQL 16（docker compose，地端 port 54330）
- AI：Claude API（Go 直接呼叫 Anthropic API；回覆用 haiku、分析用 sonnet）
- IG：官方 Instagram API with Unified Login（Facebook 登入的「配置」；官方建議新整合採用的設定），可同時連多個 IG 專業帳號；開發期用模擬器假造 webhook
- 前端：Vue 3＋TS＋Vite＋Element Plus＋Pinia＋vue-router；`frontend/`；用 npm（pnpm 在 Node 20 上會壞）
- i18n：只做繁中，不掛 vue-i18n（單一店家）
- 認證：多管理員，bcrypt＋JWT httpOnly cookie；第一個 admin 由 env 建立且為店長（is_owner；有超管不影響，只看非超管是否存在）
- 超管（`is_super`）：位階在店長之上；IG 帳號連結（`/api/settings`、`/api/ig/oauth/*`、`/api/ig/connections/*`、`/api/ig/accounts/{id}/*` 寫入；`sync-media` 除外，見貼文排程）與憑證（`/api/settings/credentials*`）只限超管（`auth.RequireSuper`）；`GET /api/ig/accounts` 仍所有登入者可讀。超管可管理所有人（含店長），只有超管能建立／變更超管、重設 GA；最後一位啟用中的超管不能停用或降級；一律可見全部帳號
- 預設超管：migration 00016 建 `superAdmin`／`admin123`（Mike 2026-11-12 裁示：repo 公開、密碼在 repo 內，接受此風險；緩解＝首次登入鎖定：改密碼＋綁 GA 前，除 `/auth/me`、改密碼、綁 GA、登出外一律 403 `setup_required`，含 SSE）
- GA 兩步驟登入（`internal/auth/totp.go`，pquerna/otp）：超管必綁、其他人可自選（頭像選單）；已綁定者密碼正確只拿到待驗證碼 cookie `litty_mfa`（5 分鐘、一次性、另一把簽章鑰匙），`POST /api/auth/totp` 驗 6 位數（±1 時間步、同步不重用）或備援碼（8 組、只存雜湊、一次性）才換正式 session；錯碼每帳號 5 分鐘 5 次後 429；secret 以 `internal/secrets` 加密（AAD 綁管理員 id）；超管可重設別人的 GA（被重設的超管立即登出、要重綁）。前端 `/setup` 三步驟（改密碼→掃 QR→備援碼）
- 權限：功能逐項勾選（`internal/auth/perms.go`：inbox, contacts, automation, schedule, analytics, admins, audit；ig_accounts、credentials 自 00016 起超管專屬、不可授予）；店長全權（超管專屬除外）。middleware 每次請求查 DB，停用即踢；`RequirePerm` 包住 `router.go` 各路由群組；最後一位啟用中的店長不能停用或降級；非店長不能授予自己沒有的權限、不能動店長
- 可見帳號（安全邊界）：`admin_users.ig_account_ids`（NULL＝全部；店長一律全部），見 `internal/auth/scope.go`、`internal/httpapi/scope.go`。清單類 API 用 `?account=` 過濾，範圍外 403；單筆資源（對話、附件、顧客、貼文…）範圍外一律 404；自動回覆、上傳、AI 文案、模擬器必須指定帳號；連線層級（連結、同步、斷開）只限可見全部帳號的人；只看部分帳號的人只能授予自己看得到的帳號、不能授予「全部」
- 真人回覆記錄 `messages.sent_by_admin_id`，收件匣顯示回覆者名字
- 處理流程：webhook 先落 `webhook_events`，再由背景處理器依序走規則 → AI → 送出
- 容器：後端、前端各一個 Docker 映像（`backend/Dockerfile`、`frontend/Dockerfile`）；前端映像＝靜態檔＋nginx，nginx 同時反向代理 `/api`、`/webhooks`、`/media`、`/healthz` 到後端
- 部署：AWS Lightsail 單機同跑 dev＋prod（各自 PostgreSQL／Redis／後端／nginx，docker compose）；映像推 GHCR；GitHub Actions CI／CD 經 Tailscale SSH 部署；dev 只在 Tailscale 內網、prod 經 Cloudflare Tunnel 對外

## 地端起法

```
docker compose up -d db
cd backend && cp .env.example .env   # 填 JWT_SECRET / ADMIN_PASSWORD
set -a; . ./.env; set +a; go run ./cmd/server     # :8790
cd frontend && npm install && npm run dev          # :5190，/api 代理到 :8790
```

## 部署

- 細節（架構、首次開機、GitHub Secrets／Environments、部署與回滾、備份還原、查日誌）見 `deploy/README.md`
- push `dev` → 部署 dev：CI 與建映像同時跑（只推 `<sha>` tag），兩者都過才改標 `dev` 並部署；push `main` → 部署 prod：同一個 commit 在 dev 成功部署過就直接把那份映像改標 `prod`（不重跑 CI、不重建），否則走完整流程（`.github/workflows/deploy.yml`）
- 主機上的真 `.env` 在 `/opt/litty/<env>/.env`，範本在 `deploy/env/`，真值不進 git

## 開發模擬器

`.env` 設 `DEV_SIMULATOR=1`：送訊走假 sender（`internal/ig/fake.go`），後台收件匣出現「模擬訊息」按鈕（`POST /api/dev/simulate`，body `account`＝收訊的我方帳號），組出與真 webhook 同形的 payload 走同一條入庫路徑。要先到設定頁（模擬）連結，否則 409「請先到設定頁連結 IG 帳號（模擬）」。

- 測試：`TEST_DATABASE_URL=postgres://litty:litty@localhost:54330/litty_test?sslmode=disable go test -p 1 ./...`（`litty_test` 庫需先建；`-p 1` 必要：各套件共用同一個測試庫會互相 TRUNCATE；未設時整合測試跳過）
- webhook 公開路徑：`GET/POST /webhooks/instagram`（驗 `X-Hub-Signature-256`，金鑰＝Meta App Secret）；串真 IG 時需公開 HTTPS
- webhook 只收「已連結」的帳號（`entry.id` 在 `ig_accounts`、啟用中、有掛連線）；其他一律不入庫、不自動建帳號，`webhook_events.error = '未連結的帳號'`。重複送達靠 `ig_mid` 去重
- 統一收件匣：多個帳號的私訊合併在一起，可依帳號篩選；回覆一律從「對話所屬帳號」、用該帳號連線的 token 送出
- 即時推送：`GET /api/inbox/stream`（SSE，行程內 Hub；多實例時換 PG LISTEN/NOTIFY）；事件 `message.created`／`message.updated`（表情、收回）／`conversation.updated`，資料都帶 `igAccountId`；只推登入者可見帳號的事件，每 25 秒心跳時重讀登入者（停用、失去收件匣權限即斷線，可見帳號變更立即生效）
- 附件：收到當下下載到 `MEDIA_DIR/inbox/`（≤25MB，以內容判型、只收圖片／影片／音訊），只經登入後的 `/api/inbox/media/{name}` 提供（看不到該對話帳號的人回 404）；下載失敗保留 IG 原網址並記 `messages.media_error`
- 已讀（messaging_seen）→ `conversations.contact_read_at`；表情（message_reactions）只記顧客按的 → `messages.reaction`；收回（is_deleted）→ 清空內容與本地檔、留 `deleted_at`
- 模擬器另可送「客人已讀／按表情／收回」事件；可選送到哪個店家帳號
- Meta App 後台的 webhook 欄位要勾 `messages`、`messaging_seen`、`message_reactions`（連結時程式也會逐一用 `POST /{ig-id}/subscribed_apps` 訂閱這三個）

## 自動回覆

- 流程：顧客訊息入庫 → 等合併秒數（設定頁可調）→ 黑名單則跳過 → handler=human 則跳過 → 降頻判斷 → 只處理該對話最新一則 → 降頻間隔內則跳過 → 關鍵字規則（priority 小的先）→ AI（`internal/automation/ai.go`，結構化輸出 reply/handoff＋intent＋abusive/abuseReason）→ 送出、轉真人或列入黑名單
- 依帳號：設定、知識庫、規則都有 `ig_account_id`；引擎用「對話所屬帳號」的那一份。`ig_account_id IS NULL` 的是範本，帳號第一次用到時（`automation.Store.EnsureAccount`）從範本複製一份。自動回覆頁上方選帳號，沙盒也用選的帳號；API 除 `/automation/status` 外都要帶 `?account=`
- AI：`ANTHROPIC_API_KEY`、`AI_MODEL`（預設 claude-opus-5-5）、`AI_EFFORT`（預設 low）；開啟 server-side refusal fallback（`fallbacks: default`）
- 意圖寫進顧客訊息的 `messages.meta.intent`，分析頁用
- 真人閒置自動交回 AI（`automation/idle.go`，每分鐘掃）：handler=human、自動回覆與 AI 開、非黑名單、在 24h 窗口內、最後一則未回覆的顧客訊息（之後沒有真人回覆）≥ `automation_settings.human_idle_minutes`（預設 15；只收 0/5/10/15/30/60，0＝關）→ 切回 bot、寫系統事件「真人 N 分鐘未回覆，已交回 AI」、走同一條引擎（規則→AI，AI 被要求先致歉「久等了」）；AI 又轉真人不再送轉真人訊息；同一則只交回一次（`conversations.idle_handback_msg_id`）；交回後沒人處理（圖片等）還給真人。不記操作紀錄
- 系統事件訊息：`messages.sender_type='system'`（`kind='system'`、`direction='out'`、不送 IG、不改對話最後訊息），推 `message.created`；分析、回應時間、每日摘要、蒸餾、AI 對話紀錄一律排除，新查詢要記得 `sender_type <> 'system'`

## 黑名單與標記

- 顧客標記 `contacts.risk`：`blocked` 黑名單｜`watch` 問題顧客｜`''` 一般；`risk_source`：`manual` 管理員｜`rate` 大量訊息｜`ai` AI 判斷騷擾；`risk_reason`、`risk_at`、`risk_by_admin_id`。解除＝risk 清空但保留是誰、何時解除（`internal/inbox/risk.go`）
- 黑名單：訊息照收、留在收件匣（列表與對話頭有標籤、對話上方橫幅），但不跑規則與 AI、不推 `handoff.requested`（`inbox.Handoff` 也擋）；管理員仍可手動回覆或解除。只有 AI 判斷騷擾（`ai_moderation` 開）或手動才會進黑名單
- AI 判斷騷擾：設定開啟時 decision schema 多 `abusive`、`abuseReason`，system prompt 說明範圍（騷擾、辱罵、性內容、詐騙、打聽他人個資；一般客訴不算）。abusive=true → 列入黑名單、不回、不轉真人；模型拒答不封鎖（記 ai_unsure、照舊轉真人）。沙盒顯示判斷（`kind=block`）但不寫 DB
- 大量訊息＝降頻，不封鎖：`spam_window_secs` 內顧客訊息超過 `spam_max_msgs`（每帳號設定，預設 300 秒／10 則，0＝關）→ 標 `watch`（source=rate，只覆蓋一般或先前 rate 標的）＋ `contacts.throttle_level`=1。降頻中每 5×2^(N-1) 分鐘最多自動回一次（上限 6 小時），間隔內只收不回；每回一次升一級；某則顧客訊息與前一則相隔超過目前間隔＝顧客安靜過 → 歸零（watch 標記保留作紀錄）。降頻中 AI 收到「降頻模式」：一句話答主要問題、委婉說店員稍後回覆、不得有敵意；該轉真人照轉、照通知。上次自動回覆時間由 messages（ai／bot）推得
- 標記 API：`PATCH /api/contacts/{id}` 帶 `risk`＋`riskReason`，顧客或收件匣權限皆可（只有收件匣權限者不能改備註／標籤，回應只含標記欄位）；顧客清單 `?risk=blocked|watch`；變更推 `conversation.updated`（contact 帶 risk／riskReason／riskSource／riskAt／throttleLevel）
- 待改進問答 `message_reviews`：`ai_wrong`（收件匣在 AI／規則回覆上按「回答有誤」，`POST /api/messages/{id}/reviews`，收件匣權限，note 必填，未處理前同一則不重複）、`ai_unsure`（AI 轉真人或拒答時自動記，掛顧客那則，note＝AI 理由）。`GET /api/reviews?account=&kind=&status=open|resolved`、`GET /api/reviews/open-count`、`POST /api/reviews/{id}/resolve`（自動回覆權限）；清單帶顧客問題（ai_wrong＝回覆前最近一則顧客訊息）與回覆。可見帳號規則同其他（清單 403、單筆 404）
- 後台：收件匣標籤／「標記」下拉／橫幅／「回答有誤」；自動回覆頁「待改進」分頁與「AI 設定」的門檻與騷擾開關；顧客頁「黑名單」「問題顧客」篩選與抽屜內標記；總覽「待改進問答 N 則」（自動回覆權限）

## 顧客與分析

- 標籤 `tags`／`contact_tags`；顧客清單一次查詢帶出標籤（json_agg）與互動數
- 顧客清單顯示來自哪個帳號、可依帳號篩選；標籤全店共用，標籤數只算可見（或篩選）帳號的顧客
- 分析 `GET /api/analytics/summary?days=N&account=`（省略＝可見的全部帳號；熱門規則同名合併）：依店家時區（`TZ_STORE`，預設 Asia/Taipei；binary 內嵌 tzdata）切日；同時回前一個等長區間供漲跌
- 回應時間口徑：顧客「開啟回合」（前一則不是顧客訊息）到店家第一則回覆；同回合連傳只算一次
- 圖表色：淺色 自動＝`#2a78d6`、真人＝`#eb6834`；深色 自動＝`#3987e5`、真人＝`#d95926`（surface `#251a14`）。dataviz 參考色盤 slot 1/2，兩模式都跑過 validator；咖啡色系配色 chroma 不足或紅綠色盲分不開，故不用。單一系列一律用 slot 1。元件一律用 `--lc-chart-*` 變數
- 深色模式：`html.dark`＋Element Plus 官方 dark css-vars，再以 `theme.scss` 的 `html.dark` 覆寫成暖棕色系；設定頁「外觀」選 跟隨系統／淺色／深色（localStorage `lc-theme`，`index.html` 內聯腳本防首繪閃白）；主色填色上的字在深色用深字（白字對焦糖底對比不足）。`IgPreview` 刻意維持 IG 淺色外觀

## 貼文排程

- 媒體：`POST /api/media` 存到 `MEDIA_DIR`（預設 `./data/media`，不進 git），以公開路徑 `/media/{隨機檔名}` 提供；IG 從 `PUBLIC_BASE_URL` + 該路徑抓檔，所以正式環境必須是 IG 抓得到的 HTTPS。PNG 一律轉 JPEG（IG 只收 JPEG），透明處補白
- 狀態：draft → scheduled → publishing → published｜failed。失敗自動重試共 3 次（1 分、5 分後），用盡才 failed；failed 可修改後重新排程（計數歸零）
- 中斷復原：publishing 超過 10 分鐘視為中斷；有 `ig_container_id` 時先查容器狀態，PUBLISHED 即直接標已發佈，不重發
- 依帳號：每篇貼文有 `ig_account_id`（編輯器選帳號，預設上次用的；預覽顯示該帳號頭像與名稱）；月曆／清單標示帳號、可篩選。媒體上傳時就歸屬帳號（`POST /api/media` 表單欄位 `account`），存貼文時檢查媒體都屬於貼文的帳號
- 發佈 token：貼文帳號所屬連線的 token（`account.Service.Credentials`），IG 帳號 ID 直接讀 `ig_accounts.ig_user_id`（系統用戶 token 的 `/me` 不是 IG 帳號，不打）；帳號沒掛連線才用 `IG_ACCESS_TOKEN` 兜底；停用的帳號不發（DEV_SIMULATOR=1 時走 `ig.FakePublisher`，token 一樣來自模擬連線）
- AI 寫文案：`POST /api/posts/caption`（body `account` 必填），用該帳號自動回覆的人設與知識庫
- 成效（Insights）：用貼文帳號的 token；`posts.InsightsRefresher` 每 15 分掃一次，只更新近 30 天已發佈的；未滿 7 天每 6 小時、之後每天；限動只在 24 小時內。指標：貼文／輪播／Reels＝views, reach, likes, comments, saved, shares；限動＝views, reach, replies, shares（`impressions` 已棄用）。手動 `POST /api/posts/{id}/insights/refresh`。需 `instagram_manage_insights` 權限（配置裡加權限後要重新連結）
- IG 貼文同步（`posts.MediaSyncer`，`internal/posts/sync.go`）：帳號上不是從後台發的貼文／輪播／Reels／限動寫進 `scheduled_posts`（`source='ig'`、status＝published、`scheduled_at`＝`published_at`＝IG 時間）；貼文每 6 小時（第一次抓最近 200 篇，之後從上次同步往回 30 天）、限動每 30 分鐘；每個啟用中帳號、單一帳號失敗不影響其他，token 失效走 `NoteAPIError`
- 類型映射：`media_product_type` STORY→story、REELS→reel；`media_type` CAROUSEL_ALBUM→carousel；其他→image。後台發的（已有 `ig_media_id`）同步到時只更新文案（以 IG 為準）與連結；中斷復原沒有 media id 的，以同類型＋同文案＋±2 小時認領；同帳號同 media 只一筆（部分唯一索引）
- 縮圖：下載到 `MEDIA_DIR`（`ig-` 開頭檔名，不進 `media_assets`），清單回 `igThumb`＝`/media/{檔名}`；IG CDN 網址會過期所以不存網址。限動同步進來當下抓一次成效，其餘照 `InsightsRefresher`
- `source='ig'` 唯讀：改內容／排程／刪除 409；只能切 `PATCH /api/posts/{id}/ai-exclude`（body `{aiExclude}`，兩種來源都可）與手動更新成效。清單可 `?source=app|ig`
- 手動同步 `POST /api/ig/accounts/{id}/sync-media`：排程權限＋帳號可見（範圍外、不存在 404），回 `{posts,stories}`（本次處理篇數）；每帳號 1 分鐘冷卻 429＋Retry-After。模擬模式 `ig.FakePublisher` 給每個帳號 3 篇假貼文＋1 則假限動、縮圖現場產生
- AI 參考近期貼文（`automation/postsref.go`）：`automation_settings.posts_kb_enabled`（預設開）時，自動回覆／建議回覆／寫文案／蒸餾的 system prompt 末尾加「近期 IG 貼文」`<data>` 區塊：該帳號最近 30 篇已發佈、未排除、有文案的（兩種來源、含限動），每篇 `[日期｜類型]`、文案經 `Untrusted(…,300)`、整段 ≤6000 字從舊的丟；只用文案不送圖。規則：貼文可能過期，活動／優惠只當「某日曾公告」、不得說成仍有效，不確定就轉真人／寫「確認一下再回覆您」，不得引用網址。排序固定、不放今天日期（prompt cache 穩定）

## 設定頁／IG 帳號連結

- 方式：Instagram API with Unified Login（`internal/ig/oauth.go`）。Meta App ID／App Secret（應用程式設定 → 基本資料）＋Facebook 登入「配置」的配置 ID 三者齊全才開（後台「憑證」或 `IG_APP_ID`／`IG_APP_SECRET`／`IG_CONFIG_ID`）；redirect URI＝`WEB_ORIGIN/api/ig/oauth/callback`（Meta 後台登記同一個，須 HTTPS）。配置要勾 `instagram_basic`、`instagram_manage_messages`、`instagram_content_publish`、`instagram_manage_insights`
- 流程：`/api/ig/oauth/start` 產 state 放 cookie（10 分、一次性）→ `facebook.com/dialog/oauth?client_id&config_id&redirect_uri&response_type=code&state`（不帶 scope）→ callback 驗 state → `GET graph.facebook.com/v27.0/oauth/access_token` 換到業務組合的系統用戶 token（SUAT）→ `/me?fields=id,client_business_id` → `/me/assigned_instagram_accounts`（跟著 `paging.cursors` 翻頁）同步全部帳號 → 逐一 `POST /{ig-id}/subscribed_apps` → 導回 `/settings?ig=connected|ig_error=`
- 資料：一次授權＝一個連線（`ig_connections`：client_business_id、系統用戶 id、config_id、token 以 AES-256-GCM 加密、`token_expires` NULL＝永不過期、last_error）；帳號（`ig_accounts`）以 `connection_id` 掛在連線下、可停用（`active`）。同一業務組合重新連結＝更新同一個連線；授權沒再包含的帳號會脫離連線（需重新連結）。webhook 先到過的同 ID 帳號會合併，顧客對話不分家
- SUAT 只能打 `graph.facebook.com`（`IG_API_BASE` 預設 `https://graph.facebook.com/v27.0`）；token 壽命在配置裡設（預設永不過期），連結時讀 `expires_in`，沒有就試 `GET /debug_token`；剩不到 14 天設定頁顯示「需重新連結」，過期就不給 token
- token 失效（店家移除 app、帳號移到別的業務組合、過期）：IG API 回 code 190 時寫在連線的 `last_error`，設定頁顯示
- 設定頁：依連線列出全部帳號（頭像、名稱、啟用狀態、私訊訂閱、連線到期）。每個帳號可「停用／啟用」（停用＝webhook 拒收、不送訊不發文，資料保留）、「重試訂閱」；每個連線可「同步帳號」（`POST /api/ig/connections/{id}/refresh`）、「重新連結」、「斷開連線」（只刪 token，帳號、顧客、對話保留）
- 帳號清單 `GET /api/ig/accounts`：所有登入者可用，只回可見帳號（各頁的帳號篩選／選擇器用）
- 模擬模式：`ig.FakeOAuth` 授權頁直接導回 callback，連上兩個模擬帳號 `dev-litty-coffee`、`dev-litty-roastery`
- 改密碼：`POST /api/auth/password`，自己目前的 session 不撤銷；新密碼不能與目前相同；改完清掉「必須改密碼」

## 憑證（後台可設定）

- `app_settings` 表；Meta App ID／Secret／配置 ID／驗證碼、Claude key／模型／強度可在設定頁「憑證」填寫，DB 值優先於 `.env`，存檔即生效（`appsettings.Store` 快照＋`LiveOAuth`／`automation.LiveAI` 原子替換）
- Meta 欄位：App ID、App Secret、配置 ID、Webhook 驗證 Token
- 機密欄位與 IG 連線 token 都以 AES-256-GCM 加密（`internal/secrets`），鑰匙＝env `SECRET_KEY`；未設時由 `JWT_SECRET` 推導（MVP 兜底，畢業前要設獨立 `SECRET_KEY`，換鑰匙＝已存密文讀不回）。API 只回尾 4 碼
- 「測試連線」用 Anthropic Models List（不計費）；IG App 沒有可靠的便宜檢查，未做

## 單一登入

- 一個帳號同時只能在一台裝置登入：每次登入產生隨機 `admin_users.session_id`，放進 JWT 的 `sid`；`auth.Middleware` 每次請求比對，不符 → 401＋清 cookie
  - DB 有別的 session＝已在其他裝置登入 → `{"error":"此帳號已在其他裝置登入，你已被登出","code":"session_replaced"}`，前端 `api/http.ts` 先跳提示再導去登入頁；被踢的那邊記一筆「被踢」（同一個舊 session 只記一次，唯一索引去重）
  - DB 沒有 session（登出、被停用、被重設密碼）或舊版沒有 `sid` 的 token → 一般 401（升級上線後所有人要重新登入一次）
- 撤銷時機：登出（只有 token 仍是目前 session 才清，被踢的舊裝置登出不影響新裝置）、管理員被停用（重新啟用後仍要重新登入）、被重設密碼。改自己的密碼不撤銷
- SSE（`/api/inbox/stream`）每 25 秒心跳重讀登入者，session 不同即斷線
- 在線：`last_seen_at` 由 middleware 最多每分鐘寫一次；管理員清單回 `lastSeenAt`、`online`（2 分鐘內有動靜且有有效 session）

## 操作紀錄

- 表 `audit_logs`（`internal/audit`）：admin_id（登入失敗找不到帳號時 NULL）、admin_name 快照、action（機器鍵）、summary（中文）、target_type／target_id、ig_account_id（不設 FK）、detail jsonb、ip、user_agent、status、success。保留 365 天，背景每日清（`audit.Store.RunCleanup`）
- 記什麼：登入後所有異動請求（POST/PUT/PATCH/DELETE，含 4xx/5xx 失敗、權限不足）由 `httpapi.auditMiddleware` 在回應後記（方法、chi 路由樣式、URL 參數、狀態碼、登入者）；登入成功／失敗（記嘗試的帳號，不記密碼）、登出、被踢、IG OAuth callback 連結成功／失敗由各 handler 記。背景自動動作（AI 回覆、自動黑名單、降頻）不記，看 messages／contacts
- 動作名稱：`internal/audit/actions.go` 的 `Routes`「方法＋路由樣式 → 鍵＋中文名」；`router.go` 新增異動路由要一併登記（`TestRouteNamesCoverRouter` 會抓漏；漏了退回「METHOD 樣式」）
- body 摘要：只記 JSON；鍵名（小寫去符號後）含 password／passwd／pwd／token／secret／apikey／session／cookie／authorization／credential 或以 key 結尾 → 值換成「[已遮蔽]」（空值保留）；字串截 200 字、陣列 50 項；改密碼／重設密碼／上傳媒體整個 body 不記；無法解析或超過 1MB 不記內容
- 帳號歸屬：依目標資源查 DB（對話、訊息、待改進、顧客、貼文、規則、知識庫、IG 帳號），其次 `?account=`、body 的 `account`／`igAccountId`；都沒有＝不屬於帳號
- API（`audit` 權限「操作紀錄」）：`GET /api/audit-logs?admin=&action=&from=&to=&account=&page=&pageSize=`（from/to 收 RFC3339 或 YYYY-MM-DD；只看部分帳號的人只看得到自己帳號的與不屬於帳號的紀錄，`?account=` 範圍外 403）、`GET /api/audit-logs/actions`（篩選下拉）、`GET /api/audit-logs/admins`（管理員下拉）
- 前端：`views/admins/AuditLogPanel.vue`、`api/audit.ts`
- IP 取 `X-Forwarded-For` 第一個（反向代理後），否則 RemoteAddr；未在代理後面時可被偽造

## 總覽（監控＋分析合併）、每日摘要、AI 建議回覆

- 「分析」已併入總覽（`/analytics` 轉 `/dashboard`）；頁面不放說明段落，必要說明用 `components/HelpTip.vue` 的 `?`
- 手機體驗是一級需求（Mike 2026-11-12：「整個後台都要考慮手機使用的絲滑體驗」）：每個改動都要在 390 寬實測；不擠、不出現巢狀小捲動區、點日期不叫出鍵盤、編輯類用全螢幕；主要分頁 KeepAlive＋切 tab 保留捲動位置（`router/index.ts` scrollBehavior）；手機總覽分「概況／圖表／貼文」、圖表左右滑
- 系統狀態：`internal/monitor` 每 60 秒檢查（每個 IG 帳號的 token／訂閱／最後收到 webhook、Claude key 與最近一次呼叫、webhook 積壓、排程貼文、DB），閾值在 `monitor.go` 開頭；`GET /api/system/status` 依可見帳號過濾；轉紅與恢復時推 `system.alert`（`GET /api/system/stream`，給有 `automation` 權限者＝含店長、超管）；token 失效同時寫連線 `last_error`
- 自動回覆總開關：`automation_settings.auto_reply_enabled`（依帳號），關閉＝只收不回（不跑規則／AI／降頻／轉真人通知）
- 每日摘要：`internal/digest`，每帳號在 `automation_settings.digest_time`（預設 09:00，`TZ_STORE`）整理前一天；重要度排序見 `digest/score.go`；AI 摘要只送統計與每則 ≤60 字節錄（輸入 ≤2400 字）；`GET /api/digests/latest`、`POST /api/digests/generate`
- AI 建議回覆：`POST /api/conversations/{id}/suggest`（inbox 權限、依帳號範圍、每人每分鐘 10 次）；只把草稿放進輸入框、絕不自動送出；顧客訊息以 `<conversation>` 資料區塊包裝防注入；操作紀錄不記草稿；呼叫計入 Claude 狀態
- 背景預先產生（`automation/suggest_bg.go`，migration 00019 `conversation_suggestions` 每對話一筆）：顧客訊息入庫且不會由 AI 自動回覆（handler=human、自動回覆關、規則／AI 轉真人後）→ 等合併秒數、只針對最後一則、同一則只產生一次；同時最多 3 個、每帳號每小時 60 次（行程內計數）；0 個版本重試一次；黑名單與沒 key 不產生；新顧客訊息進來舊的即刪。完成（含 failed）推 `suggestion.ready`（`{conversationId, igAccountId, forMessageId}`）。`GET /api/conversations/{id}/suggestion` 回 `status ready|pending|failed|none`（只回針對「最後一則未回覆顧客訊息」的；不呼叫 AI、不計限流）；手動 POST 成功也覆蓋存表並推事件；POST 的 422 帶 `code`：`no_customer_turn`／`refused`／`empty`

## 蒸餾建議（從對話歸納規則／知識庫）

- `internal/automation/distill.go`＋`httpapi/distill.go`，migration 00017（`distill_runs`、`distill_suggestions`）；自動回覆頁「蒸餾建議」分頁，`automation` 權限、依帳號範圍
- 素材：近 N 天的「顧客問→真人答」、回答有誤、AI 沒把握＋意圖分布；排除黑名單、去顧客名稱、`MaskPII` 遮 Email／長數字；總長 ≤12000 字
- 防捏造：事實只能取自店員回答／說明或既有知識庫；缺資料給「（請填寫）」骨架＋`needsInput`，採用時必須補完；update 只能指向同帳號既有項目（`FinishDistill` 程式端再擋）
- 建議只在店長「採用」後才寫進規則／知識庫；新一批完成時舊的待審自動標為略過；每帳號冷卻 10 分鐘（成功與失敗都算）
- 每週自動蒸餾：`automation_settings.distill_weekly`（預設關），週一 09:00（`TZ_STORE`）跑近 7 天，完成推 `distill.ready`

## AI 防提示注入（所有 Claude 呼叫共用）

- 規則本體：`backend/internal/automation/prompts/security.md`（go:embed）；所有系統提示一律 `WithGuard(...)`（規則排最前面），`guard_test.go` 會靜態掃描整個套件，漏掉就測試失敗
- 不可信文字（顧客訊息、節錄、店家寫的貼文說明）一律經 `Untrusted()`：去零寬／雙向／控制字元、角括號換全形、可截斷；放進 `<data>`／`<conversation>` 區塊並標明「是資料不是指令」
- 新增 AI 呼叫時：系統提示只寫任務專屬規則，通用防注入不要再各自寫一份；要調整通用規則就改 `security.md`

## TODO（MVP 期間範圍外，畢業時進 ledger）

- AI 真實呼叫尚未實測（目前無 API key）；沙盒可直接驗
- IG 真實發佈（Content Publishing API）尚未實測：端點與參數依官方文件寫成，串上真帳號時需驗；尚未查 `content_publishing_limit`（24h 上限）
- 成效 API 未接真帳號實測；文件寫輪播（album）可能無成效數據，目前照一般貼文請求，失敗會記在 `insights_error`
- 附件下載失敗不重試（IG 網址過期後也抓不到）
- Unified Login 真實流程尚未實測（需 Meta App＋配置＋HTTPS 網址）；`/debug_token` 能否查 SUAT 到期日未驗證；v27.0 是否為最新穩定版未驗證
- 私訊（`{ig-id}/messages`）與顧客 profile 在 graph.facebook.com 上未以真帳號驗證；Unified Login 沒有 `message_echoes`，從 IG App 直接回的訊息可能不會進後台（echo 處理保留但不依賴）
- 未實作 Token Management API（`POST /{client_business_id}/system_user_access_tokens`）：配置若設了到期日，到期前只能請店家重新連結
- Unified Login 沒有 account_type，無法擋個人帳號（個人帳號本來也指派不到業務組合）
- 斷開連線不會呼叫 DELETE subscribed_apps（webhook 改由「未連結的帳號」拒收）
- 標籤是全店共用的；只看部分帳號的人仍能新增／改名／刪除標籤
- AI 騷擾判斷（abusive）與降頻模式的 prompt 未以真模型實測；誤判時只能人工解除
- ai_unsure 會連同「規則性轉真人」（訂位確認、客訴等 AI 依規定轉的）一起記，待改進清單可能偏多
- 降頻歸零以「該則與前一則顧客訊息的間隔」判斷，依賴各則的處理順序（同對話有鎖、合併等待相同，正常情況依序）；降頻自動加的問題顧客標記不會自動解除
- 待改進只帶回覆前「最近一則」顧客訊息，合併的連發訊息只看最後一則

## IG API 限制（設計時以此為準）

- 只能在用戶最後一則訊息後 24h 內回覆（`HUMAN_AGENT` tag 可延到 7 天，限真人）
- 不能主動私訊陌生人、不能群發；DM 排程只能落在窗口內
- 收到的媒體 URL 會過期，要即時下載（已做，見開發模擬器段）
