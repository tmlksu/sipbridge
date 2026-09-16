# リポジトリ分割: public を base にする

## 方針

**public が唯一の source of truth。private (`ops/`) は public を「使う」側。**

```
public   sipbridge          コード + 汎用ドキュメント。開発はここで行う
private  sipbridge-ops      インスタンス固有の値・秘密・運用手順 (= このリポジトリの ops/)
```

向きが重要。「private を master にして public へ export する」構成にすると、
public 側の issue / PR が private に還流せず、export のたびに上書きが起きて
二重管理になる。逆向きにすれば、public は普通の OSS リポジトリとして完結する。

これが成立する条件は **public のコードがインスタンス固有値を一切持たないこと**。
本プロジェクトは元々ほぼそうなっていた:

- relay — 設定は全て環境変数 (`SIP_HOST`, `CF_TEAM_DOMAIN`, `DEV_TOKEN`, …)
- アプリ — 設定は全てアプリ内 (relay URL, Access トークン, SIP アカウント)

固有値が漏れていたのは**ドキュメントの例示と運用スクリプトだけ**だった。
そこを直せば分離は自然に維持される。

## ディレクトリ構成

作業ディレクトリは 1 つのままでよい。`ops/` を独立した git リポジトリにする。

```
~/Projects/sipbridge/          ← public リポジトリ
  relay/  android/  deploy/  docs/  scripts/  fdroid/
  .gitignore                   ← ops/ を無視
  .githooks/pre-commit         ← コミット前に公開安全性を検査 (任意で有効化)
  ops/                         ← private リポジトリ (別の .git)
    .gitignore                 ← ops にも秘密情報を入れない
    ENVIRONMENT.md             ← 置換表・秘密の在り処
    deny-patterns.txt          ← 漏洩ガードの禁止パターン (実値)
    SETUP-INSTANCE.md          ← 実値入りの配備・運用手順
    deploy.sh  e2e.sh  add-test-peer.sh
    split-repos.sh             ← この分割を実行 (履歴退避・GitHub 作成・push)
```

## 何をどちらに置くか

| | public | ops (private) |
|---|---|---|
| relay / アプリのコード | ○ | |
| 汎用の手順 (`docs/SETUP.md`, `docs/PROTOCOL.md`) | ○ | |
| ビルド・テストのハーネス (`scripts/check.sh`, `adb-setup.sh`, `gen-icon.py`) | ○ | |
| F-Droid メタデータ (`fdroid/`, `android/fastlane/`) | ○ | |
| 実ホスト名・実 IP・team ドメイン・内線番号 | | ○ |
| 配備スクリプト (ssh 先が固定) | | ○ |
| 秘密そのもの (SIP パスワード, Access トークン, FCM SA JSON) | | **どちらにも置かない** (サーバ上のみ) |

判断に迷ったら「他人のインスタンスでもそのまま意味を持つか」で決める。
持つなら public、持たないなら ops。

## プレースホルダの決まり

public 側では実値の代わりに次を使う (対応表は `ops/ENVIRONMENT.md`)。

| 用途 | public 側の表記 |
|---|---|
| relay の公開ホスト名 | `relay.example.com` |
| Cloudflare Zero Trust の team ドメイン | `example.cloudflareaccess.com` |
| グローバル IP | `203.0.113.10` (RFC 5737 のドキュメント用アドレス) |
| LAN 内のホスト | `192.168.1.x` (RFC 1918) |

## 漏洩ガード

`scripts/check-public-safe.sh` が、公開対象に固有情報が混ざっていないか検査する
(`scripts/check.sh public`、`scripts/check.sh all` からも走る)。検査内容:

1. `ops/deny-patterns.txt` の固定文字列 (実ホスト名・実 IP・team ドメイン等)
2. 秘密鍵ブロック、`.env` / `google-services.json` / `service-account*.json` の追跡
3. `example.` 以外の `*.cloudflareaccess.com`
4. ドキュメント用でないグローバル IPv4 リテラル
5. `ops/` が git に追跡されていないか

**このスクリプト自身は公開される**ので、実値は書かず `ops/deny-patterns.txt`
(非公開) から読む。public を clone しただけの人の環境では `ops/` が無いので
検査 1 はスキップされ、残りだけが走る。

## 移行手順

分離作業 (ファイルの移動・プレースホルダ化・ガード追加) は完了済み。
残るのはリポジトリ操作だけで、`ops/split-repos.sh` (非公開) が以下の手順をそのまま
実行する。GitHub のアカウント名やリポジトリ名という固有値を扱うため、スクリプトは
public ではなく `ops/` に置いてある。

```sh
cd ~/Projects/sipbridge

# 何をするか確認 (ドライラン)
ops/split-repos.sh

# 実行 (ガードが通ってから、下記を順に行う)
ops/split-repos.sh --apply
```

`--apply` が行うこと (済んだ手順はスキップするので何度でも実行できる):

1. `scripts/check-public-safe.sh` で実値の混入が無いことを確認
2. `ops/` を独立した private リポジトリにし、`<owner>/sipbridge-ops` を origin に
   して `main` を push
3. 今の `.git` を `../sipbridge-history.git` に退避して public を新規 init
   (履歴は引き継がない) → 初回コミット + `v1.2` タグ
4. GitHub: 既存の private な `sipbridge` を `sipbridge-history` (非公開) に rename して
   旧履歴を残し、新しく public な `sipbridge` を作って `main` とタグを push

`ops/` は `.gitignore` 済みなので、手順 3 の `git add -A` には含まれない。

### GitHub 側のリポジトリ名

| リポジトリ | 公開設定 | 中身 |
|---|---|---|
| `<owner>/sipbridge` | public | コード + 汎用ドキュメント (新しい履歴) |
| `<owner>/sipbridge-ops` | private | `ops/` (実値・配備・運用手順) |
| `<owner>/sipbridge-history` | private | 分割前の開発履歴 (旧 `.git`、参照用に退避) |

旧 `sipbridge` の履歴には固有値が残っているため、**そのまま public に切り替えず**、
退避名へ rename してから新規 public リポジトリを作る。

### F-Droid へ提出

`fdroid/net.peyan.sipbridge.yml` を fdroiddata へ MR する。`SourceCode` と
`commit: v1.2` は上記 public リポジトリを指す。

### コミット前の検査

`git config core.hooksPath .githooks` を一度実行すると、`.githooks/pre-commit` が
コミットのたびに `scripts/check-public-safe.sh` を走らせる (`--apply` は public の
init 時に自動で設定する)。

## 以後の運用

- **コードと汎用ドキュメントの変更は public リポジトリで行う。** ops は生成元ではない。
- インスタンス固有の手順・値を書きたくなったら `ops/` に書く。public には
  プレースホルダで書く。新しい実値を導入したら `ops/deny-patterns.txt` に足す。
- コミット前に `scripts/check.sh public` を通す。`.githooks/pre-commit` を置いてあり、
  `git config core.hooksPath .githooks` で自動化できる (public の init 時に設定される)。

## 未決 (要判断)

- `CLAUDE.md` / `AGENTS.md` — ローカルのツールチェーンパス (`~/go-toolchain`,
  `~/android-build`) と、エージェント割当・参照元パスが書いてある。秘密ではないが
  他人には無意味。汎用のビルド手順・コード規約だけを public に残し、環境固有の記述を
  `ops/` 側に移して `@ops/ENVIRONMENT.md` として import する形を推奨する。
  現状は分割せず public 側に置いたままにしてある。
- `TASKS.md` — エージェント割当を含む内部のタスク分割。public に残すか ops へ移すか。
  現状は public 側に置いたまま (固有情報は除去済み)。
