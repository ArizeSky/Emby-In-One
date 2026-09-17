FROM golang:1.23-bookworm AS builder
ARG VERSION=dev
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod ./
COPY third_party ./third_party
COPY cmd ./cmd
COPY internal ./internal
COPY public ./public
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/emby-in-one ./cmd/emby-in-one

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN mkdir -p /app/config /app/data /app/public
COPY public ./public
COPY --from=builder /out/emby-in-one ./emby-in-one
# 非 root 运行 (uid 1000，与 docker-compose.yml 的 user 一致)
RUN useradd -r -u 1000 -U -s /usr/sbin/nologin eio && chown -R eio:eio /app
USER eio
EXPOSE 8096
# 健康检查打的是 Emby 兼容的公开端点（改过 server.port 时需同步调整）
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8096/System/Info/Public || exit 1
CMD ["./emby-in-one"]
