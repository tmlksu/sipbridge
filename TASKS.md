# TASKS — 実装タスク分割 (paseo エージェント向け)

進捗 (2026-09-16): T1〜T6 完了 (docs/E2E.md)。**第 2 期 (T7〜T10)**: UI 設計 (`docs/UI-DESIGN.md`) の実装と、SIP アカウント (内線番号/パスワード) をアプリ側で設定する方式への移行 (`docs/PROTOCOL.md` の「SIP アカウント」節)。

前提: `DESIGN.md` と `docs/PROTOCOL.md` を先に読むこと。各タスクは **担当ディレクトリ以外を触らない**。git commit はしない (レビュー担当 Fable が行う)。

| ID | 内容 | ディレクトリ | 担当モデル | 依存 |
|---|---|---|---|---|
| T1 | relay コア (WS サーバ, プロトコル, 認証, 状態機械, フェイク SIP バックエンド, 単体テスト) | `relay/` | Muse Spark 1.3 | – |
| T2 | relay SIP バックエンド (sipgo: REGISTER/UAS/UAC/RTP) + Asterisk Docker 結合テスト | `relay/internal/sipbackend/`, `deploy/test/` | Opus 5 | T1 |
| T3 | Docker/compose/cloudflared/SETUP 手順 | `deploy/`, `docs/SETUP.md` | Muse Spark 1.3 | – (T1 の `cmd/relay` 名だけ前提) |
| T4 | Android アプリ: SIP スタック撤去 → RelayClient (WSS) 化, 設定 UI, 暗号化保存, ビルド通過 | `android/` | Muse Spark 1.3 | – (PROTOCOL.md 準拠) |
| T5 | FCM push: relay 送信 (`internal/push`) + Android `gms` flavor 受信 | `relay/internal/push/`, `android/` | Muse Spark 1.3 | T1, T4 |
| T6 | 実機 E2E チェックリスト実施 (ユーザー + Fable) | `docs/E2E.md` | – | T2, T4 |
| T7 | relay マルチアカウント化 (`sip_account`, 永続化, account ごとの Backend/Manager/Hub, 既定アカウント互換) + E2E 用 WS プローブ CLI | `relay/`, `deploy/test/` | Opus 5 | – (PROTOCOL.md v1.1 準拠) |
| T8 | Android アプリ UI 刷新 (`docs/UI-DESIGN.md`) + SIP アカウント設定 + 履歴/連絡先 + 通話中操作 (ミュート/スピーカー/DTMF/縮小/終了確認) | `android/` | Muse Spark 1.3 (pi) | – (PROTOCOL.md v1.1 準拠) |
| T9 | ハーネス: ビルド/テスト一括 (`scripts/check.sh`), VPS 配備 (`ops/deploy.sh`), E2E (`ops/e2e.sh`), 端末投入 (`scripts/adb-setup.sh`) | `scripts/`, `ops/` | Fable | T7, T8 |
| T10 | VPS 配備 + 実機 E2E (Echo Show ×2 / P780) + docs 更新 | `docs/` | Fable | T9 |

## T1: relay コア (Go)

成果物 (`relay/`):
- `go.mod` (module `github.com/tmlksu/sipbridge/relay`, go 1.27)
- `cmd/relay/main.go`: 環境変数で設定 (`LISTEN=127.0.0.1:8080`, `AUTH_MODE=cf-access|token`, `CF_TEAM_DOMAIN`, `CF_ACCESS_AUD`, `DEV_TOKEN`, `SIP_HOST`, `SIP_PORT` (旧名 `ASTERISK_HOST`/`ASTERISK_PORT` も可), `SIP_USER`, `SIP_PASSWORD`, `SIP_DISPLAY`, `LOCAL_IP` (SDP/Contact に載せる自 IP; 空なら自動検出), `RTP_PORT_MIN/MAX` (既定 20000-20100), `BACKEND=fake|sip`, `LOG_LEVEL`)。`/healthz` を持つ。
- `internal/config`: 環境変数読み込みと検証。
- `internal/proto`: PROTOCOL.md の JSON 型 (`t` で判別) の encode/decode。テーブル駆動テスト。
- `internal/auth`: `Authenticator` インタフェース。`TokenAuth` (Bearer 一致) と `CFAccessAuth` (JWKS を `https://<team>/cdn-cgi/access/certs` から取得し 10 分キャッシュ, RS256, `aud` 検証, `exp` 検証)。テストは `httptest` で JWKS を配信し、生成した RSA 鍵で署名した JWT を検証する。
- `internal/session`: WS セッション管理 (`X-Device-Id` 単位)。`hello` 送信、ping/pong 20 秒、複数セッションへの `incoming` ファンアウト、最初の `answer` が勝つ、バイナリフレームはアクティブ通話の勝者セッション⇄バックエンド間だけ中継。
- `internal/call`: 状態機械 (PROTOCOL.md の遷移)。`Backend` インタフェース:
  ```go
  type Backend interface {
      Start(ctx context.Context, ev chan<- Event) error   // Event: Registered{ok,detail} / Incoming{callId,from,display,pt} / Ended{callId,reason,code} / Answered{...} / Ringing{...}
      Answer(callId string, pt int) (MediaPipe, error)    // 200 OK を送り RTP パイプを返す
      Reject(callId string, code int) error
      Hangup(callId string) error
      Dial(to string) (callId string, err error)
  }
  type MediaPipe interface { Send(rtp []byte) error; Recv() <-chan []byte; Close() error }
  ```
- `internal/fakebackend`: テスト用。外部から `InjectIncoming()` でき、`Answer` 後は `Send` したパケットをそのまま `Recv` に返す (エコー)。
- `internal/push`: `Pusher` インタフェースと no-op 実装のみ (実装は T5)。`register_push` の永続化は `PUSH_STATE_FILE` (JSON) に保存。
- テスト: `go test ./...` が通る。`go vet` クリーン。セッション⇄fakebackend の結合テスト (WS クライアントで接続 → InjectIncoming → `incoming` 受信 → `answer` → バイナリ送信がエコーで戻る → `hangup` → `ended`)。
- ビルド: `CGO_ENABLED=0 go build ./cmd/relay` で静的バイナリ。

受け入れ: `go build ./... && go vet ./... && go test ./...` 成功。`BACKEND=fake AUTH_MODE=token DEV_TOKEN=x ./relay` で起動し `curl localhost:8080/healthz` が 200。

## T2: SIP バックエンド (Go, sipgo)

- `relay/internal/sipbackend`: `call.Backend` 実装。`github.com/emiago/sipgo` を使用。
  - REGISTER (digest MD5, qop=auth 対応), Expires 300, 240 秒で再登録, 失敗時は 5→60 秒バックオフ。`Registered` イベント。
  - 着信 INVITE (UAS): 100/180 即応、SDP から相手 RTP IP/port/PT 取得 (PCMU/PCMA のみ, それ以外は 488)。`Answer` で RTP UDP ソケット (`RTP_PORT_MIN..MAX` から確保) を開き 200 OK (SDP: `LOCAL_IP`, sendrecv, ptime 20)。ACK 待ち, 200 再送 (T1 タイマ)。CANCEL → 487。BYE → 200 + `Ended`。re-INVITE は同一 SDP で 200 + 宛先追従。OPTIONS → 200。
  - `Reject` → 486 等。`Hangup` → BYE (ACTIVE) / CANCEL (RINGING_OUT)。
  - `Dial` (UAC): INVITE with SDP → 1xx で `Ringing` (183 は early=true) → 200 で ACK + `Answered`。401/407 で digest 再送。
  - `MediaPipe`: UDP ソケットの read → `Recv` チャネル (バッファ 50, 溢れは古い方を捨てる)。`Send` → 相手宛 UDP。RTCP は無視。
- **T1 完了後の追記 (必読)**: `call.Backend` は `relay/internal/call/call.go` に確定済み。発信は `EvRinging{Early:true, Pipe}` (183) / `EvAnswered{Pipe}` (200) で **RTP パイプをイベントに載せて返す** (INVITE 送信時に確保したソケット)。着信の `Answer()` は戻り値でパイプを返す。Backend メソッドは Manager のロック中に呼ばれるため、**イベント送信 (`ev <-`) をメソッド内で同期的に行ってはならない** (goroutine で送る)。`fakebackend` が参考実装。`cmd/relay/main.go` の `case "sip"` を差し替えて配線すること。
- `deploy/test/asterisk/`: Asterisk 公式イメージ (or `andrius/asterisk`) 用の最小 `pjsip.conf` (内線 101 = relay, 102 = テスト発呼側), `extensions.conf` (102→101 Dial, `*43` エコーテスト)。`docker compose -f deploy/test/compose.yml up` で起動。
- 結合テスト (`//go:build integration`): Asterisk コンテナに relay を REGISTER → sipgo UAC (テスト内) が 102 として 101 に INVITE → relay が `Incoming` を上げる → `Answer` → 双方向 RTP が到達 → `Hangup`。`*43` エコーを `Dial` で呼び、送った RTP が戻ることも確認。

受け入れ: 単体 + `go test -tags integration ./...` (Docker 利用可) が成功。

## T3: デプロイ

- `deploy/Dockerfile`: multi-stage (golang:1.27 → scratch or distroless), `CGO_ENABLED=0`, `USER nonroot`, `HEALTHCHECK` は compose 側 (scratch に curl 無し) → relay に `-healthcheck` サブコマンドを追加してもらう (T1 と調整: `relay healthcheck` で `/healthz` を叩いて exit code を返す)。
- `deploy/docker-compose.yml`: `relay` (network_mode: host, env_file `.env`, restart unless-stopped, healthcheck) + `cloudflared` (`cloudflare/cloudflared:latest`, `tunnel run --token ${TUNNEL_TOKEN}`, network_mode host, restart unless-stopped)。
- `deploy/.env.example`: 全環境変数とコメント。
- `docs/SETUP.md` (日本語): (1) Cloudflare Zero Trust で Tunnel 作成 → 公開ホスト名 `sip.<domain>` → `http://localhost:8080`; (2) Access アプリケーション (Self-hosted) 作成 → ポリシー **Service Auth** → Service Token 発行 → AUD タグ取得 → `.env`; (3) Asterisk 側 pjsip endpoint 例 (内線 101, `allow=ulaw,alaw`, `direct_media=no`, `rtp_symmetric=yes`, `qualify_frequency=60`); (4) `docker compose up -d`; (5) 動作確認 (`curl` with `CF-Access-Client-Id/Secret` で `/healthz`); (6) Firebase プロジェクト作成と `google-services.json`, サービスアカウント JSON の置き場 (T5 と整合); (7) 端末側設定値一覧。
- `deploy/systemd/` は不要 (Docker で統一)。

受け入れ: `docker compose config` が通る。`docker build -f deploy/Dockerfile relay/` が成功 (T1 完了後に再確認)。

## T4: Android アプリ

`android/` は EchoSIP standby v1.5 のコピー。これを sipbridge クライアントに改造する。
- applicationId / namespace: `io.github.tmlksu.sipbridge`、パッケージも移動。アプリ名「SIP Bridge」。`rootProject.name = "SipBridge"`。
- **削除**: `SipEngine.kt`。UDP LISTEN を完全撤去 (`DatagramSocket` を残さない)。
- **追加** `RelayClient.kt`: OkHttp 4.12 WebSocket。ヘッダ `CF-Access-Client-Id/Secret` (設定が空なら `Authorization: Bearer <devToken>`), `X-Device-Id` (初回生成し保存), `X-Client-Version`。`docs/PROTOCOL.md` の JSON を `org.json` で encode/decode (外部 JSON ライブラリ不要)。ping 20 秒、指数バックオフ再接続、`hello` で状態同期。リスナー: `onHello/onRegistration/onIncoming/onAnswered/onEnded/onError/onDisconnected`。
- **改造** `RtpEngine.kt`: `DatagramSocket` を `MediaSink { fun send(rtp: ByteArray) }` + `onRtpReceived(ByteArray)` に置き換え (パケット生成/解析はそのまま)。ジッタバッファ 5 フレーム / 上限 20。
- `SipService.kt` → `BridgeService.kt`: RelayClient を保持。モード `PERSISTENT` (常時接続) / `PUSH` (着信 push か発信操作で接続し、通話終了 60 秒後に切断)。通知文言は「Bridge 接続中 (登録OK/未登録)」。
- `IncomingCallActivity` / `CallOverlayManager` / `NotificationHelper` / `CallHub` はそのまま流用 (`SipEngine.IncomingSession` 依存を `CallHub.CallSession` インタフェースに置換)。
- `MainActivity` 設定項目: relay URL, Access Client ID, Access Client Secret, Dev Token, モード, マイクゲイン, オーバーレイ, 自動起動。`EncryptedSharedPreferences` (`androidx.security:security-crypto:1.1.0-alpha06`) に保存。
- flavor: `foss` (FCM 無し, Echo Show 用) / `gms` (Firebase Messaging; 受信処理は T5)。`gms` は `google-services.json` が無い場合もビルドが通るようにする (plugin を条件適用)。
- テスト: `app/src/test/` に プロトコル encode/decode とジッタバッファの JVM テスト。
- ビルド: `export JAVA_HOME=~/android-build/jdk PATH=~/android-build/jdk/bin:$PATH ANDROID_HOME=~/android-build/sdk; cd android && ~/android-build/gradle-8.9/bin/gradle :app:assembleFossDebug :app:testFossDebugUnitTest --no-daemon` が成功すること。
- `android/DESIGN.md` / `INSTALL.md` を新構成に書き換える。

受け入れ: 上記ビルド+テスト成功。`grep -r DatagramSocket android/app/src` がゼロ件。

## T5: FCM push

- relay `internal/push/fcm.go`: FCM HTTP v1 (`https://fcm.googleapis.com/v1/projects/<id>/messages:send`), サービスアカウント JSON (`FCM_SERVICE_ACCOUNT_FILE`) で OAuth2 トークン取得。data message `{type:"incoming", callId, caller, display}` (`from` は FCM 予約キーのため `caller`), `android.priority=high`, TTL 30 秒。トークン無効 (404/UNREGISTERED) は削除。
- Android `gms` flavor: `FirebaseMessagingService` 実装 → `BridgeService` を `ACTION_WAKE_INCOMING` で起動 → 接続後 `hello.call` から着信 UI。`onNewToken` → `register_push`。
- `docs/SETUP.md` の Firebase 節を実手順に更新。

## T6: E2E チェックリスト (`docs/E2E.md`)

1. relay を Docker で起動、Asterisk に登録 OK (`pjsip show contacts`)。
2. S25 (gms/PUSH) と Echo Show (foss/PERSISTENT) を設定。
3. 内線→relay 着信: 両端末に `incoming`、片方応答、双方向音声、BYE 両方向。
4. アプリ強制終了状態で着信 → FCM で起床 → 応答できる (S25)。
5. 25 秒無応答 → 発呼側に 480。
6. Wi-Fi 切断→再接続で再登録・再接続。
7. Cloudflare Access のトークン無しで接続 → 401。


---

## 第 2 期 (2026-09-16 着手)

### T7: relay マルチアカウント (Go) — 担当 Opus 5

`docs/PROTOCOL.md` の「SIP アカウント」節が仕様。現状は 1 プロセス = 1 Backend = 1 Manager = 1 Hub。

- `internal/state` (新設。`internal/push.Store` を置き換え/包含): version 2 の状態ファイル
  (accounts / devices{account, push})。旧形式の自動移行。0600 で原子的に書く (tmp → rename)。
  `push.Store` の API を使っている箇所 (`push/fcm.go` の UNREGISTERED 削除など) は新 Store に移す。
- `internal/config`: `STATE_FILE` (旧 `PUSH_STATE_FILE` も読む)。`SIP_USER/SIP_PASSWORD/SIP_DISPLAY` は任意
  (既定アカウント)。`EXTENSION` は廃止 (extension = account user)。
- `internal/session`: Hub を account ごとのグループ (Manager, devs, winner/dialer, resumeTimer, pump) に分割。
  `Conn` はグループへのポインタを持ち、`sip_account` で付け替える。Backend 生成は
  `BackendFactory func(user, password, display string) (call.Backend, error)` を注入
  (main: sipbackend / テスト: fakebackend)。account ごとの `context` で Backend を停止できること
  (sipbackend の Start は ctx キャンセルで UA を閉じる実装済み。**停止時に REGISTER Expires:0 を送る**こと
  を追加)。パスワード規則・`no_account`・`account_password_mismatch` を実装。
  push は account に結び付くオフライン端末のみ。
- `internal/proto`: `SipAccount` 型、`Hello.Account`。
- `cmd/relay`: 起動時に state の全 account を起動。`RelayVersion` を `0.2.0` に。
- `cmd/wsprobe` (新設, E2E 用 CLI。`go build ./cmd/wsprobe`): フラグ `-url`, `-token` | `-cf-id/-cf-secret`,
  `-device` (既定はランダム), `-account user:pass`, 動作 `-dial <to>` / `-wait-incoming`, `-answer`,
  `-talk 5s` (PCMU 1kHz トーンを 20ms 間隔で送り、受信 RTP を数える), `-hangup-after 5s`, `-timeout 30s`,
  `-expect-registered`。結果を 1 行 JSON (`{"hello":…,"registered":…,"incoming":…,"rtpSent":n,"rtpRecv":n,"ended":…}`)
  で出し、期待に反したら exit 1。
- テスト: 既存テストを新構成に更新 + 追加: (a) 2 account × 2 device で fakebackend の着信が正しい
  グループだけに届く、(b) password mismatch、(c) 旧形式 state の移行、(d) 既定アカウント互換、
  (e) `-tags integration` で Docker Asterisk (deploy/test) に 101/102 を **同一 relay プロセス** で登録し、
  device A (101) が `dial 102` → device B (102) に `incoming` → answer → RTP 双方向 → hangup
  (`deploy/test/asterisk/extensions.conf` に `102` の Dial を追加)。
- `docs/SETUP.md` / `deploy/.env.example`: 環境変数の変更を反映 (SIP_USER 任意化、STATE_FILE)。

受け入れ: `go build ./... && go vet ./... && go test ./...` + `go test -tags integration ./...` (Docker) 成功。
gofmt 済み。

### T8: Android アプリ UI 刷新 — 担当 Muse Spark 1.3 (pi)

仕様: `docs/UI-DESIGN.md` (画面) + `docs/PROTOCOL.md` v1.1 (`sip_account`, `hello.account`)。
段階的に進める (各段階でビルド+単体テストが通ること):

1. **P1 基盤**: Nocturne カラートークン/テーマ、`MainActivity` を BottomNavigation + 4 Fragment に。
   `SettingsFragment` (UI-DESIGN §1.4 全項目。SIP アカウント欄を含む)。`BridgeConfig` に
   `sipUser/sipPassword/sipDisplay/speakerOnAnswer/confirmHangup` 追加。`RelayProtocol` に
   `buildSipAccount` と `Hello.account`、`RelayClient`/`BridgeService` で最初の hello 後に
   `sip_account` を送る (2 回目以降の hello では送らない)。`error no_account` 等は設定画面の状態に反映。
   debug 用 `DebugConfigReceiver`。versionCode 2 / versionName 1.1。
2. **P2 通話 UI**: `CallActivity` (着信/発信/通話中/終了確認/DTMF シート/縮小)、`RtpEngine.sendDtmf`
   (in-band, DTMF 二重音 120ms + 無音 80ms、送信フレームにミックスではなく置換)、スピーカー/ミュート、
   `CallOverlayManager` の通話中ピル、`NotificationHelper` の文言 (UI-DESIGN §2.1)。`layout-land`。
3. **P3 履歴・連絡先**: `HistoryStore`/`ContactStore` (JSON, `Context.filesDir`)、`HistoryFragment`/
   `ContactsFragment`、`BridgeService` からの履歴書き込み、着信時の名前解決、`KeypadFragment` の
   一致表示。
4. **P4 仕上げ**: `android/DESIGN.md` / `INSTALL.md` を新 UI と SIP アカウント設定に合わせて更新。
   JVM テスト: `RelayProtocolTest` に sip_account/hello.account、`HistoryStore`/`ContactStore` の
   保存/検索、DTMF トーン生成 (振幅/長さ) のテスト。

制約: 外部依存の追加は `androidx.fragment`/`androidx.recyclerview`/`material` (既存) の範囲。
`grep -r DatagramSocket android/app/src` はゼロ件のまま。foss/gms 両 flavor のビルドが通ること。

### T9 / T10: ハーネスと実機 — 担当 Fable

- `scripts/check.sh`: relay (gofmt/vet/test) + android (assembleFossDebug/assembleGmsDebug/test) を一括。
- `ops/deploy.sh`: amd64 ビルド → scp → `/opt/sipbridge/relay` 差し替え → restart → healthz。
- `ops/e2e.sh`: VPS 上にテスト用 relay (AUTH_MODE=token, :18080) を一時起動し、`wsprobe` で
  テスト内線から本番内線へ発呼 → Echo Show (adb) で応答 → RTP 双方向を確認。
- `scripts/adb-setup.sh <serial> <flavor>`: APK インストール + 権限付与 + debug 設定投入。

### T11: アプリ UI/UX 追い込み (v1.2) — 担当 Muse Spark 1.3 (pi)

実機確認後のユーザー指摘 3 点。仕様は `docs/UI-DESIGN.md` (v1.2) の該当節。`android/` のみ触る。

1. **触覚フィードバック** (UI-DESIGN §1.1 / §3.1): キーパッドのキー・⌫・DTMF シートのキーを
   押したときだけ `performHapticFeedback(KEYBOARD_TAP)` (長押しは `LONG_PRESS`)。
   端末の設定が OFF なら振動しない (`FLAG_IGNORE_GLOBAL_SETTING` 禁止)。他のボタンでは振動させない。
2. **「スピーカーで応答」既定 OFF** (UI-DESIGN §1.4): `BridgeConfig.speakerOnAnswer` の既定を
   `false` に。加えて `AudioRoute.hasEarpiece(am)` を追加し、受話口が無い端末 (Echo Show) では
   設定に関わらずスピーカーへ出す (`BridgeService` の応答時経路設定)。文言も「既定 ON」→修正。
3. **端末の連絡先との連携** (UI-DESIGN §1.3「端末の連絡先との連携」): `DeviceContacts.kt` 新設。
   (a) 追加ダイアログの「端末の連絡先から選ぶ」(ACTION_PICK on `Phone.CONTENT_URI`、権限不要、
   番号単位なので複数番号でも選べる)、(b) 設定トグル「端末の連絡先を表示」(既定 OFF、
   `READ_CONTACTS` 実行時要求) で連絡先タブにグループ「端末」を番号ごとの行で表示、
   (c) 着信時の名前解決を `ContactStore` → `DeviceContacts` の順に。

受け入れ: `scripts/check.sh` (foss/gms 両 flavor + JVM テスト) が通ること。
