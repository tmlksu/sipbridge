# Telecom 統合 (OS 標準の通話画面) — 方式検討と実機検証結果

検証日: 2026-09-17 / 検証: Claude Opus 5 (実機プローブ)

## 0. 背景

実機を使ってもらったユーザーからの指摘 2 点:

1. **着信時の画面が標準と違うので分かりづらい** (「使い慣れたものじゃないと使い方が分からない」)
2. **架電のグループや連絡先の見方が発信経路によって違う**。経路ごとに分けたいのではなく、
   **同じところで使い分けたい**

対象はスマホ (Galaxy S25)。Echo Show 5 は telephony も InCallService も持たないため、
この文書の「ティア A」は原理的に適用できない (§4)。

## 1. 3 ティア構成

Android は機種・ROM 差が大きく、managed が成立しない端末が現実にある。
**単一方式に賭けず、呼ごとにティアを決めて落とせる**構造にする。

| ティア | 方式 | 通話画面 | 成立条件 |
|---|---|---|---|
| A | managed (`CAPABILITY_CALL_PROVIDER`) | **OS 標準** | Telecom あり + InCallService あり + **ユーザーがアカウントを有効化** |
| B | self-managed (`CAPABILITY_SELF_MANAGED`) | 自前 | Telecom あり (`MANAGE_OWN_CALLS` のみ。ユーザー操作不要) |
| C | 現行 (Telecom 不使用) | 自前 + 通知 + オーバーレイ | 常に成立 |

ティア B が挟まることで、A が駄目でも Bluetooth の通話ボタン・通話競合の仲裁は生き残る。

### 設計の要: `CallHub` を真実の保持者のまま残す

Telecom は**状態機械ではなく表示層**として扱う。`Connection` は `CallHub.session` へ委譲する
薄い殻にする。これをやっておくと**呼のセットアップ中にティアを落とせる**。
逆に Telecom に状態を持たせると、フォールバックした瞬間に整合が取れなくなる。

ティア判定は**呼ごとにセットアップ時点で確定させ、通話中は固定**する。
確立後の切り替えはやらない。

```
RelayClient events ──▶ CallHub (唯一の真実。今と同じ)
                          ├─▶ TelecomManagedPresenter    (ティア A)
                          ├─▶ TelecomSelfManagedPresenter (ティア B)
                          └─▶ LegacyPresenter             (ティア C: 現行コード)
```

ティア C が残る以上、`AudioRoute.kt` / `CallActivity` / `CallOverlayManager` は**削除しない**。

## 2. 実機検証の結果

プローブ: `android/app/src/probe/` (probe ビルドタイプ。applicationId は `.probe` で本番と同居可)。
操作: `scripts/telecom-probe.sh <serial>` (adb) または端末上の `ProbeActivity` (adb 無し)。

| # | 項目 | Galaxy S25 (One UI 8.0 / API 36) | TINNO P780 (Android 11 / Google Dialer) |
|---|---|---|---|
| 1 | `CALL_PROVIDER` 登録 | OK (登録直後は無効) | OK (登録直後は無効) |
| 2 | 有効化の導線 | OK。One UI の「発信アカウント」に **SIM と同列**で並ぶ | OK |
| 3 | 標準の着信画面 | **出る**。消灯から自動点灯 → 全画面着信 | **出る**。点灯中はヘッドアップ |
| 4 | 応答 | OK (`KEYCODE_CALL` = ヘッドセットの通話ボタンで `onAnswer`) | OK |
| 5 | **内線番号の正規化** | **起きない** (`2104`/`2105`/`09012345678` すべて無改変) | **起きない** |
| 6 | 標準の通話中画面 | 出る (録音/消音/Bluetooth/スピーカー/キーパッド) | 出る |
| 7 | self-managed (ティア B) | 未検証 | OK (`onShowIncomingCallUi` も正常) |

`addNewIncomingCall` → `onCreateIncomingConnection` の実測:

| 端末 | managed | self-managed |
|---|---|---|
| S25 | **13 ms** | — |
| P780 | 32〜84 ms | 36 ms |

`placeCall` → `onCreateOutgoingConnection` は 125〜216 ms。

→ **フォールバック用ウォッチドッグは 1.5〜2 秒で十分**。沈黙失敗と正常を取り違える余地はない。

懸念していた **Samsung の内線番号の書き換えは発生しなかった**。
システムの発信入口 (`com.android.server.telecom/.components.UserCallActivity`) 経由でも無改変。

## 3. 実装時の注意 (実測で判明)

### 3.1 Samsung では `getPhoneAccount()` が自分のアカウントに null を返す

有効化済みでも `TelecomManager.getPhoneAccount(handle)` が `null` を返す (AOSP の P780 では
正しく返る)。`getPhoneAccount()?.isEnabled` で分岐すると **Samsung で必ずフォールバックに落ちる**。

```kotlin
// NG: Samsung で常に false になる
val ok = tm.getPhoneAccount(handle)?.isEnabled == true
// OK
val ok = tm.callCapablePhoneAccounts.contains(handle)
```

### 3.2 `tel:` の intent-filter は追加しない

S25 では `tel:` を Groundwire も握っているため、素の `ACTION_CALL` だと Telecom の手前で
「アプリを選択」が挟まる。**これ自体が「発信経路によって見方が違う」の一因の可能性がある**。
managed アカウントとして Telecom に出れば標準の連絡先・ダイヤラーから選べるので、
`tel:` の intent-filter を足すとアプリ選択ダイアログを一段増やすだけになる。

### 3.3 沈黙失敗のウォッチドッグが実装の本体

例外が飛ぶケース (`registerPhoneAccount` の失敗、`isIncomingCallPermitted()` が false、
`addNewIncomingCall` の例外) は簡単。危険なのは**例外は出ないが画面が出ない**沈黙失敗で、
これは ROM 由来で普通に起きる。

```
addNewIncomingCall() の時刻を記録
  → 1.5〜2 秒以内に onCreateIncomingConnection が来なければ
     Telecom を諦め、その呼はティア C で自前 full-screen intent を出す
     (relay には 180 を返したまま、25 秒タイムアウトの内側)
```

発信側も `placeCall` 後の `onCreateOutgoingConnection` で同様に。

### 3.4 端末ごとに学習させる (sticky degradation)

F-Droid 配布で段階リリースができないため、端末ごとに黙って賢くなる作りにする。

- `BridgeConfig` に「実績のあるティア」を保存。ティア落ちが起きたら記録し、以後その端末では
  そのティアを既定にする。
- アプリ更新・OS バージョン変化を検知したら**再探索** (ROM 更新で直ることがある)。
- 設定に手動上書き「通話画面: 自動 / OS 標準 / アプリ独自」。自動が既定。

既存の `SystemStatus.kt` / `PushHealth.kt` / `HealthCheckReceiver.kt` がそのまま使える。
「通話アカウントが無効になっています」を `Issue` として足せば、ユーザーが設定でオフに戻した
場合も 1 日 1 回の健康診断で気づいて通知できる。

### 3.5 有効化の導線

`Intent("android.telecom.action.CHANGE_PHONE_ACCOUNTS")` で AOSP 標準の
`EnableAccountPreferenceActivity` に直行できる (One UI 8 でも同じ)。
`PhoneAccount.setShortDescription()` が副題として表示されるので、ここに説明を入れる。
`SetupSheet` (§6.4) に「通話アカウントを有効化」の行を足す。

### 3.6 その他

- 登録解除しても Telecom の `defaultOutgoing` に stale な参照が残る (実害なし)。
  ティア切替でアカウントを外すときは意識しておく。
- `Connection.setCallerDisplayName()` に渡した名前は標準 UI にそのまま出る。
  標準 UI は端末連絡先しか名前解決しないので、relay の `display` はここで渡す。
- 保留は relay プロトコルに無いため `CAPABILITY_HOLD` を宣言できない。通話中に携帯着信が
  来ると「終了して応答」のみになる。v1 では許容し、relay の re-INVITE 対応で後から足す。

## 4. 連絡先の一元化 (指摘 2)

現状はリストが実質 3 つある:

```
アプリ内 ContactStore   … グループ「ウチ」「ソト」(contacts.json)
アプリ内「端末」グループ … READ_CONTACTS で読んだもの (既定 OFF)
標準の連絡先アプリ      … 別世界
```

**これは Telecom の成否と独立に解決できる**:

- データの一元化 = 端末連絡先を唯一の正にする。`DeviceContacts.kt` が既にあるので、
  設定「端末の連絡先を表示」を既定 ON にし、`ContactStore` を移行専用に降ろす。
  既存データの書き出し (`WRITE_CONTACTS`、初回 1 回) を設定に用意する。**ティア C でも成立する**。
- 発信の入り口が標準アプリになるのはティア A のときだけ。B/C では入り口が sipbridge のままだが、
  **見えるリストと名前は同じ**になるので「発信経路で見方が違う」は解消される。

番号ごとの既定アカウントは、連絡先プロバイダの
`preferred_phone_account_component_name` / `preferred_phone_account_id` 列で保持される
(One UI 8 にも存在。現在すべて NULL)。優先アカウントを SIM のままにして、
内線の番号だけ sipbridge に振る形が狙い。

**未確認**: One UI 8 の「発信アカウント」設定に「毎回確認」の選択肢が見当たらない
(Rakuten / UQ / 第三者アカウントの単一選択のみ)。番号ごとの既定アカウントを
**ユーザーがどの UI から設定できるか**は、実アカウントを常設しないと確かめられない。
本実装の段階で確認する。

## 5. 端末ごとの適用

| 端末 | ティア | 備考 |
|---|---|---|
| Galaxy S25 | A (フォールバック B → C) | 検証済み |
| Echo Show 5 | **C 固定** | telephony feature も InCallService も無い (`pm list packages` で確認済み)。 `com.android.server.telecom` はあるので B は動く可能性があるが未検証 |

Echo Show 向けの大きいボタン UI・オーバーレイの通話ピルはティア C で維持される。

## 6. 進め方の提案

1. **v1.5**: 連絡先の一元化 (§4) + `NotificationCompat.CallStyle`。Telecom 不要、全端末で効く。
2. **v1.6**: Presenter の抽象化 + ティア B/C + ウォッチドッグ機構。
3. **v1.7**: ティア A。土台とフォールバックが既にあるので、追加は `PhoneAccount` の
   capability と有効化導線が中心。

## 7. プローブの使い方

```bash
# ビルド (本番と別 applicationId: io.github.tmlksu.sipbridge.probe)
cd android && ~/android-build/gradle-8.9/bin/gradle :app:assembleFossProbe --no-daemon

# adb がある端末
scripts/telecom-probe.sh <serial>

# adb が無い端末: APK を手で入れてランチャーの「sipbridge probe」から操作
```

**SIM が入った実機で使うときの注意**:

- 既定の発信アカウントが SIM の端末では、`ProbeActivity` の「5B. 発信 標準経路」ボタンを
  押すと**実際に携帯発信してしまう**。押さないこと。
- `scripts/telecom-probe.sh` は `telecom set-user-selected-outgoing-phone-account` を実行する。
  SIM 入りの実機では使わないか、**実行前に `dumpsys telecom | grep defaultOutgoing` で
  元の値を記録し、必ず戻すこと**。
- 検証が終わったら「6. 解除」→ アンインストール。アンインストールすれば PhoneAccount ごと消える。
- Samsung 端末を USB で繋ぐには udev ルールが要る:
  `SUBSYSTEM=="usb", ATTR{idVendor}=="04e8", MODE="0666", GROUP="plugdev"`
