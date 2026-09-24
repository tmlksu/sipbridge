# sipbridge relay ⇄ app プロトコル v1 (v1.1 追記: SIP アカウント / hello.account)

1 端末 = 1 WebSocket 接続。URL: `wss://<host>/v1/session` (例 `wss://relay.example.com/v1/session`)。

## 接続とヘッダ

| ヘッダ | 値 |
|---|---|
| `CF-Access-Client-Id` / `CF-Access-Client-Secret` | Cloudflare Access Service Token (本番) |
| `Authorization: Bearer <token>` | 開発用共有トークン (relay が `AUTH_MODE=token` のときのみ) |
| `X-Device-Id` | 端末識別子 (UUID, 端末ごとに固定) |
| `X-Client-Version` | アプリ名/バージョン |

relay は `AUTH_MODE=cf-access` のとき `Cf-Access-Jwt-Assertion` を JWKS (`https://<team>.cloudflareaccess.com/cdn-cgi/access/certs`) で検証し、`aud` が `CF_ACCESS_AUD` と一致しなければ HTTP 401 で拒否する。

Keep-alive: 双方 WS ping/pong を 20 秒周期。40 秒無応答で切断。切断後アプリは 1,2,4,…30 秒の指数バックオフで再接続。

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
- **パスワード規則**: account が現在 `registered=true` で、送られた `password` が保存値と異なる場合は
  `error {code:"account_password_mismatch"}` を返し結び付けを変更しない (登録済み内線の乗っ取り・DoS 防止)。
  未登録/登録失敗中/新規なら受理して (再) 登録する。Asterisk 側でパスワードを変えた後は
  登録が失敗状態になるので、新パスワードを送れば切り替わる。
- 既定アカウント: `SIP_USER` が設定されていれば、結び付けの無い端末は接続時にそれへ暫定的に結び付ける
  (永続化しない)。旧アプリ (sip_account を送らない) との互換用。
- `account=""` (結び付け無し・既定も無し) の端末が `answer/reject/hangup/dial` を送ると
  `error {code:"no_account"}`。
- push: `incoming` の FCM 送信先は **その account に結び付いたオフライン端末** のみ。
- 状態ファイル (`STATE_FILE`, 旧名 `PUSH_STATE_FILE` も可, 0600):
  `{"version":2,"accounts":{"<user>":{"password":"…","display":"…"}},"devices":{"<deviceId>":{"account":"<user>","push":{"provider":"fcm","token":"…"}}}}`。
  旧形式 (`{"<deviceId>":{"provider","token"}}`) は起動時に自動移行する。

エラーコード一覧 (`error.code`): `bad_message`, `answer_failed`, `reject_failed`, `hangup_failed`,
`dial_failed`, `not_supported`, `store_failed`, `no_account`, `account_password_mismatch`, `account_failed`。

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
