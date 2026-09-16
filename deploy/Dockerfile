# syntax=docker/dockerfile:1
# sipbridge relay 用マルチステージビルド。
# ビルドコンテキストは relay/ を想定。リポジトリ直下から実行すること:
#   docker build -f deploy/Dockerfile relay/ -t sipbridge-relay:latest
# または deploy/ で docker compose build (context: ../relay)。
# 前提: relay/ に go.mod と cmd/relay/main.go があること (T1 の成果物)。

ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# 依存の先行取得 (キャッシュ効率のため go.mod/go.sum を先にコピー)。
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 静的バイナリ (scratch で動かすため CGO 無効)。
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/relay ./cmd/relay

FROM scratch
# JWKS (Cloudflare Access) / FCM への HTTPS 通信に必要。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/relay /relay
# scratch に /etc/passwd が無いため数値 UID を指定 (65532 = distroless の nonroot と同じ)。
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/relay"]
# HEALTHCHECK は入れない (scratch に curl/wget が無いため)。
# 死活監視は compose 側で ["CMD", "/relay", "healthcheck"] を使う
# (T1: relay に `healthcheck` サブコマンド = /healthz を叩いて exit code を返す実装を前提)。
