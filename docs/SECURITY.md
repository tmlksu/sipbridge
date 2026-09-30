# sipbridge セキュリティモデル

sipbridge (アプリ ⇄ Cloudflare ⇄ relay ⇄ Asterisk) が何を守り、何を守らないか、
運用で何をすべきかをまとめる。方式全体は `DESIGN.md` §4、プロトコルは `docs/PROTOCOL.md`、
構築手順は `docs/SETUP.md`。括弧内の `#番号` は GitHub issue。

## 1. 守るものと想定する攻撃者

守るもの:

- 内線からの**発信** (なりすまし・通話料)。
- **着信の情報** (発信者番号・表示名) と**通話音声**。
- **SIP パスワード**、Access Service Token、FCM サービスアカウント、push トークン。

想定する攻撃者:

| 攻撃者 | 持っているもの | 主な対策 |
|---|---|---|
| インターネット上の第三者 | relay のホスト名 | Cloudflare Access (Service Token) + relay での JWT 再検証 |
| 漏洩した Service Token を持つ者 | 1 端末分の Client ID / Secret | 端末ごとのトークンと失効、SIP パスワード照合、試行制限、DEVICE_BINDING、既定アカウントを使わない |
| 同一 LAN の機器 | LAN 内の到達性 | WS は loopback のみ待ち受け。SIP/RTP は送信元 IP フィルタのみ (偽装は防げない、§9) |
| Cloudflare / Google | 経路上の平文 | **信頼する第三者として扱う** (§8 の残存リスク) |

## 2. 信頼境界

```
[アプリ]
   │ ① WSS (TLS 1.3) + CF-Access-Client-Id/Secret
   ▼
[Cloudflare edge]  ── TLS 終端・Access のポリシー判定・JWT 発行 (Cf-Access-Jwt-Assertion)
   │ ② Cloudflare Tunnel (cloudflared からのアウトバウンド接続)
   ▼
[cloudflared]  (relay と同一ホスト)
   │ ③ HTTP 平文 (loopback 127.0.0.1:8080 のみ)
   ▼
[relay]  ── JWT 再検証 (署名 RS256 / aud / iss / exp)、X-Device-Id 検証、sip_account 照合
   │ ④ SIP (UDP 5060) / RTP (UDP) 平文 (LAN)
   ▼
[Asterisk]  ── 内線 / 外線への発信
   
[relay] ── ⑤ HTTPS ──▶ [FCM (Google)] ──▶ 端末 (PUSH モードの起床)
```

| 区間 | 守られること | 守られないこと |
|---|---|---|
| ① | 経路上の盗聴・改ざん。Access を通らない接続は edge で 403 | edge で TLS が終端するため **Cloudflare には平文が見える** (音声・`sip_account` の SIP パスワード・発信者番号) (#34) |
| ② | インバウンドのポート開放が不要 | Tunnel トークン (`TUNNEL_TOKEN`) を持つ者はトンネルを乗っ取れる |
| ③ | relay は loopback のみで待ち受け、LAN から直接叩けない | 同一ホストの他プロセスからは到達できる (ホストを信頼する前提) |
| relay | Access を素通りした (JWT の無い・偽造・他チーム・期限切れ・exp 無し) 接続を 401 で拒否 | 有効な JWT を持つ接続同士の区別は principal (§3) と SIP パスワードに頼る |
| ④ | インターネットからは見えない | LAN 内では平文。LAN 上の機器は盗聴・偽装できる (§9、v2 で TLS/SRTP) |
| ⑤ | Google までは TLS | push の中身 (発信者番号・表示名) が Google を経由する (#38、§8) |

## 3. 認証と認可 (relay 側)

### 3.1 Cloudflare Access JWT の再検証 (#36)

`AUTH_MODE=cf-access` のとき、relay は `Cf-Access-Jwt-Assertion` を次の条件で検証する。

- 署名: RS256 のみ (`alg` 混同・`none` は拒否)。鍵は `https://<CF_TEAM_DOMAIN>/cdn-cgi/access/certs` の JWKS (10 分キャッシュ)。
- `aud` に `CF_ACCESS_AUD` を含むこと。
- `iss` が `https://<CF_TEAM_DOMAIN>` と完全一致すること (他のチームで発行された JWT を拒否)。
- `exp` が**存在し**、期限内であること。

`CF_TEAM_DOMAIN` はスキーム無しで書く (例 `example.cloudflareaccess.com`)。`https://` や末尾 `/`・大文字が
混ざっていても relay が正規化する。

JWT から **principal** (認証主体) を取り出す: ユーザー認証なら `sub:<ユーザー ID>`、Service Token なら
`sub` が空なので `cn:<common_name>` (= Client ID)。接頭辞で由来を分け、別種の主体と取り違えないようにしている。
principal は接続ログ (`WS 接続 … principal=…`) と DEVICE_BINDING (§5) に使う。`DEVICE_BINDING=enforce` では
principal の取れない JWT (sub も common_name も無い) を 401 で拒否する。

接続元 IP (`CF-Connecting-IP`) は個人情報のため、**認証失敗時と principal 不一致時だけ**ログに出す。

`AUTH_MODE=token` (開発用共有トークン) は端末を区別できず、HTTP 平文で流れるため、`LISTEN` が loopback
(`127.0.0.0/8`, `::1`, `localhost`) 以外なら起動しない (#37)。前段で TLS 終端する審査用 relay などで意図的に
開く場合だけ `ALLOW_INSECURE_LISTEN=1` を付ける (起動時に警告ログが出る)。

### 3.2 X-Device-Id とオンライン端末数 (#30)

- `X-Device-Id` は `^[A-Za-z0-9._:-]{1,64}$` 以外を HTTP 400 で拒否する (ログ・状態ファイルへの注入防止)。
  アプリの UUID v4、wsprobe の `wsprobe-150405.000`、ops の `e2e-reg` は通る。
- 同時接続の端末 (deviceID) 数が `MAX_ONLINE_DEVICES` (既定 32) に達すると、新しい端末は HTTP 503。
  **接続中の端末の再接続 (張り替え) と状態ファイルに保存済みの端末は上限に関係なく通る**。

### 3.3 sip_account のパスワード照合 (乗っ取り防止)

| 送ってきた端末 | account の状態 | パスワード | 結果 |
|---|---|---|---|
| その account に結び付いた端末 | 登録済み | 一致 | 受理 |
| 同上 | 登録済み | 不一致 | `account_password_mismatch` (結び付けは変えない) |
| 同上 | 未登録 / 登録失敗中 | 不一致 | **パスワード変更として受理** (Asterisk 側で変えた後の追従)。全体の試行制限と 1 接続あたりの回数計上の対象。REGISTER 成功まで仮 |
| 結び付いていない端末 (既定アカウントへの暫定参加を含む) | 状態に関わらず | 一致 | 受理 (2 台目以降の端末の追加) |
| 同上 | 状態に関わらず | 不一致 | `account_password_mismatch` |
| (任意) | 新規 (存在しない) | — | 作成 (上限・試行制限・回数計上あり)。REGISTER 成功まで仮 |

比較は定数時間 (`crypto/subtle`)。以前は「未登録の間は誰のパスワード変更も受理」していたため、Asterisk の
再起動中や再登録バックオフ中に誤パスワードで結び付いて内線を奪えた (正規端末が戻っても攻撃側の結び付きが
残り発信できた)。現在は未登録中の変更を**既に結び付いた端末**に限っている。

**仮の資格情報**: 新規 account とパスワード変更で送られた資格情報は、REGISTER に一度成功するまで仮とする。
仮のまま SIP サーバに認証で拒否 (401/403/407) されることが **3 回以上、かつ最初の拒否から 2 分以上**続くと
(再 REGISTER のバックオフは 5, 10, 20, 40, 60 秒…なので、実際には最初の拒否から約 2 分 15 秒後)、新規 account は
状態ファイルから削除し (結び付けも外れる)、パスワード変更は変更前の資格情報に戻して REGISTER し直す
(元の資格情報での再起動に失敗したら状態ファイルは変えない)。誤パスワードの account が REGISTER を打ち続けて
`MAX_ACCOUNTS` を占拠したり、Asterisk の fail2ban に relay 自身を ban させたりしないためである。その間に一度でも
REGISTER に成功すれば検証済みになる。SIP サーバに届かない (タイムアウト) 失敗は数えない。一度成功した
資格情報はその後に拒否されても削除・差し戻ししない (Asterisk 側のパスワード変更は下の手順で追従する)。
仮かどうかは状態ファイルに保存しないため、取り消し前に relay を再起動すると検証済みとして読み込まれる (§8)。

**変更・追加の順序**:

- **パスワード変更は Asterisk 側を先に変えるのが確実**。relay は次の再 REGISTER (登録中は 4 分ごと) で失敗状態に
  なるので、その後にアプリへ新しいパスワードを入れる (変更として受理される)。relay がまだ登録済みの間に
  アプリを先に変えると `account_password_mismatch` で拒否される。
- 登録が失敗している間 (Asterisk の再起動中など) にアプリを先に変えた場合は、**2 分以内に Asterisk 側も
  変える**。過ぎると元のパスワードに差し戻される (`account_failed`)。そのときは Asterisk 側を変えてから
  アプリで入れ直す。
- 新しい内線をアプリに先に設定した場合も、2 分以内に Asterisk 側へ内線を追加する。過ぎると account は削除される
  ので、追加後にアプリで入れ直す。

**Asterisk 側でパスワードを変えた場合**:

- その account に結び付いた端末が 1 台でもあれば、その端末のアプリに新しいパスワードを設定するだけでよい
  (登録が失敗状態になっているので変更として受理される)。
- 結び付いた端末が 1 台も無い場合 (全端末を消した・新しい端末だけで使う等) は、結び付いていない端末から
  新しいパスワードを送っても保存値と一致しないため拒否される。relay を止めて状態ファイルの
  `accounts.<user>` を削除する (または `password` を新しい値に書き換える) か、Asterisk 側で一時的に元の
  パスワードに戻してから端末を結び付け、改めて変更する。

  ```sh
  # Docker 構成の例 (deploy/ で実行)。procd でも relay を止めてから同じ編集をする。
  docker compose stop relay
  sudo jq 'del(.accounts["<user>"])' data/state.json | sudo tee data/state.json.new >/dev/null
  # 書き換える場合: jq '.accounts["<user>"].password = "<新パスワード>"'
  sudo install -m 600 -o 65532 -g 65532 data/state.json.new data/state.json && sudo rm data/state.json.new
  docker compose start relay
  # その後、アプリで新パスワードを設定する (削除した場合は新規 account として作られる)。
  ```

### 3.4 総当たり・水増しの抑止 (#30)

- パスワード不一致 (と試行制限) の応答は 1 秒遅らせる。1 接続は逐次処理なので 1 接続あたり毎秒 1 回が上限。
  新規 account の作成とパスワード変更も、受理されたうえで同じく遅らせて 1 回に数える (新しいパスワードを
  SIP サーバに試す操作のため)。
- 1 接続で失敗 (と新規作成・パスワード変更) が 3 回になると WS を 1008 (policy violation) で閉じる。
- relay 全体のトークンバケット (10 回連続、以後 6 秒に 1 回) を、**保存値と一致しない `sip_account`
  (結び付いた端末のパスワード変更を含む)**、**結び付いていない端末による既存 account への試行**、
  **新規 account の作成**、**状態ファイルに無い端末の `register_push`** が消費する。結び付いた端末が同じ
  パスワードを再送する (再接続ごと) のは消費しない。枯渇中は `rate_limited`。
- 保存済み端末の push トークン変更は 10 秒に 1 回まで (`rate_limited`)。同じトークンの再送は書き込まない。
- 新規 account は `MAX_ACCOUNTS` (既定 16) まで (`too_many_accounts`)。状態ファイルに保存する端末は
  `MAX_STORED_DEVICES` (既定 64) まで (`too_many_devices`)。どちらも**新規作成時だけ**適用し、起動時の
  状態ファイルは超過していても全件読む。保存数が上限のとき、既定アカウント (`SIP_USER`) の無い構成では
  account に結び付いていない未接続の端末 (push 登録だけの端末。着信 push の対象にならない) のうち最も古い
  ものを削除して空きを作る。ランダムな端末 ID の `register_push` で保存枠を恒久的に埋められないようにするため。

## 4. 秘匿情報の所在と権限

| もの | 置き場所 | 中身 | 権限 |
|---|---|---|---|
| `.env` | `deploy/.env` (Docker) / procd の設定 | `TUNNEL_TOKEN`、`DEV_TOKEN`、`SIP_PASSWORD` (既定アカウントを使う場合)、`FCM_*` | `600`、運用者のみ。リポジトリに置かない |
| データディレクトリ | `deploy/data` → `/var/lib/sipbridge` | 下記 2 ファイル | `700`、relay の実行ユーザー (Docker は UID 65532) |
| `state.json` | データディレクトリ | **SIP パスワード (平文)** (#33)、端末と account の結び付け、FCM push トークン、principal | `600` (relay が作成時に設定) |
| `service-account.json` | データディレクトリ | FCM 送信用の Google サービスアカウント秘密鍵 | `600`、relay の実行ユーザー |
| アプリ側 | 端末の EncryptedSharedPreferences | Access Client ID / Secret、SIP パスワード | Android のアプリサンドボックス |

- `state.json` の SIP パスワードは Asterisk へ REGISTER するため復元可能な形で必要 (#33)。バックアップ・
  コピー先の権限にも注意する。
- Docker 構成では relay も `env_file: .env` を読むため、relay コンテナの環境変数にも `TUNNEL_TOKEN` が入る
  (relay は使わない)。気になる場合は cloudflared 用の値を別ファイルに分ける。
- cloudflared のトークンは **argv ではなく環境変数 `TUNNEL_TOKEN`** で渡す。argv は `ps` や
  `/proc/<pid>/cmdline` から同一ホストの全ユーザーが読める。
- 本番 (NanoPi R2S, OpenWrt) は Docker を使わず procd で動かしている。procd でも同じ権限を守り、relay は
  root 以外の専用ユーザーで動かすことを推奨する (`procd_set_param user …`)。

## 5. DEVICE_BINDING (端末 ID と principal の結び付け, #31)

`X-Device-Id` は秘密ではない (端末内・ログ・状態ファイルに現れる)。別の Service Token を持つ者が他人の
端末 ID を名乗ると、その端末の account の結び付けを使って発信でき、さらにその端末が「オンライン」扱いに
なって本物の端末への着信 push が止まる。これを防ぐため、端末 ID ごとに最初の principal を記録する (TOFU)。

| `DEVICE_BINDING` | 動作 |
|---|---|
| `off` | 記録も検査もしない |
| `warn` (既定) | 記録する。記録と異なる principal の接続はログ (`端末 ID の principal が記録と不一致 … rejected=false`) のみ |
| `enforce` | 記録する。記録と異なる principal の接続を **WS へ昇格する前に HTTP 409** (本文 `device_binding_mismatch`) で拒否する (オンライン扱いにしない)。principal の取れない JWT は 401 |

- 記録は `state.json` の `devices.<id>.principal` に `cn:<Client ID>` / `sub:<ユーザー ID>` の形で入る。
- 記録するのは、端末が状態ファイルに保存されたとき (`sip_account` の受理・`register_push`)、または保存済みで
  principal が未記録の端末が接続したとき。接続しただけの端末 (wsprobe の使い捨て ID 等) は記録しない。
  書き込みは値が変わるときだけ (microSD 配慮)。
- `AUTH_MODE=token` は principal が無いので対象外。
- 端末の記録 (account の結び付けも push 登録も無くなった端末) が消えると principal の記録も消える。

運用:

1. まず `warn` で動かし、全端末が一度接続して principal が記録されるのを待つ
   (`state.json` の `devices.<id>.principal`)。
2. ログに不一致が出ていないことを確認してから `DEVICE_BINDING=enforce` にする。
3. **端末の Service Token を作り直す (Client ID が変わる) と principal も変わる**。
   - `enforce` では、その端末は 409 で締め出される。現行アプリは 409 を認証エラーとは表示せず、通常の
     接続失敗として 30 秒間隔までのバックオフで再接続を続ける (設定画面は「接続中/再接続中」のまま。PUSH
     モードでも起床のたびに再試行する)。relay のログには接続ごとに `端末 ID の principal が記録と不一致 …
     rejected=true` が出る。下の手順で記録を消せば、次の再接続で新しい principal が記録されて繋がる
     (アプリ側の操作は不要)。
   - `warn` では接続できるが、記録は古いままなので**接続のたびに不一致の Warn が出続ける**。正当な
     作り直しなら同じ手順で記録を消して止める (放置すると本物の不一致を見落とす)。
4. 1 本の Service Token を複数端末で共有していると principal が同じになり、端末同士の区別には役立たない
   (§6 の端末ごとのトークンを推奨)。

記録の解除 (該当端末の principal を消す):

relay は状態をメモリに持ち変更時に書き戻すため、**必ず relay を止めてから**編集する。

```sh
# Docker 構成の例 (deploy/ で実行)
docker compose stop relay
sudo jq 'del(.devices["<deviceId>"].principal)' data/state.json | sudo tee data/state.json.new >/dev/null
sudo install -m 600 -o 65532 -g 65532 data/state.json.new data/state.json && sudo rm data/state.json.new
docker compose start relay
```

procd (OpenWrt) では relay のサービスを止めてから同じ編集をし (jq が無ければ手で該当の `"principal": …` 行を
消す)、所有者と `600` を保ったまま起動する。次の接続時に新しい principal が記録される。

## 6. 推奨設定と Service Token 漏洩時の影響

### 漏洩した Service Token 1 本でできること

- relay に接続できる (Access を通過し、JWT も正しい)。
- **既定アカウント (`SIP_USER`) が設定されていると、SIP パスワード無しでその内線から発信できる**
  (起動時に警告ログが出る)。
- 既存の account に結び付くには SIP パスワードが必要 (§3.3)。総当たりは §3.4 で絞られる。
- 存在しない account 名を指定して新規作成すると、relay がその名前とパスワードで Asterisk に REGISTER する。
  つまり **Asterisk 上の未使用内線のパスワードを、パスワード変更と合わせて relay 全体で毎分 10 回程度まで
  試せる** (§3.4)。認証で拒否され続けた仮の資格情報は約 2 分で取り消される (§3.3)。強いパスワードで補う。
- **Asterisk の fail2ban について**: relay は全内線の REGISTER を 1 つの送信元 (relay のホスト。同居なら
  127.0.0.1) から送るため、Asterisk 側の fail2ban が relay を ban すると**全内線が止まる**。relay の送信元
  アドレスは fail2ban の `ignoreip` に入れ、relay 経由の総当たりの検知は relay のログ
  (`account のパスワード不一致`、`sip_account の試行が多すぎる`、`新規アカウントの REGISTER が認証で拒否され
  続けたため削除する`) で行う。fail2ban は relay 以外の送信元 (LAN の SIP 端末等) に対して使う。
- 他人の端末 ID を知っていれば、その端末の結び付けで発信できる (`DEVICE_BINDING=enforce` で防げる。ただし
  トークンを共有していない場合に限る)。
- 端末枠 (`MAX_ONLINE_DEVICES`) を埋めて**新規**端末の接続を妨げられる (保存済みの端末は影響を受けない)。

### 推奨設定

- **Service Token は端末ごとに発行する**。失効の単位になり、DEVICE_BINDING が効く。トークンに有効期限
  (Duration) を設定する。
- **既定アカウント (`SIP_USER`/`SIP_PASSWORD`) は使わない**。アプリの設定で端末ごとに `sip_account` を送る。
- `DEVICE_BINDING=enforce` (§5 の手順で移行)。
- `LISTEN=127.0.0.1:8080` のまま。`AUTH_MODE=cf-access`。
- Access のポリシーは Service Auth のみ (ブラウザログイン用ポリシーを作らない)。
- **Asterisk 側で relay 内線の発信 context を制限する**。relay 経由の内線が漏れても国際電話・高額番号へ
  発信できないようにする。例:

  ```ini
  ; pjsip.conf: relay が使う内線は専用 context に入れる
  [101]
  type=endpoint
  context=from-relay
  ; …

  ; extensions.conf
  [from-relay]
  ; 内線同士
  exten => _1XX,1,Dial(PJSIP/${EXTEN},25)
  ; 外線は必要な範囲だけ明示的に許可する (例: 国内の固定・携帯)。
  exten => _0[1-9]XXXXXXXX,1,Dial(PJSIP/${EXTEN}@trunk,60)
  exten => _0[789]0XXXXXXXX,1,Dial(PJSIP/${EXTEN}@trunk,60)
  ; 国際・高額/特殊番号は拒否する。上の 10 桁パターンにも一致するが、
  ; Asterisk はより具体的なパターンを優先するのでこちらが勝つ。
  exten => _010.,1,Hangup(21)
  exten => _0570XXXXXX,1,Hangup(21)
  exten => _0990XXXXXX,1,Hangup(21)
  exten => _0180XXXXXX,1,Hangup(21)
  exten => _X.,1,Hangup(21)
  ```

  パターンは例。回線 (trunk 名)・番号計画に合わせて調整し、`dialplan show from-relay` で確認する。

## 7. 端末紛失時の失効手順

1. **Service Token を失効**: Zero Trust → Access → Service Auth → Service Tokens → 該当トークンを Revoke / 削除。
   以後の新規接続は edge で拒否される。
   - relay は JWT を**接続時にだけ**検証する。失効前から繋がっている WS はそのまま残るので、relay を再起動して
     既存の接続を切る (他の端末は自動で再接続する)。
2. **SIP パスワードを変更**: 紛失端末には SIP パスワードも保存されている。その端末が使っていた内線の
   パスワードを Asterisk 側で変える。登録が失敗状態になるので、残りの端末 (その account に結び付いた端末) の
   アプリに新しいパスワードを設定すれば切り替わる (§3.3)。
3. **状態ファイルから端末を削除**: 紛失端末の結び付けと push トークンを消す (消さないと着信時に紛失端末へ
   発信者番号入りの push が送られ続ける)。端末 ID はログの `WS 接続 device=… principal=…` や
   `state.json` の `principal` で特定する。relay を止めてから:

   ```sh
   sudo jq 'del(.devices["<deviceId>"])' data/state.json | sudo tee data/state.json.new >/dev/null
   sudo install -m 600 -o 65532 -g 65532 data/state.json.new data/state.json && sudo rm data/state.json.new
   ```

   その account に結び付いた端末が 0 になった場合、account は次回起動時に REGISTER されるが、どの端末も
   結び付かないまま残る。不要なら `accounts` からも消す。手順 2 でパスワードを変えた account に結び付いた
   端末が残っていない場合は、§3.3「Asterisk 側でパスワードを変えた場合」の手順で `accounts.<user>` を
   削除 (または書き換え) してから、残りの端末で新パスワードを設定する。

## 8. 残存リスク

| 項目 | issue | 内容 / 緩和 |
|---|---|---|
| Cloudflare に平文が見える | #34 | edge で TLS 終端するため音声・SIP パスワード・発信者番号が Cloudflare に見える。v2 で端末⇄relay の追加暗号化を検討 (DESIGN.md §4) |
| state.json の SIP パスワード平文 | #33 | REGISTER に必要。権限 (`600`/`700`) とバックアップの扱いで守る |
| FCM push の内容 | #38 | `incoming` の push に発信者番号・表示名が載り Google を経由する。今回はコード変更なし。起床だけを push し内容は WS で取る方式が候補 |
| LAN の SIP/RTP が平文 | #28 #29 #35 | 送信元 IP フィルタで単純な注入は弾くが、UDP の送信元偽装・盗聴は防げない。VLAN 分離 (§9) |
| JWT は接続時のみ検証 | — | トークン失効後も既存の WS は残る。失効時は relay を再起動する (§7) |
| 仮の資格情報は永続化しない | #30 | 取り消し (§3.3、約 2 分) の前に relay を再起動すると、誤パスワードの新規 account / 変更後パスワードが検証済みとして読み込まれ、認証拒否のまま再 REGISTER (最大 60 秒間隔) を続ける。ログの `REGISTER 結果 ok=false` で気付いたら §3.3 の手順で状態ファイルを直す |
| TOFU の初回 | #31 | principal 未記録の端末 ID を先に使われると攻撃側の principal が記録される。共有トークンでは区別できない |
| 新規 account 経由の Asterisk パスワード試行 | #30 | 毎分 10 回程度まで。Asterisk 側の失敗検知で補う |
| 仮 account による `MAX_ACCOUNTS` の占拠 | #30 | 誤パスワードの新規 account は取り消しまで約 2 分残るため、漏洩トークンで作り続けると上限 (16) を埋められ、正規の新規設定が `too_many_accounts` / `rate_limited` になりうる。トークン失効で解消 (§7) |
| 新規端末の接続妨害 | #30 | 漏洩トークンで `MAX_ONLINE_DEVICES` を埋められる。保存済み端末は影響なし。トークン失効で解消 |
| ログの個人情報 | — | principal (Service Token の Client ID / ユーザー ID)・端末 ID がログに出る。IP は認証失敗・不一致時のみ |

## 9. LAN 側 SIP/RTP の送信元検証と発信先の検証 (#28 #29 #32 #35)

**信頼境界は LAN セグメント**。relay は SIP サーバ (Asterisk / ひかり電話 HGW) と平文 UDP の
SIP/RTP でやり取りする。SIP の待受ポート・REGISTER 送信ソケット (いずれもエフェメラル)・
RTP ポート (`RTP_PORT_MIN..MAX`) は全インタフェース (IPv4/IPv6 デュアルスタック) で受ける。
README の「平文 UDP を待ち受けず」は端末 (アプリ) 側の話で、アプリは WSS だけを使う。

### 送信元フィルタ (#28 #29)

relay は SIP 要求 (INVITE / BYE / CANCEL など) を、SIP サーバとみなせる送信元 IP からのもの
だけ受け付ける。応答 (`SIP/2.0 …`) はトランザクション照合で守られるので通す。許可するのは:

- `SIP_HOST` の解決結果 (起動時と再 REGISTER ごとに再解決、DNS 失敗時は前回値を保持)
- REGISTER 200 OK の送信元
- 同居時 (`SIP_HOST` がループバックか自ホストのアドレス) は 127.0.0.1 / ::1 / 自ホストの
  インタフェースアドレス (完全一致)。明示した `LOCAL_IP` も自ホストのアドレスとみなす
  (`LOCAL_IP` がインタフェースに無い NAT 越しの公開 IP で、`SIP_HOST` = `LOCAL_IP` の構成でも
  Asterisk からの INVITE / RTP を落とさないため)
- `SIP_TRUSTED_SOURCES` (CIDR か IP のカンマ区切り)

それ以外は応答せずに捨て、初回と 100 件ごとに `許可外の送信元からの SIP 要求を破棄 src=…` を
記録する。RTP は、SDP `c=` の IP と上記の信頼集合の IP からのパケットだけを受け付ける
(ポートは NAT / symmetric RTP でずれるので照合しない)。RTP v2 でない・12 バイト未満の
パケットも捨てる。同じ Call-ID の新規 INVITE は 482 で拒否する (通話エントリの上書き防止)。

**これは認証ではない**。UDP の送信元は偽装できるため、同じセグメントに悪意あるホストがいれば
Asterisk の IP を騙った偽 INVITE (着信表示の詐称、回線の占有) や RTP の注入はできる。
盗聴できる者は通話内容の傍受、ダイアログ識別子を使った BYE / re-INVITE、REGISTER の
digest (MD5) を材料にしたオフライン総当たりもできる。フィルタは同一 LAN の別ホストからの
単純な攻撃を弾く多層防御にすぎない。

`SIP_TRUSTED_SOURCES` は、`SIP_HOST` の解決結果と実際の送信元が一致しない構成で使う。
例: Asterisk を Docker の bridge ネットワークで動かし、INVITE の送信元がコンテナ IP
(172.17.0.x) になる場合は `SIP_TRUSTED_SOURCES=172.17.0.0/16`。LAN 全体のような広い範囲を
指定するとフィルタの意味がなくなる。破棄ログの `src=` が正規の SIP サーバならその IP を足す。

### 発信先・アカウント情報の検証 (#32)

`dial.to` は percent-decode し、全角の数字・`＋＊＃`・空白・括弧・ハイフン類 (`－ ‐ – — ― − ー` 等) を
ASCII に畳んだうえで、電話番号 (`+` と `0-9*#`、空白と `( ) - .` は除去) か英数字の SIP user 名
(`A-Za-z0-9._-`) に限る。それ以外 (CR/LF、`@ : ; < >` など) は `dial_failed`。`sip_account.user` は
`A-Za-z0-9._+-` の 64 文字まで (違反は `account_failed`。`SIP_USER` が違反なら relay は起動しない)、
`display` は制御文字と書式文字 (U+202E 等の双方向制御・ゼロ幅空白・BOM。絵文字の ZWJ は残す) を除き、
ヘッダに載せる直前に `"` と `\` をエスケープする。

**発信可能な宛先の制限は Asterisk 側で行う**。relay は宛先の妥当性 (文字種) しか見ない。
relay 用内線の context は必要な宛先 (内線・国内) に絞り、国際・高額番号へ出られないようにする。

### 推奨構成 (#35)

- relay と Asterisk は同一ホストに置くか、専用 VLAN で繋ぐ。ゲスト Wi-Fi / IoT 機器と分ける。
- ルータ (R2S など) で動かす場合は、WAN 側と IPv6 (グローバル) から relay の SIP ポート・
  RTP 範囲への入力がファイアウォールで拒否されていることを確認する (relay は `[::]` で待ち受ける)。
- ひかり電話 HGW との間は仕様上平文 (digest/UDP) で、TLS 化できない。
- 将来: Asterisk 環境では SIP over TLS + SRTP (DESIGN.md の v2 項目) で LAN 上の盗聴・偽装にも対処できる。

## 10. cloudflared の更新

バージョンは固定している (`deploy/docker-compose.yml` の `image: cloudflare/cloudflared:<版>`)。自動更新
(`--no-autoupdate`) は使わない。

1. [cloudflared のリリースノート](https://github.com/cloudflare/cloudflared/releases) で変更点
   (特にセキュリティ修正・非互換) を確認する。
2. Docker 構成: `deploy/docker-compose.yml` のタグを上げ、
   `docker compose pull cloudflared && docker compose up -d cloudflared`。
   `docker compose logs cloudflared` で `Registered tunnel connection` が出ることを確認する。
3. procd (OpenWrt) 構成: バイナリ / パッケージを差し替えてサービスを再起動する。起動引数は Docker 構成と
   同じく `tunnel --no-autoupdate --metrics 127.0.0.1:20241 run` とし、トークンは環境変数 `TUNNEL_TOKEN` で渡す
   (`--metrics` を省くと metrics サーバ (`/metrics` `/config` `/debug/pprof`) が LAN に開くことがある)。

relay の Go ツールチェーンも `deploy/Dockerfile` の `GO_VERSION` でパッチ版まで固定している。Go の
セキュリティリリースが出たら上げて再ビルドする。
