#!/usr/bin/env bash
# Telecom 統合 (OS 標準の通話画面) の実機検証。debug ビルド専用の probe/ を叩く。
# 使い方: scripts/telecom-probe.sh <serial> [--no-install]
#
# 確認すること:
#   1. CAPABILITY_CALL_PROVIDER の PhoneAccount を登録・有効化できるか
#   2. 標準ダイヤラー経由の発信で内線番号がどう正規化されるか
#   3. addNewIncomingCall で標準の着信画面が出るか / 何 ms で ConnectionService に来るか
#   4. self-managed (ティア B) でも同じ経路が動くか
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SERIAL="${1:?serial}"; shift || true
INSTALL=1
for a in "$@"; do [[ "$a" == "--no-install" ]] && INSTALL=0; done

# probe ビルドは applicationId が `.probe` の別アプリ。本番の sipbridge と同居できる。
PKG="io.github.tmlksu.sipbridge.probe"
CLS="io.github.tmlksu.sipbridge.probe"
SVC="$PKG/$CLS.ProbeConnectionService"
RCV="$PKG/$CLS.TelecomProbeReceiver"
ACTION="io.github.tmlksu.sipbridge.PROBE"
APK="$ROOT/android/app/build/outputs/apk/foss/probe/app-foss-probe.apk"
OUT="${TMPDIR:-/tmp}/telecom-probe-$(echo "$SERIAL" | tr -c 'A-Za-z0-9' _)"
mkdir -p "$OUT"

a() { adb -s "$SERIAL" "$@"; }
wait_s() { a shell sleep "$1" >/dev/null 2>&1; }
probe() {  # probe <cmd> [k=v ...]
  local cmd="$1"; shift
  local args=(--es cmd "$cmd")
  for kv in "$@"; do args+=(--es "${kv%%=*}" "${kv#*=}"); done
  a shell am broadcast -n "$RCV" -a "$ACTION" "${args[@]}" >/dev/null 2>&1
  wait_s 1
}
banner() { echo; echo "######## $* ########"; }

if [[ "$INSTALL" == 1 ]]; then
  [[ -f "$APK" ]] || { echo "APK が無い: $APK (gradle :app:assembleFossProbe を先に)"; exit 1; }
  echo "== install"
  a install -r -g "$APK" | tail -1
  a shell pm grant "$PKG" android.permission.CALL_PHONE 2>/dev/null
  a shell pm grant "$PKG" android.permission.READ_PHONE_STATE 2>/dev/null
fi

echo "== 端末: $(a shell getprop ro.product.manufacturer | tr -d '\r') $(a shell getprop ro.product.model | tr -d '\r') / API $(a shell getprop ro.build.version.sdk | tr -d '\r')"
echo "== default dialer: $(a shell telecom get-default-dialer 2>&1 | tr -d '\r')"

a logcat -c 2>/dev/null
a logcat -v time -s TelecomProbe:V > "$OUT/logcat.txt" 2>&1 &
LOGPID=$!
trap 'kill $LOGPID 2>/dev/null' EXIT

banner "1. managed (CALL_PROVIDER) 登録"
probe register mode=managed
banner "1b. adb で有効化 (UI を触らずに有効化できるか)"
a shell telecom set-phone-account-enabled "$SVC" "probe-managed" 0 2>&1 | tr -d '\r'
probe status
banner "3. 着信: addNewIncomingCall (managed)"
probe incoming mode=managed from=2104
wait_s 3
a exec-out screencap -p > "$OUT/incoming-managed.png" 2>/dev/null
echo "スクリーンショット: $OUT/incoming-managed.png"
a shell dumpsys telecom 2>/dev/null | sed -n '1,25p'
probe hangup
wait_s 1

banner "2. 発信: アプリから placeCall (managed)"
probe outgoing mode=managed to=2104
wait_s 2
probe hangup
wait_s 1

banner "2b. ★発信: 標準の電話経路 (ACTION_CALL) — 番号の正規化を見る"
probe expect to=2104
a shell telecom set-user-selected-outgoing-phone-account "$SVC" "probe-managed" 0 2>&1 | tr -d '\r'
a shell am start -a android.intent.action.CALL -d "tel:2104" >/dev/null 2>&1
wait_s 3
a exec-out screencap -p > "$OUT/outgoing-managed.png" 2>/dev/null
probe hangup
wait_s 1

banner "4. self-managed (ティア B)"
probe register mode=self
probe status
probe incoming mode=self from=2104
wait_s 3
a exec-out screencap -p > "$OUT/incoming-self.png" 2>/dev/null
probe hangup
wait_s 1

banner "後始末"
probe unregister
wait_s 1
kill $LOGPID 2>/dev/null
# 終了を待たないとリダイレクト先がまだフラッシュされておらず、結果が空になる
wait $LOGPID 2>/dev/null || true
trap - EXIT

echo
echo "================ 結果 (logcat) ================"
grep -a -E "CMD|RESULT|STATE|EVENT" "$OUT/logcat.txt" | sed 's/^[^ ]* [^ ]* //'
echo
echo "全ログ: $OUT/logcat.txt / 画像: $OUT/*.png"
