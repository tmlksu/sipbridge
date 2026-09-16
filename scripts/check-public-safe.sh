#!/usr/bin/env bash
# 公開リポジトリにインスタンス固有の情報が混ざっていないか検査する。
#
# 使い方:
#   scripts/check-public-safe.sh          # 検査して、見つかれば exit 1
#
# 検査対象は git が追跡しているファイルと、追跡外だが .gitignore されていない
# ファイル。`ops/` は .gitignore 済みなので自動的に対象外になる。
#
# 重要: このスクリプト自身は公開されるので、**実値を直接書かない**。
# インスタンス固有の禁止パターンは ops/deny-patterns.txt (非公開) から読む
# (大文字小文字は区別しない)。
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
report() { printf '\n[NG] %s\n' "$1"; shift; printf '%s\n' "$@"; fail=1; }

# ops/ が追跡されたままだと公開リポジトリに載ってしまう
tracked_ops=$(git ls-files --cached -- 'ops/*')
if [ -n "$tracked_ops" ]; then
  report "ops/ が git に追跡されている (git rm --cached -r ops/ で外す)" "$tracked_ops"
fi

# 検査対象ファイル (ops/ と バイナリは除く)
mapfile -t FILES < <(git ls-files --cached --others --exclude-standard -- . ':!ops/' \
  | grep -vE '\.(png|jpg|jpeg|gif|pdf|apk|aab|jar|so|aar|keystore|jks)$')
[ ${#FILES[@]} -eq 0 ] && { echo "検査対象なし"; exit 0; }

# --- 1. インスタンス固有の禁止パターン (ops/deny-patterns.txt) ---
DENY="ops/deny-patterns.txt"
if [ -f "$DENY" ]; then
  hits=$(grep -nIFif <(grep -vE '^\s*(#|$)' "$DENY") "${FILES[@]}" 2>/dev/null)
  [ -n "$hits" ] && report "ops/deny-patterns.txt のパターンが公開対象に残っている" "$hits"
else
  echo "[--] $DENY が無いのでインスタンス固有の検査はスキップ (public クローンでは正常)"
fi

# --- 2. 秘密鍵・認証情報らしきもの ---
hits=$(grep -nIE -- '-----BEGIN [A-Z ]*PRIVATE KEY-----' "${FILES[@]}" 2>/dev/null)
[ -n "$hits" ] && report "秘密鍵が含まれている" "$hits"

hits=$(git ls-files --cached | grep -E '(^|/)(google-services\.json|service-account.*\.json|\.env)$')
[ -n "$hits" ] && report "秘密情報を含むファイルが追跡されている" "$hits"

# --- 3. Cloudflare Access の team ドメイン (example. 以外) ---
hits=$(grep -nIE '[a-z0-9-]+\.cloudflareaccess\.com' "${FILES[@]}" 2>/dev/null \
       | grep -vE 'example\.cloudflareaccess\.com|<[a-z-]+>\.cloudflareaccess\.com')
[ -n "$hits" ] && report "実在の Cloudflare team ドメインらしき記述" "$hits"

# --- 4. グローバル IPv4 リテラル ---
# 第 1 オクテットが 32 以上のものだけを見る。バージョン番号 (6.5.7.0 など) や
# 1.1.1.1 (Cloudflare WARP の通称) を誤検出しないための割り切り。
# 実在の値そのものは ops/deny-patterns.txt (検査 1) が確実に捕まえる。
# 許可: ループバック / RFC1918 / リンクローカル / マルチキャスト以上 /
#       ドキュメント用 (RFC 5737)。8.8.8.8 等は第 1 オクテット < 32 なので元々対象外。
hits=$(grep -nIoE '[0-9]{1,3}(\.[0-9]{1,3}){3}' "${FILES[@]}" 2>/dev/null \
  | awk -F: '$NF ~ /^(3[2-9]|[4-9][0-9]|1[0-9][0-9]|2[0-5][0-9])\./' \
  | awk -F: '$NF !~ /^(127|169\.254|172\.(1[6-9]|2[0-9]|3[01])|192\.168|192\.0\.2|198\.51\.100|203\.0\.113|22[4-9]|2[3-5][0-9])\./')
[ -n "$hits" ] && report "ドキュメント用でないグローバル IPv4 が書かれている (RFC 5737 の 203.0.113.x 等を使う)" "$hits"

if [ "$fail" -eq 0 ]; then
  echo "[OK] 公開対象にインスタンス固有の情報は見つからなかった (${#FILES[@]} ファイル)"
else
  printf '\n公開前に上記を修正すること。実値は ops/ (非公開) に置き、\n'
  printf '公開側はプレースホルダにする (対応表は ops/ENVIRONMENT.md)。\n'
fi
exit "$fail"
