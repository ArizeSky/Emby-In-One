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
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN mkdir -p /app/config /app/data /app/public
COPY public ./public
COPY --from=builder /out/emby-in-one ./emby-in-one
EXPOSE 8096
CMD ["./emby-in-one"]