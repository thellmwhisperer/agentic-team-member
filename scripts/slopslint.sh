#!/usr/bin/env bash
# Runs the pinned slopslint release with whatever arguments it is given.
# The binary is downloaded once into .tmp/ (git-ignored), checked against the
# release's SHA256SUMS, and reused. The same script runs locally, in the
# no-mistakes lint step and in CI, so what blocks a merge is the same everywhere.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="v0.3.0"
CACHE_DIR=".tmp/slopslint/${VERSION}"

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) echo "slopslint: no published binary for $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch="arm64" ;;
  x86_64 | amd64) arch="x64" ;;
  *) echo "slopslint: no published binary for $(uname -m)" >&2; exit 1 ;;
esac

asset="slopslint-${os}-${arch}"
binary="${CACHE_DIR}/${asset}"

if [ ! -x "$binary" ]; then
  mkdir -p "$CACHE_DIR"
  base="https://github.com/thellmwhisperer/slopslint/releases/download/${VERSION}"
  curl -fsSL -o "${binary}.part" "${base}/${asset}"
  curl -fsSL -o "${CACHE_DIR}/SHA256SUMS" "${base}/SHA256SUMS"
  expected="$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1}' "${CACHE_DIR}/SHA256SUMS")"
  actual="$(shasum -a 256 "${binary}.part" | awk '{print $1}')"
  if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
    echo "slopslint: checksum mismatch for ${asset} (expected '${expected}', got '${actual}')" >&2
    rm -f "${binary}.part"
    exit 1
  fi
  chmod +x "${binary}.part"
  mv "${binary}.part" "$binary"
fi

exec "$binary" "$@"
