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

ATM_LLAMA_SERVER_BIN="${ATM_LLAMA_SERVER_BIN:-llama-server}"
ATM_LLAMA_MODEL="${ATM_LLAMA_MODEL:-${ATM_LLAMA_MODEL_PATH:-}}"
ATM_LLAMA_MODEL_ARG="${ATM_LLAMA_MODEL_ARG:--hf}"
ATM_LLAMA_ALIAS="${ATM_LLAMA_ALIAS:-}"
ATM_LLAMA_HOST="${ATM_LLAMA_HOST:-127.0.0.1}"
ATM_LLAMA_PORT="${ATM_LLAMA_PORT:-11435}"
ATM_LLAMA_CTX_SIZE="${ATM_LLAMA_CTX_SIZE:-32768}"
ATM_LLAMA_GPU_LAYERS="${ATM_LLAMA_GPU_LAYERS:-99}"
ATM_LLAMA_JINJA="${ATM_LLAMA_JINJA:-true}"
ATM_LLAMA_REASONING="${ATM_LLAMA_REASONING-on}"
ATM_LLAMA_REASONING_BUDGET="${ATM_LLAMA_REASONING_BUDGET--1}"
ATM_LLAMA_PARALLEL="${ATM_LLAMA_PARALLEL-1}"
ATM_LLAMA_SPEC_TYPE="${ATM_LLAMA_SPEC_TYPE-draft-mtp}"
ATM_LLAMA_SPEC_DRAFT_N_MAX="${ATM_LLAMA_SPEC_DRAFT_N_MAX-2}"
ATM_LLAMA_NO_WEBUI="${ATM_LLAMA_NO_WEBUI:-false}"
ATM_LLAMA_EXTRA_ARGS="${ATM_LLAMA_EXTRA_ARGS:-}"

is_enabled() {
  case "$1" in
    1 | true | TRUE | yes | YES | on | ON) return 0 ;;
    *) return 1 ;;
  esac
}

if [[ -z "${ATM_LLAMA_MODEL}" ]]; then
  echo "Set ATM_LLAMA_MODEL in ${TARGET_DIR}/.env.local" >&2
  exit 2
fi

cd "${REPO_ROOT}"

command=(
  "${ATM_LLAMA_SERVER_BIN}"
  "${ATM_LLAMA_MODEL_ARG}" "${ATM_LLAMA_MODEL}"
)

if [[ -n "${ATM_LLAMA_ALIAS}" ]]; then
  command+=(-a "${ATM_LLAMA_ALIAS}")
fi

command+=(
  --host "${ATM_LLAMA_HOST}"
  --port "${ATM_LLAMA_PORT}"
  -c "${ATM_LLAMA_CTX_SIZE}"
  -ngl "${ATM_LLAMA_GPU_LAYERS}"
)

if is_enabled "${ATM_LLAMA_JINJA}"; then
  command+=(--jinja)
fi

if [[ -n "${ATM_LLAMA_REASONING}" ]]; then
  command+=(--reasoning "${ATM_LLAMA_REASONING}")
fi

if [[ -n "${ATM_LLAMA_REASONING_BUDGET}" ]]; then
  command+=(--reasoning-budget "${ATM_LLAMA_REASONING_BUDGET}")
fi

if [[ -n "${ATM_LLAMA_PARALLEL}" ]]; then
  command+=(--parallel "${ATM_LLAMA_PARALLEL}")
fi

if [[ -n "${ATM_LLAMA_SPEC_TYPE}" ]]; then
  command+=(--spec-type "${ATM_LLAMA_SPEC_TYPE}")
fi

if [[ -n "${ATM_LLAMA_SPEC_DRAFT_N_MAX}" ]]; then
  command+=(--spec-draft-n-max "${ATM_LLAMA_SPEC_DRAFT_N_MAX}")
fi

if is_enabled "${ATM_LLAMA_NO_WEBUI}"; then
  command+=(--no-webui)
fi

if [[ -n "${ATM_LLAMA_EXTRA_ARGS}" ]]; then
  # shellcheck disable=SC2206
  extra_args=(${ATM_LLAMA_EXTRA_ARGS})
  command+=("${extra_args[@]}")
fi

echo "Starting llama-server on ${ATM_LLAMA_HOST}:${ATM_LLAMA_PORT}"
exec "${command[@]}"
