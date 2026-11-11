# syntax=docker/dockerfile:1
# litty-coffee 後端：多階段建置，CGO 關閉、靜態 binary；執行階段 alpine＋CA 憑證、非 root。
# migration 與時區資料都嵌在 binary 內（internal/db 的 go:embed、cmd/server 匯入 time/tzdata），
# 執行時唯一讀寫的本機路徑是 MEDIA_DIR（上傳媒體與收件匣附件），掛 volume 到 /data/media。

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.24
# ca-certificates：打 Meta Graph API 與 Anthropic API（HTTPS）；wget（busybox）給 healthcheck 用
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /data/media \
    && chown -R app:app /data
COPY --from=build /out/server /usr/local/bin/server
USER app:app
ENV ADDR=:8790 \
    MEDIA_DIR=/data/media
# 具名 volume 第一次建立時會複製這個目錄的擁有者（app），非 root 也寫得進去
VOLUME ["/data/media"]
EXPOSE 8790
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8790/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
