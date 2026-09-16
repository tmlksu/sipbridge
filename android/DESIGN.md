# SIP Bridge — Android クライアント (Nocturne UI, v1.1)

sipbridge の Android 側。EchoSIP standby v1.5 (自前 SIP/RTP スタック) から
**SIP スタック (`SipEngine.kt`) と UDP 待ち受けを完全撤去**し、relay への
WebSocket クライアント + Nocturne UI (`docs/UI-DESIGN.md`) に作り替えたもの。
プロトコルは `docs/PROTOCOL.md` v1.1 (`sip_account`, `hello.account`, 発信 `dial`)。

- package / applicationId: `io.github.tmlksu.sipbridge` (旧 EchoSIP と共存可)
- アプリ名: 「SIP Bridge」。`rootProject.name = "SipBridge"`
- versionCode 2 / versionName 1.1
- 端末側で LISTEN するポートはゼロ (`DatagramSocket` 禁止。CI で grep 検査)
- ダーク専用テーマ (`Theme.SipBridge`)、色は Nocturne トークンのみ (`colors.xml`)
- 画面文言は `strings.xml` に集約 (ログ文言・`CallHub.status` の動的状態文は対象外)

## 1. 要件・対象端末

- Echo Show 5 (LineageOS 18.1 / API 30, GApps 無し): `foss` flavor +
  `PERSISTENT` モード (ForegroundService で WSS 常時接続)
- Galaxy S25 (API 35, GMS あり): `gms` flavor + `PUSH` モード
  (FCM 起床→接続→通話終了 60 秒後に切断)
- 音声: G.711 μ-law (PT 0) / A-law (PT 8), 8 kHz mono, ptime 20 ms

## 2. 画面構成 (`MainActivity` + BottomNavigationView + 4 タブ)

起動時はキーパッド。下部ナビは `nocturne_surface`・高さ 56dp、選択中は accent。
各タブは Fragment (`activity_main.xml` は入れ物の LinearLayout のみ。
旧 1 画面設定 UI・旧 `activity_main` の設定項目群は廃止済み)。

| タブ | Fragment | 概要 (UI-DESIGN §1) |
|---|---|---|
| キーパッド | `KeypadFragment` | 状態ピル (登録OK=accent/未登録=text_dim/未接続=border) + モードチップ (タップで設定へ)。番号表示 (40sp) + キャプション (連絡先一致名/内線番号/外線)。3×4 丸キー (0 長押しで `+`)。発信ボタン + 削除 (長押し全消去)。発信中/通話中は「通話に戻る」ピル。通話中はアプリ内ピル「📞 mm:ss」(View 版。タップで通話画面) |
| 履歴 | `HistoryFragment` | セグメント「すべて/不在着信」+ カード内 `RecyclerView` (方向アイコン円 ↙↗、名前、2 行目「着信・101」、右端時刻 今日=HH:mm/昨日=「昨日」/それ以前=M/d)。行タップ=発信、長押し=削除/連絡先に追加、右上「全消去」。空表示あり。下に relay 注記 |
| 連絡先 | `ContactsFragment` | 検索欄 (名前部分一致・番号前方一致) + グループ見出し「ウチ」「ソト」+ カード (アバター円/名前/番号/受話器)。行タップ=発信、長押し=編集/削除、FAB「＋」で追加ダイアログ (名前/番号/グループ + 「端末の連絡先から選ぶ」取り込み)。設定で端末の連絡先表示を ON にすると「端末」グループを追加 (番号ごとに 1 行、読み取り専用) |
| 設定 | `SettingsFragment` | 下記 §3 |

### §3 設定タブの構成 (UI-DESIGN §1.4)

上から順に:

1. **状態カード**: 「● relay に接続中」/「○ 未接続」/「再接続中…」+ 2 行目
   `wss://…・内線 2104` + 「停止」/「開始」ボタン + `CallHub.status` の状態文。
   `hello.account` が空 (かつ SIP アカウント未設定) のときは
   「SIP アカウント未設定: 設定タブで内線番号を入力してください」と出る。
2. **接続カード**: relay URL / Access Client ID (先頭 4…末尾 4) /
   Access Secret / Dev Token (秘密は `••••`)。行タップで編集ダイアログ。
3. **SIP アカウントカード**: 内線番号 (SIP user) / パスワード / 表示名 (任意)。
   「relay がこのアカウントで Asterisk に登録します。…」の説明付き。
4. **動作カード**: モード切替「常時接続 | PUSH 起床」+ 説明文。
   トグル: 起動時に自動開始 / オーバーレイ着信 / スピーカーで応答 (既定 OFF。受話口の無い端末では常にスピーカー) /
   端末の連絡先を表示 (既定 OFF)。**「終了前に確認」は v1.1 で廃止** (終了は即時)。
5. **音声カード**: マイクゲイン スライダー (0.5〜4.5)。
6. **権限カード**: オーバーレイ / 電池除外 / 通知 / 全画面通知 (API 34+)。
   状態表示 + タップで該当設定画面へ。
7. **テスト**: 「テスト着信を表示」(relay 無しで着信 UI を確認)。
8. **フッター**: アプリ版 / relay 版 (`hello.relayVersion`) / device id (先頭 8 桁)。

保存はダイアログ OK で即 `BridgeConfig.save`。接続に関わる項目
(relay URL / Access / Dev Token / SIP アカウント / モード) は Service を
再起動して反映する。保存前に relay URL 妥当性と
「Access Client ID か Dev Token のどちらか必須」を検証する。

## 3. 通話画面 (`CallActivity`。旧 `IncomingCallActivity` を置換)

着信中 / 発信中 (呼出中) / 通話中 / 終了確認 / DTMF シートを 1 Activity で
状態遷移する。真実は `CallHub` (state / outgoing / session) が持ち、
本画面は表示に徹する (`CallHub.StateListener` で再描画)。

共通: キャプション「SIP BRIDGE・着信/発信/通話中」、アバター円 (96dp・1 文字目)、
名前 (28sp)、番号、状態行。ロック画面上表示・画面点灯・`KEEP_SCREEN_ON`・
着信音/バイブあり。`layout-land/activity_call.xml` で横画面対応
(左: アバター+名前+番号+状態、右: 操作ボタン)。

### 状態遷移図 (テキスト)

```
[着信: RINGING(in)] --応答--> [IN_CALL] --終了--> [終了表示 1.5秒] --閉じる--> [IDLE]
       --拒否--> [IDLE]                        (確認 ON なら BottomSheet を挟む)
[発信: RINGING(out)] --answered--> [IN_CALL]
       --切断--> [IDLE] (確認なし)
[IN_CALL] --縮小--> [ピル表示 + Activity finish] --タップ--> [IN_CALLへ復帰]
[IN_CALL] --ミュート/スピーカー/キーパッド(DTMF)--> [IN_CALL] (トグル・シート)
[*] relay からの ended / 他端末応答 / タイムアウト → [IDLE] (+履歴確定、ピル消去)
```

- 状態行: 着信中/呼出中は「•••」3 点点滅 (400ms tick)。
  呼出中は 183 early media 時に「呼出中 (相手側応答音)」。
  通話中は経過時間 `mm:ss` (accent_text)。
- 通話中ボタン (3×2): ミュート (トグル) / キーパッド (DTMF シート) /
  スピーカー (トグル) / 保留・通話を追加・連絡先 (v1 は無効。alpha 0.35)。
  下部: 縮小 (outline 丸) + 終了 (neutral_btn 72dp)。
- 終了確認: 設定 ON なら BottomSheet「通話を終了しますか？/名前 (番号)・通話時間」。
  OFF なら即終了。呼出中の切断・着信拒否に確認は出さない。
- 通話終了後: 「通話終了 mm:ss」を 1.5 秒表示して閉じる。
- 着信拒否・発信切断は即 `finish()` (確認なし)。

## 4. クラス一覧

`app/src/main/java/io/github/tmlksu/sipbridge/` (package `io.github.tmlksu.sipbridge`)。

| クラス/ファイル | 種別 | 役割 |
|---|---|---|
| `MainActivity` | Activity | BottomNavigation + 4 Fragment の入れ物。`selectTab()` で設定タブへ飛べる |
| `KeypadFragment` | Fragment | キーパッド (§2)。発信は `BridgeService.ACT_DIAL` を投げる |
| `HistoryFragment` | Fragment | 履歴 (§2)。`CallHub` 変化で再読込。`formatListTime()` (JVM テスト不要だが公開) |
| `ContactsFragment` | Fragment | 連絡先 (§2)。行・ダイアログはコードで組み立て |
| `SettingsFragment` | Fragment | 設定 (§2-§3)。編集ダイアログ + 検証 + Service 再起動。権限導線 |
| `CallActivity` | Activity | 通話画面 (§3)。着信音/バイブ/DTMF シート/終了確認 |
| `BridgeService` | Service | 常駐 ForegroundService。`RelayClient` 保持・通話制御・履歴書き込み・音声経路・ピル・PUSH 切断 (§5, §6) |
| `RelayClient` | 通信 | OkHttp WebSocket。認証ヘッダ・ping 20 秒・指数バックオフ再接続・送受信 (§5) |
| `RelayProtocol` | 通信 | PROTOCOL.md の JSON encode/decode (`org.json` のみ) + `normalizeRelayUrl`。Android 非依存 (JVM テスト可) |
| `CallHub` | 状態 | Service⇔Activity 間の通話状態共有シングルトン。`CallSession` インタフェース (旧 `SipEngine.IncomingSession` を置換) |
| `RtpEngine` | 音声 | マイク→RTP→sink / 受信→ジッタ→再生。`sendDtmf()` (in-band 置換送出)。`MediaSink` = `RelayClient.sendRtp` |
| `RtpPacket` | 音声 | RTP 12B ヘッダの組み立て・分解 (純粋 Kotlin。JVM テスト可) |
| `JitterBuffer` | 音声 | 5 フレーム開始/上限 20 (純粋 Kotlin。JVM テスト可) |
| `G711` | 音声 | μ-law/A-law 変換 (EchoSIP から流用) |
| `DtmfTone` | 音声 | in-band DTMF トーン生成 (120ms 二重音 + 80ms 無音。Android 非依存。JVM テスト可) (§7) |
| `DialHelper` | Fragment 共通 | 履歴・連絡先からの発信共通処理 (マイク権限→`ACT_DIAL`) |
| `HistoryStore` | 永続化 | 通話履歴 (`filesDir/history.json`、最大 200 件。Android 非依存。JVM テスト可) (§8) |
| `ContactStore` | 永続化 | 連絡先 (`filesDir/contacts.json`、番号正規化・検索。Android 非依存。JVM テスト可) (§8) |
| `DeviceContacts` | 連絡先連携 | 端末の連絡先の権限判定 + クエリ (番号ごとに 1 件・名前順。UI は持たない。重複除去・検索フィルタは Android 非依存で JVM テスト可) (§8) |
| `BridgeConfig` | 設定 | `EncryptedSharedPreferences` (失敗時は平文フォールバック)。sipUser/sipPassword/sipDisplay/speakerOnAnswer (既定 OFF)/deviceContactsEnabled (既定 OFF) を含む (§8) |
| `NotificationHelper` | 通知 | チャンネル・常駐/着信/通話中通知 (UI-DESIGN §2.1 の文言) |
| `CallOverlayManager` | オーバーレイ | 着信バブル (応答/拒否) + 通話中ピル (§9)。`hide()` は両方隠す |
| `BootReceiver` | Receiver | 起動時自動開始 (`autostart` ON のとき `BridgeService.start`) |
| `DebugConfigReceiver` | debug 専用 | adb 設定投入 (§10。`src/debug` 配下 + debug manifest。release に含まれない) |
| `BridgeMessagingService` | gms 専用 | FCM 受信→`ACT_WAKE_INCOMING` 起床 + トークン配送 (`src/gms` 配下。foss に含まれない) |

削除済み: `SipEngine.kt` (SIP/UDP スタック)、`SipConfig.kt` (旧設定)、
`IncomingCallActivity` (→`CallActivity`)、`activity_incoming.xml`、
未使用 drawable `circle_accent.xml`、`RtpEngine.setAudioManagerMode()` (死にコード)。

## 5. 接続・認証・`sip_account` フロー (PROTOCOL.md v1.1)

- 接続先: 設定の relay URL を正規化 (`wss://host` → `wss://host/v1/session`。
  `https://` は `wss://` に読み替え。詳細は `normalizeRelayUrl`)。
- ヘッダ: Access Client ID/Secret があれば Service Token 方式。
  空なら開発用 `Authorization: Bearer <devToken>`。
  `X-Device-Id` (初回生成 UUID を保存)・`X-Client-Version` (`SipBridge/1.1`) を必ず付ける。
- Keep-alive: OkHttp の WS ping/pong 20 秒。切断後は 1,2,4,…30 秒の指数バックオフで再接続。
- **接続ごとの `sip_account` 送信は 1 回だけ** (`BridgeService.sipAccountSent`)。
  最初の `hello` 受信時に設定済み (`sipUser` 非空) なら送り、relay の受理後
  `hello` (2 回目) では送らない。`onDisconnected` と新規接続開始時
  (`connectFresh()`: 手動切断では `onDisconnected` が発火しないため) にリセットする。
- `hello.account` (無ければ `extension`) を `CallHub.extension` に採用する。
  空 + 設定も空 → 設定画面に「SIP アカウント未設定」。
- `error no_account` / `account_password_mismatch` / `account_failed` は
  設定画面の状態文に日本語で反映する。
- 接続直後の `hello.call` で状態同期 (保留着信があれば提示、通話中 `active` なら
  メディア再開 + IN 履歴化。通話が無くなっていたら履歴確定して畳む)。

### 通話フロー

- 着信: `incoming` → 全画面 Activity を先に出す (出せなければ 2.5 秒後に通知+バブル)。
  `answer` → 通話中 (RtpEngine 開始)。`reject` (既定 486) / `hangup`。
- 発信: `dial {to}` → `ringing` (呼出中。183 は early media 表示) → `answered` (通話中)。
  未接続での発信要求は `pendingDial` に保持し、接続後の `hello` で送る。
- `ended {reason}` で終了 + 履歴確定 + ピル消去 + 音声経路復元。
  `answered_elsewhere` は表示を畳むだけ。
- `hello.call==null` ののに表示中 (他端末応答等) も同じ後始末をする
  (履歴確定・`AudioManager` 復元・ピル/バブル/着信通知の消去)。
- メディア: バイナリフレーム = RTP そのまま。app 生成の SSRC/seq/ts。
  受信は `RtpPacket.parse` → G.711 デコード → ジッタバッファ → AudioTrack。RTCP は扱わない。
- 音声経路: 応答/発信時に `MODE_IN_COMMUNICATION` + 設定 (`speakerOnAnswer`。既定 OFF。ただし `AudioRoute.hasEarpiece()` が false の端末では強制スピーカー) に切替、
  通話終了 (ended/拒否/切断/他端末応答確定の全経路) で `MODE_NORMAL` + 変更前スピーカー状態に復元する。

### PUSH モード

- `PUSH_IDLE_DISCONNECT_MS` (60 秒): 通話終了後に切断 (`schedulePushDisconnect`)。
  発信・`ACT_WAKE_INCOMING` (FCM)・未登録 FCM トークン保持時は `ensureConnected()` で接続。
- `pendingPushToken`/`registeredPushToken` で `register_push` を hello 後に 1 回だけ送る。
  prefs (`sipbridge_gms`) からの復元でサービス再生成時の取りこぼしを防ぐ。

## 6. 履歴の書き込み規則 (`HistoryStore` + `BridgeService`)

- 着信応答 → IN (応答時に作成、終了時に通話時間を更新)。
- 未応答の着信 → MISSED。対象 reason を網羅する:
  `timeout` / `cancel` / `reject` / `answered_elsewhere` (+ `error` 等その他)。
  いずれも `ended` 受信時 (state=RINGING・着信・未応答) に 1 回だけ書く。
- ローカル拒否/切断でも MISSED/OUT 確定を先行書きし、後続の `ended` とは
  `historyDoneFor` (callId 単位の dedupe) で二重書き込みしない。
- 発信 → OUT (dial 時に作成。未応答なら duration 0 のまま)。
- 通話時間は `updateDuration()` で確定する。
- 着信時の表示名解決: relay の `display` → `ContactStore.lookup(number)` →
  (権限があるときだけ) `DeviceContacts.lookup` → 番号。UI スレッドから呼ばれた場合は
  番号を返してバックグラウンド解決後に表示を更新する。

## 7. in-band DTMF (UI-DESIGN §3.1)

- relay には送らない (`dtmf` メッセージ未使用。Asterisk 側 `dtmfmode=inband`)。
- `DtmfTone`: DTMF 二重音 (低群 697/770/852/941 × 高群 1209/1336/1477/1633 Hz、
  振幅 -6dBFS 程度) 120ms + 無音 80ms を PCM 生成し、20ms フレームに分割。
- `RtpEngine.sendDtmf()`: フレームをキューに積み、送信スレッドがマイクの代わりに
  **置換で**送る (ミックスではない。ミュート中でも送る)。未対応文字は無視。
- UI: 通話中の「キーパッド」で BottomSheet (3×4 + 入力済み表示 + 閉じる)。
  押下ごとに送出 + ローカルの短い操作音 (`ToneGenerator`)。

## 8. 設定・保存

- `BridgeConfig` (`EncryptedSharedPreferences`。失敗時は平文フォールバック):

| 項目 | 備考 |
|---|---|
| relay URL / Access Client ID / Secret / Dev Token | 接続認証。ID か Dev Token のどちらか必須 |
| SIP アカウント (sipUser/sipPassword/sipDisplay) | relay が Asterisk に登録する内線。複数端末で共有可 (同時着信・先応答が通話) |
| モード | PERSISTENT / PUSH |
| マイクゲイン (0.5〜4.5) | Echo Show は 1.5〜3.0 推奨 |
| オーバーレイ / 自動起動 / スピーカーで応答 (既定 OFF) / 端末の連絡先を表示 (既定 OFF) | 動作トグル |
| deviceId | `X-Device-Id` (初回生成 UUID を保持。relay の再接続 resume 用) |

- `HistoryStore` (`filesDir/history.json`、最大 200 件)、
  `ContactStore` (`filesDir/contacts.json`)。いずれもアプリ内 JSON・原子書き込み
  (tmp → rename)。**履歴/連絡先はアプリ内保存** (Asterisk からの取り込みなし、
  relay への送信なし、バックアップ対象外 `allowBackup=false`)。

## 9. オーバーレイピル (UI-DESIGN §3.2)

- 「縮小」で Activity を閉じ、`CallOverlayManager.showInCallPill()` が他アプリの上に
  小さなピル「📞 mm:ss」(`nocturne_surface` 地、右上、1 秒更新、タップで復帰、
  ドラッグで移動可) を出す。オーバーレイ権限が無い場合は常駐通知の
  「通話中 mm:ss — タップで戻る」に代替 (1 秒更新)。
- アプリ内 (キーパッド等) では同じピルを View で代替表示する。
- **通話終了で必ず消える**: `onEnded`/拒否/切断の全経路 + 通話画面復帰
  (`ACT_UI_SHOWN`) + 他端末応答確定時 (`hideInCallPill()` + `overlay.hide()` +
  代替通知ティッカー解除)。`hide()` は着信バブルとピルの両方を隠す
  (通話終了時の取り残し防止)。

## 10. debug 専用 receiver (`DebugConfigReceiver`)

- `src/debug` 配下 + debug manifest の receiver のため **release には含まれない**。
- `adb shell am broadcast -a io.github.tmlksu.sipbridge.DEBUG_SET_CONFIG` で設定投入
  (Echo Show など入力しづらい端末用。詳細は `INSTALL.md` と `scripts/adb-setup.sh`)。
- `DEBUG_CALL_ACTION` (action=answer|reject|hangup|dial, to=番号) で通話操作
  (E2E スクリプト用)。
- debug ビルドは平文 `ws://` も許可 (`usesCleartextTraffic`。adb reverse 試験用)。

## 11. 通知文言 (UI-DESIGN §2.1、`NotificationHelper`)

- 着信通知: タイトル「着信中・<名前>」、本文「<番号> から (relay 経由)」、
  アクション「拒否」「応答」(応答は accent 色)。フルスクリーンインテント付き。
- 常駐通知: 「relay に接続中・内線 101」/「常時接続／常駐通知 (消去不可)」。
  PUSH 待機は「待機中 (PUSH 起床)」/「PUSH 起床待ち／常駐通知 (消去不可)」。

## 12. 横画面 (Echo Show 5, 960×480)

- `layout-land/`: `activity_call` (左: アバター+名前+番号+状態、右: 操作ボタン)、
  `fragment_keypad` (左: 番号+発信、右: 3×4 キー) を用意。
- `fragment_history` (RecyclerView が weight で縮む)、`fragment_contacts`・
  `fragment_settings` (ScrollView) はポートレート版のまま 480dp 高さに収まる
  (主要操作が見切れない。設定/連絡先はスクロール可)。
- ボトムナビは横画面でも下部固定 (高さ 56dp)。

## 13. flavor

| flavor | 用途 | FCM |
|---|---|---|
| `foss` | Echo Show (GApps 無し) | 無し。PUSH モードは使えない (表示上注意あり) |
| `gms` | S25 (GMS あり) | `BridgeMessagingService` (FCM 受信→`ACT_WAKE_INCOMING` 起床、`onNewToken`→`register_push`) |

`google-services.json` が無くてもビルドが通るよう、google-services プラグインは
`app/google-services.json` / `app/src/gms/google-services.json` が存在するときのみ適用
(foss の process タスクは無効化)。

## 14. ビルド・テスト

```
export JAVA_HOME=~/android-build/jdk PATH=~/android-build/jdk/bin:$PATH \
  ANDROID_HOME=~/android-build/sdk ANDROID_SDK_ROOT=~/android-build/sdk
cd android && ~/android-build/gradle-8.9/bin/gradle \
  :app:assembleFossDebug :app:assembleGmsDebug :app:testFossDebugUnitTest --no-daemon
```

- JVM テスト (`app/src/test/`): `RelayProtocolTest` (encode/decode・URL 正規化・
  `sip_account`/`hello.account`)、`JitterBufferTest`、`RtpPacketTest` (RTP+G711 往復)、
  `HistoryStoreTest` (保存/上限/通話時間更新)、`ContactStoreTest` (保存/検索/名前解決)、
  `DtmfToneTest` (振幅/長さ/周波数)。
- `grep -r DatagramSocket android/app/src` がゼロ件であること (UDP 撤去の証跡)。
- Kotlin 警告: プラットフォーム API の deprecation
  (`isSpeakerphoneOn`/`requestPermissions`/window flag 等。minSdk 29 互換のため代替不可)
  は残る。自前コードの警告 (冗長初期化子等) は潰す。

## 15. EchoSIP からの継承・変更点

- 継承: 着信 UI の排他制御 (全画面優先・裏回り時のみ通知+バブル復活)、呼出音・バイブ、
  スピーカーフォン、マイクミュート、通話タイマ、KEEP_SCREEN_ON、WifiLock/WakeLock、
  G.711 変換、AEC/NS 狙いの録音設定。
- 変更: SIP 応答 (100/180/200)・digest 認証・re-INVITE 追従は relay 側の責務。
  端末側の RTP 宛先追従 (`updateRemote`) は不要になり削除。
  ジッタは 3→5 フレーム開始、上限 10→20。
  DTMF は in-band 送出 (`RtpEngine.sendDtmf` + `DtmfTone`)。
  保留/通話を追加/通話中の連絡先は v1 では無効ボタン
  (保留は relay の re-INVITE 対応後)。
- 未対応 (v2): Opus、PSK 追加暗号化 (`enc` 予約)、`dtmf` メッセージ送出
  (in-band で足りるため未使用)。
