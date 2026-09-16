# 配布 (F-Droid を第一ターゲット)

Google Play ではなく **F-Droid** を先にやる。理由:

- Play は 2026-08-31 以降 `targetSdk` 36 が要求される見込みで、API 30 (Echo Show 5) と
  API 35 (S25) の実機リグレッションが必要になる。F-Droid に targetSdk の要件は無いので、
  **`compileSdk`/`targetSdk` 35 のまま出せる**。
- Play は個人アカウントだと本番公開前に「12 人 × 14 日のクローズドテスト」が要る。
  自ホスト前提のニッチなアプリでテスターを 12 人集めるのは現実的でない。
- Play はレビュアーがアプリを動かせないと却下される。本アプリは relay と SIP サーバが
  無いと何もできないため、審査用のデモ環境かデモモードを別途作る必要がある。
- `foss` フレーバー (FCM 無し) は F-Droid の要件とそのまま噛み合う。

Play は「リーチを広げたい」と判断した時点で改めて検討する (§4)。

## 1. 検証済みの事実

| 項目 | 結果 |
|---|---|
| `foss` リリースビルド | `gradle assembleFossRelease` 成功 (unsigned APK, 約 13 MB) |
| `foss` の実行時依存 | **Firebase / Google Play services はゼロ**。全依存が Apache-2.0 (androidx, kotlin(x), okhttp/okio, material, tink, gson, guava-listenablefuture) |
| ビルド成果物の中身 | `AndroidManifest.xml` と `classes.dex` に `firebase`/`gms` の参照 0 件 |
| リポジトリ内のバイナリ | 無し (`.jar`/`.so`/`.aar`/`.apk` いずれも未追跡) |
| gradle wrapper の jar | 無し (F-Droid はリポジトリ内バイナリを嫌うので、無いのが望ましい) |
| git 履歴の秘密情報 | `.env` / `google-services.json` / keystore の追加履歴なし |

検証コマンド:

```sh
export JAVA_HOME=$HOME/android-build/jdk PATH=$HOME/android-build/jdk/bin:$PATH \
       ANDROID_HOME=$HOME/android-build/sdk ANDROID_SDK_ROOT=$HOME/android-build/sdk
cd android && ~/android-build/gradle-8.9/bin/gradle assembleFossRelease --no-daemon
~/android-build/gradle-8.9/bin/gradle -q :app:dependencies \
    --configuration fossReleaseRuntimeClasspath --no-daemon | grep -iE 'firebase|gms'
APK=app/build/outputs/apk/foss/release/app-foss-release-unsigned.apk
unzip -p "$APK" classes.dex | strings | grep -cE 'Lcom/google/(firebase|android/gms)'
```

## 2. 対応済み

- **applicationId** — `net.peyan.sipbridge` → **`io.github.tmlksu.sipbridge`** (v1.3)。
  公開リポジトリの持ち主から辿れる逆ドメインにした。F-Droid のメタデータ名も
  `fdroid/io.github.tmlksu.sipbridge.yml` に改名済み。
  収録前に変えたので fdroiddata 側への影響は無いが、**旧 ID で入れた端末は
  上書き更新できない** (アンインストールしてから入れ直す)。

- **`LICENSE`** — Apache-2.0。F-Droid はライセンスファイルを要求し、SPDX 識別子を
  メタデータに書く。依存がすべて Apache-2.0 なので整合する。
- **ランチャーアイコン** — 以前はフレームワーク標準の
  `@android:mipmap/sym_def_app_icon` を使っていた (`mipmap` ディレクトリ自体が無かった)。
  `scripts/gen-icon.py` で生成するようにした。Nocturne アクセント (`#6B5FD6`) の地に
  白の受話器と、その上を渡る橋のアーチ。
  - `drawable/ic_launcher_{foreground,background}.xml` (VectorDrawable)
  - `mipmap-anydpi-v26/ic_launcher{,_round}.xml` (adaptive-icon, monochrome 付き)
  - `mipmap-{m,h,xh,xxh,xxxh}dpi/ic_launcher.png` (aapt のアイコン抽出と古いランチャー用)
  - `android/fastlane/.../images/icon.png` (512x512, ストア用)

  生成スクリプトは Python 標準ライブラリのみで動く (この環境に ImageMagick も PIL も
  無いため)。ベクタと PNG を同じパスデータから作るので見た目が一致する。
  意匠を変えたら `python3 scripts/gen-icon.py` で作り直す。
- **fastlane メタデータ** — `android/fastlane/metadata/android/{en-US,ja}/`。
  F-Droid はリポジトリ内のこの配置を自動で拾う。`title` / `short_description` /
  `full_description` / `changelogs/3.txt` / `images/icon.png`。
  relay の自前運用が必須であること、foss ビルドに push が無いことを本文に明記した。
- **F-Droid ビルドレシピ** — `fdroid/io.github.tmlksu.sipbridge.yml`。fdroiddata へ出す雛形。
  `subdir: android/app`, `gradle: [foss]`, タグ追従 (`UpdateCheckMode: Tags`)。
- **`android/gradle/wrapper/gradle-wrapper.properties`** — Gradle 8.9 を宣言するためだけに
  置く。`gradle-wrapper.jar` と `gradlew` は意図的にコミットしない (F-Droid は
  リポジトリ内のバイナリを削除し、自前の gradle に差し替えるため)。これが無いと
  F-Droid 側が AGP 8.5.2 と噛み合わない gradle を選ぶおそれがある。
- **バージョン整合** — `versionCode` 2 → 3, `versionName` 1.1 → 1.2 (直近のコミットで
  入った v1.2 の実態に合わせた)。あわせて `RelayProtocol.CLIENT_VERSION` を
  `BuildConfig.VERSION_NAME` から組み立てるようにし、二重管理をやめた
  (`buildFeatures { buildConfig = true }` を追加)。

## 3. 残り (提出前に必要)

1. **リポジトリの public 化**。F-Droid はソースが公開されていないと収録できない。
   インスタンス固有の情報の分離は完了している (`docs/REPO-SPLIT.md`)。残るのは
   第三者向けの `README.md` / `docs/SETUP.md` への書き換え
   (HGW 直結の最小構成を入り口にすると導入ハードルが下がる)。
2. **`v1.2` の git タグ**。F-Droid のレシピは `commit: v1.2` を参照する。
3. **fdroiddata へマージリクエスト**。`fdroid lint` → `fdroid build -l` で手元検証してから出す。
   初回収録はレビュー待ちが数週間かかることがある。
4. ~~(任意) GitHub Releases 用の署名済み APK~~ — **対応済み** (v1.3)。
   `android/keystore.properties` (gitignore 済み) から読む `signingConfigs.release` を追加し、
   タグ push で `.github/workflows/release.yml` が署名済み foss APK を Releases に添付する。
   手順と鍵の扱いは `docs/RELEASE.md`。
   **F-Droid 版と GitHub 版は署名が異なり相互に上書きインストールできない**点は変わらない。
5. (任意) **R8 の有効化**。現在 `minifyEnabled` 未設定で APK が約 13 MB ある。
   `isMinifyEnabled`/`shrinkResources` で大幅に減る見込みだが、リフレクションを使う
   箇所 (JSON 周り) の動作確認が要る。

## 4. Play に進む場合の追加項目 (今回は着手しない)

- `compileSdk`/`targetSdk` 36 化 + Echo Show 5 (API 30) / S25 (API 35) の実機リグレッション
- リリース署名 + Play App Signing
- 制限付き権限の宣言: `REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`, `USE_FULL_SCREEN_INTENT`
- 権限の棚卸し: `SYSTEM_ALERT_WINDOW` (full-screen intent があるので外せる可能性)、
  `CHANGE_WIFI_STATE` (WifiLock には不要なはず)
- プライバシーポリシー URL、データセーフティ申告 (音声・連絡先)
- 審査用のデモ環境かデモモード
- 個人アカウントなら 12 テスター × 14 日のクローズドテスト
