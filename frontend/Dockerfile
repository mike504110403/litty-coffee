# syntax=docker/dockerfile:1
# litty-coffee 前端：node 建置 → nginx stable alpine 提供靜態檔，並反向代理 /api、/webhooks、/media、/healthz 到同環境後端。

FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginx:stable-alpine-slim
# 後端位址（compose 內固定服務名）；官方映像啟動時以 envsubst 把 templates/*.template 轉成 conf.d/*.conf
ENV BACKEND_ADDR=backend:8790 \
    NGINX_ENVSUBST_FILTER=^BACKEND_
RUN rm -f /etc/nginx/conf.d/default.conf
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1/nginx-health || exit 1
