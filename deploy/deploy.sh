#!/usr/bin/env bash
# 在主機上部署某個環境：拉映像 → up -d（等 healthy）→ 經 nginx 打 /healthz 驗整條鏈 → 清掉懸空映像。
# CI（deploy.yml）會把本檔與 compose 檔一起複製到 /opt/litty/<env>/ 後以 deploy 使用者執行；也可手動跑：
#   /opt/litty/prod/deploy.sh prod
# 映像 tag 由 /opt/litty/<env>/.env 的 IMAGE_TAG 決定（平常是移動 tag dev／prod）。
set -euo pipefail

env_name=${1:?用法：deploy.sh dev|prod}
case "$env_name" in
  dev | prod) ;;
  *) echo "未知環境：$env_name" >&2; exit 2 ;;
esac

dir=/opt/litty/$env_name
cd "$dir"
if [[ ! -f .env ]]; then
  echo "$dir/.env 不存在（從 deploy/env/$env_name.env.example 建立）" >&2
  exit 1
fi

compose=(docker compose -p "litty-$env_name" -f compose.yml -f "compose.$env_name.yml")

"${compose[@]}" config --quiet
"${compose[@]}" pull --quiet
"${compose[@]}" up -d --remove-orphans --wait --wait-timeout 180

echo "健康檢查（web → backend /healthz）"
for _ in $(seq 1 30); do
  if out=$("${compose[@]}" exec -T web wget -q -O - http://127.0.0.1/healthz 2>/dev/null); then
    echo "ok: $out"
    "${compose[@]}" ps --format 'table {{.Service}}\t{{.Image}}\t{{.Status}}'
    docker image prune -f >/dev/null
    exit 0
  fi
  sleep 2
done

echo "健康檢查失敗" >&2
"${compose[@]}" ps
"${compose[@]}" logs --tail 80 backend web >&2
exit 1
