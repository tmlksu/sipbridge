# deploy/test — Asterisk 結合テスト環境 (T2)

relay の SIP バックエンド (`relay/internal/sipbackend/`) を
本物の Asterisk と対向させるための最小構成。

## 起動

```sh
cd deploy/test
docker compose -f compose.yml up -d
docker logs sipbridge-test-asterisk | head -20   # 起動確認
```

Asterisk は `network_mode: host` で動く (ホストの UDP 5060 と
RTP 10000-19999 を使う)。テスト側は `127.0.0.1` で接続する
(RTP は T2 が 40000 番台、T7 (`internal/session`) が 41000 番台)。

## 結合テスト実行

```sh
cd relay
export PATH=$HOME/go-toolchain/go/bin:$PATH
go test -tags integration ./internal/sipbackend/ -v   # T2: SIP バックエンド単体
go test -tags integration ./internal/session/ -v      # T7: 同一 relay 内の 101 → 102
```

環境変数 (既定値は `asterisk/*.conf` のダミー値と対応):

| 変数 | 既定 | 説明 |
|---|---|---|
| `TEST_ASTERISK_HOST` | `127.0.0.1` | Asterisk のホスト |
| `TEST_ASTERISK_PORT` | `5060` | Asterisk の SIP ポート |
| `TEST_SIP_101_PASS` | `test101pass` | 内線 101 (relay) のパスワード |
| `TEST_SIP_102_PASS` | `test102pass` | 内線 102 (テスト発呼側) のパスワード |

パスワードは結合テスト専用のダミーであり、本番とは無関係。

## 後片付け

```sh
cd deploy/test
docker compose -f compose.yml down
```

## 内線表

| 内線 | 用途 |
|---|---|
| 101 | relay (`sipbackend`, `Backend` が REGISTER) |
| 102 | テスト発呼側 (T2: テスト内の sipgo UAC が REGISTER→INVITE) / relay の 2 つ目のアカウント (T7: マルチアカウント結合テスト) |
| `*43` | Echo アプリ (発信テスト: 送った RTP がそのまま戻る) |
