# 通話品質・待機コストの観測 (設計メモ)

目的: 「RTP over WS で足りているか」「端末の音声処理は足りているか」「待機中に何をどれだけ消費しているか」を
**勘ではなく数字で** 判断する。以下の改善案の採否はこの数字を見て決める。

| 案 | 内容 | 判断に使う数字 |
|---|---|---|
| 1 | 音声経路の UDP 化 (方式A/C への移行) | 到着ギャップ (stall) の頻度、RTT |
| 2 | relay メディアパスの作り替え | relay の送信キュー破棄数 |
| 3 | Oboe/AAudio 移行 | AudioTrack underrun (ネットワーク正常時) |
| 5 | 状態ファイルの SQLite 化 | 件数 (本計測の対象外) |

## 大原則: アプリを重くしない

- **待機中はゼロコスト**: タイマー・定期送信・ウェイクアップを追加しない。
- **通話中**: 既存の受信/再生/送信スレッドのパケット処理に整数カウンタを足すだけ。新スレッド・タイマーなし。
- **送信は通話終了時に 1 回** (`call_stats`, 数百バイト)。RTT は既存の JSON `ping`/`pong` を通話開始直後と終了直前に 1 回ずつ。
- UI には出さない。relay の journal に 1 通話 1 行。アプリ側は logcat に同じ内容を 1 行 (debug/release 共通、PII なし)。

## 計測項目

### アプリ (下り = relay → app)

| キー | 意味 |
|---|---|
| `rx.pkts` | 受信 RTP 数 |
| `rx.gaps` | seq の欠け (失われた数)。WS 区間は TCP なので本来 0。非 0 なら relay 側破棄か LAN 区間 |
| `rx.reorder` | seq の逆行・重複 |
| `rx.jitterMs` | RFC 3550 の interarrival jitter (RTP ts と到着時刻から。通話終了時の値) |
| `rx.maxGapMs` | 連続する 2 パケットの到着間隔の最大値 |
| `rx.stall100` / `stall200` / `stall500` | 到着間隔が 100/200/500 ms を超えた回数 (= TCP 滞留で音が止まった回数) |
| `jb.underrun` | ウォームアップ後に再生キューが空で無音 (20 ms) を挿入した回数。再生ループは 40 ms 待って空なら 1 加算するので、途切れ時間の目安は 1 = 約 40 ms |
| `jb.overflow` | 上限超過で捨てたフレーム数 (JitterBuffer + playQueue) |
| `playUnderrun` | `AudioTrack.getUnderrunCount()` の通話終了時の値 (端末音声パイプラインの取りこぼし) |
| `tx.pkts` / `tx.drop` / `tx.lost` | 上り送信数 / バックプレッシャー破棄数 (既存カウンタ) / WS 未接続 (再接続待ち) で送れなかった数 |
| `tx.lateMs` | 送信ループの 20 ms 周期からの最大遅れ |
| `rttMs` | `[開始直後, 終了直前]` の JSON ping 往復 (取れなければ -1)。開始直後は呼確立の処理と重なり高めに出る (P780 実測 230-290 ms vs 終了直前 40 ms) ので、回線 RTT としては終了直前の値を使う |
| `net` | `wifi` / `cellular` / `other` (通話開始時。ConnectivityManager の既存コールバックから) |

### relay (上り = app → relay、および Asterisk 側)

- app→relay の上り RTP について、アプリ下りと同じ `pkts/gaps/reorder/jitterMs/maxGapMs/stall*`
- Asterisk→relay の RTP について `pkts/gaps` (LAN 区間の確認用)
- 下り送信キューでの破棄数 (既存)

### ログ形式 (relay)

`call_stats` 受信時 (受信できなければ通話終了から 5 秒後) に、relay 自身の計測と合わせて 1 行出す:

```
msg=call_stats callId=… account=… dev=… dur=… net=… up.pkts=… up.gaps=… … ast.pkts=… ast.gaps=… ast.qdrop=… txDrop=… app="{…JSON…}"
```

- 対象はメディアが始まった通話のみ。5 秒後に届いた / 記録の無い callId の app 統計は `msg=call_stats_orphan` 行で別に出す。

番号・表示名など PII は出さない (callId, account=内線番号, dev=デバイス ID のみ)。

## プロトコル (v1.2)

app → relay:

| t | フィールド | 意味 |
|---|---|---|
| `call_stats` | `callId`, `dur` (ms), `net`, `rx`, `jb`, `playUnderrun`, `tx`, `rttMs` | 通話終了時に 1 回。relay はログに出すだけで応答しない |

- 旧 relay (≤0.2.0) は未知の `t` に `error bad_message` を返す (切断はしない)。アプリは
  `hello.relayVersion` が `0.3.0` 以上のときだけ送る。relay は `RelayVersion` を `0.3.0` に上げる。

## 付随: 通話中の下り途絶監視 (再接続トリガ)

同一網のまま上流が死ぬと、OkHttp の ping (60 s) では検知まで最大 120 s かかり relay の resume 猶予 (10 s) に間に合わない。
下り到着ギャップは上記計測で既に追っているので、それを流用して **通話中 (active) に下り RTP が 3 s 途絶えたら WS を張り直す**。

- 追加コストなし: 判定は既存の再生スレッド (20 ms ループ) で「最終到着時刻からの経過」を見るだけ。待機中は動かない。
- 保留等で相手が RTP を止める場合に備え、張り直しは 1 通話あたり連続で 1 回 (次の RTP 受信でリセット)。
- 発動は `call_stats` に `rx.reconnects` として記録する。

## 実装メモ

- 現在の `JitterBuffer` はウォームアップ後は素通しの FIFO (seq 並べ替え・欠け検知なし)。今回は **計測のみ** で並べ替えはしない。
- 到着時刻は `SystemClock.elapsedRealtimeNanos()` / Go は `time.Now()` の monotonic。
- カウンタは通話ごとにリセット。スレッド間は受信スレッドのみ書き込み・終了時に読む (volatile / atomic で足りる)。
- 計測ロジックは Android 非依存の純 Kotlin クラス (`RtpStats` 等) と Go の小さな struct にしてユニットテストする。

## 実験計画

### E1: 待機コスト (コード変更なし)
P780 (試験機) で PERSISTENT / PUSH をそれぞれ数時間放置し、以下を比較する。

```
adb shell dumpsys batterystats --reset
# … 放置 (画面オフ、充電ケーブルは測定中は外す / USB 給電を切る) …
adb shell dumpsys batterystats io.github.tmlksu.sipbridge(.debug)  # wakelock, mobile/wifi radio active, パケット数
adb shell cat /proc/net/xt_qtaguid/stats  # もしくは dumpsys netstats detail で UID 別バイト数
```

変数: アプリ側 OkHttp ping 間隔 (#25)、relay の WS ping 間隔 (現在 20 s 固定。`session.Config.PingInterval` はあるが env から設定できないので、
実験用に `WS_PING_INTERVAL` 等の env を追加する)。
Cloudflare の WS アイドル切断 (~100 s) より短い範囲で 20 / 45 / 60 s を比較し、
「半死に接続の検知時間」とのトレードオフで既定値を決める。

### E2: 通話品質 (call_stats 実装後)
エコー内線 2199 に 2〜5 分発信し、条件ごとに call_stats を集める。

- Wi-Fi 宅内 / Wi-Fi 外部 / モバイル回線 (S25 の実運用ログも見る)
- 詰まりの人工注入: root 可能な端末 (.214 Echo Show) で `ip6tables -A OUTPUT -d <CF edge> -j DROP` を数百 ms〜数秒入れて stall* と jb.underrun の反応を確認
- 移動中の網切替 (Wi-Fi → モバイル)

判断の目安: モバイル回線で `stall500` が 1 分あたり複数回 → 案1 を検討。
ネットワーク系が 0 付近なのに `playUnderrun` が増える → 案3 を検討。relay `txDrop` > 0 → 案2 を検討。
