"""Red-green verification helpers for the agent runner."""

import os
import re
import shlex
import shutil
import subprocess
import tempfile
from collections.abc import Callable

from agentic_tdd_runner.paths import resolve_repo_path


def is_invalid_red_phase_failure(output: str) -> bool:
    lowered = output.lower()
    if "__set" not in lowered and "fortests" not in lowered:
        return False
    invalid_markers = (
        "not a function",
        "is undefined",
        "is not defined",
        "cannot import",
        "does not provide an export",
        "has no exported member",
    )
    return any(marker in lowered for marker in invalid_markers)


def verification_infra_error(output: str) -> str | None:
    lowered = output.lower()
    if re.search(r"no module named\s+['\"]?pytest['\"]?(?=$|[^a-z0-9_])", lowered):
        return "pytest is unavailable in the verification environment"
    missing_binary = re.search(
        r"no such file or directory:\s*['\"]?(python3|python|pytest|bun|node)['\"]?(?=$|[^a-z0-9_./-])",
        lowered,
    )
    if missing_binary:
        binary = missing_binary.group(1)
        if binary in {"python3", "python", "pytest"}:
            return "pytest is unavailable in the verification environment"
        if binary == "bun":
            return "bun is unavailable in the verification environment"
        if binary == "node":
            return "node is unavailable in the verification environment"

    markers = [
        (
            "pytest: command not found",
            "pytest is unavailable in the verification environment",
        ),
        (
            "/bin/sh: pytest: command not found",
            "pytest is unavailable in the verification environment",
        ),
        (
            "bun: command not found",
            "bun is unavailable in the verification environment",
        ),
        (
            "node: command not found",
            "node is unavailable in the verification environment",
        ),
    ]
    for needle, message in markers:
        if needle in lowered:
            return message
    return None


def mechanical_edit_paths(mechanical_edits: list[dict] | None, workdir: str) -> list[str]:
    """Return unique repo-relative paths touched by mechanical edits."""
    paths: list[str] = []
    seen: set[str] = set()
    for edit in mechanical_edits or []:
        path = edit.get("path")
        if not path:
            continue
        try:
            resolved = resolve_repo_path(path, workdir)
        except ValueError:
            continue
        rel = os.path.relpath(resolved, workdir)
        if rel not in seen:
            seen.add(rel)
            paths.append(rel)
    return paths


def verify_red_green(
    test_file: str,
    *,
    workdir: str,
    config: dict,
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
    apply_mechanical_edits: Callable[[list[dict], str], int],
    mechanical_edits: list[dict] | None = None,
) -> tuple[bool, str]:
    """Verify red-green: test fails without fix, passes with fix."""
    from agentic_tdd_runner.languages import get_language

    lang = get_language(test_file)
    if lang and lang.runner == "pytest":
        run_cmd = "python3 -m pytest"
    else:
        run_cmd = config["runner"]["command"]
    test_timeout = config["timeouts"]["test_run"]

    emit("\n=== RED-GREEN VERIFICATION ===")

    test_full = os.path.join(workdir, test_file)
    test_backup = None
    if os.path.isfile(test_full):
        tmp = tempfile.NamedTemporaryFile(
            suffix=os.path.basename(test_file),
            delete=False,
        )
        test_backup = tmp.name
        tmp.close()
        shutil.copy2(test_full, test_backup)

    subprocess.run(["git", "stash", "--include-untracked"], cwd=workdir, capture_output=True)
    mechanical_paths = mechanical_edit_paths(mechanical_edits, workdir)

    run_argv = shlex.split(run_cmd)

    red_result = None
    red_passed = False
    red_output = ""
    red_output_full = ""
    red_exec_error = ""
    green_result = None
    green_passed = False
    green_output = ""
    green_output_full = ""
    green_exec_error = ""

    try:
        if test_backup:
            os.makedirs(os.path.dirname(test_full), exist_ok=True)
            shutil.copy2(test_backup, test_full)

        if mechanical_edits:
            applied = apply_mechanical_edits(mechanical_edits, workdir)
            emit(f"  [RED] Re-applied {applied} mechanical edit(s)")

        emit("  [RED] Running test WITHOUT fix...")
        try:
            red_result = subprocess.run(
                [*run_argv, test_file],
                cwd=workdir, capture_output=True, text=True, timeout=test_timeout,
            )
            red_passed = red_result.returncode == 0
            red_output_full = red_result.stdout + red_result.stderr
            red_output = red_output_full[:500]
            emit(f"  [RED] exit={red_result.returncode} {'PASS (BAD!)' if red_passed else 'FAIL (good)'}")
            for line in red_output.split("\n")[:10]:
                emit(f"    {line}")
        except subprocess.TimeoutExpired:
            red_passed = False
            red_output = f"TIMEOUT: test command exceeded {test_timeout}s without fix."
            emit(f"  [RED] TIMEOUT after {test_timeout}s")
            emit(f"    {red_output}")
        except (FileNotFoundError, OSError) as exc:
            red_exec_error = str(exc)
            red_output_full = red_exec_error
            red_output = red_output_full[:500]
            emit("  [RED] INFRA ERROR")
            emit(f"    {red_output}")
    finally:
        if test_backup:
            if os.path.isfile(test_full):
                os.remove(test_full)
            if os.path.isfile(test_backup):
                os.remove(test_backup)
        if mechanical_paths:
            subprocess.run(
                ["git", "checkout", "--", *mechanical_paths],
                cwd=workdir,
                capture_output=True,
            )
        subprocess.run(["git", "stash", "pop"], cwd=workdir, capture_output=True)

    emit("  [GREEN] Running test WITH fix...")
    try:
        green_result = subprocess.run(
            [*run_argv, test_file],
            cwd=workdir, capture_output=True, text=True, timeout=test_timeout,
        )
        green_passed = green_result.returncode == 0
        green_output_full = green_result.stdout + green_result.stderr
        green_output = green_output_full[:500]
        emit(f"  [GREEN] exit={green_result.returncode} {'PASS (good)' if green_passed else 'FAIL (BAD!)'}")
        for line in green_output.split("\n")[:10]:
            emit(f"    {line}")
    except subprocess.TimeoutExpired:
        green_passed = False
        green_output = f"TIMEOUT: test command exceeded {test_timeout}s with fix."
        emit(f"  [GREEN] TIMEOUT after {test_timeout}s")
        emit(f"    {green_output}")
    except (FileNotFoundError, OSError) as exc:
        green_exec_error = str(exc)
        green_output_full = green_exec_error
        green_output = green_output_full[:500]
        emit("  [GREEN] INFRA ERROR")
        emit(f"    {green_output}")

    log("verify", {
        "test_file": test_file,
        "red_passed": red_passed,
        "green_passed": green_passed,
        "red_output": red_output,
        "green_output": green_output,
    })

    red_infra_error = verification_infra_error(red_exec_error or red_output_full)
    if red_infra_error:
        return False, (
            f"REJECTED: Your red phase for {test_file} failed because the verification environment is broken, "
            f"not because the bug was reproduced. {red_infra_error}. "
            f"Fix the runner environment and rerun verification. Error: {red_output[:300]}"
        )

    if red_result is None:
        return False, (
            f"REJECTED: Your test ({test_file}) timed out WITHOUT your source fix. "
            f"Fix the timeout or make the test more targeted. Error: {red_output[:300]}"
        )

    if red_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) passes even WITHOUT your source fix. "
            f"This means it doesn't test the real code — it probably uses local stub functions "
            f"instead of importing from the source. Rewrite the test to import the real "
            f"function and mock its dependencies properly."
        )

    if is_invalid_red_phase_failure(red_output_full):
        return False, (
            f"REJECTED: Your red phase for {test_file} failed because the test scaffold is incomplete, "
            "not because the bug was reproduced. The failure mentions missing test-only seams/exports "
            f"(for example __setXForTests). Rework the test so the pre-fix run executes the real code path "
            f"and fails on behavior. Error: {red_output[:300]}"
        )

    green_infra_error = (
        verification_infra_error(green_exec_error or green_output_full)
        if not green_passed
        else None
    )
    if green_infra_error:
        return False, (
            f"REJECTED: Your green phase for {test_file} failed because the verification environment is broken. "
            f"{green_infra_error}. Fix the runner environment and rerun verification. "
            f"Error: {green_output[:300]}"
        )

    if green_result is None:
        return False, (
            f"REJECTED: Your test ({test_file}) timed out WITH your source fix. "
            f"Fix the timeout or make the test more targeted. Error: {green_output[:300]}"
        )

    if not green_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) fails even WITH your source fix. "
            f"The test has errors. Fix them. Error: {green_output[:300]}"
        )

    return True, "VERIFIED: Test fails without fix, passes with fix. Real red-green."
