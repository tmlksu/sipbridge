# E2E チェックリスト (実機検証)

前提: `docs/SETUP.md` の手順で relay + cloudflared が起動し、Asterisk に内線 101 として登録済み。

## 0. 端末側の事前設定

| 端末 | ビルド | モード | 追加設定 |
|---|---|---|---|
| Echo Show 5 (LineageOS 18.1 / API 30) | `assembleFossDebug` → `app-foss-debug.apk` | PERSISTENT | オーバーレイ許可・電池最適化除外・自動起動 (EchoSIP と同じ) |
| Galaxy S25 (One UI / API 35) | `assembleGmsDebug` (T5 完了後は `google-services.json` 必須) | PUSH | 下記「API 34+ の注意」 |

API 34+ (S25) の注意
- **フルスクリーン通知**: 設定 → アプリ → SIP Bridge → 「全画面通知の許可」を ON (adb sideload なら既定 ON のことが多いが要確認)。
- **他のアプリの上に表示** (SYSTEM_ALERT_WINDOW) を ON にしないと、バックグラウンドからの着信画面起動が Android に抑止される。
- 通知権限 (POST_NOTIFICATIONS) を初回起動時に許可する。
- 電池: 「制限なし」に設定。Samsung の「スリープ状態にしないアプリ」に追加。

## 1. relay 単体

- [ ] `docker compose ps` で relay/cloudflared が `healthy`/`running`
- [ ] Asterisk `pjsip show contacts` に 101 が `Avail`
- [ ] `curl https://sip.<domain>/healthz` に Access ヘッダ無し → 403 (Cloudflare) / 偽ヘッダ → 401 (relay)
- [ ] 正しい Service Token → 200

## 2. 接続・登録

- [ ] Echo Show: サービス開始 → 通知が「Bridge 接続中 (登録OK: 101)」
- [ ] S25: 設定保存 → 「待機中 (PUSH モード)」。発信ボタン → 接続 → 呼出中
- [ ] relay ログに `WS 接続 device=<uuid>` が端末ごとに出る

## 3. 着信 (別内線 → 101)

- [ ] 両端末が同時に鳴る (`incoming` ファンアウト)
- [ ] Echo Show で応答 → S25 に「他端末で応答」→ 双方向音声 (Echo Show ⇄ 発呼側)
- [ ] Echo Show で終了 → 発呼側が切れる (BYE)。逆に発呼側が切る → Echo Show が「通話終了」
- [ ] 音質: 途切れ・遅延を体感で記録 (RTP over WS の評価。悪ければ DESIGN.md §6 の代替へ)

## 4. push (S25, T5 完了後)

- [ ] アプリを強制終了した状態で 101 に着信 → S25 が起床し着信画面 → 応答できる
- [ ] 25 秒放置 → 発呼側に 480、S25 側は表示が消える

## 5. 異常系

- [ ] 通話中に S25 の Wi-Fi を切る → 10 秒以内に再接続 (モバイル回線) で通話継続。10 秒超で relay が BYE
- [ ] cloudflared を止める → 端末は「再接続中…」、復旧後に自動で「登録OK」
- [ ] Service Token を Cloudflare で失効 → 端末に「認証失敗 (403)」
- [ ] relay 再起動 → Asterisk に再登録、端末は自動再接続

## 6. UI v1.2 (T11) の確認項目

アプリ UI/UX 追い込み (触覚フィードバック / スピーカー既定 OFF / 端末連絡先連携) 後の実機確認。

- [ ] キーパッドのキー・⌫ を押すと短く振動する。**発信ボタン・タブ・設定トグル・履歴/連絡先の行では振動しない**
      (端末設定の「触覚フィードバック」を OFF にすると一切振動しないこと)。
- [ ] 通話中の DTMF シートのキーでも振動する。
- [ ] **Echo Show 5 (受話口なし)**: 設定「スピーカーで応答」OFF のまま応答して**相手の声が聞こえる**
      (`AudioRoute.hasEarpiece()` が false → 強制スピーカー。ここが本変更の一番のリスク)。
- [ ] Galaxy S25: 既定 (OFF) で応答すると受話口から鳴り、通話中のスピーカーボタンで切り替えられる。
- [ ] 連絡先タブ → ＋ → 「端末の連絡先から選ぶ」→ **複数番号を持つ相手で番号を選べる**
      → 名前・番号が欄に入る → 保存でローカルに入る (READ_CONTACTS の許可ダイアログは**出ない**)。
- [ ] 設定「端末の連絡先を表示」を ON → 権限ダイアログ → 許可すると連絡先タブに「端末」グループが出る。
      拒否するとトグルが OFF に戻る。OS 設定で権限を剥がすと次回表示時に自動で OFF。
- [ ] 端末グループの行: タップで発信、長押しで「連絡先に保存」のみ (編集・削除が出ない)。
- [ ] 端末連絡先に載っている番号から着信すると名前が出る (relay の display が空のとき)。

## 7. 記録

結果・気づきは本ファイル末尾に日付付きで追記する。

### 2026-09-15 (本番 VPS / Echo Show 5 ×2 実機)

構成は `docs/SETUP.md` 参照 (この環境固有の実値は非公開の `ops/` 側)。relay はネイティブ systemd、cloudflared は
トークン貼付待ち。テスト経路は `adb reverse` + `ssh -L` チェーン (`ws://127.0.0.1:18080`)、
`AUTH_MODE=token`。

- [x] relay 起動・`/healthz` 200・Asterisk 内線 2104 登録 (`sip show peers` 127.0.0.1, 1ms)
- [x] Echo Show ×2 が WSS 接続 (device 28cf4242 / 60d937f4)
- [x] `channel originate SIP/2104 application Playback demo-congrats` →
      バックグラウンドから全画面着信 → 応答 → RTP 双方向 0% loss → 終了 → 0 channels
- [x] 同時着信ファンアウト (2 端末同時に全画面着信) → 1 端末応答 →
      他端末に「他端末で応答」表示 → 終了 → BYE
- [ ] §1 の `docker compose ps` 項目は Docker 未使用のため対象外 (VPS ネイティブ版)
- [ ] S25 項目 (§4 push / API 34+ 注意) は S25 未接続のため未実施
- [x] tunnel 開通後: `wss://relay.example.com` + Access 切替 (`cf-access`) での再検証
      (2026-09-16 下記参照。AuthMode=cf-access 運用へ切替済み)

気づき:
- 端末側 TCP 8080 は旧 EchoSIP が使用中 → テストは 18080 を使用。
- debug APK に平文 ws:// 許可を追加 (`app/src/debug/AndroidManifest.xml`)。
- `extensions.conf` の sed 編集は `&` 展開で破損するため手動/python3 推奨 (§1.4)。

### 2026-09-16 (本番経路 `wss://relay.example.com` + Access / Echo Show 5 .133 単端末)

cloudflared tunnel + Access (Service Auth ポリシー) 開通、relay は `AUTH_MODE=cf-access` に
切替済み (relay 起動ログ: `認証: Cloudflare Access JWT team=example.cloudflareaccess.com`)。
端末側は Access Client ID/Secret を設定済み。

- [x] `curl https://relay.example.com/healthz` → 200 (認証不要)
- [x] `/v1/session` Access 無し → 302 (エッジで拒否) / 正規 Service Token → relay 到達
- [x] 端末 WS 接続 (`WS 接続 device=28cf4242`) → 通知「Bridge 接続中 (登録OK: 2104)」
- [x] `channel originate SIP/2104 application Echo` → 全画面着信 (Displayed +333ms) →
      応答 → **RTP 双方向 0% loss (各 544pkt, ジッタ 0.0086s)** →「終了」→ BYE →
      `sip show channels` 2104 は 0

気づき:
- 同一プロセスから WS が **2 本**張られるバグ (着信 2 重配信・IncomingCallActivity 2 重起動)。
  `RelayClient.connect()` の二重呼び出し競合が原因。`openSocket()` に接続済み/接続中ガードを追加
  し修正済み (再インストール後 WS 1 本を確認)。
- push (FCM) は relay 側未設定 (`FCM_PROJECT_ID` 未設定) のため P780 (API 30, GMS あり) での
  push テストは設定投入後に実施予定。gms flavor ビルド・インストールは済み。

### 2026-09-16 (続) — push (FCM) テスト / P780 (API 30, GMS あり, PUSH モード)

構成: Echo Show .133 は PERSISTENT (WS 常時接続)、P780 は PUSH モード。
FCM プロジェクト `sipbridge-e860c`、relay `push: FCM 有効`。

- [x] FCM トークン登録 (register_push → `/var/lib/sipbridge/push-state.json` 永続化)
- [x] `.133` 接続中の状態で originate → **P780 に FCM push → 起床 → WS 再接続 → 全画面着信**
      (push 送信→受信→WS open→着信UI まで ≈1.6秒)
- [x] P780 で応答 → **RTP 双方向 0% loss (各 272pkt, ジッタ 0.0057s)** → 終了 → BYE → 0 calls

テスト中に発見・修正した不具合:

1. **FCM data payload の予約語 `from`** → HTTP v1 が 400 INVALID_ARGUMENT で送信失敗。
   `internal/push/fcm.go` の data キーを `from` → `caller` に変更
   (Android `BridgeMessagingService` は `caller` を読み `from` に後方互換)。
2. **push が「WS 接続ゼロ時」しか飛ばない** → Echo Show (PERSISTENT) が接続中だと
   PUSH モード端末に push が飛ばない (マルチデバイスで着信が欠落)。
   `internal/session/session.go` を「接続中デバイスを除く全オフラインデバイスに送る」
   (`pushIncomingOffline`) に修正。
3. **FCM トークンの再起動喪失** → onNewToken→setPushToken が BridgeService 再生成前に
   起きると `pendingPushToken` が失われ register_push が送られない。
   `BridgeService` が gms の prefs (`last_fcm_token`) から復元 + PUSH モード起動時に
   未登録の可能性があれば一度接続して登録するよう修正。
4. `am force-stop` 直後の端末は Android の仕様で FCM を受信しない (停止状態の為)。
   push テストは通常の待機状態 (サービス生存 or プロセス死亡) で行うこと。
