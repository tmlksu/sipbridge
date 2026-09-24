# 接続上のリスク一覧

作成: 2026-09-24 / 対象: relay 0.2.x + Android アプリ v1.5.2 (commit 1157dc7 時点)

relay (Go) と Android アプリのコードを読み、**着信が鳴らない・通話が落ちる・音声が片道/遅延する・
再接続できない** につながる箇所を洗い出した。設計上の前提 (`DESIGN.md` §8) と、実機で既に踏んだ
不具合 (`docs/E2E.md` の記録) も含める。
GitHub issues #1〜#11 (2026-09-23 に別エージェントが起票) もコードと突き合わせて評価し、§4 にまとめた。

- 重大度: **高** = 着信取りこぼし / 通話断が普通の使い方で起きうる。**中** = 条件付きで起きる。**低** = 稀・影響小。
- 確度: **確認** = コードで挙動を確認済み。**推定** = コードからの推測で、実機での再現はまだ。
- 行番号は作成時点のもの。

## 対応状況 (2026-09-24, relay 0.2.x / アプリ v1.5.3)

PR #12〜#17 で対応したもの (詳細は各 PR):

| リスク | 対応 | PR |
|---|---|---|
| R1 網切替 | デフォルト網の `NetworkCallback` で即張り直し、通話中のバックオフ上限 2 秒、resume 猶予 30 秒 (`RESUME_TIMEOUT_SEC`) | #12 #13 |
| R3 残った resume タイマ | 呼の終了でタイマを止め、張った時点の callId にだけ効かせる (回帰テスト付き) | #12 |
| R7 (一部) | イベントバッファ 128、取りこぼしを WARN。**`answer` と勝者設定の競合は未対応** | #15 |
| R11 (一部) | relay 側の RTP 破棄を WARN で可視化。**アプリ側の送信背圧・ジッタバッファは未対応** | #16 |
| R12 WakeLock | 非参照カウント + 50 分ごとに取り直し | #17 |
| R14 JWKS | 取得をロック外・1 本化、失敗時は期限切れ鍵を 30 分まで使う。**未知 kid での即再取得・leeway は未対応** | #16 |
| R16 (一部) | FCM 送信を並列化。**429/5xx の再試行は未対応** | #16 |
| R18 | 推測時に WARN/ERROR、リンクローカル除外、`LOCAL_IP` の形式検査 | #16 |
| R21 制御メッセージ | 送信失敗を 10 秒まで保持し再接続後に再送 | #14 |
| R22 逐次書き込み | 接続ごとの送信キュー。溢れたら RTP は捨て、制御なら接続を閉じる (非同期) | #15 |
| R23 HTTP タイムアウト | `ReadHeaderTimeout` / `IdleTimeout` | #15 |

**未対応 (次の作業)**: R2 (半死に WS が push を止める)、R5 (発信の取り残し)、R6 (SIP イベントの順序逆転)、
R4 (push 到達時間の実測)、R7 の応答競合、R8、R9、R10、R13、R15、R17、R19、R20。

## 0. 経路と弱点の位置

```
[App] ─①WSS─▶ Cloudflare Edge ─②Access─▶ cloudflared ─③─▶ relay ─④SIP/UDP─▶ Asterisk
  ▲                                                       │  └─⑤RTP/UDP─▶
  └──────────────⑥FCM (PUSH モード)◀────────────────────────┘
```

| 区間 | 主なリスク |
|---|---|
| ① 端末 ⇄ Edge | 網切替の検知が遅い、半死にソケット、TCP 上の RTP の遅延累積 |
| ② Access | トークン失効・302 リダイレクトが「認証失敗」として見えない |
| ③ relay の WS | 同一端末の重複接続、resume タイマ、イベント順序 |
| ④ relay ⇄ Asterisk | REGISTER 期限と再登録間隔、Asterisk 再起動の検知遅れ、UDP のみ |
| ⑤ RTP | 対称 RTP ではない、`LOCAL_IP` の自動検出 |
| ⑥ push | 25 秒以内に起床・接続・応答まで終わる必要がある、送信の再試行が無い |

## 1. 一覧 (重大度順)

| # | 重大度 | 確度 | 区間 | 概要 |
|---|---|---|---|---|
| R1 | 高 | 確認 | ①③ | 網切替時に 10 秒の resume 猶予内に再接続できず、通話が切れる |
| R2 | 高 | 確認 | ③⑥ | 半死にの WS が「オンライン」扱いになり、その端末へ FCM が飛ばない |
| R3 | 高 | 確認 | ③ | resume タイマが通話終了後も残り、次の着信を 480 で落とす |
| R4 | 高 | 推定 | ⑥ | push → 起床 → WSS → 応答が 25 秒に収まらないと 480 |
| R5 | 中 | 確認 | App | `hello` 前に `dial` を送り、`no_account` 等のエラーで発信 UI が永久に「呼出中」 |
| R6 | 中 | 確認 | ④ | SIP イベントを goroutine で個別送信しており順序が保証されない (幻の着信) |
| R7 | 中 | 確認 | ③ | `answer` と勝者設定の競合で、他端末に `answered_elsewhere` が届かない |
| R8 | 中 | 推定 | ⑤ | relay は対称 RTP ではなく SDP の宛先にだけ送る (NAT 越しで片道音声) |
| R9 | 中 | 確認 | ④ | 再登録が固定 240 秒。サーバが短い Expires を返すと登録が切れる |
| R10 | 中 | 確認 | ④ | Asterisk 再起動を最大 240 秒検知できない (OPTIONS による監視が無い) |
| R11 | 中 | 確認 | ① | 送信の背圧・古い RTP の破棄が無く、網が詰まると音声遅延が溜まる |
| R12 | 中 | 推定 | App | 12 時間で WakeLock が切れ、PERSISTENT 端末の再接続が止まる恐れ |
| R13 | 中 | 推定 | App | Android 14+ でバックグラウンドから microphone 型 FGS を開始して失敗する |
| R14 | 中 | 確認 | ② | JWKS 取得失敗や鍵ローテーションの直後に、全接続が 401 になる |
| R15 | 低 | 確認 | App | 認証失敗 (401/403) でも最大 30 秒間隔で永久に再試行する |
| R16 | 低 | 確認 | ⑥ | FCM が 429/5xx でも再試行しない。複数トークンで 10 秒を共有する |
| R17 | 低 | 確認 | ④ | SIP は UDP のみ。UPDATE / session timer は 405 を返す |
| R18 | 低 | 確認 | ⑤ | `LOCAL_IP` の自動検出が起動時の 1 回だけで、docker0 や VPN を拾いうる |
| R19 | 低 | 確認 | ③ | healthcheck が SIP 未登録でも OK を返す |
| R20 | 低 | 確認 | 全体 | Cloudflare が TLS を終端するため、音声が Cloudflare に見える |
| R21 | 中 | 確認 | App | 切断中に押した応答/拒否/終了を再送せず、黙って失う (#3) |
| R22 | 中 | 確認 | ③ | relay の全端末向け書き込みが逐次 (1 接続 10 秒)。遅い端末 1 台が他端末への制御イベントを遅らせる (#4) |
| R23 | 低 | 確認 | ③ | `http.Server` にタイムアウトが無い。ただし待ち受けは 127.0.0.1 のみ (#8) |

## 2. 詳細

### R1 網切替で通話が切れる (高)

- アプリは `ConnectivityManager.NetworkCallback` を使っていない。網の変化は OkHttp の WS ping
  (`RelayClient.kt:49`, 20 秒) の失敗でしか検知できず、そのあとにバックオフ (1 秒〜) が入る。
- relay 側の検知も ping 20 秒 + タイムアウト 10 秒 (`session.go:32,34`) で、最大約 30 秒かかる。
- resume の猶予は 10 秒 (`PROTOCOL.md` §状態遷移)。**Wi-Fi ⇄ モバイルの切替では、ほぼ猶予を過ぎて BYE になる**。
- 対策案: `NetworkCallback.onAvailable` / `onLost` を受けたら即 `connectFresh()` する。
  通話中は WS を張り替えながら古いソケットも猶予内は生かす。resume 猶予を 20〜30 秒に延ばすことも検討する。

### R2 半死に WS が push を止める (高)

- 同じ `X-Device-Id` の重複接続を許しており、古い接続を追い出さない (`session.go:318-327`)。
- push は「オンラインでない端末」にだけ送る (`session.go:233-238`)。
  スリープで死んだのに relay が未検知 (最大約 30 秒) の WS があると、**その端末には push が飛ばない**。
- PUSH 端末では通話終了から 60 秒後に切断するが、網が切れてそれが relay に届かない場合も同じことが起きる。
- 重複接続中は下り RTP が両方に流れ、上りも両方から受け付ける (二重送信)。
- 対策案: 同じ device の新しい接続が来たら古い接続を閉じる。push 判定では
  「最後に pong を受けてから N 秒以内」の接続だけをオンラインとみなす。
  あるいはオンラインでも PUSH モードの端末には push を送る (アプリ側で重複を捨てる)。

### R3 残った resume タイマが次の着信を落とす (高)

- `armResumeLocked` (`group.go:201-218`) のタイマは、`EvEnded` (`group.go:266-271`) で止められない。
  `winnerDevice` は `""` に戻る。
- 発火すると `g.devs[""]` が見つからないので `mgr.HangupTimeout()` が呼ばれ、
  **その時点の別の呼** (RINGING_IN なら 480) を切る。
- 再現条件: 勝者の WS が切れる → 相手が 10 秒以内に切る → 残りの猶予内に次の着信が来る。
- 対策案: `EvEnded` で `resumeTimer.Stop()` する。タイマに callId を持たせ、一致するときだけ切る。

### R4 push 経由の応答が 25 秒に収まらない (高・推定)

- relay は INVITE を受けてから 25 秒で 480 を返す (`call.go:28`)。Asterisk 側の `Dial(...,25)` とも競合する。
- この 25 秒の中に、FCM の配送 (Doze 中は遅延しうる)、FGS の起動、DNS/TLS/Access、WS の upgrade、`hello`、
  着信 UI、人間の応答が全部入る。実測では P780 で push から着信 UI まで約 1.6 秒 (`E2E.md` 2026-09-16)。
  ただしモバイル回線、Doze 中、S25 の電池最適化の下では未計測。
- アプリの connect timeout は 15 秒 (`RelayClient.kt:50`) で、一度失敗すると残り時間ではまず間に合わない。
- 対策案: S25 実機でモバイル回線・画面 OFF 長時間のあとの到達時間を計測する。
  relay のタイムアウトを設定値にして 30〜40 秒へ延ばす (Asterisk の Dial タイムアウトも合わせる)。

### R5 `hello` 前の `dial` とエラー時の取り残し (中)

- `dialViaRelay` は `isConnected()` が true ならすぐ `dial` を送る (`BridgeService.kt:1132`)。
  この値は `onOpen` で立つので、最初の `hello` と `sip_account` より前に送ることがある。
- relay が `error {no_account}` や `dial_failed` を返しても、`onError` は状態文言を更新するだけで
  (`BridgeService.kt:580-592` 付近) 通話状態を戻さない。
  v1.5.2 の修正 (callId が空の発信は畳まない) と重なり、**発信 UI が「呼出中」のまま残る**。
- 対策案: 発信は必ず `hello` (+ `sip_account` 受理) の後に送る。`dial_failed` / `no_account` を
  受けたら発信状態を終了させる。発信にウォッチドッグ (例: 30 秒で callId が付かなければ終了) を付ける。

### R6 SIP イベントの順序逆転 (中)

- `Backend.emit` はイベントごとに goroutine を起こして送る (`backend.go:255-276`)。
  速い CANCEL の `EvEnded` が `EvIncoming` を追い越すと、Manager は Ended を無視してから RINGING_IN に入る。
  その結果、**25 秒間の幻の着信**と、その間の別着信への 486 が起きる。
- 対策案: Backend ごとに単一の送信キュー (goroutine 1 本 + バッファ付きチャネル) にして、順序を保つ。

### R7 応答の競合で他端末が鳴り続ける表示になる (中)

- `Manager.Answer` はロック中に `EvAnswered` を出す (`call.go:280`)。
  `setWinner` は `Answer` から戻ったあと (`session.go:576-580`) なので、pump が先に `onAnswered` を処理すると
  winner が空のまま全端末に `answered` を配り、`answered_elsewhere` を送らない。
- Manager のイベントバッファ (32) が溢れたときも黙って捨てる (`call.go:135,152-159`)。
  pump は WS 書き込みを同期で行う (1 接続 10 秒) ため、詰まった端末が 1 台あると `ended` 等を失いうる。
- 対策案: `setWinner` を `Answer` を呼ぶ前 (ロック内) で行う。broadcast を端末ごとの非同期キューにする。

### R8 対称 RTP ではない (中・推定)

- relay は受信した RTP の送信元を見ず、SDP にある宛先にだけ送る (`rtp.go:60-67,89`)。
- relay と Asterisk が同じホスト / LAN なら問題ない (現行構成)。
  Asterisk が NAT の向こうにいる構成や、SDP に内部アドレスを書く機器 (HGW 等) では**片道音声**になる。
- 対策案: 最初に届いた RTP の送信元を学習して、以後そこへ送る (Asterisk の `rtp_symmetric` 相当)。

### R9 / R10 REGISTER の期限と Asterisk 再起動 (中)

- Expires 300 で要求し、**サーバが返した Expires に関係なく 240 秒ごと**に再登録する (`backend.go:36-37,321`)。
  短い Expires を返す registrar では登録が切れる。423 / Min-Expires も処理しない。
- Asterisk の再起動やレジストラ喪失は次の再登録まで (最大 240 秒) 検知できず、その間の着信は届かない。
  relay から Asterisk への OPTIONS による監視は無い。
- relay の SIP ソケットはアカウントごとのエフェメラルポートで、relay を再起動するたびに変わる。
  Asterisk 側に古い Contact が残らないよう `remove_existing=yes` (pjsip) / 1 contact 前提にしておく。
- 対策案: 200 OK の Expires (Contact の expires) を読み、その 80% で再登録する。
  60 秒周期で OPTIONS を送り、失敗したら即再登録する。

### R11 TCP 上の RTP で遅延が累積する (中)

- 設計どおり RTP を WS (TCP) に載せているため、パケットロスは再送と HoL ブロッキングになる (`DESIGN.md` §6)。
- アプリ: `send()` の戻り値を見ず、古いフレームを捨てる仕組みも無い (`RtpEngine.kt:29,227`)。
  網が詰まると OkHttp のキュー (上限 16 MiB) にたまり、復旧時にまとめて届く。
- アプリのジッタバッファはシーケンス番号で並べ替えず到着順に再生する。上限 20 フレーム (400 ms) まで遅延が伸びる。
- relay: 下り RTP のキューは 50 パケット (約 1 秒) で、溢れたら古い方を捨てる (`rtp.go:14,101-114`)。
  WS への書き込みは同期 (`group.go:364-373`)。
- 対策案: アプリで送信キューの長さ (`WebSocket.queueSize()`) を見て、閾値 (例: 5 フレーム) を超えたら捨てる。
  ジッタバッファを seq 順にし、溜まりすぎたら先頭を捨てて追いつく。

### R12 WakeLock の 12 時間期限 (中・推定)

- `PARTIAL_WAKE_LOCK` を `acquire(12h)` で一度取るだけで、取り直さない (`BridgeService.kt:161`)。
  再接続は `Handler.postDelayed` (uptime 基準) なので、ロックが切れて deep sleep に入ると再接続が止まる恐れがある。
- Echo Show 5 は deep sleep 無効 (`echoshow5-device-notes.md`) なので実害は出にくい。
  問題になるのは PERSISTENT モードのスマホ。
- PUSH モードでは待機中 (未接続) もロックを持ち続け、電池を無駄に使う。
- `WIFI_MODE_FULL_HIGH_PERF` は API 34 で非推奨。
- 対策案: 接続中だけロックを持つ。期限付きで定期的に取り直す。再接続を `AlarmManager` 併用にする。

### R13 FGS 起動制限 (中・推定)

- FCM からの `startForegroundService` → `startForeground` は `runCatching` の中で失敗しても握りつぶす (`BridgeService.kt:144-152`)。
  Android 14+ でバックグラウンドから `microphone` 型を始めると SecurityException になりえ、
  続いて `ForegroundServiceDidNotStartInTimeException` でクラッシュする恐れがある。
- `HealthCheckReceiver` (7 日ごとの再登録) は inexact alarm からの FGS 起動になるため、
  電池最適化から除外されていないと Android 12+ で黙って失敗しうる。
- `BootReceiver` は `LOCKED_BOOT_COMPLETED` を受けるが、`directBootAware` でないため届かない (実害は小さい)。
- 対策案: 着信待ちの段階では `phoneCall` 型だけで `startForeground(id, n, type)` を明示し、
  `microphone` 型は応答時に付け足す。S25 (API 36) で push 起床のログを確認する。

### R14 Access の JWKS (中)

- JWKS のキャッシュは 10 分。TTL を過ぎて再取得に失敗すると古い鍵を捨て、**全ての WS upgrade が 401** になる (`auth.go:152-165`)。
  Cloudflare 側や VPS の DNS の一時障害が、そのまま全端末の接続不能になる。
- 未知の kid (鍵ローテーション) でも TTL 切れまで再取得しない (`auth.go:136-139`)。
- exp/nbf にクロックスキューの許容が無い。VPS の時刻がずれると通らない。
- 認証は upgrade 時だけなので、確立済みのセッションは JWT の期限後も生きる (これは運用上は好都合)。
- 対策案: 取得失敗時は古い鍵を使い続ける (stale-while-error)。未知の kid なら即再取得する (レート制限付き)。leeway は 30〜60 秒。

### R15〜R20 (低)

- **R15** 401/403 でも最大 30 秒間隔で再試行を続ける (`RelayClient.kt:177-179`)。Access が 302 でログイン画面へ
  飛ばした場合は OkHttp がリダイレクトを追い、「認証失敗」として表示されない (推定)。
  `onOpen` でバックオフを 1 秒に戻すため、upgrade 直後に閉じられると約 1 秒ごとの再接続ループになる。
- **R16** FCM は 429/5xx/ネットワークエラーでも再試行しない。複数トークンを 1 つの 10 秒 context で順に送る (`fcm.go:162-182`)。
  削除するのは UNREGISTERED だけ。`FCM_PROJECT_ID` があって鍵ファイルが無いと起動時に終了し、
  `restart: unless-stopped` で再起動を繰り返す。
- **R17** SIP は UDP のみで、MTU を超える INVITE は IP フラグメント頼み。UPDATE/INFO は 405。
  Asterisk が session timer を UPDATE で更新する設定だと通話が切れる恐れがある。
  保留の re-INVITE (sendonly / `c=0.0.0.0`) にも `sendrecv` で答える。
- **R18** `LOCAL_IP` の自動検出は、`SIP_HOST` への UDP dial が失敗すると最初の非 loopback IPv4 を使う。
  docker0 や VPN の IF を拾いうる。検出はバックエンド起動時の 1 回だけ。
  **本番では `LOCAL_IP` を明示する**のが安全。
- **R19** `/healthz` は SIP が未登録でも 200 を返す。Docker の healthcheck では登録喪失を検知できない。
- **R20** Cloudflare Edge で TLS が終端するため、音声は Cloudflare から復号可能 (`DESIGN.md` §4)。
  対策は予約済みの `enc` (PSK / AES-GCM)。

### R21 切断中の制御メッセージが失われる (中, #3)

- `RelayClient.sendText` は `ws == null` か `send()` が false なら false を返すだけ (`RelayClient.kt:225-228`)。
  呼び出し側 (`BridgeService.kt:1200-1207`) は戻り値を見ない。
- WS が生きている間に `send()` が false になることはほぼ無い。問題になるのは**切断を検知してから再接続するまでの間**
  (R1 により数十秒になりうる) に応答や終了を押した場合。
  - 応答: relay 側は鳴り続け、25 秒で 480。アプリは応答済み表示のまま。
  - 終了: 通話中なら relay の resume 猶予 (10 秒) で BYE されるので、取り残しは短時間で済む。
- 対策案: `answer` / `reject` / `hangup` は失敗したら保留し、再接続後の `hello` を見て
  (まだ有効な callId なら) 再送する。失敗は UI に出す。RTP は再送しない。

### R22 relay の書き込みが逐次 (中, #4)

- `writeAll` は対象の接続へ順番に書き込み (`group.go:317-321`)、1 接続あたり 10 秒のタイムアウト (`session.go:464`)。
  下り RTP も同じ経路 (`group.go:371`)。
- TCP が詰まった端末が 1 台いると、同じイベントの他端末への配送 (着信の `incoming` を含む) が最大 10 秒遅れる。
  RTP の配送先は勝者だけなので、音声への影響は勝者自身の接続が詰まったときに限られる。
- R7 のイベント欠落 (Manager のバッファ 32) の引き金にもなる。
- 対策案: 接続ごとに送信 goroutine と有界キューを持たせ、`writeAll` は enqueue だけにする。
  溢れたら RTP は捨て、制御メッセージが溢れた接続は閉じる。

### R23 HTTP サーバのタイムアウト未設定 (低, #8)

- `http.Server{Addr, Handler}` のみで、`ReadHeaderTimeout` 等は未設定 (`cmd/relay/main.go:130`)。
- 既定の `LISTEN=127.0.0.1:8080` なら、到達できるのは同じホストと cloudflared だけ。
  Cloudflare の Edge が前段にいるため、Slowloris の現実的な危険は小さい。
- `ReadHeaderTimeout: 10s` を足すのは 1 行で済み、副作用も無い (WS は hijack 後なので影響しない)。ついでに直す程度。

## 3. 優先して手を付ける順 (提案)

1. **R3** resume タイマの停止漏れ (修正が小さく、確実なバグ)
2. **R2** 同一 device の旧接続の追い出し + push 判定の見直し
3. **R1 (#1, #2)** `NetworkCallback` による即時再接続 + resume 猶予の延長
4. **R5 / R21 (#3)** `dial` を `hello` の後に送る、エラー時に発信を畳む、制御メッセージの再送
5. **R6 / R7 / R22 (#4, #5)** relay のイベント順序、勝者設定の競合、接続ごとの送信キュー
6. **R4** S25 実機での push 到達時間の計測 → タイムアウト値の調整 (#11 の並列化も同時に)
7. **R9 / R10** Expires 追従と OPTIONS による監視
8. **R14 (#9)** JWKS の stale-while-error
9. 小物: **R23 (#8)**、**R18 (#10)** の起動ログ、**R11 (#6)** のドロップ数ログ

## 4. GitHub issues (#1〜#11) の評価

2026-09-23 に別エージェント (Devin) が起票した 11 件を、コードと突き合わせて評価した (2026-09-24)。

| Issue | 対応 | 評価 | 重大度 | コメント |
|---|---|---|---|---|
| #1 NetworkCallback 未使用 | R1 | **妥当** | 高 | 事実どおり。推奨対応もそのまま採れる |
| #2 resume 10 秒とバックオフ 30 秒の不整合 | R1 | **結論は妥当・理由は一部誤り** | 高 | `onOpen` でバックオフは 1 秒に戻る (`RelayClient.kt:137`) ので、通話中の切断では最初の再試行は 1 秒後。「バックオフが 8 秒以上に伸びている状態」は通話中には起きにくい。本当のボトルネックは**切断の検知** (ping 20 秒 + α)。#1 と一緒に直すべき (バックオフの上限を下げても効かない) |
| #3 制御メッセージを再送しない | R21 (新規) | **妥当** | 中 | 事実どおり。起きるのは切断中に操作したときに限られ、終了は resume 猶予で自然に BYE される。問題になるのは主に応答 |
| #4 writeAll が逐次 | R22 (新規) | **妥当・一部過大** | 中 | 逐次で 10 秒のタイムアウトは事実。ただし RTP の送り先は勝者だけなので、他端末の音声を巻き込むことは無い。巻き込まれるのは制御イベント |
| #5 イベント満杯で無言破棄 | R7 | **妥当** | 中 | `call.go:152-159` は事実どおり。ただし「backend 側も容量 32 で捨てる」は誤り: sipbackend の `emit` は ctx 付きで**ブロックして待つ** (`backend.go:264-270`)。実際に危ないのは backend 側の**順序逆転** (R6) のほう |
| #6 RTP 受信キュー 50 | R11 | **妥当 (改善)** | 低 | 1 秒で古いものから捨てるのは、遅延を溜めないための正しい設計。欠点は観測できないことだけ。ドロップ数のログ化に賛成 |
| #7 WakeLock 12 時間 | R12 | **妥当・Echo Show への影響は過大** | 中 | 取り直さないのは事実。ただし Echo Show (LineageOS cronos) は deep sleep 無効 (`echoshow5-device-notes.md`) なので、ロックが切れても CPU は眠らない。本番機 (.133) が長期稼働で着信を落としていないことと整合する。影響が出るのは PERSISTENT モードのスマホ |
| #8 http.Server のタイムアウト | R23 (新規) | **妥当・重大度は低** | 低 | 待ち受けは 127.0.0.1 のみで、前段に Cloudflare がいる。1 行で直せるので直してよい |
| #9 JWKS 取得を mutex 保持のまま | R14 | **妥当** | 中 | 事実どおり (`auth.go:152-165`)。さらに重要なのは、**取得に失敗すると古い鍵も使えなくなり全接続が 401** になる点で、issue の推奨 (stale-while-revalidate) で両方解決する |
| #10 LOCAL_IP の自動検出 | R18 | **一部誤り** | 低 | 「明示上書きできるようにする」は**既に `LOCAL_IP` がある** (`config.go:65`)。検出も第一段は `SIP_HOST` への経路で決めるので、Asterisk と同居の現行構成では正しい IP (127.0.0.1) になる。誤検出はフォールバックに落ちたときだけ。起動ログへの出力と、SETUP.md で `LOCAL_IP` の明示を勧めることで十分 |
| #11 FCM 逐次送信で 25 秒に間に合わない | R16 | **現象の説明が誤り・改善は妥当** | 低 | 送信全体が 1 つの 10 秒 context に収まる (`session.go:252`) ので、「最後のトークンが 25 秒付近」にはならない。実際に起きるのは、最初のトークンが遅いと**後続のトークンが context 切れで失敗する**こと。並列化はこの対策として有効 |

まとめ:

- 11 件とも根拠のコード位置は正しく、捏造は無い。**そのまま採れる**のは #1 #3 #9。
- 理由や影響範囲に誤りがあり、**起票どおりに直すと的を外す**のは #2 (バックオフ上限ではなく検知が問題)、
  #10 (上書きは既にある)、#11 (遅延ではなく失敗)。
- #7 は正しいが、本番の Echo Show での緊急度は低い。
- issues には無く、この文書で新たに見つけたもので重大度が高いのは **R2 (半死に WS が push を止める)**、
  **R3 (残った resume タイマ)**、**R5 (発信の取り残し)**、**R6 (イベントの順序逆転)**。起票を推奨する。

## 5. 検証の手がかり

- 網切替: 通話中に `adb shell svc wifi disable` を実行し、relay ログで「勝者切断、猶予後に BYE」から再接続までの秒数を見る。
- 半死に WS: PUSH 端末を機内モードにした直後 (30 秒以内) に着信させ、push が飛ばないことを確認する。
- resume タイマ: 通話中に端末の WS を切る → 発呼側から 10 秒以内に切る → すぐ再着信させ、480 になるか見る。
- push 到達時間: モバイル回線・画面 OFF 30 分以上の状態で `ops/e2e.sh` 相当の発呼を行い、
  relay ログの「着信」から `answer` までの時間を記録する。
