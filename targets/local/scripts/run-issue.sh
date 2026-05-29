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

ATM_PYTHON="${ATM_PYTHON:-python3.12}"
ATM_CONFIG_PATH="${ATM_CONFIG_PATH:-config/agent.toml}"
ATM_LOG_DIR="${ATM_LOG_DIR:-.atm/logs}"
ATM_RUN_ROOT="${ATM_RUN_ROOT:-.atm/worktrees}"
ATM_BASE_REF="${ATM_BASE_REF:-main}"

if [[ -z "${ATM_TARGET_REPO:-}" ]]; then
  echo "Set ATM_TARGET_REPO in ${TARGET_DIR}/.env.local" >&2
  exit 2
fi

mkdir -p "${REPO_ROOT}/${ATM_LOG_DIR}" "${REPO_ROOT}/${ATM_RUN_ROOT}"

command=(
  "${ATM_PYTHON}"
  -m agentic_tdd_runner.agent
  --repo "${ATM_TARGET_REPO}"
  --base-ref "${ATM_BASE_REF}"
  --run-root "${REPO_ROOT}/${ATM_RUN_ROOT}"
  --log-dir "${REPO_ROOT}/${ATM_LOG_DIR}"
  --config "${REPO_ROOT}/${ATM_CONFIG_PATH}"
)

if [[ -n "${ATM_GITHUB_REPO:-}" && -n "${ATM_ISSUE_NUMBER:-}" ]]; then
  command+=(--github-repo "${ATM_GITHUB_REPO}" --issue-number "${ATM_ISSUE_NUMBER}")
elif [[ -n "${ATM_ISSUE_FILE:-}" ]]; then
  issue_file="${ATM_ISSUE_FILE}"
  if [[ "${issue_file}" != /* ]]; then
    issue_file="${REPO_ROOT}/${issue_file}"
  fi
  if [[ ! -f "${issue_file}" ]]; then
    echo "ATM_ISSUE_FILE does not exist: ${ATM_ISSUE_FILE}" >&2
    exit 2
  fi
  if [[ ! -r "${issue_file}" ]]; then
    echo "ATM_ISSUE_FILE is not readable: ${ATM_ISSUE_FILE}" >&2
    exit 2
  fi
  command+=("${issue_file}")
else
  echo "Set ATM_GITHUB_REPO + ATM_ISSUE_NUMBER, or ATM_ISSUE_FILE" >&2
  exit 2
fi

if [[ -n "${ATM_SOURCE:-}" ]]; then
  command+=(--source "${ATM_SOURCE}")
fi

if [[ -n "${ATM_SYMBOL:-}" ]]; then
  command+=(--symbol "${ATM_SYMBOL}")
fi

cd "${REPO_ROOT}"
echo "Running ATM against ${ATM_TARGET_REPO}"
exec "${command[@]}"
