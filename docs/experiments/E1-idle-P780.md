# E1 待機コスト — P780 (2026-09-25)

設計: `docs/QUALITY_STATS.md`「実験計画 E1」。背景: issue #25 (アプリ WS ping 間隔) / #26。

## 条件と環境

| 条件 | ビルド | アプリ WS ping | relay WS ping | モード |
|---|---|---|---|---|
| A | HEAD `e2b3504` (`git archive` → /tmp でビルド) | 20 s | 20 s (本番 relay、変更なし) | PERSISTENT |
| B | 作業ツリー (#23/#25 適用、`RelayClient.CLIENT_PING_INTERVAL_SEC = 60`) | 60 s | 20 s | PERSISTENT |
| C | B と同じ APK | 60 s (接続していないので無関係) | 20 s | PUSH |

- 端末: P780、Android 11 (RKQ1.210715.001)、SIM 無し → Wi-Fi のみ (RSSI -43 dBm、「great」100%)。root 無し。
- アプリ: `io.github.tmlksu.sipbridge` gms debug 1.5.4、UID 10266 (u0a266)、内線 2105。deviceidle の user whitelist に登録済み (Doze 中もネットワーク可)。
- 各条件 60 分 (実測 3603〜3604 s)、画面オフ (`input keyevent 223`、全期間 `mWakefulness=Dozing`)、アプリは FGS で待機。
- 手順: `install -r` → `DEBUG_SET_CONFIG mode=<M> restart=true` → websocket open を確認 → 150 s 待ち → `dumpsys battery unplug`
  → `dumpsys netstats --poll` + UID 別累計 → `dumpsys batterystats --reset` → 画面オフ → 60 分 (5 分ごとに deviceidle の状態を記録)
  → `dumpsys batterystats <pkg>` / `--checkin` / `dumpsys alarm` / netstats 差分 → `dumpsys battery reset`。
- 実施時刻 (JST): A 02:16–03:16、B 03:19–04:19、C 04:22–05:22。生データは `/tmp/sipbridge-e1/{A,B,C}/` (リポジトリ外)。

## 結果 (60 分の値。実測が 60.0 分なので 1 時間あたりの値と同じ)

### アプリ UID (u0a266)

| 項目 | A (HEAD, ping 20 s, PERSISTENT) | B (WT, ping 60 s, PERSISTENT) | C (WT, PUSH) |
|---|---|---|---|
| Wi-Fi パケット rx / tx (batterystats) | **735 / 735** | **512 / 511** | 0 / 0 |
| Wi-Fi バイト rx / tx (batterystats) | 61.67 KB / 61.96 KB | 45.97 KB / 44.12 KB | 0 / 0 |
| netstats 差分 rx / tx パケット | 661 / 661 | 501 / 500 | 32 / 32 (※1) |
| netstats 差分 rx / tx バイト | 56,748 / 57,056 B | 45,998 / 44,253 B | 7,844 / 3,977 B (※1) |
| wake lock `SipBridge:lock` (partial) | 59 分 43 秒 (実質 100%) | 59 分 47 秒 (実質 100%) | 0 |
| Full Wi-Fi Lock (`WIFI_MODE_FULL_HIGH_PERF`) | 100% | 100% | 0 |
| wakeup alarm 回数 | 0 | 0 | 0 |
| CPU: アプリのプロセス自身 | 4.48 s usr + 0.99 s krn = **5.5 s** | 4.80 s usr + 1.05 s krn = **5.9 s** | **1 ms** |
| CPU: UID 合計 (`*wakelock*` への按分を含む ※2) | 2 分 34 秒 usr + 4 分 41 秒 krn | 2 分 26 秒 usr + 4 分 34 秒 krn | 1 ms |
| batterystats の推定電力 (モデル値) | 32.0 mAh (cpu 30.9 / wifi 0.984) | 31.0 mAh (cpu 29.9 / wifi 0.979) | 0.00015 mAh |
| 再接続 / 切断 | 0 | **1** (04:14:11 `failure: null http=null` → 2.8 s 後に open) | 0 (接続なし。FCM 着信もなし) |

※1 C では batterystats (`nt` も含めて) がアプリの通信を 0 と記録している。netstats の 32 パケットは、測定直前 (04:20:59) の PUSH idle 切断に伴う
TLS close / FIN などが後から計上されたものとみられる (logcat にこの時間帯の接続記録は無い)。
※2 画面オフ中にどの UID にも帰属しない CPU 時間は、partial wake lock の保持者へ `Proc *wakelock*` として按分される。PERSISTENT は wake lock を
1 時間ずっと持っているので、端末全体の「起きている間の CPU 時間」がほぼ全部アプリの負担として記録される。アプリが実際に使った CPU は
`Proc io.github.tmlksu.sipbridge` の行 (5〜6 s/時)。

### 端末全体 (参考)

| 項目 | A | B | C |
|---|---|---|---|
| partial wake lock の合計時間 | 1 時間 0 分 4 秒 | 1 時間 0 分 4 秒 | **24.6 秒** |
| Wi-Fi Sleep / Idle | 2.3% / 96.7% | 2.3% / 96.7% | **94.5% / 5.1%** |
| Wi-Fi の推定消費電力 (モデル値) | 1.35 mAh | 1.34 mAh | 0.205 mAh |
| Doze (light / deep) | 30 分 22 秒 / 25 分 53 秒 | 30 分 23 秒 / 26 分 42 秒 | 33 分 25 秒 / 23 分 41 秒 |
| deep IDLE に入った時刻 (開始からの経過) | 約 35 分 | 約 35 分 | 約 40 分 |
| GMS (u0a144) netstats rx / tx パケット | 224 / 216 | 129 / 139 | 57 / 53 |
| GMS の wakeup alarm (checkin `wua`) | — (※3) | HEARTBEAT 1, GCM_RECONNECT 2 | HEARTBEAT 2 |

※3 A の `--checkin` は保存済みの過去の checkin ファイル (9/17 開始、約 10.4 時間ぶん) を出力しており、今回の 1 時間ではなかったので使っていない
(A の数値は `dumpsys batterystats <pkg>` のテキストから取った)。B/C の checkin は当該 1 時間のもので、テキスト版と一致している。
端末全体の Wi-Fi パケット数 (A 16,000 / B 12,651 / C 16,501 rx) は LAN のブロードキャストや他アプリの通信が大半を占めるため、比較には使っていない。

## A と B の差 (アプリ ping 20 s → 60 s)

- アプリ UID の Wi-Fi パケット: rx −223 / tx −224 (−30%)、バイト数: rx −15.7 KB / tx −17.8 KB (約 −27%)。
- wake lock・Wi-Fi Lock・CPU (アプリ自身 5〜6 s/時)・推定電力 (32.0 → 31.0 mAh、Wi-Fi 分は 0.984 → 0.979 mAh) は誤差の範囲で、ほぼ変わらない。
  PERSISTENT では wake lock と `WIFI_MODE_FULL_HIGH_PERF` を常に持っていて、CPU はサスペンドせず、Wi-Fi もパワーセーブに入らない
  (Wi-Fi Sleep 2.3%)。**このため Wi-Fi 環境の PERSISTENT では、ping の回数を減らしても電力はほとんど変わらない**。電力を決めているのはロックのほう。
- B には再接続が 1 回ある (TLS/WS の張り直しで十数〜数十パケット)。これを除けば、差は下記の想定値 (−240) にさらに近づく。

## B と C の差 (PERSISTENT → PUSH)

- アプリの通信は 1,023 パケット/時 → 0 になり、wake lock と Wi-Fi Lock が無くなる。端末の partial wake lock は 60 分 → 25 秒、Wi-Fi Sleep は 2.3% → 94.5%、
  Wi-Fi の推定電力は 1.34 → 0.205 mAh。
- PUSH の待機コストは GMS 側 (FCM の常時接続。HEARTBEAT アラーム 2 回/時、GMS 全体で 110 パケット前後/時) に移るが、これは FCM を使う他のアプリと共有している。
- 今回の 3 条件のなかで差が大きいのはここ (モードとロック) で、A→B (ping 間隔) の差はそれより小さい。

## 想定との比較

想定: relay → アプリの WS ping 180 回/時 + アプリ → relay の WS ping (A 180 回/時、B 60 回/時)。
1 往復あたり「ping 1 + pong 1」で、各方向ほぼ 2 パケット (相手の ACK が pong や次のセグメントに相乗りしない分を含む) とすると次のようになる。

| | 想定の往復回数/時 | 想定パケット/方向 (×2) | 実測 (batterystats) rx / tx |
|---|---|---|---|
| A | 180 + 180 = 360 | 720 | 735 / 735 |
| B | 180 + 60 = 240 | 480 (+ 再接続 1 回ぶん) | 512 / 511 |
| 差 | −120 | −240 | −223 / −224 |

- 実測は想定とよく合う (A +2%、B +7% のうち大部分は再接続 1 回ぶん)。平均は約 84〜86 B/パケット (IP 層、TLS 上の WS 制御フレーム + TCP/IP ヘッダ)。
- ping 以外の待機トラフィック (アプリレベルの keep-alive、SIP 再登録の通知など) は、アプリの WS には実質的に流れていない。

## 測定上の限界

- **Wi-Fi のみ**: モバイル回線だと、ping ごとに RRC の tail (数秒の高電力状態) が発生する。relay の ping とアプリの ping は位相がずれているので、
  A は最大 360 回/時、B は最大 240 回/時ほどラジオを起こしうる。モバイルでは A→B の効果がパケット比 (−30%) 以上に電力へ効く可能性があるが、今回は測れていない。
- **電力は実測していない**: USB 給電のままなので `actual drain: 0`。mAh はすべて batterystats の power_profile に基づくモデル値。
- 1 条件につき 1 回、60 分ずつ。時間帯が違う (02–05 時)。B の再接続 1 回がどのくらいの頻度で起きるのかは、この長さでは分からない。
- Doze: `battery unplug` で電池駆動扱いにしたため、3 条件とも light idle のあと、開始から約 35〜40 分で deep IDLE に入った。アプリは user whitelist
  に入っているのでネットワークと wake lock は制限されない。whitelist に入っていない端末の挙動 (Doze でソケットが止まる) は測っていない。
- root が無いので `/sys/kernel/debug/wakeup_sources` は読めない。PERSISTENT は wake lock を持ち続けるので、サスペンドからの wakeup 回数はそもそも発生しない。
- `/proc/net/xt_qtaguid` は Android 11 には無い。netstats は 2 時間バケットなので、`--poll` 後の累計の差分で求めた (batterystats とは計上の区切りが少しずれる)。
- relay 側の ping/pong カウンタは見ていない。tcpdump も使っていない。

## 端末の復元

- 最後の条件 C のあと、作業ツリーのビルド (B と同じ APK。端末内 APK の sha256 先頭 `57e52f034d6efc01` と一致) + PUSH モードの状態になっている (元の状態)。
- 各条件の終わりと最後に `dumpsys battery reset` を実行済み (`USB powered: true` に戻ったことを確認)。
- アンインストールはしていない (`install -r` のみ)。設定の変更は DEBUG_SET_CONFIG の `mode` だけで、relay・SIP・Access の設定には触れていない。
  BridgeService は FGS で稼働中。deviceidle whitelist もそのまま。
- `dumpsys batterystats --reset` を 3 回実行したので、P780 の電池統計は実験前の履歴を失っている。A の `--checkin` で保存済みの checkin ファイルも消費された。

## 要確認

1. B の 04:14:11 の `failure: null http=null` (即時に再接続して成功) の原因。同じ時刻に relay の再デプロイ / 再起動や Cloudflare 側の切断が
   無かったか、relay のログで確認したい。アプリ ping を 60 s にしたこととの関係は、この 1 回だけでは判断できない。
2. PERSISTENT で待機中も `PARTIAL_WAKE_LOCK` + `WIFI_MODE_FULL_HIGH_PERF` を持ち続けることが、Wi-Fi 待機時の支配的なコスト。
   Echo Show (常時給電) では問題にならないが、電池駆動の端末で PERSISTENT を使う場合は、ping 間隔よりロック方針 (#19 の PUSH と同様に待機中は外すか)
   を見直すほうが効く。ただし OkHttp の ping タイマーはサスペンド中は動かない (relay からの ping で起こされる前提になる) ので、別途検証が必要。
3. モバイル回線で A/B の差を見たい場合は、SIM のある端末で同じ手順を行う必要がある (S25 は本番機なので対象外)。
