# AGENTS.md — sipbridge 作業ルール (paseo エージェント共通)

- 目的・方式: `DESIGN.md`。プロトコル: `docs/PROTOCOL.md`。タスク: `TASKS.md`。**担当タスクの範囲外のディレクトリは編集しない**。
- **git commit / push はしない** (依頼で commit / push を求められたときだけ実施)。作業完了時は変更ファイル一覧・実行したビルド/テストコマンドと結果を最終報告に書く。
- 秘密情報 (Access トークン, サービスアカウント, SIP パスワード) をリポジトリに書かない。`.env.example` にはダミー値。
- 言語: コード内コメント・ドキュメントは日本語可。識別子は英語。
- Go: toolchain は `~/go-toolchain/go/bin/go` (Go 1.27)。`export PATH=$HOME/go-toolchain/go/bin:$PATH`。`gofmt` 済み、`go vet` クリーン、外部依存は最小限 (sipgo, coder/websocket, golang-jwt は可)。
- Android: JDK/SDK/Gradle は `~/android-build/` 配下。
  `export JAVA_HOME=$HOME/android-build/jdk PATH=$HOME/android-build/jdk/bin:$PATH ANDROID_HOME=$HOME/android-build/sdk ANDROID_SDK_ROOT=$HOME/android-build/sdk`
  ビルドは `cd android && ~/android-build/gradle-8.9/bin/gradle <task> --no-daemon`。minSdk 29 / compileSdk 35。Echo Show 5 は API 30 (LineageOS 18.1, GApps 無し)、Galaxy S25 は API 36 (Android 16 / One UI 8.0)。
- Docker はローカルで利用可 (`docker`, `docker compose`)。ネットワークアクセス可 (`go mod download`, Gradle 依存取得)。
- 参照元 (読み取り専用): `/home/sudosu/echoshow5/sipapp` (EchoSIP v1.5 の原本。`android/` はそのコピー)。
- 不明点は最終報告に「要確認」として列挙し、妥当な仮定で進める (レビュー担当が判断する)。
