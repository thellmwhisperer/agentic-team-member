#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${TARGET_DIR}/../.." && pwd)"

if [[ -f "${TARGET_DIR}/.env" ]]; then
  set -a
  source "${TARGET_DIR}/.env"
  set +a
fi

if [[ -f "${TARGET_DIR}/.env.local" ]]; then
  set -a
  source "${TARGET_DIR}/.env.local"
  set +a
fi

ATM_LOG_DIR="${ATM_LOG_DIR:-.atm/logs}"
log_dir="${REPO_ROOT}/${ATM_LOG_DIR}"

mtime_entry() {
  local path="$1"
  local mtime

  if mtime="$(stat -f "%m" "${path}" 2>/dev/null)" && [[ "${mtime}" =~ ^[0-9]+$ ]]; then
    printf '%s %s\n' "${mtime}" "${path}"
    return 0
  fi

  if mtime="$(stat -c "%Y" "${path}" 2>/dev/null)"; then
    printf '%s %s\n' "${mtime}" "${path}"
  fi
}

latest="$(
  find "${log_dir}" -maxdepth 1 -name 'agent-*.jsonl' -type f -print 2>/dev/null \
    | while IFS= read -r log_file; do
        mtime_entry "${log_file}"
      done \
    | sort -n \
    | tail -n 1 \
    | cut -d ' ' -f 2- || true
)"

if [[ -z "${latest}" ]]; then
  echo "No agent logs found under ${log_dir}" >&2
  exit 2
fi

echo "Tailing ${latest}"
exec tail -f "${latest}"
