# Google Play 出品に向けた整理 (2026-09-18)

`docs/DISTRIBUTION.md` は F-Droid を第一ターゲットにしている。本書は **Google Play にも出す**ために
必要なことを、2026-09-18 時点の Play 公式ドキュメントとリポジトリの実態を照らして整理したもの。
調査の全文 (根拠・出典・コード上の該当箇所) は `ops/play-audit-2026-09-18.md` (非公開) にある。

## 0. 先に決めること (方針)

| # | 決めること | 推奨 | 理由 |
|---|---|---|---|
| D1 | **Play に出す flavor** | **FCM 無し (`foss` 相当) を出す** | Play 配布の AAB には開発者の Firebase プロジェクトが焼き込まれるため、`gms` を出しても **ユーザーの自ホスト relay からは push を送れない** (§8)。同一 applicationId で 2 flavor は出せない |
| D2 | **署名鍵** | 既存 `release.keystore` を PEPK で **app signing key として Play に預ける** | GitHub Releases 版と Play 版の署名が一致し、アンインストール無しで乗り換えられる。**公開テスト / 本番へロールアウトする前**にやる (後戻り不可)。F-Droid 版とは署名が違うままになる (再現可能ビルドが通らない限り避けられない) |
| D3 | **審査用環境** | `BACKEND=fake` + `AUTH_MODE=token` の **審査専用 relay を本番と別ホストに常設** | fake backend は応答後にエコーを返すので SIP サーバ不要でレビュアーが通話を体験できる。本番 relay の URL・トークンを Play Console に置かない |
| D4 | **Play Console アカウント** | 個人アカウント・完全無料 (課金・広告なし) | 公開されるのは氏名・国・developer email のみになる見込み (§6)。**アカウント作成日が 2023-11-13 より前なら 12 人テスト要件が不要** (最優先で確認) |

## 1. 必須 (これが無いと提出できない / 審査で落ちる)

| # | 項目 | 現状 | 作業量 |
|---|---|---|---|
| M1 | **targetSdk / compileSdk 36** — 2026-08-31 以降、新規提出は API 36 必須 (期限は経過済み) | 35 | 中 |
| M2 | **AGP 8.9.1+ / Gradle 8.11.1+** — compileSdk 36 の前提。手元の `gradle-8.9` では足りない。`gradle-wrapper.properties` と `fdroid/*.yml` の整合も要る | AGP 8.5.2 / Gradle 8.9 | 小〜中 |
| M3 | **AAB で提出** | `bundleGmsRelease` は現構成で成功 (5.7 MiB、`.so` 0 件) | 済 |
| M4 | **Play App Signing の鍵方針** (D2) | 未着手 | 小 (一発勝負) |
| M5 | **プライバシーポリシー URL** (GitHub Pages か repo 内 `docs/PRIVACY.md` の raw/Pages URL) | 無し | 小 |
| M6 | **Data safety フォーム** (音声・SIP 認証情報・連絡先の申告。回答案は ops 版 §4) | 未着手 | 小 |
| M7 | **Foreground service 申告 + 実演動画** — `phoneCall` / `microphone` の 2 型 | 未着手 | 中 (動画) |
| M8 | **Full-screen intent 申告** — 通話アプリなので資格あり | 未着手 | 小 |
| M9 | **App access (審査用手順)** (D3) | 未着手 | 小 |
| M10 | **スクリーンショット 2 枚以上** (JPEG / アルファ無し PNG、320〜3840 px) | 0 枚 | 小 |
| M11 | **フィーチャーグラフィック 1024×500** | 無し | 小 |
| M12 | **コンテンツレーティング (IARC) 質問票・ターゲット層・広告有無・カテゴリ** | 未着手 | 小 |
| M13 | **クローズドテスト 12 人 × 連続 14 日** — 2023-11-13 以降作成の個人アカウントのみ (D4) | 未確認 | 大 |
| M14 | **開発者本人確認** (個人は Payments プロファイル or 政府発行 ID。D-U-N-S 不要) | 未確認 | 小 |
| M15 | **FCM の扱いを決める** (D1) | 未検討 | 小 (D1 推奨案なら) |
| M16 | **fastlane ロケール** — Play は `ja-JP`、F-Droid は `ja`。両方置く | `ja` のみ | 小 |
| M17 | **`full_description` の Play 版** — 現行 en-US は「foss flavour / push なし」の説明で、Play 向けに書き直す (D1 なら「Google Play services を含まない。着信は常駐モード前提」と平易に) | 要改稿 | 小 |

## 2. 推奨 (審査リスクを下げる)

| # | 項目 | 理由 |
|---|---|---|
| R1 | `CHANGE_WIFI_STATE` を削除 | コードでの利用 0 件。`WifiLock` には `ACCESS_WIFI_STATE` で足りる |
| R2 | `READ_CONTACTS` を Play 版では削除 | 端末連絡先の一覧混ぜ (既定 OFF) だけに使用。連絡先選択は Contact Picker で権限不要。削れば Contacts の申告も消える |
| R3 | FGS 型を `phoneCall` のみにできるか実機検証 | `microphone` 型を落とせれば申告と動画が 1 型分減る |
| R4 | `CALL_PHONE` / `READ_PHONE_STATE` の扱いを決める | 制限付き権限ではないが managed PhoneAccount 専用。「残して正当化」でも「Play 版は落とす」でもよい |
| R5 | `BootReceiver` を `exported="false"` に | `BOOT_COMPLETED` は protected broadcast なので `false` で受信できる |
| R6 | relay URL の `ws://` を禁止か警告 | Data safety の「転送中は暗号化」を正しく答える前提。現状スキーム検証が無い |
| R7 | `themes.xml` の `statusBarColor` / `navigationBarColor` を掃除 | targetSdk 35+ で無視される |
| R8 | `WifiManager.createWifiLock(WIFI_MODE_FULL_HIGH_PERF)` の見直し | API 29 で非推奨。常駐モードの安定性に関わる |
| R9 | 内部テストで pre-launch report を取る | relay 未設定でクラッシュしないことをクローラで確認 |
| R10 | フィーチャーグラフィックを `scripts/gen-icon.py` と同じ手法で生成 | 環境に PIL/ImageMagick が無いが、既存スクリプトは標準ライブラリだけで PNG を書ける |

## 3. 任意 (今回はやらない)

- R8 / 難読化 (5.7 MiB で十分小さい。tink の keep ルールが要るので提出直前には入れない)
- 16 KB ページサイズ (ネイティブコード無しで該当なし)、Play Integrity (不要)、プロモ動画
- CI からの自動アップロード (初回は手動アップロードが必須)
- UnifiedPush 対応 (FCM 問題の中長期の本筋。Play 出品の条件ではない)

## 4. targetSdk 36 化でこのアプリに効く挙動変更

コードを見た結果、**予測バック・向き制限・`scheduleAtFixedRate`・JobScheduler・Safer Intents は該当コードが無く影響なし**。
edge-to-edge は targetSdk 35 で対応済み (`MainActivity` / `CallActivity` の insets 処理)。テーマ付きアイコンは monochrome レイヤ済み。
残るリスクは **Local Network Permission** (段階導入中)。HGW 直結構成で LAN 内の relay に接続する場合、
将来 enforce されると権限が要る。Cloudflare Tunnel 経由の構成では無関係。
Echo Show 5 は API 30 なので targetSdk 36 の targeting 変更は適用されず、リグレッションリスクは低い。

## 5. 署名と配布経路の整理

| 経路 | 署名 | 相互更新 |
|---|---|---|
| GitHub Releases (`foss` APK) | `release.keystore` | Play 版と一致 (D2 の案なら) |
| Google Play | Play App Signing (D2 で既存鍵を預ければ同一) | GitHub 版と一致 |
| F-Droid | F-Droid の鍵で再署名 | 他と不一致。乗り換えはアンインストールが必要 (`docs/RELEASE.md §5` を Play にも拡張して案内) |

## 6. 個人情報の露出 (Play Console で公開されるもの)

| 情報 | 個人アカウント | 最小化策 |
|---|---|---|
| 法的な氏名 | **公開** | 避けられない (組織アカウントは D-U-N-S と法人住所公開が要り、むしろ悪化) |
| 住所 | 収益化アカウントは完全な住所を公開。非収益化は国のみという運用報告があるが公式には明記なし | **完全無料を維持**し、Play Console の「デベロッパープロフィール」で実際の表示を確認する |
| developer email | **公開** (必須) | 個人名を含まない専用エイリアスにする。プライバシーポリシーの連絡先も同じアドレスにすれば追加露出ゼロ |
| 電話番号 | 非公開 | 個人アカウントを選ぶこと自体が最小化策 |
| ストア掲載の連絡先 | 入力したものは公開 | 電話番号は空欄、ウェブサイトは GitHub リポジトリ URL |

別軸で、Android developer verification (2027 に全世界展開予定) は GitHub Releases 直配布も対象になる。

## 7. 審査用環境 (D3) の手順

1. `BACKEND=fake` / `AUTH_MODE=token` / `DEV_TOKEN=<審査専用のランダム値>` / FCM 未設定で relay を **本番と別ホスト・別ホスト名**に立てる。HTTPS 終端は Cloudflare Tunnel でも Caddy でもよいが **Cloudflare Access はかけない** (レビュアーはブラウザログインできない)。
2. 審査中も更新のたびも**常時稼働**させる (App access は「常にアクセス可能」が条件)。
3. Play Console → App content → App access に英語で手順を書く (relay URL・共有トークン・任意の内線番号・任意のパスワード・常駐モードを設定し、任意の番号に発信すると即応答してエコーが返る)。
4. 着信 UI は fake backend の `InjectIncoming()` を外から叩く口が無いため、FGS 申告用の実演動画で見せる。

## 8. `gms` flavor と FCM (D1 の背景)

現行設計は「ユーザーが自分の Firebase プロジェクトでセルフビルドし、自分の relay にサービスアカウントを置く」前提。
Play 配布の AAB は開発者が 1 回ビルドしたバイナリで、開発者の Firebase プロジェクトが `resources.pb` に焼き込まれる
(実測: `google_app_id` / `gcm_defaultSenderId`、firebase 参照 551 件)。ユーザーの relay からそのトークンには送れない。

| 案 | 内容 | 評価 |
|---|---|---|
| a | Play には FCM 無しを出す | **短期の推奨**。Data safety も単純になる |
| b | `FirebaseApp.initializeApp(context, FirebaseOptions)` で設定画面の値から実行時初期化 | 中期候補。`FirebaseMessagingService` が実行時初期化で受信できるか要検証 |
| c | 開発者が push ゲートウェイを運用 | 非推奨。開発者がデータ処理者になり「開発者は何も受け取らない」という売りを壊す |
| d | UnifiedPush (ntfy 自ホスト) | 中長期の本筋。F-Droid / GApps 無し端末でも push が使える |

## 9. 公開前の個人情報チェック

リポジトリ全体の個人情報・旧識別子の棚卸しは `ops/pii-inventory-2026-09-18.md` (非公開) にある。
作業ツリーの旧 applicationId 言及、git author メールのドメイン、実機の個体識別子・LAN IP・FCM プロジェクト ID が主な対象。
`scripts/check-public-safe.sh` はこれらの多くをカバーしていない (RFC1918 は意図的に除外) ので、
`ops/deny-patterns.txt` への追記と、author メール検査の追加が必要。

## 10. 要確認

1. **Play Console 個人アカウントの作成日** (2023-11-13 より前なら M13 不要)。未作成なら新規扱い。
2. 非収益化の個人アカウントで住所がどこまで公開されるか (デベロッパープロフィールで実物確認)。
3. targetSdk 35 のまま延長申請 (2026-11-01 まで) で新規アプリを出せるか。延長に頼らず M1 を進めるのが健全。
4. AGP / Gradle / Kotlin の具体的な組み合わせと、F-Droid ビルドへの影響。
5. `phoneCall` 型 FGS だけでマイクが使えるか (S25 で実機検証)。
6. 案 b の `FirebaseMessagingService` 受信可否。
7. relay URL のスキーム検証の有無。
8. プライバシーポリシーに連絡先メールの明記が必須か (GitHub Issues URL で足りる事例あり)。
9. F-Droid の再現可能ビルド (開発者署名) を目指すか。

## 出典 (主要)

- Target API level requirements: https://support.google.com/googleplay/android-developer/answer/11926878
- Android 16 behavior changes: https://developer.android.com/about/versions/16/behavior-changes-16
- Use Play App Signing: https://support.google.com/googleplay/android-developer/answer/9842756
- App testing requirements for new personal developer accounts: https://support.google.com/googleplay/android-developer/answer/14151465
- Verify your developer identity: https://support.google.com/googleplay/android-developer/answer/10841920
- Firebase init options: https://firebase.google.com/support/guides/init-options
