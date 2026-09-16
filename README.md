# sipbridge

宅内 Asterisk を **ポートを開けず・平文 UDP を待ち受けず・push で着信** できるようにする、
「Asterisk + 変換装置 (relay, Docker) + Android アプリ」構成。

- `DESIGN.md` — 方式検討と採用理由
- `docs/PROTOCOL.md` — relay ⇄ アプリの WebSocket プロトコル (v1.1: SIP アカウントはアプリ側で設定)
- `docs/UI-DESIGN.md` — アプリ画面仕様 (`docs/design/android-app-design.pdf` の文章化)
- `scripts/` — ビルド/テスト一括 (`check.sh`)・端末投入 (`adb-setup.sh`)・アイコン生成 (`gen-icon.py`)
- `docs/DISTRIBUTION.md` — 配布 (F-Droid を第一ターゲット) の検証結果と残タスク
- `docs/RELEASE.md` — リリース署名 (applicationId / keystore) と GitHub Releases の手順
- `docs/REPO-SPLIT.md` — public / private (`ops/`) の分割方針と移行手順
- `TASKS.md` — 実装タスク分割 (paseo エージェント割当)
- `relay/` — 変換装置 (Go)。SIP サーバ (Asterisk / ひかり電話 HGW など) には SIP/UDP 内線として振る舞い、アプリには Cloudflare Tunnel 越しの WSS を提供
- `deploy/` — Docker/compose/cloudflared
- `android/` — アプリ (EchoSIP standby v1.5 から派生)
- `docs/echoshow5-device-notes.md` — Echo Show 5 (cronos) の LineageOS 化メモ (参考)

経路: `App ─WSS─▶ Cloudflare (Access: Service Token) ─Tunnel─▶ relay ─SIP/RTP (LAN)─▶ Asterisk`

## ライセンス

Apache License 2.0 (`LICENSE`)。
