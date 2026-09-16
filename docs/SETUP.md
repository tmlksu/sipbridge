# sipbridge セットアップ手順

relay (変換装置) と cloudflared を Docker で動かし、宅内 Asterisk を
インターネットに穴を開けずに使うための手順。全体像は `DESIGN.md`、
relay ⇄ アプリのプロトコルは `docs/PROTOCOL.md` を参照。

前提

- 宅内 LAN に Asterisk が動いていること (内線 101 を relay 用に空けておく)。
- Cloudflare Zero Trust を使えること (チームドメイン例: `example.cloudflareaccess.com`)。
- Docker + Docker Compose が入ったホスト (Asterisk と同じ LAN。RTP のため relay と同一ホスト推奨)。
- 自分のドメイン (例: `example.com`) を Cloudflare で管理していること。

---

## 1. Cloudflare Tunnel の作成

1. Cloudflare Zero Trust ダッシュボード → Networks → Tunnels → Create a tunnel。
2. コネクタのインストールは不要 (後で Docker の cloudflared を使う)。**トークンをコピー**し、後述の `.env` の `TUNNEL_TOKEN` に貼る。
3. Public Hostname を追加する:
   - Subdomain: `relay`、Domain: 自分のドメイン (例: `example.com`)。
     以降この手順では、できあがるホスト名を `relay.example.com` と書く。
   - Service Type: `HTTP`、URL: `http://localhost:8080`。
     - cloudflared と relay は同じホストの host ネットワークで動くので `localhost` でよい。
     - relay の `LISTEN` を変更した場合は URL のポートを合わせる。
4. WebSocket の注意: Tunnel 経由の WebSocket はそのまま通る。アプリ↔relay の keep-alive
   (WS ping 20 秒周期、40 秒無応答で切断→指数バックオフ再接続) はアプリ側実装済み
   (`docs/PROTOCOL.md`) のため、Cloudflare 側の追加設定は不要。

## 2. Cloudflare Access (Service Token) の設定

アプリからの接続は Cloudflare Access の Service Token で保護する
(平文 UDP 待ち受けの代替。端末ごとに発行・失効できる)。

1. Zero Trust ダッシュボード → Access → Applications → Add an application → **Self-hosted**。
   - Application domain: `relay.example.com` (手順 1 のホスト名)。
2. ポリシーを追加する:
   - Action: `Service Auth`、Rule: 適当な名前 (例: `sipbridge-devices`)。
   - アプリからの接続はこのポリシーでのみ許可し、ブラウザログイン用ポリシーは作らない
     (作ると人間用ログイン画面が出てアプリが通れなくなる)。
3. Access → Service Auth → Service Tokens → Create Service Token。Client ID と
   Client Secret を控える (Secret は再表示不可)。
4. アプリケーションの Overview にある **AUD タグ** を控える。
5. `.env` に記入する (手順 4):
   - `AUTH_MODE=cf-access`
   - `CF_TEAM_DOMAIN=example.cloudflareaccess.com`
   - `CF_ACCESS_AUD=<AUD タグ>`
   - 端末側 (手順 7) には Client ID / Secret を設定する。
6. 開発時 (Access を通さない直結確認) のみ `AUTH_MODE=token` + `DEV_TOKEN=<適当な文字列>` を使う。
   本番では必ず `cf-access` に戻すこと。

relay は `Cf-Access-Jwt-Assertion` を JWKS
(`https://<CF_TEAM_DOMAIN>/cdn-cgi/access/certs`) で再検証し (`aud`/`exp` 検証)、
Access を素通りした不正接続は 401 で拒否する。

## 3. Asterisk 側の設定 (relay が使う内線)

`pjsip.conf` の例 (内線 101)。**v1.1 から relay は複数の内線 (SIP アカウント) を
同時に扱える**。アカウントはアプリ側の設定 (`sip_account`) から登録するので、
端末ごとに別内線を割り当てる場合は、同じ形の endpoint/auth/aor を内線の数だけ
用意する (102, 103, …)。1 アカウント = 同時 1 通話 (G.711 のみ)。

```ini
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0:5060

[101]
type=endpoint
context=from-internal
disallow=all
allow=ulaw,alaw
direct_media=no
rtp_symmetric=yes
force_rport=yes
rewrite_contact=yes
qualify_frequency=60
auth=101-auth
aors=101

[101-auth]
type=auth
auth_type=userpass
username=101
password=<SIP_PASSWORD と同じ値>

[101]
type=aor
max_contacts=1
qualify_frequency=60
```

- `direct_media=no` (音声は必ず Asterisk 経由。relay が RTP 終端するため必須)。
- `rtp_symmetric=yes` + `rewrite_contact=yes` (NAT/ホスト越え対策)。
- `qualify_frequency=60` (Asterisk 側から relay の生存監視。relay は Expires 300 で
  240 秒ごとに再 REGISTER する)。
- コーデックは ulaw/alaw のみ許可すること (relay v1 は PCMU/PCMA 以外に 488 を返す)。
- relay の `RTP_PORT_MIN/MAX` (既定 20000-20100) は Asterisk の `rtp.conf` (`rtpstart`/`rtpend`, 既定 10000-20000) と
  **重ならない範囲** にすること (relay と Asterisk が同一ホストの場合は必須)。
- relay が複数 NIC を持つホストで動く場合は `LOCAL_IP` を Asterisk から届く IP に明示する (自動検出は Asterisk への経路から推定)。
- 認証は digest MD5 (`auth_type=userpass`) で検証済み。`password_digest`/SHA-256 系は未検証。
- ダイヤルプランは既存のままでよい (例: `102` から `101` への発信で着信テストができる)。

- 内線を追加した場合は、その内線宛のダイヤルプラン (`extensions.conf` の
  `exten => 102,1,Dial(PJSIP/102,25)` など) も用意する。

パスワードの渡し方は 2 通りある (v1.1):

- **アプリで設定 (推奨)**: アプリの設定画面に内線番号とパスワードを入れる。
  relay は受け取ったアカウントを状態ファイル (`STATE_FILE`) に保存し、
  端末が未接続でも REGISTER を維持する。relay の `.env` に `SIP_USER/SIP_PASSWORD`
  は不要。
- **relay の既定アカウント (旧方式・互換用)**: `.env` の `SIP_USER/SIP_PASSWORD`
  に内線を書くと、アカウント未設定の端末はその内線に暫定的に結び付く。
  ここの `password` と `SIP_PASSWORD` は一致させること。

なお、既に **登録済み (registered)** のアカウントに別パスワードで `sip_account` が
来た場合、relay は `error {code:"account_password_mismatch"}` を返して拒否する
(登録済み内線の乗っ取り防止)。Asterisk 側でパスワードを変えると登録が失敗状態に
なるため、その後に新パスワードを送れば切り替わる。

## 4. relay + cloudflared の起動

```sh
cd deploy
mkdir -p data && sudo chown 65532:65532 data   # relay の状態ファイル置き場 (コンテナ内 /var/lib/sipbridge)
cp .env.example .env
# .env を編集: TUNNEL_TOKEN / CF_ACCESS_AUD / SIP_HOST /
# (必要なら LOCAL_IP) を埋める。SIP_USER/SIP_PASSWORD は任意
# (アプリ側でアカウントを設定する場合は空でよい。詳細は手順 3)
docker compose config   # 検証 (エラーが出なければ OK)
docker compose up -d --build
docker compose ps
docker compose logs -f relay
```

- `docker compose config` は `.env` が無くても通る
  (compose の `env_file` は `required: false`)。ただし本番起動には `.env` が必須。
- 状態ファイル (`STATE_FILE`, 既定 `/var/lib/sipbridge/state.json`) に
  アカウント・端末の結び付け・push トークンが保存される (0600)。
  旧 `PUSH_STATE_FILE` (push トークンのみの形式) を指定していた場合は、
  そのファイルを起動時に新形式へ自動移行する (環境変数名も旧名のまま使える)。
- relay のログに `アカウント起動 count=N` が出る。N は起動時に REGISTER を
  始めたアカウント数 (状態ファイルの保存分 + 既定アカウント)。
  アカウントが 0 でも relay は起動する (アプリが `sip_account` を送ると開始する)。
- relay のログに Asterisk への REGISTER 成功が出れば OK
  (Asterisk 側でも `pjsip show contacts` で `101` が `Avail` になる)。
- 更新時: `docker compose up -d --build` (relay は `--build` を忘れずに)。
- 停止時: `docker compose down`。
- systemd の設定は不要 (Docker の `restart: unless-stopped` で自動復旧する)。

疎通の切り分け順: `curl` で /healthz (手順 5) → Asterisk 登録確認 →
アプリ接続 (手順 7)。Asterisk 無しで relay 単体を確認したい場合は
`.env` で `BACKEND=fake` にするとフェイクバックエンドで起動できる。

## 5. 動作確認

relay ホスト上で:

```sh
# ヘルスチェック (認証不要のはず)
curl -i http://127.0.0.1:8080/healthz
# → HTTP/1.1 200
```

Tunnel + Access 経由 (外部 or LAN の別端末から):

```sh
curl -i https://relay.example.com/healthz \
  -H "CF-Access-Client-Id: <Service Token の Client ID>" \
  -H "CF-Access-Client-Secret: <Service Token の Client Secret>"
# → Access 通過で 200。ヘッダ無し/誤りなら 403 (Cloudflare 側で拒否)
```

WebSocket の確認 (開発モード `AUTH_MODE=token` の場合の例):

```sh
# wscat 等で wss://relay.example.com/v1/session に接続し、
# ヘッダ Authorization: Bearer <DEV_TOKEN>、X-Device-Id: <UUID> を付ける。
# 接続直後に {"t":"hello",...} が返れば OK。
```

ヘルスチェック用サブコマンド (compose の `healthcheck` が使用):

```sh
docker exec sipbridge-relay /relay healthcheck && echo OK
```

## 6. Firebase (FCM push 用)

Galaxy S25 (gms flavor・PUSH モード) の起床用。Echo Show 5 (foss flavor・常駐モード) には不要。
トークンの流れ: アプリ `onNewToken` → `BridgeService.setPushToken` → 接続確立後の
`hello` で `register_push {provider:"fcm", token}` → relay が状態ファイル
(`state.json`) に保存 → 着信時に **そのアカウントに結び付いた WS 未接続の端末だけ** に送信。relay 側の送信仕様は `relay/README.md` の「push 送信 (FCM)」を参照。

### 6-1. Firebase プロジェクトと google-services.json

1. [Firebase コンソール](https://console.firebase.google.com/)でプロジェクトを作成する
   (例: `sipbridge`。Google Analytics は不要なので OFF でよい)。
2. プロジェクトの概要 → 「Android アプリを追加」:
   - Android パッケージ名: `net.peyan.sipbridge` (デバッグ版も同じ ID)
   - SHA-1 の登録は不要 (FCM data message のみ使うため)
3. `google-services.json` をダウンロードし、次のどちらかに置く
   (リポジトリに commit しないこと。`.gitignore` 済み):
   - `android/app/google-services.json`、または
   - `android/app/src/gms/google-services.json`
4. ビルドしてインストールする:
   ```sh
   cd android
   ~/android-build/gradle-8.9/bin/gradle :app:assembleGmsDebug --no-daemon
   adb install -r app/build/outputs/apk/gms/debug/app-gms-debug.apk
   ```
   - `google-services.json` が無くても `assembleGmsDebug` は通る
     (plugin を条件適用しているため)。ただしその場合 FCM は動作しない
     (実行時に Firebase が初期化されず、push もトークン発行も起きない)。
   - Echo Show 5 用は `:app:assembleFossDebug` (FCM コードを含まない)。

### 6-2. サービスアカウント (relay → FCM 送信用)

1. Firebase コンソール → プロジェクト設定 → サービスアカウント →
   「新しい秘密鍵の生成」→ JSON をダウンロードする。
2. その JSON を relay ホストの `deploy/data/service-account.json` に置く
   (コンテナ内では `/var/lib/sipbridge/service-account.json` に見える。
   リポジトリに commit しないこと。`.gitignore` 済み):
   ```sh
   cd deploy
   install -m 600 -o 65532 -g 65532 /path/to/downloaded.json data/service-account.json
   ```
   (`65532` は relay イメージの nonroot ユーザー。`docker compose` の
   `data:/var/lib/sipbridge` マウント経由で読む)
3. `.env` に記入する:
   ```ini
   FCM_SERVICE_ACCOUNT_FILE=/var/lib/sipbridge/service-account.json
   FCM_PROJECT_ID=<Firebase プロジェクト ID>
   STATE_FILE=/var/lib/sipbridge/state.json
   ```
   - プロジェクト ID は Firebase コンソールのプロジェクト設定 → 全般 →
     「プロジェクト ID」(プロジェクト名とは別物)。
   - どちらかが空のままでは push は送られない (no-op 扱い)。
4. `docker compose up -d --build` で relay を再起動する。

### 6-3. 動作確認 (S25・PUSH モード)

1. アプリの設定でモードを `PUSH` にし、relay URL と Access 情報を入れる。
2. アプリを起動する (初回に `register_push` が送られる)。relay ホストで確認:
   ```sh
   # {"version":2,"accounts":{...},"devices":{"<deviceId>":{"account":"101","push":{...}}}}
   sudo cat deploy/data/state.json
   ```
3. アプリを強制終了 (タスクキル) した状態で内線から relay 内線 (101) に発信する。
4. 期待動作: 数秒で S25 が起床 → 着信 UI → 応答できる。
   応答後、通話終了 60 秒で切断され「待機中 (PUSH モード)」に戻る。
5. 25 秒以内に応答が無ければ発呼側に `480` が返る。
6. アプリのアンインストール/データ消去後は relay 側のトークンが無効になる。
   次回着信時の FCM 応答 `404/UNREGISTERED` で relay が自動削除する
   (削除後は `state.json` の `devices` から消える。再起動すれば再登録される)。

切り分け:

| 症状 | 確認 |
|---|---|
| `state.json` に push 登録が無い | アプリが起動時に relay へ接続できているか (手順 7 の設定値)、relay ログ |
| 登録はあるが起床しない | `FCM_PROJECT_ID` と `google-services.json` のプロジェクトが一致しているか、端末のネットワーク (FCM は TCP 5228 等を使用)、バッテリー最適化の除外 |
| relay ログに「着信を push できず (登録トークン無し)」 | 全端末のトークンが UNREGISTERED 削除されたか、未登録か。アプリを起動して再登録 |
| relay ログに「push 送信失敗」 | サービスアカウントの配置/`FCM_PROJECT_ID`、FCM API の有効化 (通常は自動で有効) |

## 7. 端末側の設定値一覧

アプリ (設定画面) に入れる値:

| 項目 | 値の例 | 備考 |
|---|---|---|
| relay URL | `wss://relay.example.com/v1/session` | 手順 1 のホスト名 + `/v1/session` |
| Access Client ID | (手順 2 で発行) | 端末ごとに別トークン推奨 (失効単位にできる) |
| Access Client Secret | (手順 2 で発行) | 同上。端末紛失時はこのトークンを失効させる |
| Dev Token | (開発時のみ) | `AUTH_MODE=token` のときだけ使用 |
| SIP 内線番号 | `101` | この端末が使う SIP アカウント (v1.1)。空なら relay の既定アカウント |
| SIP パスワード | (Asterisk の `password`) | 手順 3 の `auth` と一致させる |
| SIP 表示名 | `居間` 等 | 任意。発信時の From 表示名 |
| モード | Echo Show 5: `PERSISTENT` / S25: `PUSH` | PERSISTENT=常時接続、PUSH=着信時だけ接続 |
| マイクゲイン | 既定値のまま | 通話相手の聞こえ方で調整 |
| オーバーレイ / 自動起動 | Echo Show 5 は ON | S25 は OFF でも FCM で起床する |

`X-Device-Id` はアプリが初回起動時に生成・保存する (意識する必要なし)。
アプリ名は「SIP Bridge」。旧 EchoSIP (`com.echosip`) とは applicationId が
異なる (`net.peyan.sipbridge`) ため共存できる。

---

## トラブルシュート

| 症状 | 確認 |
|---|---|
| `docker compose config` で env_file エラー | compose が古い可能性。v2.24+ を使用 (`docker compose version`) |
| relay が Asterisk に REGISTER できない | アプリ側の SIP 内線/パスワード (または `.env` の `SIP_USER/SIP_PASSWORD`) と手順 3 の `password` の一致、`.env` の `SIP_HOST/SIP_PORT`、`LOCAL_IP` の要否、Asterisk の `pjsip show contacts` |
| Access で 403 | Client ID/Secret の転記ミス、ポリシーが Service Auth になっているか、AUD タグとホスト名の一致 |
| 着信が鳴らない (PUSH) | 手順 6 のサービスアカウント配置、`FCM_PROJECT_ID`、アプリの `register_push` 到達 (relay ログ) |
| 音声が片道/無音 | `direct_media=no`、`rtp_symmetric=yes`、`RTP_PORT_MIN/MAX` がホストで空いているか、Asterisk の `rtp.conf` との重複 |
| WS が頻繁に切れる | 20 秒 ping に対する Cloudflare のアイドル切断は想定内。アプリの指数バックオフ再接続と `hello` での状態同期で復帰する |
