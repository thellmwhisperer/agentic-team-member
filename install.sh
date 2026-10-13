#!/bin/sh
# Installs atm, the latest release or --version vX.Y.Z, into ${ATM_PREFIX:-$HOME/.local/bin}:
#
#   curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/agentic-team-member/main/install.sh | sh
#
# The sha256 is verified before anything is written; atm is replaced only by mv of a whole file, so a killed
# run leaves the previous one answering; an atm in the prefix that is not this product is left alone.
# ATM_GITHUB_API stands in for GitHub's API. POSIX sh: it runs under whatever /bin/sh a `curl | sh` gets.
set -eu

API="${ATM_GITHUB_API:-https://api.github.com}"
PREFIX="${ATM_PREFIX:-$HOME/.local/bin}"
TARGET="$PREFIX/atm"
RELEASE=latest

die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

case "${1:-}" in
  "") ;;
  --version) [ -n "${2:-}" ] || die "--version needs a tag, like v0.2.0"; RELEASE="tags/$2" ;;
  *) die "usage: install.sh [--version vX.Y.Z]" ;;
esac

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "atm installs on macOS and Linux; on Windows, use WSL" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "no atm is published for $(uname -m)" ;;
esac

if [ -e "$TARGET" ] && ! "$TARGET" --version 2>/dev/null | grep -q '^atm '; then
  die "$TARGET is not atm and is left alone: move it, or install elsewhere with ATM_PREFIX"
fi

WORK=$(mktemp -d "${TMPDIR:-/tmp}/atm-install.XXXXXX")
STAGED="$PREFIX/.atm-install.$$"
trap 'rm -rf "$WORK" "$STAGED"' EXIT
trap 'exit 1' INT TERM

curl -fsSL -o "$WORK/release.json" "$API/repos/thellmwhisperer/agentic-team-member/releases/$RELEASE" ||
  die "GitHub has no release at $RELEASE"
field() { grep -o "\"$1\": *\"[^\"]*\"" "$WORK/release.json" | sed 's/.*"\([^"]*\)"$/\1/'; }
TAG=$(field tag_name | head -1)
ARCHIVE="atm-$TAG-$os-$arch.tar.gz"

for f in "$ARCHIVE" checksums.txt; do
  url=$(field browser_download_url | grep "/$f\$" | head -1)
  [ -n "$url" ] || die "release $TAG publishes no $f"
  curl -fsSL -o "$WORK/$f" "$url" || die "cannot download $url"
done

want=$(awk -v f="$ARCHIVE" '$2 == f { print $1 }' "$WORK/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$WORK/$ARCHIVE" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "$WORK/$ARCHIVE" | awk '{ print $1 }')
fi
[ -n "$want" ] && [ "$want" = "$got" ] ||
  die "the sha256 of $ARCHIVE is $got, checksums.txt says '$want': nothing was installed"

tar -xzf "$WORK/$ARCHIVE" -C "$WORK" atm
mkdir -p "$PREFIX"
cp "$WORK/atm" "$STAGED"
chmod 755 "$STAGED"
mv -f "$STAGED" "$TARGET"
"$TARGET" --version
