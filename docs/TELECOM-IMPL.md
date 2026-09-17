# Telecom 統合 — 実装仕様 (v1.5)

前提: `docs/TELECOM.md` (方式検討と実機検証結果) を先に読むこと。
本書は **実装の契約** であり、書くファイル・シグネチャ・遷移・失敗時の挙動を確定させる。
検証済みの事実 (`TELECOM.md` §2/§3) は再検証しない。

対象は `android/` のみ。**relay (Go) とプロトコルは変更しない**。

## 0. 到達目標

- Galaxy S25 で、着信も発信も **OS 標準の通話画面**になる (ティア A)。
- ティア A が成立しない端末・成立しなかった呼では、**沈黙せずに** ティア B → C と落ちる。
- Echo Show 5 (telephony 無し) は従来どおりティア C で一切変わらない。
- 既存の通話ロジック (`CallHub` / `RelayClient` / `RtpEngine` / 履歴) は**真実の保持者のまま**。
  Telecom は表示層としてだけ足す。

## 1. 用語とティア

```kotlin
enum class CallTier { MANAGED, SELF_MANAGED, LEGACY }
```

| ティア | PhoneAccount | 通話 UI | 成立条件 |
|---|---|---|---|
| `MANAGED` | `CAPABILITY_CALL_PROVIDER` | OS 標準 | Telecom あり + アカウント登録済み + **`callCapablePhoneAccounts` に自ハンドルが含まれる** |
| `SELF_MANAGED` | `CAPABILITY_SELF_MANAGED` | 自前 (`CallActivity`) | Telecom あり + `isIncomingCallPermitted`/`isOutgoingCallPermitted` が true |
| `LEGACY` | 無し | 自前 + 通知 + オーバーレイ (現行) | 常に成立 |

**ティアは呼ごとにセットアップ時点で確定し、通話中は変えない。**

## 2. 新規ファイル

### 2.1 `TelecomCompat.kt`

Telecom API 呼び出しをこのファイルに閉じ込める (`SystemStatus.kt` と同じ方針)。
UI とサービスはここが返す値だけを見る。**例外は全部ここで握り潰し、boolean/null で返す。**

```kotlin
object TelecomCompat {
    const val ACCOUNT_ID_MANAGED = "sipbridge"
    const val ACCOUNT_ID_SELF    = "sipbridge-self"

    fun handle(ctx: Context, id: String): PhoneAccountHandle

    /** 端末に Telecom があるか (TelecomManager + FEATURE_TELECOM または
     *  FEATURE_CONNECTION_SERVICE。**両方の feature 名を見ること**。TELECOM.md §3.5b)。 */
    fun hasTelecom(ctx: Context): Boolean

    /** managed アカウントを登録する。成功で true。 */
    fun registerManaged(ctx: Context, label: String, shortDesc: String): Boolean
    /** self-managed アカウントを登録する。 */
    fun registerSelfManaged(ctx: Context, label: String, shortDesc: String): Boolean
    fun unregister(ctx: Context, id: String)
    fun unregisterAll(ctx: Context)

    /**
     * managed が**今**使えるか。
     * TELECOM.md §3.1: `getPhoneAccount(handle)?.isEnabled` は Samsung で必ず null に
     * なるため使ってはいけない。`callCapablePhoneAccounts.contains(handle)` で判定する。
     */
    fun isManagedEnabled(ctx: Context): Boolean

    /** managed アカウントが登録だけされているか (有効化の案内を出すかの判定用)。 */
    fun isManagedRegistered(ctx: Context): Boolean

    fun isSelfManagedUsable(ctx: Context, incoming: Boolean): Boolean

    /** 発信アカウント設定 (AOSP `EnableAccountPreferenceActivity`) への Intent 候補。 */
    fun enableAccountIntents(ctx: Context): List<Intent>

    fun addNewIncomingCall(ctx: Context, id: String, extras: Bundle): Boolean
    fun placeCall(ctx: Context, number: String, extras: Bundle): Boolean
}
```

- `registerManaged` は `PhoneAccount.builder(...)`
  `.setCapabilities(CAPABILITY_CALL_PROVIDER)`
  `.addSupportedUriScheme(SCHEME_TEL)` `.addSupportedUriScheme(SCHEME_SIP)`
  `.setAddress(Uri.fromParts("tel", <設定の内線番号 or "sipbridge">, null))`
  `.setShortDescription(shortDesc)` `.setIcon(Icon.createWithResource(...))`。
  `shortDescription` は**有効化画面で副題に出る** (TELECOM.md §3.5) ので、
  `R.string.telecom_account_desc` = 「SIP Bridge の内線通話」相当を渡す。
- `registerSelfManaged` は `CAPABILITY_SELF_MANAGED` のみ。`setAddress` は不要。
- `enableAccountIntents` の先頭は `Intent("android.telecom.action.CHANGE_PHONE_ACCOUNTS")`、
  フォールバックに `Settings.ACTION_SETTINGS`。`SystemStatus.startFirstResolvable` で開く。
- **`tel:` の intent-filter は追加しない** (TELECOM.md §3.2)。

### 2.2 `SipConnectionService.kt` + `SipConnection`

```kotlin
class SipConnectionService : ConnectionService() {
    override fun onCreateIncomingConnection(h, req): Connection
    override fun onCreateIncomingConnectionFailed(h, req)
    override fun onCreateOutgoingConnection(h, req): Connection
    override fun onCreateOutgoingConnectionFailed(h, req)
}
```

`SipConnection : Connection()` は **薄い殻**。状態は持たず、操作を `BridgeService` に転送し、
表示を `CallHub` から受ける。

| Telecom からの呼び出し | 転送先 |
|---|---|
| `onAnswer()` / `onAnswer(videoState)` | `BridgeService.ACT_ANSWER` を `startService` |
| `onReject()` / `onReject(reason)` | `ACT_REJECT` |
| `onDisconnect()` | `ACT_HANGUP` |
| `onAbort()` | `ACT_HANGUP` |
| `onPlayDtmfTone(c)` | `CallHub.rtp?.sendDtmf(c)` (in-band。relay には送らない) |
| `onStopDtmfTone()` | 何もしない (in-band は 120ms 固定長) |
| `onCallAudioStateChanged(s)` | `CallHub.rtp?.muted = s.isMuted` **のみ**。経路は Telecom に任せる (§5) |
| `onSilence()` | 何もしない (着信音は Telecom 側) |
| `onStateChanged(state)` | ログのみ |

設定:
- `audioModeIsVoip = true`
- `connectionCapabilities = CAPABILITY_MUTE`
  **`CAPABILITY_HOLD` / `CAPABILITY_SUPPORT_HOLD` は宣言しない**
  (TELECOM.md §3.6: relay に保留が無い。宣言すると保留できるふりになる)
- `setAddress(Uri.fromParts("tel", number, null), PRESENTATION_ALLOWED)`
- `setCallerDisplayName(CallHub.display.ifBlank { number }, PRESENTATION_ALLOWED)`
- self-managed のときだけ `connectionProperties = PROPERTY_SELF_MANAGED`

`onShowIncomingCallUi()` (self-managed のみ): 自前の着信 UI を出す =
`BridgeService` に `ACT_SHOW_INCOMING_UI` を送り、現行の `presentIncoming` の UI 部分
(`CallActivity` 起動 + 2.5 秒フォールバック) を実行させる。

### 2.3 `TelecomCallRegistry.kt`

生成された `SipConnection` を 1 個だけ保持する軽い singleton
(同時通話は relay が 1 本しか扱わないため 1 個で足りる)。

```kotlin
object TelecomCallRegistry {
    @Volatile var connection: SipConnection?
    fun setRinging() / setDialing() / setActive() / setDisconnected(cause: Int) / clear()
}
```

`BridgeService` の `CallHub` 更新点から**必ずここを呼ぶ** (§4 の表)。
`connection == null` (= ティア C の呼) のときは全部 no-op。

### 2.4 `TelecomTierManager.kt`

ティアの決定と、端末ごとの学習 (sticky degradation、TELECOM.md §3.4)。

```kotlin
object TelecomTierManager {
    /** 設定の手動上書き。AUTO が既定。 */
    enum class Pref { AUTO, SYSTEM, APP }   // SYSTEM=OS標準を優先 / APP=アプリ独自に固定

    /** 起動時・設定変更時に呼ぶ。必要なアカウントを登録/解除する。 */
    fun sync(ctx: Context)

    /** この呼で使うティアを決める。 */
    fun decide(ctx: Context, incoming: Boolean): CallTier

    /** ティア落ちを記録する (以後この端末ではそのティアを既定にする)。 */
    fun recordDegrade(ctx: Context, from: CallTier, to: CallTier, reason: String)

    /** アプリ更新・OS 変化を検知したら学習を捨てて再探索する。 */
    fun resetIfEnvironmentChanged(ctx: Context)
}
```

学習の保存先は `BridgeConfig` (§3)。判定順:

```
Pref.APP                         -> LEGACY
!TelecomCompat.hasTelecom()      -> LEGACY
学習上限 (telecomMaxTier) で頭打ち
MANAGED 可 (isManagedEnabled)    -> MANAGED
SELF_MANAGED 可                  -> SELF_MANAGED
それ以外                          -> LEGACY
```

`resetIfEnvironmentChanged`: 保存した `telecomEnvFingerprint`
(`"${BuildConfig.VERSION_CODE}/${Build.VERSION.SDK_INT}/${Build.FINGERPRINT.hashCode()}"`)
と現在値が違えば `telecomMaxTier` を `MANAGED` に戻し、指紋を更新する。

## 3. `BridgeConfig` への追加

```kotlin
/** 通話画面の方式 (自動 / OS 標準 / アプリ独自)。既定 AUTO。 */
val telecomPref: TelecomTierManager.Pref = Pref.AUTO,
/** 学習した上限ティア。ティア落ちのたびに下がる。既定 MANAGED。 */
val telecomMaxTier: CallTier = CallTier.MANAGED,
/** 学習をリセットする条件の指紋。 */
val telecomEnvFingerprint: String = "",
```

`load`/`save` に対応する `getString`/`putString` を足す。**enum は name で保存し、
未知の値は既定に落とす** (`runCatching { valueOf(...) }.getOrDefault(...)`)。

## 4. `BridgeService` の変更

既存メソッドは**消さない**。ティア C の経路は現行のまま残す。

### 4.1 着信 `presentIncoming(callId, from, display, pt)`

```
CallHub を更新 (現行どおり)                     ← ここは共通
tier = TelecomTierManager.decide(ctx, incoming=true)
when (tier) {
  MANAGED, SELF_MANAGED -> {
      currentTier = tier
      ok = TelecomCompat.addNewIncomingCall(...)
      if (!ok) { degrade("addNewIncomingCall false"); presentIncomingLegacy() }
      else watchdog(TELECOM_WATCHDOG_MS) {
          // onCreateIncomingConnection が来ていない = 沈黙失敗
          degrade("no onCreateIncomingConnection"); presentIncomingLegacy()
      }
  }
  LEGACY -> presentIncomingLegacy()
}
```

- `presentIncomingLegacy()` = 現行 `presentIncoming` の UI 部分
  (`startActivity(CallActivity)` + 2.5 秒後の通知/バブル)。**そのまま切り出すだけ**。
- `TELECOM_WATCHDOG_MS = 2000L` (実測 13〜84 ms。TELECOM.md §2)。
- ウォッチドッグは `onCreateIncomingConnection` / `onCreateIncomingConnectionFailed`
  のどちらが来ても解除する。`Failed` なら即フォールバック。
- `extras` に入れるもの:
  `TelecomManager.EXTRA_INCOMING_CALL_ADDRESS` = `Uri.fromParts("tel", from, null)`,
  `TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE` = 自ハンドル,
  `TelecomManager.EXTRA_CALL_SUBJECT` は使わない,
  自前キー `EXTRA_CALL_ID` = relay の callId。
- **relay には何も変化を返さない**。180 を返したままで 25 秒タイムアウトの内側 (TELECOM.md §3.3)。
- SELF_MANAGED のときは `isIncomingCallPermitted` を直前に確認し、false ならティア落ち。

### 4.2 発信 `dialFromAnywhere(to)`

現行の処理を `dialViaRelay(to)` に切り出し、入口を分ける。

```
dialFromAnywhere(to):
  tier = TelecomTierManager.decide(ctx, incoming=false)
  if (tier != LEGACY && TelecomCompat.placeCall(ctx, to, extras)) {
      currentTier = tier
      watchdog(TELECOM_WATCHDOG_MS) { degrade(...); dialViaRelay(to, showUi = true) }
      return                      // 続きは onCreateOutgoingConnection から
  }
  dialViaRelay(to, showUi = true)
```

`SipConnectionService.onCreateOutgoingConnection` は
`startService(ACT_DIAL_TELECOM, EXTRA_TO=<request.address の schemeSpecificPart>)` を投げ、
`BridgeService` はそれを `dialViaRelay(to, showUi = false)` として実行する
(**`CallActivity` は起動しない** — OS 標準画面が出ているため)。

- **標準ダイヤラーからの発信** (ティア A の本命) も同じ経路に入る。
  このとき `BridgeService` は動いていない/未接続のことがあるので、
  `dialViaRelay` の先頭で `ensureConnected()` を呼び、false なら
  `TelecomCallRegistry.setDisconnected(DisconnectCause.ERROR)` して終わる。
- `placeCall` の extras:
  `TelecomManager.EXTRA_PHONE_ACCOUNT_HANDLE` = 自ハンドル、
  `TelecomManager.EXTRA_OUTGOING_CALL_EXTRAS` に自前キー `EXTRA_FROM_APP=true`。
- 番号は**加工しない** (TELECOM.md §2: 正規化は起きない)。
  `request.address` が `tel:` なら `schemeSpecificPart`、`sip:` なら `@` の前を取る。
- `CALL_PHONE` 権限が要る。**未許可なら placeCall を試さず LEGACY に落とす**
  (例外を投げさせない)。`MANAGE_OWN_CALLS` は self-managed 用で宣言済み。

### 4.3 状態の反映 (`TelecomCallRegistry` の呼び先)

| BridgeService の地点 | 呼ぶもの |
|---|---|
| `onRinging` (early 含む) | `setDialing()` (発信呼出中) |
| `answerFromAnywhere` | 何もしない (Telecom 側が `onAnswer` の起点。`onAnswered` で `setActive`) |
| `onAnswered` | `setActive()` |
| `onEnded(reason)` | `setDisconnected(cause)` → `clear()` |
| `rejectFromAnywhere` | `setDisconnected(REJECTED)` → `clear()` |
| `hangupFromAnywhere` | `setDisconnected(LOCAL)` → `clear()` |

`reason` → `DisconnectCause` の対応:
`bye`→`REMOTE`, `cancel`→`CANCELED`, `reject`→`BUSY`, `timeout`→`MISSED`,
`answered_elsewhere`→`ANSWERED_ELSEWHERE` (API 25+), その他→`UNKNOWN`。

**`setDisconnected` の後は必ず `destroy()`** (`TelecomCallRegistry.clear()` の中で行う)。
これを忘れると OS 標準画面が残り続ける。

### 4.4 UI の抑制

ティアが `MANAGED` の呼では、次を**出さない**:
- `CallActivity` の自動起動 (着信・発信・応答時すべて)
- 着信通知 (`NotificationHelper.ID_INCOMING`) とオーバーレイバブル
- 通話中ピル (`showInCallPill`) — OS 標準の通話中通知があるため

`SELF_MANAGED` と `LEGACY` では現行どおり全部出す。
判定は `currentTier == CallTier.MANAGED` の 1 箇所を見るヘルパ
`private fun suppressOwnUi(): Boolean` にまとめる。

`currentTier` は `CallHub` ではなく `BridgeService` のフィールドに持ち、
`CallHub.resetCall()` のタイミングで `LEGACY` に戻す。
ただし **`CallActivity` がティアを知る必要がある** ため `CallHub.tier` (@Volatile) も置き、
`CallActivity` はティア A のとき `finish()` する (誤って開かれた場合の保険)。

## 5. 音声経路

- ティア A/B では `Connection.audioModeIsVoip = true` により Telecom が
  オーディオフォーカスと `MODE_IN_COMMUNICATION` を管理する。
  `applyInCallAudioRoute()` は**呼ばない**。代わりに `onCallAudioStateChanged` で
  `AudioRoute.setSpeaker(am, route == ROUTE_SPEAKER)` を反映する。
  ただし `restoreAudioRoute()` は現行どおり終了時に呼ぶ (no-op になる)。
- ミュートは `CallAudioState.isMuted` → `CallHub.rtp?.muted`。
- **`onCallAudioStateChanged` で `AudioRoute` を触ってはいけない** (レビューで判明)。
  API 31+ の `AudioRoute.setSpeaker(am, false)` は `setCommunicationDevice(EARPIECE)` に
  なるため、Telecom が選んだ経路 (Bluetooth・有線ヘッドセット) を**後勝ちで奪う**。
  ティア A で OS 画面から Bluetooth を選んでも受話口に引き戻されてしまう。
  ティア A/B では経路は Telecom に任せ、反映するのは**ミュートだけ**にする。
- ティア C は現行のまま一切変えない。

## 6. マニフェスト

`android/app/src/main/AndroidManifest.xml`:

```xml
<uses-permission android:name="android.permission.CALL_PHONE" />
<uses-permission android:name="android.permission.READ_PHONE_STATE" />
<!-- MANAGE_OWN_CALLS は宣言済み -->

<service
    android:name=".SipConnectionService"
    android:exported="true"
    android:permission="android.permission.BIND_TELECOM_CONNECTION_SERVICE">
    <intent-filter>
        <action android:name="android.telecom.ConnectionService" />
    </intent-filter>
</service>
```

`tel:` の `intent-filter` は**足さない**。
`android/app/src/probe/AndroidManifest.xml` に
`<service android:name=".SipConnectionService" tools:node="remove" />` を足し、
probe ビルドで本番の ConnectionService が載らないようにする。

`CALL_PHONE` はランタイム権限。`SetupSheet` の行 (§7) から要求する。
**未許可でもアプリは従来どおり動く** (ティア C に落ちるだけ)。

## 7. 設定 UI

### 7.1 `SettingsFragment`
「通話」セクションに 1 行足す:
- **通話画面** — ドロップダウン/ダイアログで「自動 (推奨) / OS 標準 / アプリ独自」。
  選択で `BridgeConfig.telecomPref` を保存し `TelecomTierManager.sync(ctx)` を呼ぶ。
- 直下に現在の実効ティアを 1 行で出す
  (例: 「現在: OS 標準の通話画面を使用中」/「現在: アプリ独自 (通話アカウントが無効)」)。
- Telecom が無い端末 (`hasTelecom()==false`) では**行ごと出さない**
  (`SystemStatus` の非対応 null と同じ流儀)。

### 7.2 `SetupSheet` / `SystemStatus`
- `SystemStatus` に追加:
  - `callPhoneGranted: Boolean`
  - `telecomAccountEnabled: Boolean?` — Telecom 非対応端末や `telecomPref == APP` では `null`
    (行を出さない)。
- `SetupSheet.Item` に `TELECOM_ACCOUNT` を足す (順序は OVERLAY の後、FULLSCREEN の前)。
  - 未許可バッジは **「推奨」** (BLOCKING ではない)。
  - アクション: `CALL_PHONE` 未許可ならまずランタイム権限、
    許可済みなら `TelecomCompat.enableAccountIntents(ctx)` を開く。
- `HealthCheckReceiver` の 1 日 1 回の健康診断に
  「通話アカウントが無効になっています」を足す
  (`telecomAccountEnabled == false` かつ `telecomPref != APP` のとき)。
  既存の `Issue` の作りに合わせること。

### 7.3 文言
すべて `res/values/strings.xml` に追加し、ハードコードしない。日本語。

## 8. テスト (`android/app/src/test/`)

Robolectric は入れない。**JVM で回る純粋ロジックだけ**テストする:

- `TelecomTierManagerTest`: `decide()` の判定表
  (`Pref.APP`→LEGACY / Telecom 無し→LEGACY / `telecomMaxTier` による頭打ち /
  managed 可→MANAGED / self のみ可→SELF_MANAGED)。
  端末依存部分は関数引数 (`hasTelecom: Boolean`, `managedOk: Boolean`, `selfOk: Boolean`)
  に切り出した `decideFrom(...)` を作ってテストする。
- `DisconnectCause` 対応表 (`reason` 文字列 → Int) を純関数
  `TelecomCompat.disconnectCauseFor(reason: String): Int` に切り出してテスト。
- 発信番号の取り出し (`tel:2104` / `sip:2104@example` / `2104`) の純関数
  `TelecomCompat.numberFrom(uri: String): String` をテスト。
- `BridgeConfig` の enum 復元 (未知文字列 → 既定) は
  `BridgeConfig` を触らずに済むよう小さな純関数に切り出してテストしてよい。

既存テストは壊さないこと。

## 9. バージョン

`android/app/build.gradle`: `versionCode = 6`, `versionName = "1.5"`。

## 10. 禁止事項 (再掲)

- `getPhoneAccount(handle)?.isEnabled` で有効化を判定しない (§3.1 / TELECOM.md §3.1)。
- `tel:` の intent-filter を足さない。
- `CAPABILITY_HOLD` を宣言しない。
- `relay/`・`docs/PROTOCOL.md` を変更しない。
- ティア C のコード (`CallActivity` / `CallOverlayManager` / `AudioRoute` /
  `NotificationHelper`) を削除しない。
- Telecom の呼び出しで例外を上げない (全部 `runCatching`)。落ちるくらいならティア C。
