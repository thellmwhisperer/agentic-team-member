from __future__ import annotations

import importlib
import json
import os
import sys
import tempfile


def main() -> int:
    if sys.version_info < (3, 12):
        raise RuntimeError("ATM AWS AgentCore smoke requires Python 3.12 or newer")

    with tempfile.TemporaryDirectory(prefix="atm-agentcore-smoke-") as home:
        os.environ["HOME"] = home
        os.environ.setdefault("ATM_ENABLE_BEDROCK_PROXY", "false")
        os.environ.setdefault("ATM_ENABLE_GIT_HTTPS_AUTH", "false")
        os.environ.setdefault("ATM_PYTHON", "python")
        os.environ.setdefault("ATM_HARNESS_MODULE", "agentic_tdd_runner.agent")

        importlib.import_module("agentic_tdd_runner.agent")
        importlib.import_module("runtime.atm_agent")
        importlib.import_module("atm_cloud.runner")

        from runtime.atm_agent import handle

        result = handle(
            {
                "repo": "owner/repo",
                "issue_number": 1,
                "mode": "dry_run",
                "run_id": "smoke",
            }
        )

    print(json.dumps(result, sort_keys=True))
    if result.get("status") != "succeeded":
        raise RuntimeError(f"smoke failed: {result}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
