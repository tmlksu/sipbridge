# SIP Bridge アプリ — 導入・試験手順 (Nocturne UI, v1.3)

APK: `app/build/outputs/apk/foss/debug/app-foss-debug.apk` /
`app/build/outputs/apk/gms/debug/app-gms-debug.apk`
(package `io.github.tmlksu.sipbridge`。旧 EchoSIP と共存可)

> v1.2 以前は `net.peyan.sipbridge` だった。ID が変わったので上書き更新はできない。
> 先に `adb uninstall net.peyan.sipbridge` してから入れること (設定は入れ直しになる)。

前提: relay が Docker で起動し、Asterisk に登録できる状態であること
(`docs/SETUP.md` の手順済み。relay URL と Access Service Token を控えておく)。
**SIP アカウント (内線番号/パスワード) はアプリ側で設定する**
(relay 側 env ではない。`docs/PROTOCOL.md` の「SIP アカウント」節)。

## 1. 導入

1. 端末を PC と `adb` 接続 (`adb devices` で認識確認)
2. flavor を選んでインストール
   - Echo Show 5 (GApps 無し): `adb install -r app-foss-debug.apk`
   - Galaxy S25: gms APK (`app-gms-debug.apk`)
3. アプリ「SIP Bridge」を開く (起動時はキーパッドタブ)

## 2. 初期設定 (設定タブ。上から順に)

設定タブの構成: 状態カード / 接続 / **SIP アカウント** / 動作 / 音声 / 権限 /
テスト / フッター (詳細は `DESIGN.md` §2-§3)。

| 項目 | 例 | 備考 |
|---|---|---|
| relay URL | `wss://relay.example.com` | `https://…` やホスト名だけでも可 (自動正規化) |
| Access Client ID | `xxxx.access` | 本番。空なら Dev Token 方式 |
| Access Client Secret | `***` | 暗号化保存 |
| Dev Token | `***` | 開発用 relay (`AUTH_MODE=token`) のとき使用 |
| **内線番号 (SIP user)** | `101` | **SIP アカウント。relay がこの内線で Asterisk に登録する** |
| **パスワード** | `***` | **SIP アカウントのパスワード (暗号化保存)** |
| **表示名 (任意)** | `母屋` | 空でもよい。発信先に表示される名前 |
| 接続モード | 常時接続 / PUSH 起床 | Echo Show=常時接続、S25=PUSH 起床 |
| 起動時に自動開始 | ON | 再起動後も接続を復帰 |
| オーバーレイ着信 | ON | 他アプリの上にバブル・通話中ピルを重ねる |
| スピーカーで応答 | OFF | 応答時にスピーカー出力にする (既定 OFF)。受話口の無い端末 (Echo Show) では OFF でもスピーカーになる |
| 端末の連絡先を表示 | OFF | ON にすると連絡先タブに「端末」グループを表示 (READ_CONTACTS をその場で要求。拒否で OFF に戻る) |
| 端末の連絡先を表示 | OFF | 連絡先タブに端末の連絡先 (「端末」グループ) を混ぜる。ON で `READ_CONTACTS` を要求 |
| マイクゲイン | `2.0` | Echo Show は 1.5〜3.0 で調整 |

同じ内線を複数端末に設定すると同時着信し、先に応答した端末が通話する
(1 内線 = 同時 1 通話)。

保存 (ダイアログ OK) で即反映され、接続に関わる項目は Bridge が再起動する。
保存前の検証: relay URL 必須・正規化可、「Access Client ID か Dev Token の
どちらか必須」。状態カードが `● relay に接続中` + `登録OK: 101` になれば成功。
`○未登録` の場合は relay URL・Access 値・ネットワークを確認
(`adb logcat -s BridgeService RelayClient`)。
`hello.account` が空 (SIP アカウント未設定) のときは状態欄に
「SIP アカウント未設定: 設定タブで内線番号を入力してください」と出る。

## 3. adb での設定投入 (入力しづらい端末用。debug ビルドのみ)

`scripts/adb-setup.sh` が APK インストール + 権限付与 + 設定投入を一括で行う
(debug 専用 `DEBUG_SET_CONFIG` receiver。release には含まれない)。

```
# 例: Echo Show に foss を入れて内線 101 を設定し再起動まで行う
# (秘密はコマンド履歴に残らないよう $(cat file) で渡すこと)
scripts/adb-setup.sh <serial> foss \
  relayUrl=wss://relay.example.com \
  sipUser=101 sipPassword="$(cat ~/relay-sip-pass.txt)" \
  mode=PERSISTENT restart=true
```

`--no-install` で設定投入のみも可。`key=value` の一覧
(`DEBUG_SET_CONFIG` の extras。全て任意。受け取ったキーだけ上書き):

| キー | 値 |
|---|---|
| `relayUrl` | `wss://…` |
| `accessClientId` / `accessClientSecret` / `devToken` | 認証情報 |
| `sipUser` / `sipPassword` / `sipDisplay` | SIP アカウント (内線番号/パスワード/表示名) |
| `mode` | `PERSISTENT` / `PUSH` |
| `micGain` | 例 `2.5` |
| `overlayEnabled` / `autostart` / `speakerOnAnswer` / `deviceContactsEnabled` | `true` / `false` (boolean extra `ez` でも可) |
| `restart` | `true` で BridgeService を再起動して反映 |

E2E 用の通話操作もある (`DEBUG_CALL_ACTION`: `action=answer|reject|hangup|dial`,
`to=<番号>` は dial のみ)。

## 4. 権限の許可 (重要・常駐のため)

設定タブの権限カードから状態確認・設定画面へ飛べる。

1. 「他のアプリの上に表示」→ 許可 (着信バブル・通話中ピル用)
2. 「電池の最適化を除外」→「最適化しない」 (常駐切断防止)
3. 「通知」→ 許可 (着信はフルスクリーンインテント)
4. 「全画面通知 (API 34+)」→ 許可 (S25。API 34 未満は「対象外」)

### Echo Show 5 (横画面 960×480) の注意

- 通話画面とキーパッドは横画面レイアウト (`layout-land`) で左右 2 分割表示になる。
- 履歴/連絡先/設定は縦画面版のまま (履歴はリストが縮み、連絡先/設定はスクロール)。
  主要操作 (応答/拒否/終了/発信) はスクロール無しで見える。
- 常時電源・常時 Wi-Fi。ディープスリープ無効運用。`foss` + 常時接続で使う。

### Galaxy S25 (API 35) の注意

- `gms` + PUSH 起床で使う (foss ビルドでは PUSH は使えない)。
- **全画面通知**: API 34+ では権限カードの「全画面通知」を許可しないと
  ロック画面の着信全画面が出ない。設定タブから許可画面へ飛べる。
- **オーバーレイ**: 「他のアプリの上に表示」を許可しないと通話中ピルが出ず、
  常駐通知の「通話中 mm:ss — タップで戻る」に代替される。
- **電池**: 「電池の最適化を除外」しないと Doze で WSS/FCM が遅延する。
  通話終了 60 秒後に「待機中 (PUSH 起床)」表示・WSS 切断されるのが正常。

## 5. 履歴・連絡先 (アプリ内保存)

- 履歴 (`HistoryFragment`) と連絡先 (`ContactsFragment`) のデータは
  **端末内の JSON ファイル** (`filesDir/history.json`, `filesDir/contacts.json`) に保存する。
  Asterisk からの取り込みはしないし、relay へ送信することもない。
  機種変更時は引き継がれない (`allowBackup=false`)。
- 履歴: 着信応答=IN、未応答 (タイムアウト/キャンセル/拒否/他端末応答)=不在着信、
  発信=OUT。通話時間を記録する。行タップで再発信、長押しで削除/連絡先に追加。
- 連絡先: グループ「ウチ」(内線・家族)/「ソト」(外線)。着信時の名前解決に使う
  (relay の表示名が空・番号のみのとき。端末の連絡先表示を ON にすれば端末側も解決対象)。初期データは空。
  追加ダイアログの「端末の連絡先から選ぶ」で端末の連絡先から番号単位で取り込める (権限不要)。

## 6. 着信試験

1. 設定タブの「テスト着信を表示」で全画面 UI の表示確認 (relay 不要)
2. 実試験: 別内線から relay の内線へ発信
   - 両端末 (S25 + Echo Show) に着信表示 → 片方で応答 → 双方向音声
   - 相手が切れば「通話終了」。応答せず切れば不在着信に記録
   - 25 秒無応答で発呼側に 480 (relay 側動作)
3. 発信試験: キーパッド入力・履歴・連絡先から発信 → 相手端末が鳴動 → 応答で通話
4. 通話中操作: ミュート/スピーカー/DTMF キーパッド (in-band。Asterisk 側
   `dtmfmode=inband`) / 縮小 (ピル→タップで復帰) / 終了確認
5. ログ取得: `adb logcat -s BridgeService RelayClient RtpEngine CallActivity`

## 7. PUSH モード (S25) の確認

- 通話終了 60 秒後に「待機中 (PUSH モード)」表示・WSS 切断されること
- FCM 起床からの着信は `docs/E2E.md` の手順で試験

## 8. 再ビルド

```
export JAVA_HOME=~/android-build/jdk PATH=~/android-build/jdk/bin:$PATH
export ANDROID_HOME=~/android-build/sdk ANDROID_SDK_ROOT=$ANDROID_HOME
cd android && ~/android-build/gradle-8.9/bin/gradle \
  :app:assembleFossDebug :app:assembleGmsDebug :app:testFossDebugUnitTest --no-daemon
```

UDP 撤去の確認: `grep -r DatagramSocket android/app/src` がゼロ件であること。
