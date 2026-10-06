# sipbridge relay ⇄ app プロトコル v1 (v1.1 追記: SIP アカウント / hello.account、v1.2 追記: call_stats)

1 端末 = 1 WebSocket 接続。URL: `wss://<host>/v1/session` (例 `wss://relay.example.com/v1/session`)。

## 接続とヘッダ

| ヘッダ | 値 |
|---|---|
| `CF-Access-Client-Id` / `CF-Access-Client-Secret` | Cloudflare Access Service Token (本番) |
| `Authorization: Bearer <token>` | 開発用共有トークン (relay が `AUTH_MODE=token` のときのみ) |
| `X-Device-Id` | 端末識別子 (UUID, 端末ごとに固定)。`^[A-Za-z0-9._:-]{1,64}$` に限る |
| `X-Client-Version` | アプリ名/バージョン |

relay は `AUTH_MODE=cf-access` のとき `Cf-Access-Jwt-Assertion` を JWKS (`https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`) で検証し、
署名 (RS256 のみ)・`aud` (`CF_ACCESS_AUD`)・`iss` (`https://<team>`)・`exp` (必須) のいずれかが不正なら HTTP 401 で拒否する
(応答本文は `unauthorized` のみ)。

WS へ昇格する前に次の場合も拒否する (docs/SECURITY.md):

| HTTP | 条件 |
|---|---|
| 400 | `X-Device-Id` が無い、または `^[A-Za-z0-9._:-]{1,64}$` に合わない |
| 401 | (上記の JWT 検証失敗に加え) `DEVICE_BINDING=enforce` で JWT に認証主体 (`sub` / `common_name`) が無い |
| 409 | `DEVICE_BINDING=enforce` で、その端末 ID に記録された認証主体と異なる。本文は `device_binding_mismatch` |
| 429 | その端末 ID の同時接続数 (受け入れ途中を含む) が上限 (4)。古い接続が閉じれば次の再接続で通る |
| 503 | 同時接続の端末数が上限 (`MAX_ONLINE_DEVICES`)。接続中・保存済みの端末の再接続は対象外 |

認証主体 (principal) は `sub:<ユーザー ID>` (IdP ログイン) または `cn:<Client ID>` (Service Token) の形で扱う。
403 は Cloudflare Access 自身が返す (Service Token の誤り等) ため relay は使わない。

アプリ側の扱い (現行): 401/403 は認証失敗として表示し、PUSH モードでは再接続をやめて次の起床を待つ。
400・409・503 は通常の接続失敗として 1,2,4,…30 秒のバックオフで再接続を続ける (専用の表示は無い)。
400 と 409 は設定・記録を直さない限り続く (409 の解除手順は docs/SECURITY.md §5)。

Keep-alive: 双方 WS ping/pong を 20 秒周期 (relay 側は `WS_PING_INTERVAL` で変更可、既定 20s)。40 秒無応答で切断。切断後アプリは 1,2,4,…30 秒の指数バックオフで再接続。

## フレーム

- **テキストフレーム** = JSON 制御メッセージ。必ず `"t"` (type) を持つ。
- **バイナリフレーム** = RTP パケットそのまま (12 バイト RTP ヘッダ + G.711 ペイロード)。通話中のみ。relay は方向ごとにアドレス付け替えだけを行い中身を変更しない (SSRC/seq/ts はアプリが生成)。

## 制御メッセージ: relay → app

| t | フィールド | 意味 |
|---|---|---|
| `hello` | `relayVersion`, `extension`, `account` (v1.1), `registered` (bool), `call` (下記 CallInfo か null), `serverTime` | 接続直後、および `sip_account` 受理後に送る。`account` は結び付いている SIP user (`extension` と同値。未設定なら `""`)。保留中の着信があれば `call` に入る |
| `registration` | `ok` (bool), `detail` | Asterisk への REGISTER 状態変化 |
| `incoming` | `callId`, `from` (番号), `display` (表示名), `pt` (0 or 8) | 着信。relay は既に 180 を返している |
| `ringing` | `callId`, `early` (bool: 183 でメディアあり) | 発信中 (v1.1) |
| `answered` | `callId`, `pt` | 発信が 200 OK (v1.1) / 着信を `answer` した確認 |
| `ended` | `callId`, `reason` (`bye`/`cancel`/`reject`/`timeout`/`error`), `code` (SIP status) | 通話終了 |
| `error` | `code` (文字列), `message` | プロトコルエラー等。致命的なら relay が close する |
| `pong` | `ts` | `ping` への応答 |

CallInfo = `{callId, direction: "in"|"out", state: "ringing"|"active", from, display, pt, startedAt}`

## 制御メッセージ: app → relay

| t | フィールド | 意味 |
|---|---|---|
| `answer` | `callId`, `pt` (省略時は `incoming.pt`) | 着信応答 → relay が 200 OK (SDP: relay の RTP ソケット) を送る。以後バイナリフレームが流れる |
| `reject` | `callId`, `code` (省略時 486) | 着信拒否 |
| `hangup` | `callId` | BYE (通話中) / CANCEL (発信中) |
| `dial` | `to` | 発信 (v1.1)。relay が INVITE。`callId` は `ringing`/`answered` で返る |
| `dtmf` | `callId`, `digits` | RFC 2833 送出 (v2)。**v1.1 のアプリは in-band (G.711 音声にトーン合成) で送るため relay には送らない** (Asterisk 側 `dtmfmode=inband`) |
| `register_push` | `provider` (`fcm`), `token` | push トークン登録。relay が永続化 |
| `sip_account` | `user`, `password`, `display` (任意) | **v1.1** この端末が使う SIP アカウント (内線) を relay に登録する (下記「SIP アカウント」節)。`user=""` で解除 |
| `ping` | `ts` | アプリ側 keep-alive (WS ping が使えないクライアント向け) |
| `call_stats` | `callId`, `dur` (ms), `net`, `rx`, `jb`, `playUnderrun`, `tx`, `rttMs` | **v1.2** 通話終了時に 1 回送る品質統計 (下記「通話品質統計」節)。relay はログに出すだけで応答しない |

## 通話品質統計 (v1.2)

設計: `docs/QUALITY_STATS.md`。アプリは通話終了時に 1 回だけ送る (待機中・通話中の定期送信はしない)。

```json
{"t":"call_stats","callId":"…","dur":123456,"net":"wifi|cellular|other",
 "rx":{"pkts":0,"gaps":0,"reorder":0,"jitterMs":0,"maxGapMs":0,"stall100":0,"stall200":0,"stall500":0,"reconnects":0},
 "jb":{"underrun":0,"overflow":0},"playUnderrun":0,
 "tx":{"pkts":0,"drop":0,"lost":0,"lateMs":0},"rttMs":[80,-1]}
```

- アプリは `hello.relayVersion` が `0.3.0` 以上のときだけ送る (旧 relay ≤0.2.0 は未知の `t` に `error bad_message` を返す。切断はしない)。
- relay は `callId` 以外を解釈せず、`t`/`callId` を除いた JSON をそのままログに埋め込む (項目の追加・型のずれでもエラーにしない)。
  4096 バイトを超えるメッセージは捨てる。**応答は返さない** (不明・旧 `callId` でも `error` を返さない)。
- relay は 1 通話 1 行 `call_stats` をログに出す: アプリの `call_stats` を受けた時点 (通話終了の処理より先に届いた場合は終了時)、
  または通話終了から 5 秒以内に届かなければ relay 側の計測だけで出す。二重には出さない。
  5 秒を過ぎて届いたもの・記録の無い `callId` は `call_stats_orphan` として別の行に出す。
- relay 側の計測 (ログのキー): `up` = app→relay 上り RTP の `pkts/gaps/reorder/jitterMs/maxGapMs/stall100/200/500`、
  `ast` = Asterisk→relay RTP の `pkts/gaps/reorder/jitterMs/qdrop` (qdrop は relay 内受信キュー溢れ)、
  `txDrop` = relay→app 下りの送信キュー満による破棄数。`dur` は relay が見たメディア開始から終了までの ms。
- ログに番号・表示名などの PII は出さない (`callId`, `account`=内線, `dev`=デバイス ID のみ)。

## SIP アカウント (v1.1: 接続情報をアプリ側で設定する)

relay は複数の SIP アカウント (内線) を同時に扱う。**アカウントは端末 (アプリ) から `sip_account` で登録**し、
relay 側の環境変数 `SIP_USER/SIP_PASSWORD` は互換用の「既定アカウント」に格下げする (無くてもよい)。

- 端末は接続ごとに、最初の `hello` を受けたら `sip_account {user,password,display}` を送る
  (設定済みの場合)。relay は `X-Device-Id` → account の結び付けと account の資格情報を
  状態ファイルに永続化し、受理後に改めて `hello` を送る (`account`/`extension`/`registered`/`call`
  はその account のもの)。
- relay は起動時に永続化済みの全 account の REGISTER を開始し、端末が未接続 (PUSH モード) でも
  登録を維持する。account に結び付く端末が 0 になったら登録を止めて account を削除する。
- 同じ `user` を複数端末が送った場合は同一 account を共有し、着信は全端末にファンアウトする
  (最初の `answer` が勝つ)。1 account = 同時 1 通話。
- **パスワード規則**: 送られた `password` が保存値と異なる場合は `error {code:"account_password_mismatch"}` を
  返し結び付けを変更しない。例外は **その account に既に結び付いた端末** が、account が未登録/登録失敗中の
  ときに送った場合で、パスワード変更として受理して再登録する (Asterisk 側でパスワードを変えた後は登録が
  失敗状態になるので、新パスワードを送れば切り替わる)。結び付いていない端末 (既定アカウントへの暫定参加を
  含む) は、登録状態に関わらず保存値との一致が必要 (未登録中の乗っ取り防止)。新規 account は受理して登録する。
- **仮の資格情報**: 新規 account とパスワード変更の資格情報は、REGISTER に一度成功するまで「仮」とする。
  仮のまま SIP サーバに認証で拒否 (401/403/407) されることが 3 回以上、かつ最初の拒否から 2 分以上続くと
  (再 REGISTER は 5, 10, 20, 40, 60 秒…のバックオフなので、実際には最初の拒否から約 2 分 15 秒後)、
  新規 account は削除し (端末の結び付けも外れる)、パスワード変更は変更前の資格情報に戻す。どちらも所属端末に
  `error {code:"account_failed"}` を送る。その間に一度でも REGISTER に成功すれば検証済みになる。
  タイムアウト等 (SIP サーバに届かない) は数えない。
- **試行制限**: `account_password_mismatch` と `rate_limited` の応答は約 1 秒遅れる。新規 account の作成と
  パスワード変更は受理しても 1 回の試行として同じく遅れて数えられる。1 接続でこれらが 3 回になると、relay は
  WS を 1008 (policy violation) で閉じる (3 回目が失敗なら `error` の後に閉じる)。保存値と一致しない
  `sip_account` (結び付いた端末のパスワード変更を含む)、結び付いていない端末からの既存 account への
  `sip_account`、新規 account の作成は relay 全体で回数を制限し、超えると `rate_limited`
  (結び付いた端末が同じパスワードを再送するのは制限しない)。account 数が上限 (`MAX_ACCOUNTS`) なら新規作成は
  `too_many_accounts`、状態ファイルに保存する端末数が上限 (`MAX_STORED_DEVICES`) なら新しい端末の
  `sip_account` / `register_push` は `too_many_devices`。
- **register_push の制限**: 状態ファイルに無い端末の `register_push` は上の全体の回数制限を受ける
  (`rate_limited`)。保存済み端末のトークン変更は 10 秒に 1 回まで (`rate_limited`)。同じトークンの再送は
  制限せず、状態ファイルにも書かない。保存数が上限のときは、既定アカウント (`SIP_USER`) の無い構成に限り、
  account に結び付いていない未接続の端末のうち最も古いものを削除して空きを作る。
- 既定アカウント: `SIP_USER` が設定されていれば、結び付けの無い端末は接続時にそれへ暫定的に結び付ける
  (永続化しない)。旧アプリ (sip_account を送らない) との互換用。
- `account=""` (結び付け無し・既定も無し) の端末が `answer/reject/hangup/dial` を送ると
  `error {code:"no_account"}`。
- push: `incoming` の FCM 送信先は **その account に結び付いたオフライン端末** のみ。
- 状態ファイル (`STATE_FILE`, 旧名 `PUSH_STATE_FILE` も可, 0600):
  `{"version":2,"accounts":{"<user>":{"password":"…","display":"…"}},"devices":{"<deviceId>":{"account":"<user>","push":{"provider":"fcm","token":"…"},"principal":"cn:…","created":1790000000}}}`。
  `principal` は DEVICE_BINDING の記録、`created` は端末の記録を作った時刻 (Unix 秒)。
  旧形式 (`{"<deviceId>":{"provider","token"}}`) は起動時に自動移行する。

エラーコード一覧 (`error.code`): `bad_message`, `answer_failed`, `reject_failed`, `hangup_failed`,
`dial_failed`, `not_supported`, `store_failed`, `no_account`, `account_password_mismatch`, `account_failed`,
`rate_limited`, `too_many_accounts`, `too_many_devices`。

## 状態遷移 (relay 側, 単一通話 / account ごと)

```
IDLE --INVITE--> RINGING_IN --answer--> ACTIVE --BYE/hangup--> IDLE
                 |--reject/timeout(25s)/CANCEL--> IDLE
IDLE --dial----> RINGING_OUT --200--> ACTIVE
                 |--hangup(CANCEL)/4xx-6xx--> IDLE
```

- RINGING_IN で WS 未接続の登録済みデバイスがあれば、そのデバイスへ FCM push (data: `{type:"incoming", callId, caller, display}`) を送り (接続中デバイスには送らない。ただし WS ping に 3 秒応答しない接続は切断した上で送る)、25 秒以内に接続+`answer` が無ければ `480 Temporarily Unavailable`。
- 複数セッション接続時: `incoming` を全員に送る。最初の `answer` が勝ち、他には `ended {reason:"answered_elsewhere"}`。メディアは勝者のみ。
- 通話中に WS が切れたら relay は既定 30 秒 (`RESUME_TIMEOUT_SEC`) 待ち、同じ `X-Device-Id` の再接続で `hello.call.state=="active"` として継続 (メディア再開)。猶予超過で BYE。
  アプリ側は通話中のみ再接続バックオフを 2 秒上限にクランプするため、この猶予内に戻れる。

## RTP フレーム

- ペイロードタイプ 0 (PCMU) / 8 (PCMA)、8 kHz、ptime 20 ms、160 バイト。
- relay → app: Asterisk から受けた RTP をそのまま。app 側ジッタバッファ 5 フレーム。
- app → relay: アプリ生成 RTP。relay は宛先 (SDP で得た Asterisk の IP:port) に UDP 送信。
- RTCP は v1 では扱わない (relay は捨てる)。

## 予約

- `enc`: 将来の端末⇄relay PSK 暗号化 (AES-256-GCM, フレーム単位)。v1 は未使用。
- `codec`: `opus` を v2 で追加。
