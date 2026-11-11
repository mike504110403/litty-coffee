# 部署（Lightsail 單機，dev＋prod）

## 架構

```
                         Lightsail Tokyo 4GB（Ubuntu 24.04，不開任何公網服務埠）
                        ┌──────────────────────────────────────────────────────────────┐
 瀏覽器 / IG webhook     │  prod（compose 專案 litty-prod）                             │
 https://admin.litty-   │   cloudflared ──► web(nginx:80) ──► backend:8790 ──► postgres │
 coffee.com ──CF Tunnel─┼──►    │             │ 靜態 SPA        │            └► redis    │
                        │       (出站連線)     └ /api /webhooks /media /healthz           │
                        │                                                              │
 tailnet 內的裝置        │  dev（compose 專案 litty-dev，DEV_SIMULATOR=1）              │
 http://<TS IP>:8080 ───┼──► web(nginx) ──► backend ──► postgres / redis               │
                        │                                                              │
 GitHub Actions ──Tailscale(tag:ci)──► ssh deploy@litty-server ──► deploy.sh            │
                        └──────────────────────────────────────────────────────────────┘
 映像：ghcr.io/mike504110403/litty-coffee-{backend,web}:<sha> 與移動 tag dev／prod
```

- 兩環境的網路、容器、volume（`litty-<env>-pgdata`、`-redisdata`、`-media`）完全分開。
- 主機目錄：`/opt/litty/<env>/`（`.env`、`compose.yml`、`compose.<env>.yml`、`deploy.sh`），備份在 `/opt/litty/backups/<env>/`。
- 來源 IP：nginx 以 `CF-Connecting-IP`（沒有就用連線來源）**覆寫** `X-Forwarded-For`／`X-Real-IP`，操作紀錄記到的就是真實用戶 IP。

## 第一次開機

1. Lightsail 建機（Ubuntu 24.04、4GB、Tokyo）；防火牆先只留 22。
2. Tailscale 後台：ACL 加 `tagOwners` `tag:litty-server`、`tag:ci`，並允許 `tag:ci` → `tag:litty-server:22`；產一把 `tag:litty-server` 的 auth key。
3. 本機產部署金鑰：`ssh-keygen -t ed25519 -f litty_deploy -C litty-deploy -N ''`。
4. 執行初始化（可重複跑）：
   ```
   ssh ubuntu@<公網IP> "sudo TS_AUTHKEY='<key>' DEPLOY_PUBKEY='$(cat litty_deploy.pub)' bash -s" < deploy/bootstrap.sh
   ```
5. 確認 `ssh -i litty_deploy deploy@litty-server` 走 Tailscale 可登入後，Lightsail 防火牆把 22 對外關掉（只留「允許 Lightsail 瀏覽器 SSH」當備援）。
6. 在主機上建環境檔（owner deploy、`chmod 600`）：
   ```
   sudo -u deploy cp <填好的> /opt/litty/dev/.env    # 來源：deploy/env/dev.env.example
   sudo -u deploy cp <填好的> /opt/litty/prod/.env   # 來源：deploy/env/prod.env.example
   ```
   密碼與金鑰各環境各自 `openssl rand -hex 32`；dev 的 `WEB_BIND`／`WEB_ORIGIN` 填 `tailscale ip -4`。
7. Cloudflare Zero Trust → Tunnels 建 tunnel（token 模式），Public Hostname `admin.litty-coffee.com` → `http://web:80`，token 填 `CLOUDFLARE_TUNNEL_TOKEN`。
8. 取主機金鑰給 CI：`ssh-keyscan -t ed25519 litty-server`（在 tailnet 內跑）。

## GitHub 設定

| 種類 | 名稱 | 說明 |
| --- | --- | --- |
| Secret | `TS_OAUTH_CLIENT_ID`、`TS_OAUTH_SECRET` | Tailscale OAuth client（scope `auth_keys`，tag `tag:ci`） |
| Secret | `DEPLOY_SSH_KEY` | `litty_deploy` 私鑰全文 |
| Secret | `DEPLOY_KNOWN_HOSTS` | `ssh-keyscan` 的輸出（主機名 `litty-server`） |
| Environment | `dev`、`prod` | 建議 `prod` 限定 `main` 分支、可加 required reviewers |

GHCR 用 workflow 內建的 `GITHUB_TOKEN`（`packages: write`），不需另設；主機拉映像時用部署 job 的短效 token 登入、部署完登出。

## 部署

- push `dev` → CI（go vet、go test、前端 build）通過 → 建兩個映像（tag `<sha>` 與 `dev`）→ 部署 dev。
- push `main` → 同上，tag `prod` → 部署 prod。
- 主機端 `deploy.sh <env>`：`pull` → `up -d --wait` → 經 web 打 `/healthz`（含 DB ping）→ 清懸空映像。

## 回滾

- **Actions（建議）**：Deploy workflow → Run workflow，選環境並填舊的完整 commit sha；它把 `<sha>` 映像重新標成 `dev`／`prod` 再部署，不重建。prod 請從 `main` 分支觸發。
- **主機上緊急處理**：`/opt/litty/<env>/.env` 把 `IMAGE_TAG` 改成舊 sha → `cd /opt/litty/<env> && docker login ghcr.io` (若套件為私有) → `./deploy.sh <env>`；事後記得改回 `dev`／`prod`，否則之後的部署仍停在舊版。
- migration 只會往前跑，回滾到舊版前先確認新 migration 與舊程式相容；不相容就先還原備份。

## 備份與還原

- cron 每天 03:30（台北）跑 `/usr/local/sbin/litty-pg-backup`，兩環境各一份 `pg_dump -Fc`，保留 14 天；紀錄在 `/var/log/litty-backup.log`。手動跑：`sudo litty-pg-backup`。
- 還原（以 prod 為例）：
  ```
  cd /opt/litty/prod
  docker compose stop backend web cloudflared
  docker compose exec -T postgres dropdb -U litty litty
  docker compose exec -T postgres createdb -U litty litty
  sudo cat /opt/litty/backups/prod/litty-prod-<時間>.dump | docker compose exec -T postgres pg_restore -U litty -d litty --no-owner
  docker compose up -d
  ```
- 媒體檔（volume `litty-<env>-media`）不在每日備份內；需要時 `docker run --rm -v litty-prod-media:/m -v /opt/litty/backups:/b alpine tar czf /b/media-prod.tgz -C /m .`。

## 查狀態與日誌

```
cd /opt/litty/prod           # .env 內有 COMPOSE_PROJECT_NAME／COMPOSE_FILE，直接下 docker compose 即可
docker compose ps
docker compose logs -f --tail 200 backend
docker compose logs --since 1h web
docker stats --no-stream
```

容器日誌為 json-file，每個容器 10MB×5 輪替。
