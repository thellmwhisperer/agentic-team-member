from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any, Mapping


class JobValidationError(ValueError):
    """Raised when an AWS AgentCore ATM job payload is not executable."""


_REPO_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
_SAFE_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$")
_MODES = {"dry_run", "pull_request"}


@dataclass(frozen=True)
class AtmJob:
    """Validated AWS AgentCore ATM job contract.

    The job is intentionally GitHub-centric for the first product slice. Other
    sources can be added later without widening the runner contract.
    """

    repo: str
    issue_number: int
    run_id: str
    base_branch: str = "main"
    branch_prefix: str = "atm-agentcore/"
    mode: str = "pull_request"
    roca_project: str | None = None
    attempt: int = 1
    source: str | None = None
    symbol: str | None = None
    config_ref: str | None = None

    @classmethod
    def from_dict(cls, payload: Mapping[str, Any]) -> "AtmJob":
        repo = str(payload.get("repo", "")).strip()
        if not _REPO_RE.match(repo):
            raise JobValidationError("repo must be a GitHub slug like owner/name")

        issue_number = payload.get("issue_number")
        if not isinstance(issue_number, int) or issue_number <= 0:
            raise JobValidationError("issue_number must be a positive integer")

        attempt = payload.get("attempt", 1)
        if not isinstance(attempt, int) or attempt <= 0:
            raise JobValidationError("attempt must be a positive integer")

        mode = str(payload.get("mode", "pull_request")).strip()
        if mode not in _MODES:
            raise JobValidationError(f"mode must be one of: {', '.join(sorted(_MODES))}")

        run_id = str(payload.get("run_id") or f"{repo.replace('/', '-')}-{issue_number}-{attempt}").strip()
        if not _SAFE_ID_RE.match(run_id):
            raise JobValidationError("run_id must be a safe identifier")

        branch_prefix = str(payload.get("branch_prefix", "atm-agentcore/")).strip()
        if not branch_prefix or ".." in branch_prefix or branch_prefix.startswith("/"):
            raise JobValidationError("branch_prefix must be a safe relative branch prefix")

        base_branch = str(payload.get("base_branch", "main")).strip()
        if not _SAFE_ID_RE.match(base_branch.replace("/", "-")):
            raise JobValidationError("base_branch must be a safe git ref name")

        roca_project = payload.get("roca_project")
        return cls(
            repo=repo,
            issue_number=issue_number,
            run_id=run_id,
            base_branch=base_branch,
            branch_prefix=branch_prefix,
            mode=mode,
            roca_project=str(roca_project).strip() if roca_project else repo,
            attempt=attempt,
            source=_optional_str(payload.get("source")),
            symbol=_optional_str(payload.get("symbol")),
            config_ref=_optional_str(payload.get("config_ref")),
        )

    @property
    def branch_name(self) -> str:
        return f"{self.branch_prefix}{self.run_id}"

    def to_metadata(self) -> dict[str, Any]:
        return {
            "repo": self.repo,
            "issue_number": self.issue_number,
            "run_id": self.run_id,
            "base_branch": self.base_branch,
            "branch_name": self.branch_name,
            "mode": self.mode,
            "attempt": self.attempt,
        }


def _optional_str(value: Any) -> str | None:
    if value is None:
        return None
    text = str(value).strip()
    return text or None
