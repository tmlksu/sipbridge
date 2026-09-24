# relay (sipbridge 変換装置)

Asterisk には SIP/UDP 内線として振る舞い、アプリには
`wss://<host>/v1/session` (JSON 制御 + RTP バイナリ) を提供する。
プロトコルは `../docs/PROTOCOL.md`。

## 起動 (開発用)

```sh
cd relay
export PATH=$HOME/go-toolchain/go/bin:$PATH
BACKEND=fake AUTH_MODE=token DEV_TOKEN=x LISTEN=127.0.0.1:8080 \
  STATE_FILE=/tmp/sipbridge-state.json go run ./cmd/relay
curl localhost:8080/healthz   # {"status":"ok"}
```

## 環境変数

| 変数 | 既定 | 説明 |
|---|---|---|
| `LISTEN` | `127.0.0.1:8080` | 待ち受けアドレス |
| `AUTH_MODE` | `token` | `cf-access`\|`token` |
| `CF_TEAM_DOMAIN` | (無) | cf-access 時に必須 (例 `example.cloudflareaccess.com`) |
| `CF_ACCESS_AUD` | (無) | cf-access 時に必須 (Access の AUD タグ) |
| `DEV_TOKEN` | (無) | `token` 時に必須 (`Authorization: Bearer`) |
| `SIP_HOST/SIP_PORT` | `127.0.0.1:5060` | sip バックエンドの対向 SIP サーバ。Asterisk に限らず任意のレジストラでよい (例: ひかり電話 HGW の LAN 側 IP)。旧名 `ASTERISK_HOST/ASTERISK_PORT` も読む |
| `SIP_USER/PASSWORD/DISPLAY` | (無) | **任意**の既定アカウント (v1.1)。設定するとアカウント未設定の端末が暫定的に結び付く (永続化しない)。SIP アカウントは通常アプリが `sip_account` で登録する |
| `LOCAL_IP` | (自動) | T2 の SDP/Contact 用 |
| `RTP_PORT_MIN/MAX` | `20000/20100` | T2 の RTP 用 |
| `BACKEND` | `fake` | `fake`\|`sip` (sip: 実 SIP サーバ対向の本番用) |
| `RESUME_TIMEOUT_SEC` | `30` | 通話中に WS が切れてから BYE するまでの猶予 (1..300)。アプリの再接続が収まる値にする |
| `LOG_LEVEL` | `info` | debug\|info\|warn\|error |
| `STATE_FILE` | `/var/lib/sipbridge/state.json` | 状態ファイル (account / 端末結び付け / push トークン, 0600)。旧名 `PUSH_STATE_FILE` も読み、旧形式は起動時に自動移行 |
| `FCM_PROJECT_ID` | (無) | FCM 送信に使用 (Firebase プロジェクト ID)。空なら push は no-op |
| `FCM_SERVICE_ACCOUNT_FILE` | (無) | FCM 送信に使用 (サービスアカウント JSON のパス)。`FCM_PROJECT_ID` と対 |

## サブコマンド

- `relay healthcheck`: `LISTEN` の `/healthz` を GET し 200 なら exit 0
  (Docker HEALTHCHECK 用。scratch イメージに curl が無いため)。

## push 送信 (FCM)

`internal/push` の `FCM` が `Pusher` インタフェースを満たす。配線は
`cmd/relay/main.go` で行う。配線例:

```go
store, _ := state.New(cfg.StateFile)
var pusher push.Pusher = push.Noop{}
if cfg.FCMProjectID != "" && cfg.FCMServiceAccountFile != "" {
    fcm, err := push.NewFCM(push.FCMConfig{
        ProjectID:          cfg.FCMProjectID,
        ServiceAccountFile: cfg.FCMServiceAccountFile,
        Store:              store, // UNREGISTERED トークンの自動削除先 (push.TokenRemover)
    })
    if err != nil {
        return err
    }
    pusher = fcm
}
hub := session.NewHub(factory, pusher, store, ...)
```

仕様:

- FCM HTTP v1 (`POST https://fcm.googleapis.com/v1/projects/<id>/messages:send`)。
  OAuth2 はサービスアカウント JSON から `golang.org/x/oauth2/google` で取得
  (スコープ `https://www.googleapis.com/auth/firebase.messaging`)。
- data message `{type, callId, caller, display}` (空欄は省略。`from` は FCM の予約キーの
  ため `caller` を使う) + `android.priority=HIGH` +
  `ttl=30s` (PROTOCOL.md の 25 秒応答待ちより少し長い)。
- `404/UNREGISTERED` と判定したトークンは `push.TokenRemover`
  (`internal/state.Store` が実装) から削除し、`Send` はエラーに含めない。
  それ以外の失敗は `errors.Join` で返す。
- テスト時は `FCMConfig.Endpoint` に `httptest` サーバ URL、`TokenSource` に
  `oauth2.StaticTokenSource` を渡す (`fcm_test.go` 参照)。

## マルチアカウント (v1.1)

1 プロセスで複数の SIP アカウント (内線) を扱う。account ごとに
「グループ」(Backend + `call.Manager` + 所属接続 + 勝者 + メディアポンプ) を持つ。

- 端末は `sip_account {user,password,display}` でアカウントに結び付く。
  結び付けと資格情報は `STATE_FILE` に永続化され、起動時に全アカウントの
  REGISTER を開始する (端末が未接続でも登録を維持する)。
- 結び付いた端末が 0 になったらグループを停止し、state から account を消す
  (既定アカウント `SIP_USER` は常駐)。
- 登録済みアカウントに別パスワードが来たら `error account_password_mismatch`。
  未登録なら受理して Backend を作り直す。Backend 生成失敗は `error account_failed`。
- アカウント未設定の端末が `answer/reject/hangup/dial` を送ると `error no_account`。
- 停止時 (グループ停止・プロセス終了) は REGISTER `Expires: 0` を 1 回送る。
- ロック順序は `hub.mu → group.mu` の一方向のみ。

## wsprobe (E2E 用 WS プローブ)

```sh
CGO_ENABLED=0 go build ./cmd/wsprobe

# 発信側 (101 として接続し 102 へ発信、5 秒通話して切断)
./wsprobe -url ws://127.0.0.1:18080 -token dev -device probe-101 \
  -account 101:test101pass -expect-registered \
  -dial 102 -talk 5s -hangup-after 5s -expect-ended

# 着信側 (102 として接続し、着信を待って応答)
./wsprobe -url ws://127.0.0.1:18080 -token dev -device probe-102 \
  -account 102:test102pass -expect-registered \
  -wait-incoming -answer -talk 5s -expect-ended -timeout 60s
```

結果は 1 行 JSON (`{"hello":…,"registered":…,"incoming":…,"answered":…,
"rtpSent":n,"rtpRecv":n,"ended":…,"errors":[]}`) を stdout に出し、
期待に反したら exit 1。`-json` で stderr の進捗ログを止める。
`-cf-id/-cf-secret` で Cloudflare Access Service Token を使える。

## 開発

```sh
export PATH=$HOME/go-toolchain/go/bin:$PATH
gofmt -l . && go vet ./... && go test ./...   # 全てクリーンであること
CGO_ENABLED=0 go build ./cmd/relay ./cmd/wsprobe   # 静的バイナリ

# Asterisk 結合テスト (docker compose -f ../deploy/test/compose.yml up -d が必要)
go test -tags integration ./...
```

## 構成

- `internal/config`: 環境変数の読み込みと検証
- `internal/proto`: PROTOCOL.md の JSON 型と encode/decode
- `internal/auth`: `TokenAuth` / `CFAccessAuth` (JWKS 10 分キャッシュ、RS256、`aud`/`exp` 検証)
- `internal/call`: 単一通話の状態機械 (`Backend` インタフェース)
- `internal/fakebackend`: テスト用バックエンド (`InjectIncoming`、`Answer` 後はエコー)
- `internal/push`: `Pusher` インタフェース + no-op、FCM HTTP v1 送信 (`fcm.go`)。無効トークン (404/UNREGISTERED) は `TokenRemover` 経由で自動削除
- `internal/state`: 状態ファイル (version 2: accounts / devices{account, push}) の読み書き。旧形式の自動移行、0600 で tmp→rename の原子的書き込み
- `internal/sipbackend`: sipgo による `call.Backend` 実装 (REGISTER/UAS/UAC/RTP)
- `internal/session`: WS セッション管理 (`X-Device-Id` 単位、account グループ、`hello`、20 秒 ping、fanout、勝者のみ中継)
- `cmd/wsprobe`: E2E 用 WS プローブ CLI
