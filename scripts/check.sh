#!/usr/bin/env bash
# sipbridge 一括チェック: 公開安全性 + relay (gofmt/vet/test/build)
# + android (foss/gms ビルド + JVM テスト)。
# 使い方: scripts/check.sh [relay|android|public|all] [--integration]
#   --integration: deploy/test の Docker Asterisk を起動して relay の結合テストも回す。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="${1:-all}"
INTEGRATION=0
for a in "$@"; do [[ "$a" == "--integration" ]] && INTEGRATION=1; done

export PATH="$HOME/go-toolchain/go/bin:$PATH"
export JAVA_HOME="$HOME/android-build/jdk"
export PATH="$JAVA_HOME/bin:$PATH"
export ANDROID_HOME="$HOME/android-build/sdk" ANDROID_SDK_ROOT="$HOME/android-build/sdk"

check_public() {
  echo "== public: インスタンス固有情報の混入チェック"
  "$ROOT/scripts/check-public-safe.sh"
}

check_relay() {
  echo "== relay: gofmt / vet / test / build"
  cd "$ROOT/relay"
  local unformatted
  unformatted="$(gofmt -l .)"
  if [[ -n "$unformatted" ]]; then
    echo "gofmt 未適用:"; echo "$unformatted"; return 1
  fi
  go vet ./...
  go test ./... 2>&1 | tail -n 30
  CGO_ENABLED=0 go build -o /tmp/sipbridge-relay ./cmd/relay
  CGO_ENABLED=0 go build -o /tmp/sipbridge-wsprobe ./cmd/wsprobe
  echo "relay OK (/tmp/sipbridge-relay, /tmp/sipbridge-wsprobe)"
  if [[ "$INTEGRATION" == 1 ]]; then
    echo "== relay: integration (Docker Asterisk)"
    (cd "$ROOT/deploy/test" && docker compose -f compose.yml up -d)
    sleep 5
    set +e
    go test -tags integration ./... 2>&1 | tail -n 40
    local rc=${PIPESTATUS[0]}
    set -e
    (cd "$ROOT/deploy/test" && docker compose -f compose.yml down)
    [[ "$rc" == 0 ]] || return "$rc"
  fi
}

check_android() {
  echo "== android: assembleFossDebug / assembleGmsDebug / testFossDebugUnitTest"
  cd "$ROOT/android"
  if grep -r "DatagramSocket" app/src >/dev/null 2>&1; then
    echo "NG: DatagramSocket が残っている"; grep -rn "DatagramSocket" app/src; return 1
  fi
  "$HOME/android-build/gradle-8.9/bin/gradle" :app:assembleFossDebug :app:assembleGmsDebug \
    :app:testFossDebugUnitTest --no-daemon -q 2>&1 | grep -v "^$" | tail -n 40
  ls -la app/build/outputs/apk/foss/debug/app-foss-debug.apk app/build/outputs/apk/gms/debug/app-gms-debug.apk
  echo "android OK"
}

case "$TARGET" in
  relay) check_relay ;;
  android) check_android ;;
  public) check_public ;;
  all|--integration) check_public; check_relay; check_android ;;
  *) echo "usage: $0 [relay|android|public|all] [--integration]"; exit 2 ;;
esac
