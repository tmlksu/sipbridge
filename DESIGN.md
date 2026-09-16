# sipbridge — 方式検討 (Asterisk + 変換装置 + Android アプリ / ポートを開けない安全な内線)

作成: 2026-09-15 / 検討者: Claude Fable 5.1 (オーケストレーション・レビュー担当)

## 0. 現状と要件

現状
- 宅内 Asterisk に、Android (Echo Show 5 の自作 EchoSIP、スマホの Groundwire) が **UDP 5060 平文** で REGISTER / 着信。
- 参考実装: `android/` (EchoSIP standby v1.5, 自前 Kotlin SIP/RTP スタック, NDK 不要)。
- Cloudflare Zero Trust は利用中 (team: `example.cloudflareaccess.com`, `cloudflared` は各ホストに導入済み)。

要件
1. **インターネットに穴を開けない** (ルータのポート開放/UPnP なし)。Cloudflare Zero Trust (CFZT) 等で。
2. **平文 UDP の待ち受けをやめる** → TLS 化。端末側で 5060 を LISTEN しない。
3. **push 通知** で着信 (常時 REGISTER に依存しない)。
4. 変換装置 (relay) は **Docker** で動かしたい。
5. アプリは Groundwire のままか、スクラッチか、拡張か。
6. 実装は paseo の Opus / Muse Spark に委譲、Fable がオーケストレーションとレビュー。

## 1. 候補方式

| # | 方式 | 穴開け | UDP平文回避 | push | アプリ改修 | 音質(媒体) | 難易度 | 備考 |
|---|---|---|---|---|---|---|---|---|
| A | **WARP + Tunnel private network** (WARP-to-Tunnel) | 不要 | WARP内で暗号化 | ✕ (Acrobits push は PBX に到達不可) | 不要 (Groundwire/EchoSIP をLAN IPに向けるだけ) | UDP そのまま ◎ | 低 | 端末に WARP (1.1.1.1) アプリ必須。VPN スロット占有。Echo Show (LineageOS/GApps無) では登録フローと再認証が面倒 |
| B | **自作 relay (SIP⇄WSS 変換装置) + Cloudflare Tunnel + Access** | 不要 | WSS (TLS) | ○ FCM / 常駐WSS | 必要 (アプリからSIPスタックを撤去し軽量化) | RTP を WS(TCP) 上で運ぶ △〜○ | 中 | **推奨**。relay が Asterisk 側の SIP UA を代行し 24h 登録。アプリは JSON+音声フレームだけ |
| C | Asterisk 純正 WSS (chan_pjsip ws) + WebRTC (DTLS-SRTP) + Cloudflare TURN | 不要 | WSS+SRTP | △ (要 別途) | 大 (ICE/DTLS/SRTP 実装 or libwebrtc 同梱) | UDP (TURN経由) ◎ | 高 | Cloudflare Realtime TURN は短命クレデンシャル。Asterisk 側の ICE/TURN 設定も必要。v2 候補 |
| D | Tailscale 等の mesh VPN | 不要 | VPN内暗号化 | ✕ | 不要 | UDP ◎ | 低 | Cloudflare ではない。A と同じく VPN スロット占有・push 不可 |
| E | Asterisk 5061/TLS + SRTP を直接公開 | **必要** | TLS/SRTP | ○ (Groundwire push) | 不要 | ◎ | 低 | 要件1 に反するので不採用 |

Cloudflare 側の制約 (方式選定の前提)
- Cloudflare Tunnel の公開ホスト名で通せるのは **HTTP/HTTPS/WebSocket** のみ。生の SIP/TLS や UDP/RTP は Spectrum (Enterprise) が必要。
- UDP を通す唯一の無償経路は **WARP クライアント → Tunnel private network** (方式A)。
- WebSocket は Cloudflare Access (Service Token / ログイン) で保護できる。Access 通過後 `Cf-Access-Jwt-Assertion` が付くので relay で再検証可能。

## 2. Groundwire を使い続けるか (スクラッチ vs 拡張)

- Groundwire に拡張機構 (プラグイン/SDK) は無い。改造不可。
- Groundwire の push は Acrobits のクラウド (SIPIS) が **PBX へ直接 REGISTER** する仕組み。PBX がインターネットから到達できないと push は成立しない → 要件1+3 と両立不可。
- Groundwire は SIP over WebSocket 非対応。よって方式B の相手にはなれない。
- 結論: **push + 穴開け無し** を両立するには自作アプリしかない。ただし SIP スタックをアプリに持つ必要はなく、relay 側に寄せることで **アプリは EchoSIP の UI/音声/常駐部分の再利用 + WebSocket クライアント** で済む (SipEngine.kt は不要になる)。
- Groundwire は方式A (WARP) で LAN IP に TLS 接続する形で **併用可能** (push 無し・常時登録)。移行期間の保険として残せる。

## 3. 推奨アーキテクチャ (方式B)

```
[Android app]  ──WSS(JSON+RTP frames)──▶ Cloudflare Edge ──(Access: Service Token)──▶
   ▲ FCM push                               │ Tunnel (cloudflared, outbound QUIC)
   │                                        ▼
[FCM] ◀── relay ──────────────────────  relay (Go, Docker)  ──SIP/UDP + RTP (LAN)──▶ Asterisk
```

relay (= 変換装置, `relay/`)
- Asterisk に対しては **普通の内線電話機** として振る舞う (REGISTER 常時維持, INVITE の UAS/UAC, RTP 終端)。LAN 内なので UDP でよい (任意で pjsip TLS/SRTP)。
- アプリに対しては **WebSocket 1 本**: テキストフレーム = JSON 制御, バイナリフレーム = RTP パケットそのまま (`docs/PROTOCOL.md`)。
- 着信時: Asterisk へ即 `100/180` → アプリが接続中なら `incoming` を送る / 未接続なら FCM push → アプリが WSS 接続 → 保留中の `incoming` を渡す → `answer` で 200 OK。25 秒応答なしで `480`。
- 認証: Cloudflare Access の JWT (RS256, JWKS は `https://example.cloudflareaccess.com/cdn-cgi/access/certs`, `aud` 検証)。開発用に共有トークンモードも持つ。
- Docker: `deploy/docker-compose.yml` で `relay` + `cloudflared` (tunnel token) を `network_mode: host` で起動 (RTP の UDP ポートを素直に扱うため)。
- 単一内線・単一通話を v1 スコープとする。複数端末は同時接続を許可し `incoming` をファンアウト、最初の `answer` が勝つ。

Android app (`android/`, EchoSIP から派生)
- 削除: `SipEngine.kt` (SIP スタック)。UDP LISTEN は完全に無くなる。
- 追加: `RelayClient.kt` (OkHttp WebSocket, Access Service Token ヘッダ, 再接続, JSON 制御), `RelayMedia` (RtpEngine の DatagramSocket を WS バイナリ送受に差し替え)。
- 常駐モード 2 種: `PERSISTENT` (Echo Show: ForegroundService で WSS 常時接続, 今の運用と同じ) / `PUSH` (S25: FCM で起床し WSS 接続, 通話終了で切断)。
- 設定: relay URL, Access Client ID/Secret, モード, マイクゲイン。パスワードは EncryptedSharedPreferences。
- applicationId は `net.peyan.sipbridge` に変更 (旧 EchoSIP と共存可)。

## 4. セキュリティ層

| 区間 | 保護 |
|---|---|
| アプリ → Cloudflare Edge | TLS 1.3 (WSS)。Access Service Token (端末ごとに発行・失効可) |
| Edge → relay | cloudflared のアウトバウンド QUIC トンネル。relay は `127.0.0.1:8080` のみ LISTEN |
| relay 内 | `Cf-Access-Jwt-Assertion` を JWKS で再検証 (aud 一致必須)。Access を素通りしても入れない |
| relay → Asterisk | LAN。v1 は UDP。v2 で pjsip `transport-tls` + SRTP (relay 側実装は sipgo + pion/srtp) |
| 音声の E2E | Cloudflare Edge で TLS が終端するため **Cloudflare は音声を復号できる**。気になる場合は v2 で WS フレームを端末⇄relay の PSK (AES-GCM) で追加暗号化 |

端末側で LISTEN するポートはゼロ。Asterisk の 5060/RTP はインターネットから見えない。

## 5. push の方式

| 端末 | 方式 | 理由 |
|---|---|---|
| Galaxy S25 (GMS あり) | **FCM data message (high priority)** → `FirebaseMessagingService` → ForegroundService 起動 → WSS 接続 | Doze/アプリ強制終了後も OS が起こしてくれる唯一の手段 |
| Echo Show 5 (LineageOS, GApps 無) | **PERSISTENT**: ForegroundService + WifiLock で WSS 常時接続 (今の EchoSIP と同じ運用) | 常時給電・deep sleep 無効のため常駐で足りる。FCM 不可 |
| 将来 | UnifiedPush (ntfy を同じ Tunnel 裏に置く) | GApps 無し端末で push したくなった場合 |

relay は FCM HTTP v1 (サービスアカウント JSON) で送信。アプリは起動時に `register_push {token}` で relay にトークンを登録 (relay は SQLite/JSON ファイルに永続化)。

## 6. 音質・遅延の見積り (RTP over WebSocket)

- G.711 20 ms = 50 pps × 172 B。WS(TCP) なので再送・HoL ブロッキングが起きるが、家庭回線 + Cloudflare 経路なら実用上問題ないことが多い (WebRTC の TURN/TCP フォールバックと同等)。
- 端末側ジッタバッファは EchoSIP の 3 フレーム→ **5 フレーム (100 ms)** に増やし、上限 20 フレームで捨てる。
- 悪い場合の逃げ道: 方式A (WARP) で UDP 直、または方式C (TURN)。relay の設計は media 部分を差し替え可能にしておく (`Media` インタフェース)。
- 将来 Opus 化: relay で G.711⇄Opus のトランスコード (Asterisk 側 Opus を有効にすれば不要)。

## 7. 実装言語・ライブラリ

- relay: **Go 1.27** (単一バイナリ、Docker `scratch` ベースで数 MB, arm64/amd64 両対応)。
  - SIP: `github.com/emiago/sipgo` (REGISTER/UAS/UAC/ダイアログ)。
  - WebSocket: `github.com/coder/websocket`。
  - JWT: `github.com/golang-jwt/jwt/v5` + JWKS 取得は自前 (10 分キャッシュ)。
  - FCM: `google.golang.org/api/fcm/v1` or 直接 REST + `golang.org/x/oauth2/google`。
- Android: Kotlin, minSdk 29 (Echo Show = API 30, S25 = 35), OkHttp 4.12, Firebase Messaging (flavor で ON/OFF: `gms` / `foss`)。
- テスト: relay は Go の単体 + **Asterisk を Docker で起動した結合テスト** (`deploy/test/`)。Android はロジック層のみ JVM テスト。

## 8. リスクと未決事項

1. Cloudflare の WebSocket プロキシは長時間アイドルで切断する → 20 秒周期の WS ping + 指数バックオフ再接続 + 再接続後の `hello` で状態同期。
2. Access Service Token の有効期限 (作成時に選択, 最長「非期限」) → 端末ごとに発行し、期限を運用表に記録。
3. FCM は Firebase プロジェクトと `google-services.json` が必要 → ユーザー作業 (手順は `docs/SETUP.md` に記載予定)。
4. relay が単一障害点 → Docker `restart: unless-stopped` + healthcheck。Asterisk 側 `qualify=yes` で監視。
5. S25 は複数 SIP アプリ (Groundwire) と共存するが、本アプリは UDP を LISTEN しないので衝突しない。
6. 通話中の Wi-Fi⇄モバイル切替 → WSS 再接続時に `resume {callId}` で通話を継続 (v1.1)。

## 9. 結論

- **方式B を採用**: 「Asterisk + 変換装置 (relay, Docker) + 自作アプリ (EchoSIP 派生)」。
- 穴開け無し (Tunnel)、平文 UDP 無し (WSS + Access)、push あり (FCM / 常駐)、Docker 化、の 4 要件を同時に満たす唯一の構成。
- Groundwire は方式A (WARP) で当面併用。本アプリが安定したら不要。
- 音声を Cloudflare にも見せたくない場合は v2 で PSK 暗号化を追加 (プロトコルに `enc` フィールドを予約)。
