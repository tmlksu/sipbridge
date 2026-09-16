# リリース手順 (署名と GitHub Releases)

対象は Android アプリ。relay は Docker イメージを各自でビルドする前提なので、ここでは扱わない。

## 1. applicationId と署名の方針

- applicationId / namespace は **`io.github.tmlksu.sipbridge`**。
  GitHub の所有者 (`tmlksu.github.io`) 由来の逆ドメインで、同じ持ち主の
  [prefix-dialer](https://github.com/tmlksu/prefix-dialer) (`io.github.tmlksu.prefixdialer`)
  と揃えてある。`com.github.*` は GitHub 社の名前空間なので使わない。
- 署名鍵は **sipbridge 専用**。prefix-dialer とは別の鍵で、片方の鍵事故がもう片方に波及しない。
- 鍵と設定はリポジトリに入れない (`.gitignore` 済み)。このリポジトリは public なので、
  一度でも履歴に入れたら鍵を作り直すしかなくなる。

| ファイル | 置き場所 | git |
|---|---|---|
| keystore 本体 | `android/release.keystore` | 追跡しない (`*.keystore`) |
| 署名設定 | `android/keystore.properties` | 追跡しない |
| 設定の雛形 | `android/keystore.properties.example` | 追跡する |

`android/app/build.gradle` は `keystore.properties` の 4 つの値 (`storeFile`,
`storePassword`, `keyAlias`, `keyPassword`) が**すべて揃っているときだけ** `release`
署名設定を作る。揃っていなければ署名設定を作らず、release ビルドは未署名で通る。
鍵の無い環境 (F-Droid のビルドサーバ、クローンしただけの第三者、通常の CI) でも
`assembleRelease` と `test` を通すための意図した挙動で、そのときは
`[SipBridge] keystore.properties が無いか値が空のため...` とログに出る。

> **鍵のバックアップ**: `android/release.keystore` を失うと、同じ applicationId の
> 署名済み APK を更新できなくなる (利用者は一度アンインストールが必要)。
> リポジトリ外の安全な場所 (パスワードマネージャ等) に鍵とパスワードを保管すること。

### 鍵を作り直す / 新しい環境で用意する

```sh
cd android
cp keystore.properties.example keystore.properties
keytool -genkeypair -v -keystore release.keystore -alias sipbridge \
        -keyalg RSA -keysize 4096 -validity 10000 \
        -dname "CN=SIP Bridge, OU=tmlksu, O=tmlksu, C=JP"
# keystore.properties の storePassword / keyPassword に同じパスワードを書く
chmod 600 release.keystore keystore.properties
```

PKCS12 (JDK 9 以降の既定) では鍵のパスワードをストアと別にできない。
`keyPassword` には `storePassword` と同じ値を書くこと。別の値だと
`packageRelease` が `Get Key failed: Given final block not properly padded` で失敗する。

## 2. 手元で署名済み APK を作る

```sh
export JAVA_HOME=$HOME/android-build/jdk PATH=$HOME/android-build/jdk/bin:$PATH \
       ANDROID_HOME=$HOME/android-build/sdk ANDROID_SDK_ROOT=$HOME/android-build/sdk
cd android
~/android-build/gradle-8.9/bin/gradle assembleFossRelease --no-daemon
ls app/build/outputs/apk/foss/release/
```

ファイル名が `...-unsigned.apk` なら署名設定が読めていない。署名を確認する:

```sh
APKSIGNER=$(ls $ANDROID_HOME/build-tools/*/apksigner | sort -V | tail -1)
$APKSIGNER verify -v --print-certs app/build/outputs/apk/foss/release/app-foss-release.apk
```

期待する出力は `Verified using v3 scheme (APK Signature Scheme v3): true` と、
`Signer #1 certificate DN: CN=SIP Bridge, OU=tmlksu, O=tmlksu, C=JP`。
minSdk 29 なので v1/v2 は付けていない (v3 は Android 9 以降が読める)。

## 3. バージョンを上げてタグを打つ

1. `android/app/build.gradle` の `versionCode` (+1) と `versionName` を更新
2. `fdroid/io.github.tmlksu.sipbridge.yml` の `versionName` / `versionCode` /
   `commit` / `CurrentVersion` / `CurrentVersionCode` を同じ値に更新
3. `android/fastlane/metadata/android/{ja,en-US}/changelogs/<versionCode>.txt` を追加
4. main にマージしてから `git tag vX.Y && git push origin vX.Y`

タグを push すると `.github/workflows/release.yml` が走り、署名済みの foss APK を
`sipbridge-vX.Y-foss.apk` として GitHub Releases に添付する。

## 4. CI の署名 Secrets

`release.yml` は次の Secrets を使う (Settings > Secrets and variables > Actions)。

| 名前 | 値 |
|---|---|
| `ANDROID_KEYSTORE_BASE64` | `base64 -w0 android/release.keystore` の出力 |
| `ANDROID_KEYSTORE_PASSWORD` | keystore のパスワード (store と key で同じ値) |

鍵の別名 (`sipbridge`) は秘密ではないのでワークフローに直接書いてある。

```sh
gh secret set ANDROID_KEYSTORE_BASE64 -R tmlksu/sipbridge < <(base64 -w0 android/release.keystore)
gh secret set ANDROID_KEYSTORE_PASSWORD -R tmlksu/sipbridge
```

Secrets が未設定のままタグを push した場合、`release.yml` は**未署名 APK を配らずに失敗する**。
手元で作った APK を後から貼るなら `gh release upload vX.Y <apk>`。

## 5. 署名が違うと上書きインストールできない

同じ applicationId でも、署名鍵が違う APK は上書き更新できない (一度アンインストールが要る)。

- **GitHub Releases 版** — この鍵 (`android/release.keystore`)
- **F-Droid 版** — F-Droid が自分の鍵で署名し直す
- **debug ビルド** — Android SDK の debug 鍵

配布経路を乗り換えるときはアンインストールが必要になる、と案内すること。

## 6. gms フレーバーについて

`gms` (FCM 入り) は `android/app/src/gms/google-services.json` が要る。これはリポジトリに
入れていないため、CI では作れない。FCM を使う端末向けの APK は手元でビルドする。

**applicationId を変えたら、Firebase コンソールで新しいパッケージ名の Android アプリを
登録し直し、`google-services.json` を取り直すこと。** 古い json のままだと
`No matching client found for package name` でビルドが失敗する。
