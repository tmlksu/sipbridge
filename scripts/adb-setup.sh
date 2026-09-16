#!/usr/bin/env bash
# 端末へ APK を入れて権限を付与し、debug 専用の DEBUG_SET_CONFIG で設定を投入する。
# 使い方:
#   scripts/adb-setup.sh <serial> <foss|gms> [--no-install] [key=value ...]
#   key: relayUrl accessClientId accessClientSecret devToken sipUser sipPassword sipDisplay
#        mode(PERSISTENT|PUSH) micGain overlayEnabled autostart speakerOnAnswer restart
# 例:
#   scripts/adb-setup.sh 172.20.121.133:5555 foss sipUser=2104 sipPassword="$(cat ~/relay-sip-pass.txt)" restart=true
# 秘密はコマンド履歴に残らないよう、変数や `$(cat file)` で渡すこと。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SERIAL="${1:?serial}"; FLAVOR="${2:?foss|gms}"; shift 2
PKG="io.github.tmlksu.sipbridge"
INSTALL=1
KV=()
for a in "$@"; do
  case "$a" in
    --no-install) INSTALL=0 ;;
    *=*) KV+=("$a") ;;
    *) echo "unknown arg: $a"; exit 2 ;;
  esac
done

APK="$ROOT/android/app/build/outputs/apk/$FLAVOR/debug/app-$FLAVOR-debug.apk"
if [[ "$INSTALL" == 1 ]]; then
  [[ -f "$APK" ]] || { echo "APK が無い: $APK (scripts/check.sh android を先に)"; exit 1; }
  # v1.2 以前の applicationId。ID が変わったので上書き更新はできない。
  # 消すかどうかは設定が消える判断なので、ここでは知らせるだけにする。
  if adb -s "$SERIAL" shell pm list packages | grep -q "^package:net.peyan.sipbridge$"; then
    echo "注意: 旧 ID の net.peyan.sipbridge が入っている。別アプリとして併存する。"
    echo "      不要なら: adb -s $SERIAL uninstall net.peyan.sipbridge"
  fi
  echo "== install $APK → $SERIAL"
  adb -s "$SERIAL" install -r -g "$APK"
  echo "== permissions"
  adb -s "$SERIAL" shell pm grant "$PKG" android.permission.RECORD_AUDIO || true
  adb -s "$SERIAL" shell pm grant "$PKG" android.permission.POST_NOTIFICATIONS 2>/dev/null || true
  adb -s "$SERIAL" shell appops set "$PKG" SYSTEM_ALERT_WINDOW allow || true
  adb -s "$SERIAL" shell dumpsys deviceidle whitelist "+$PKG" >/dev/null || true
fi

if [[ ${#KV[@]} -gt 0 ]]; then
  echo "== DEBUG_SET_CONFIG (${#KV[@]} keys)"
  ARGS=()
  for kv in "${KV[@]}"; do
    k="${kv%%=*}"; v="${kv#*=}"
    # adb shell はリモート側で再度シェル解釈されるため、値を単一引用符で包む (内部の ' はエスケープ)
    q="'${v//\'/\'\\\'\'}'"
    ARGS+=(--es "$k" "$q")
  done
  # 明示的なコンポーネント指定 (API 26+ は暗黙 broadcast が届かないため)
  adb -s "$SERIAL" shell am broadcast -n "$PKG/.DebugConfigReceiver" -a "$PKG.DEBUG_SET_CONFIG" "${ARGS[@]}" | tail -n 2
fi
echo "== 状態"
adb -s "$SERIAL" shell "dumpsys package $PKG | grep -E 'versionName|lastUpdateTime' | head -2"
