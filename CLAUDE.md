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

## IG API 限制（設計時以此為準）

- 只能在用戶最後一則訊息後 24h 內回覆（`HUMAN_AGENT` tag 可延到 7 天，限真人）
- 不能主動私訊陌生人、不能群發；DM 排程只能落在窗口內
- 收到的媒體 URL 會過期，要即時下載
