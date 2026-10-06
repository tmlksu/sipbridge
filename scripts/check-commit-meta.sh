#!/usr/bin/env bash
# コミットのメタデータ (作者・コミッタ・メッセージの trailer) に個人のメールアドレスや
# インスタンス固有の情報が入っていないか検査する。
# check-public-safe.sh はファイルしか見ないので、こちらで commit 側を見る。
#
# 使い方:
#   scripts/check-commit-meta.sh --msg-file FILE   # commit-msg フック: 作成中のコミット
#   scripts/check-commit-meta.sh RANGE             # CI 等: 例 origin/main..HEAD
#
# 許可するメールは noreply 系だけ (GitHub の <ID>+<user>@users.noreply.github.com、
# noreply@github.com、noreply@anthropic.com、bot の noreply など)。
# Devin の co-author モードは依頼者の Devin アカウントのメールを trailer に入れるので、
# ここで止める (2026-10 に実名・個人メールが公開履歴に残った)。
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
report() { printf '[NG] %s\n' "$1"; fail=1; }

DENY="ops/deny-patterns.txt"
EMAIL_RE='[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'

# $1 = 表示用ラベル, $2 = 作者・コミッタ ("名前 <mail>" を改行区切り), $3 = メッセージ
check_one() {
  local label="$1" idents="$2" msg="$3" mails
  # 作者・コミッタと、メッセージ中の trailer 行 (Key: ... <mail>) のメール
  mails=$( { printf '%s\n' "$idents"; printf '%s\n' "$msg" | grep -E '^[A-Za-z-]+: .*<[^>]+>'; } \
           | grep -oE "$EMAIL_RE" | sort -u)
  local m
  for m in $mails; do
    case "$m" in
      *noreply*) ;;
      *) report "$label: noreply でないメール <$m>" ;;
    esac
  done
  if [ -f "$DENY" ]; then
    local hits
    hits=$(printf '%s\n%s\n' "$idents" "$msg" | grep -nIFif <(grep -vE '^\s*(#|$)' "$DENY"))
    [ -n "$hits" ] && report "$label: ops/deny-patterns.txt のパターンを含む: $hits"
  fi
}

if [ "${1:-}" = "--msg-file" ]; then
  [ -n "${2:-}" ] || { echo "usage: $0 --msg-file FILE | RANGE" >&2; exit 2; }
  idents=$(printf '%s\n%s\n' "$(git var GIT_AUTHOR_IDENT)" "$(git var GIT_COMMITTER_IDENT)")
  check_one "作成中のコミット" "$idents" "$(grep -v '^#' "$2")"
else
  range="${1:?usage: $0 --msg-file FILE | RANGE}"
  while read -r c; do
    check_one "$(git log -1 --format='%h %s' "$c")" \
      "$(git log -1 --format='%an <%ae>%n%cn <%ce>' "$c")" \
      "$(git log -1 --format='%B' "$c")"
  done < <(git rev-list "$range")
fi

if [ "$fail" -ne 0 ]; then
  printf '\nコミットの作者・trailer は noreply アドレスにすること (AGENTS.md)。\n' >&2
  printf 'git config user.email <ID>+<user>@users.noreply.github.com\n' >&2
fi
exit "$fail"
