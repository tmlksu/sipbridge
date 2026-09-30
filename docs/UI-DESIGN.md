# SIP Bridge アプリ UI 設計 (v1.4) — `docs/design/android-app-design.pdf` の文章化

作成: 2026-09-16。原本は Claude Design のアートボード 4 枚 (Galaxy S25 縦)。
実装対象は Android アプリ (`android/`)。**Echo Show 5 (960×480 横, API 30) でも崩れないこと**が
追加要件 (原本は縦画面のみ)。

## 0. トーン (Nocturne)

- サブ利用の通話アプリ。派手な演出なし。角丸カード + 余白で静かな密度。
- ダークのみ (DayNight 不要)。トークン (`res/values/colors.xml` に定義して全画面で使う):

| トークン | 値 | 用途 |
|---|---|---|
| `nocturne_bg` | `#0B0B11` | 画面背景 |
| `nocturne_surface` | `#14141C` | カード / 入力欄 / ボトムナビ |
| `nocturne_surface_hi` | `#1C1C27` | 押下・選択中のカード、キーパッドの丸ボタン枠内 |
| `nocturne_border` | `#262633` | カード枠線 (1dp) / 区切り線 |
| `nocturne_text` | `#ECECF4` | 主文字 |
| `nocturne_text_dim` | `#8B8BA0` | 補助文字 (番号・時刻・説明) |
| `nocturne_accent` | `#6B5FD6` | 応答/発信ボタン、選択タブ、状態ドット (登録OK) |
| `nocturne_accent_soft` | `#2A2652` | 選択中セグメントの背景、アバター円の塗り |
| `nocturne_accent_text` | `#C9C2FF` | アバターの文字、アクセント上の薄い文字 |
| `nocturne_neutral_btn` | `#2A2A36` | 拒否/終了/切断ボタン (赤は使わない。受話器ダウンのアイコンで区別) |
| `nocturne_missed` | `#D98A8A` | 不在着信の矢印/文字 (唯一の警告色) |

- 角丸: カード 16dp、入力行 12dp、丸ボタン (キーパッド 72dp / 通話操作 64dp / 応答・終了 72dp)。
- フォント: システム既定。数字表示 (入力中の番号) は 40sp light。
- アイコン: Material Icons (vector drawable を `res/drawable` に置く。外部依存追加不要)。

## 1. 画面構成 (待機時の 3 つの入口 + 設定) — `MainActivity` + BottomNavigationView

下部ナビ 4 タブ: **キーパッド / 履歴 / 連絡先 / 設定**。起動時はキーパッド。
各タブは Fragment。ナビは `nocturne_surface`、選択中は `nocturne_accent`。
アイコンは自前の vector (`res/drawable/ic_nav_*.xml`。framework の `@android:drawable/ic_menu_*` は
解像度・余白が合わず潰れるため使わない)。
**インセット**: targetSdk 35 (Android 15) は edge-to-edge が強制されるため、`MainActivity` が
システムバーのインセットを一括で配る (上・左右 = コンテンツの padding、下 = ボトムナビの padding)。
ナビの高さは `wrap_content` (固定 56dp のままジェスチャーバー分の padding が入ると
アイコンとラベルが重なって潰れる)。
**横画面はボトムナビではなく左サイドレール** (`NavigationRailView`, `layout-land/activity_main.xml`)。
Echo Show 5 は利用可能高さが 321dp しかなく、下にナビを置くとキーパッドが収まらないため。
どちらも `NavigationBarView` なので `MainActivity` の扱いは共通。

### 1.1 キーパッド (`KeypadFragment`)
- 上部左: 状態ピル「● 登録OK・内線 101」(ドットは登録OK=accent / 未登録=text_dim / 未接続=border)。
  上部右: チップ「常時接続」または「PUSH 起床」(接続モード)。タップで設定タブへ。
- 中央: 入力中の番号 (40sp)。下にキャプション: 入力が連絡先に一致すれば名前、
  内線 (2〜4 桁) なら「内線番号」、それ以外は「外線」。未入力時は
  「番号を入力、または履歴・連絡先から発信」。
- 3×4 の丸キー (1 / 2 ABC / 3 DEF / 4 GHI / 5 JKL / 6 MNO / 7 PQRS / 8 TUV / 9 WXYZ / * / 0 + / #)。
  数字は 28sp、下の英字は 9sp text_dim。0 長押しで「+」。
  横画面は縦が足りないため、キーは角丸 (スタジアム) 形で高さを画面高から算出 (40〜56dp)。
  48dp 未満になる端末では英字を省き数字だけにする (`KeypadFragment.keyHeightPx`)。
- **触覚フィードバック (v1.2)**: **キーパッドのキーと ⌫ を押したときだけ**短く振動させる
  (`View.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP)`。⌫ の長押し (全消去) と
  0 の長押し (+) は `LONG_PRESS`)。一般的な通話アプリと同じ挙動。
  **端末の触覚フィードバック設定が OFF なら振動しない** (`FLAG_IGNORE_GLOBAL_SETTING` は使わない)。
  ナビ/タブ、履歴・連絡先の行、設定のトグル、発信・応答・終了ボタンでは振動させない
  (§3.1 の DTMF シートのキーだけは同じ扱いで振動させる)。
- 最下部: 中央に accent の丸い発信ボタン (受話器アイコン, 72dp)、その右に削除 (⌫、長押しで全消去)。
  **キー・発信ボタンは画面下端寄せ** (片手操作。余白は状態ピルとの間に寄せ、番号表示はキーのすぐ上)。
  発信中/通話中は発信ボタンの代わりに「通話に戻る」ピル (§3.2)。
- 発信: `BridgeService.ACT_DIAL` を投げ、発信画面 (§2.3) へ遷移。

### 1.2 履歴 (`HistoryFragment`)
- 見出し「履歴」。セグメント「すべて | 不在着信」。
- 1 枚のカードに行を並べる (区切り線 border)。行: 左に方向アイコン円 (↙ 着信 / ↗ 発信 /
  ↙ 不在着信は `nocturne_missed`)、名前 (連絡先一致名。無ければ番号)、
  2 行目「着信・101」「不在着信・110」「発信・03-1234-5678」、右端に時刻
  (今日=HH:mm / 昨日=「昨日」/ それ以前= M/d)。
- カード下の注記: 「行をタップすると発信します。relay 経由のため端末では 5060 を待ち受けません。」
- 行タップで再発信。長押しで「削除」「連絡先に追加」。右上に「全消去」メニュー。
- 永続化: `HistoryStore` (アプリ内 JSON ファイル, 最大 200 件)。書き込みは `BridgeService`
  (着信提示→ answered なら IN、未応答/他端末応答/タイムアウトは MISSED、dial は OUT。
  通話時間を記録)。空のとき「履歴はまだありません」。

### 1.3 連絡先 (`ContactsFragment`)
- 見出し「連絡先」。検索欄 (🔍 名前・内線番号で検索)。
- グループ見出し「ウチ」「ソト」(小文字 text_dim)。各グループ 1 枚のカード。
  行: アバター円 (名前の 1 文字目, accent_soft/accent_text)、名前、番号、右端に受話器アイコン。
- 行タップ = 発信。長押し = 編集/削除。右下 FAB「＋」で追加ダイアログ
  (名前 / 番号 / グループ (ウチ/ソト))。
- 永続化: `ContactStore` (アプリ内 JSON)。着信時の表示名解決に使う
  (relay の `display` が空、または番号のみのとき `ContactStore.lookup(number)`)。
- 初期データは空 (原本の 母屋/離れ/玄関インターホン 等はダミー)。空のとき
  「連絡先を追加すると、着信時に名前で表示されます」。

#### 端末の連絡先との連携 (v1.2)
1 人に複数番号があるケースを素直に扱うため、**番号 1 件を単位**にする。実装は
`DeviceContacts.kt` に隔離する (権限判定 + クエリのみ。UI は持たない)。

- **取り込み (権限不要。既定の導線)**: 追加ダイアログに「端末の連絡先から選ぶ」ボタンを置き、
  `Intent(Intent.ACTION_PICK, ContactsContract.CommonDataKinds.Phone.CONTENT_URI)` を
  `registerForActivityResult(StartActivityForResult)` で開く。**連絡先ではなく電話番号を選ばせる**
  ピッカーなので、複数番号を持つ人でもシステム UI 側で「携帯 / 自宅 / 勤務先」を選べる。
  返り値の URI には一時的な読み取り権限が付くため `READ_CONTACTS` は要らない。
  取得した `DISPLAY_NAME` / `NUMBER` をダイアログの欄に流し込み (グループ既定は「ソト」)、
  「保存」で `ContactStore` に取り込む。連絡先アプリが無い端末は
  `ActivityNotFoundException` → ボタンを非表示 (`resolveActivity` で事前判定)。
  読み取り失敗はトースト「端末の連絡先を読み取れませんでした」。
- **一覧に混ぜる (任意。既定 OFF)**: 設定の「端末の連絡先を表示」を ON にしたときだけ
  `READ_CONTACTS` を実行時要求し、連絡先タブに 3 つ目のグループ見出し「端末」を追加する。
  **番号ごとに 1 行**に展開し、2 行目は「番号・ラベル」(`Phone.getTypeLabel`)。
  検索欄はローカル + 端末の両方を対象 (名前部分一致 / 番号前方一致)。
  端末側の行は読み取り専用 — タップ = 発信、長押し = 「連絡先に保存」(ローカルへコピー) のみ。
  権限が拒否されたらトグルを自動で OFF に戻す。読み込みはバックグラウンドスレッドで行い、
  件数が多い端末でも UI を止めない (取得後にメインスレッドで描画)。
- **着信時の表示名解決**: `ContactStore.lookup` → (権限があるときだけ)
  `DeviceContacts.lookup` (`PhoneLookup.CONTENT_FILTER_URI`) の順。権限が無ければ従来どおり番号表示。
- Echo Show 5 は GApps もアカウントも無いため端末の連絡先は 0 件。トグルを OFF のままにすれば
  権限ダイアログも出ない。

### 1.4 設定 (`SettingsFragment`)
上から:
1. **状態カード**: 「● relay に接続中」/「○ 未接続」/「再接続中…」、2 行目
   「wss://relay.example.com・内線 2104」、右に「停止」/「開始」のテキストボタン。
   下に小さく現在のステータス文 (`CallHub.status`)。
2. **接続** カード (**インライン入力**。ダイアログは使わない。秘密は password 表示 +
   目アイコンで一時表示): relay URL / Access Client ID / Access Secret / Dev Token。
   カード先頭に注記「relay URL は必須。認証は Access (Client ID + Secret) か Dev Token の
   どちらか一方で構いません。入力は欄を離れると自動保存されます。」
3. **SIP アカウント** カード (新規。relay 側 env から移す。こちらもインライン入力):
   内線番号 (SIP user) / パスワード / 表示名 (任意)。説明: 「relay がこのアカウントで Asterisk に登録します。同じ内線を
   複数端末に設定すると同時着信し、先に応答した端末が通話します。」
4. **動作** カード: 接続モード セグメント「常時接続 | PUSH 起床」+ 説明文
   (常時接続: 「常駐サービスで WSS を維持。着信が最速 (常時給電の端末向け)。」/
   PUSH: 「FCM で起床して接続し、通話終了 60 秒後に切断。スマホ向け。foss ビルドでは使えません。」)。
   トグル: 起動時に自動開始 (端末の再起動後も接続を復帰) / オーバーレイ着信
   (他アプリの上にバブルを重ねる) / スピーカーで応答 (応答時にスピーカー出力にする。
   **既定 OFF** — 一般的な通話アプリに合わせ、既定は受話口。v1.2 で ON→OFF に変更) /
   端末の連絡先を表示 (既定 OFF。ON で `READ_CONTACTS` を要求。§1.3)。
   **受話口を持たない端末 (Echo Show など) はこの設定に関わらずスピーカーへ出す**
   (`AudioRoute.hasEarpiece()` が false のときは強制 ON。無音になるのを防ぐ)。
   オーバーレイ ON で権限が無いときは直下に警告行を出し、タップで権限画面へ
   (権限が無いとバブルも通話中ピルも出ないため)。**「終了前に確認」は廃止** (下記 §3.3)。
5. **音声** カード: マイクゲイン スライダー (0.5〜4.5, 現在値表示)。
6. **権限** カード: 行「他のアプリの上に表示」「電池の最適化を除外」「通知」「全画面通知 (API 34+)」
   それぞれ現在の状態 (許可済み/未許可) と、タップで該当設定画面へ。
7. **テスト**: 「テスト着信を表示」(relay 無しで着信 UI を確認)。
8. フッター: アプリ版 / relay 版 (hello.relayVersion) / device id (先頭 8 桁)。
- 保存タイミング: 欄からフォーカスが外れた時点 (または IME の完了キー、タブ移動時) に
  **その欄だけ**保存 (`BridgeConfig.save`)。Bridge 稼働中で必須項目が揃っているときだけ
  Service を再起動して反映する。
- **必須チェックは保存時に行わない**。旧実装は 1 欄保存するたびに「relay URL 必須」と
  「Access Client ID か Dev Token 必須」を両方課していたため、どちらを先に入力しても
  弾かれて何も保存できないデッドロックになっていた。未入力は状態カードに
  「未入力: relay URL・認証 (…)」とまとめて出し、**「開始」を押したときだけ**検証する
  (不足があればトーストで具体名を出して開始しない)。relay URL の形式エラーだけは
  その場で欄の下に出す (保存は止めない)。

## 2. 着信と発信 (`CallActivity`。旧 `IncomingCallActivity` を改名して置換)

共通レイアウト: 上部キャプション「SIP BRIDGE・着信 / 発信 / 通話中」(letter spacing, text_dim, 11sp)、
アバター円 (96dp, 名前 1 文字目)、名前 (28sp)、番号 (16sp text_dim)、状態行。
背景は `nocturne_bg` に中央上へ薄い accent の放射グラデーション。
**操作ボタンは画面下端寄せ** (情報は上、余白は中間。片手で届く位置に応答/拒否/切断/終了を置く)。
システムバーのインセットは `CallActivity` が画面全体の padding に反映する (edge-to-edge 対策)。

### 2.1 通知 (ロック画面) — `NotificationHelper`
- 着信通知: タイトル「着信中・玄関インターホン」、本文「110 から (relay 経由)」、
  アクション「拒否」「応答」(応答は accent 色)。フルスクリーンインテントは既存どおり。
- 常駐通知: 「relay に接続中・内線 101」/ 2 行目「常時接続／常駐通知 (消去不可)」
  (PUSH モードは「待機中 (PUSH 起床)」)。

### 2.2 着信中
- 状態行「着信中 •••」(ドットは 3 点の点滅アニメ)。
- 下部: 左「拒否」(neutral_btn, 受話器ダウン) / 右「応答」(accent, 受話器)。ラベルは下に 12sp。
- 着信音・バイブは既存実装を流用。

### 2.3 発信中 (呼出中)
- 状態行「呼出中 •••」(183 early media 時は「呼出中 (相手側応答音)」)。
- 下部: 中央に「切断」1 つ (neutral_btn)。**縮小ボタンは置かない** —
  ホーム操作で離れれば自動で「📞 呼出中」ピルが出る (§3.2)。

## 3. 通話中の 3 状態

### 3.1 通話中
- 状態行 = 経過時間 `mm:ss` (accent_text)。
- 3×2 の丸ボタン (64dp) + 下ラベル:
  ミュート (トグル。ON で塗り accent_soft) / キーパッド (DTMF シートを開く) /
  スピーカー (トグル。ON で塗り) / **保留 (v1 は無効表示)** / 通話を追加 (無効) / 連絡先 (無効)。
  無効は alpha 0.35。
- 下部: 中央に「終了」(neutral_btn 72dp, 受話器ダウン)。縮小ボタンは置かない (§3.2)。
- **DTMF キーパッド**: BottomSheet に 3×4 キー + 入力済み文字列表示 + 閉じる。
  1 キー押下ごとに `RtpEngine.sendDtmf(digit)` (in-band トーン 120ms、Asterisk 側 `dtmfmode=inband`)
  とローカル側の短い操作音 + `KEYBOARD_TAP` の触覚フィードバック (§1.1)。
- 通話中に音声経路 (スピーカー/イヤピース) を切替: **`AudioRoute`** (API 31+ は
  `AudioManager.setCommunicationDevice()`、それ以前は `isSpeakerphoneOn`)。
  非推奨 API だけでは Android 13+ でスピーカーから受話口に戻らない端末がある。
  マイクミュート: `RtpEngine.muted`。
- **近接センサー**: 通話中かつスピーカー OFF の間だけ `PROXIMITY_SCREEN_OFF_WAKE_LOCK` を保持し、
  耳に当てている間は画面を消す (誤タップ防止)。スピーカー ON・着信/呼出中・画面を離れたら解放。

### 3.2 縮小 (オーバーレイバブル)
- ホーム操作などで通話画面を離れると、他アプリの上にピル「📞 00:34」を出す
  (**accent 塗り + 太字 17sp + 影**で目立たせる。右上)。**発信呼出中は「📞 呼出中」** (経過時間なし)。
  専用の「縮小」ボタンは置かない (ホームに戻る操作で足りるため v1.1 で削除)。
  発信を縮小したまま相手が応答した場合は通話画面を前面に戻さず、ピルを経過時間表示に切り替える。タップで通話画面に戻る。ドラッグ可 (縦位置のみでも可)。
  オーバーレイ権限が無い場合は通知 (見出し「通話中」+ 本文「タップで通話画面に戻る」) に代替。
  経過時間は通知ヘッダの chronometer (`setUsesChronometer`) で SystemUI が数える (毎秒の再発行はしない)。
- アプリ内 (キーパッド等) にいる間も同じピルを画面右上に表示 (アプリ内は View で代替してよい)。
- `CallOverlayManager` を拡張: 既存の着信バブル (応答/拒否) に加えて通話中ピル。

### 3.3 終了 (確認は出さない)
- 「終了」タップで**即座に切断**する (一般的な通話アプリと同じ)。確認ダイアログと
  設定「終了前に確認」は廃止した (v1.1 で削除。`confirmHangup` も設定から除去)。
- 通話終了後: 画面下にトースト相当「通話終了 01:08」を 1.5 秒表示して閉じる。

## 4. 横画面 (Echo Show 5, 960×480)

- `layout-land/` を用意: 通話系は左半分にアバター+名前+番号+状態、右半分に操作ボタン。
  キーパッドは左に番号表示+発信ボタン、右に 3×4 キー。設定/履歴/連絡先はそのままスクロール。
- すべての画面は 480dp 高さで縦スクロール無しに主要操作 (応答/拒否/終了/発信) が見えること。
- ボトムナビは横画面でも下部固定 (高さ 56dp)。

## 5. 実装メモ (設計外の決定)

- 保留 / 通話を追加 / 通話中の連絡先 は v1 では無効ボタン (保留は relay の re-INVITE 対応後)。
- 「連絡先・履歴のデータは仮」→ 実データは端末ローカル保存。Asterisk からの取り込みはしない。
  端末の連絡先は v1.2 で連携 (取り込み or 任意表示。§1.3)。アプリから端末側への書き込みはしない。
- 番号のフォーマットはしない (入力どおり)。
- debug ビルドのみ、adb から設定を投入できる `DebugConfigReceiver`
  (`adb shell am broadcast -a io.github.tmlksu.sipbridge.DEBUG_SET_CONFIG --es relayUrl ... --es sipUser ...`)。
  Echo Show など入力しづらい端末の初期設定用。release には含めない (`src/debug` 配下 + debug manifest)。
- 旧 `MainActivity` の 1 画面設定 UI と `activity_main.xml` は廃止。`activity_incoming.xml` も置換。

---

## 6. 到達性とセットアップ (v1.4)

「着信が届く状態か」を **OS 設定も含めて** アプリが把握し、崩れていれば直す導線を出す。
対象は `android/` のみ (relay は変更しない)。

### 6.0 前提 — Push が届かなくなる原因 (調査結果)

FCM トークンに「一定期間起動しないと失効する」固定の期限は無い。実際に届かなくなるのは次:

| # | 原因 | 検知 | 対処 |
|---|---|---|---|
| A | **未使用アプリの休止** (API 31+ / 権限の自動リセットは API 30+)。数か月使われないと強制停止され、権限が取り消され、**FCM も届かなくなる** | `PackageManager.isAutoRevokeWhitelisted()` (API 30+) | 休止の除外をユーザーに設定してもらう。除外できていないときは**休止が起きる前に**予告通知 |
| B | **One UI の「スリープ状態のアプリ」**。S25 で数日使わないと寝かされ、push が遅延/不達 | API では取れない | Samsung 端末でだけ案内行を出し、アプリ情報画面へ誘導 |
| C | **電池の最適化**が有効 | `PowerManager.isIgnoringBatteryOptimizations()` | 除外を要求 (§6.3) |
| D | **通知が無効** (POST_NOTIFICATIONS 拒否 / チャンネル OFF) | `areNotificationsEnabled()` + 権限 + `getNotificationChannel(CH_INCOMING).importance` | 要求 / 設定へ誘導 |
| E | **強制停止**・データ消去・再インストール直後で `register_push` 未送信 | `PushHealth.lastPushRegisteredAt` が古い / 無い | 定期再登録 + 予告通知 |
| F | relay 側でトークンが無効化された (FCM が `UNREGISTERED` を返し relay が削除) | アプリ側からは見えない | 定期再登録で自動復旧する |

→ 「失効の予告」は **A と E に対する事前通知** として実装する。B/C/D は状態表示と導線で潰す。

### 6.1 `SystemStatus` (新規) — OS 状態のスナップショット

`PrefixDialer` の `SystemStatus.kt` と同じ方針: **Android API の呼び出しをこのファイルに閉じ込め**、
UI は値だけを見る。`read(context)` が返す `data class`:

`micGranted` / `notificationsEnabled` (権限 + `areNotificationsEnabled` + 着信チャンネルが OFF でない) /
`overlayGranted` / `fullScreenIntentAllowed` (API 34+) / `ignoringBatteryOptimizations` /
`hibernationExempt` (`isAutoRevokeWhitelisted`。API 30 未満と、機能を持たない端末では `null` = 非対応) /
`pushTokenPresent` (gms かつ `FCM_PREFS` にトークンがある) / `isSamsung` (`Build.MANUFACTURER`)。

Intent の候補は **リストで返し、先頭から `resolveActivity` が通るものを起動する** (PrefixDialer と同じ):

- 電池: `ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` (本アプリは Play 配布ではなく
  `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS` を宣言済みなのでダイアログが出せる) →
  `ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS` → `ACTION_APPLICATION_DETAILS_SETTINGS`。
  **PrefixDialer の教訓**: 権限未宣言だと `ACTION_REQUEST_...` は Galaxy S25 で**無反応**になる。
  宣言済みでも端末によっては解決できないため、フォールバックを必ず持つ。
- 休止除外: `Intent.ACTION_AUTO_REVOKE_PERMISSIONS` (API 30+, `package:` URI) →
  `ACTION_APPLICATION_DETAILS_SETTINGS`。
- チャンネル個別: `Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS` (+ `EXTRA_CHANNEL_ID`)。

### 6.2 `PushHealth` (新規) — 到達性の履歴と判定

平文 SharedPreferences `sipbridge_health` に epoch millis を記録する (秘密を含まないため暗号化不要):

- `lastUserOpenAt`: `MainActivity.onResume` (= OS から見た「使用」)
- `lastPushRegisteredAt`: `register_push` を送って `hello` まで到達したとき (`BridgeService`)
- `lastPushReceivedAt`: gms の `BridgeMessagingService.onMessageReceived`
- `lastRelayOkAt`: `hello` 受信
- `lastWarnAt` / `lastWarnKind`: 通知の重複抑止

判定は **純関数** `PushHealth.evaluate(now: Long, s: Snapshot): List<Issue>` に切り出す
(Android 非依存。JVM テスト対象)。`Issue` は `kind` (enum) + 重要度 `BLOCKING` / `WARN`:

| kind | 条件 | 重要度 |
|---|---|---|
| `NOTIFICATIONS_OFF` | 通知が無効 | BLOCKING |
| `MIC_DENIED` | RECORD_AUDIO 未許可 | BLOCKING |
| `PUSH_TOKEN_MISSING` | PUSH モード + gms + トークン無し | BLOCKING |
| `PUSH_REGISTRATION_STALE` | PUSH モード + `now - lastPushRegisteredAt > 14 日` (未登録も含む) | WARN |
| `HIBERNATION_SOON` | 休止除外されていない + `now - lastUserOpenAt > 45 日` | WARN |
| `BATTERY_OPTIMIZED` | 電池最適化の除外なし | WARN |
| `OVERLAY_DENIED` | オーバーレイ ON 設定なのに権限なし | WARN |
| `FULLSCREEN_DENIED` | API 34+ で全画面通知が不可 | WARN |

### 6.3 定期チェック — `HealthCheckReceiver` (新規)

`BridgeService.onCreate` と `BootReceiver` から `AlarmManager.setInexactRepeating(RTC_WAKEUP,
初回 = now + 1 日, INTERVAL_DAY, …)` で 1 日 1 回。**exact alarm は使わない** (権限が要る)。

発火時:
1. **PUSH モードで `lastPushRegisteredAt` が 7 日以上前**なら `BridgeService` を起こして
   再接続 → `register_push` を送り直す (成功すれば relay 側のトークンも新しくなる。§6.0 の E/F 対策)。
   ※ 再登録のためだけの接続は既存の idle 猶予で自動切断される。
2. `evaluate` して BLOCKING または WARN があれば**通知を 1 本**出す
   (新チャンネル `sipbridge_health`, IMPORTANCE_DEFAULT, `ongoing=false`,
   タップで `MainActivity` の設定タブ)。文言:
   - BLOCKING あり: 「着信を受けられない設定があります」/ 本文 = 先頭の理由
   - `HIBERNATION_SOON`: 「しばらくアプリが使われていません」/
     「このままだと OS がアプリを休止させ、着信 push が届かなくなります。タップして開いてください」
   - それ以外: 「着信の設定を確認してください」/ 本文 = 理由の列挙 (最大 2 件)
3. **同じ `kind` の通知は 3 日に 1 回まで** (`lastWarnAt` / `lastWarnKind`)。全部解消したら
   通知をキャンセルする。

> 休止済みのアプリではアラームも発火しない。だからこの通知は「**休止する前に**気付かせる」ためのもの。
> 恒久対策は §6.4 の休止除外トグルであり、通知本文でもそこへ誘導する。

### 6.4 設定 §1.4 の変更

**「セットアップ」カードを状態カードの直下に追加** (不足が無いときは非表示):
「あと N 件の設定が必要です」+ 不足項目を最大 3 行 + ボタン「まとめて設定」。
押すと `SetupSheet` (`BottomSheetDialogFragment`) を開く。

`SetupSheet`: 項目を一覧し、行ごとに状態バッジ (必須/推奨/許可済み) と「許可」ボタン:
マイク → 通知 → 電池の最適化 → 休止の除外 → オーバーレイ → 全画面通知 の順。
ランタイム権限 (`RECORD_AUDIO` / `POST_NOTIFICATIONS`) は `registerForActivityResult`
(`RequestMultiplePermissions`) でその場のダイアログ。2 回拒否されて
`shouldShowRequestPermissionRationale` が false のときだけアプリ情報画面へ誘導する。
特別なアクセスは §6.1 の Intent 候補で開き、**戻ってきたら再判定して行を更新**する
(遷移しなかった場合に備え、状態が変わらなければ次の候補を案内する)。

`SetupSheet` は次のときに自動で出す: **初回起動時** (`lastSetupShownAt` 未設定)、
および **BLOCKING な不足があるとき** (1 日 1 回まで)。`MainActivity.onResume` から判定する。

**権限カードの行を追加**: 「マイク」「アプリの休止を無効化 (API 30+、非対応端末では出さない)」。
Samsung 端末でだけ最下部に案内行「Samsung: 設定 → バッテリー → バックグラウンド使用制限 →
『スリープ状態にしないアプリ』に追加」(タップでアプリ情報画面)。

**動作カードにトグル「常駐通知を隠す」を追加** (既定 OFF)。→ §6.5。

### 6.5 常駐通知の表示スタイル

FGS の通知はアプリ側からは消せない (API 26+ の仕様)。そこで 2 段構えにする:

1. 設定 `serviceNotificationQuiet` (既定 OFF) が ON のとき、常駐通知を
   **`CH_SERVICE_QUIET = "sipbridge_service_quiet"` (IMPORTANCE_MIN)** に出す。
   併せて `setSilent(true)` / `setShowWhen(false)` / `PRIORITY_MIN`。
   ステータスバーのアイコンが消え、通知シェードの最下部に折りたたまれる。
   **チャンネルの importance は作成後に変更できない**ため、通常用と静音用の
   2 チャンネルを持ち、`startForeground(ID_SERVICE, …)` を出し直して切り替える
   (設定変更時に `BridgeService` へ反映。Service の再起動はしない)。
2. トグル ON の直後にダイアログで案内: 「完全に非表示にするには、通知設定で
   『常駐サービス (静音)』をオフにしてください」+ ボタン「通知設定を開く」
   (`ACTION_CHANNEL_NOTIFICATION_SETTINGS`)。
   **着信通知は別チャンネル (`sipbridge_incoming`) なので影響しない**ことを併記する。
   チャンネルを OFF にしても Service は動き続ける (Android 12+ では
   「実行中のアプリ」からは見える)。

権限カードの「通知」行の状態は、**着信チャンネルが生きているか**で判定する
(常駐チャンネルを意図的に切った状態を「未許可」と表示しないため)。
