"""Tests for pull request helper behavior."""

import subprocess

from agentic_tdd_runner.pr import (
    build_pr_fallback,
    check_pr_base_hygiene,
    parse_pr_content,
    resolve_pr_base_ref,
)


def test_parse_pr_content_extracts_title_and_multiline_body():
    title, body = parse_pr_content(
        "PR_TITLE: fix: use cumulative months\n"
        "PR_BODY: ## Summary\n\n- Fixed the bug\n- Added tests"
    )

    assert title == "fix: use cumulative months"
    assert "Added tests" in body


def test_build_pr_fallback_lists_changed_files():
    title, body = build_pr_fallback("src/file.test.ts", 7, ["src/file.ts", "src/file.test.ts"])

    assert title == "fix: update file behavior"
    assert "- `src/file.ts`" in body
    assert "Harness quality checks passed" in body


def test_resolve_pr_base_ref_prefers_origin_ref(tmp_path):
    subprocess.run(["git", "init", "-b", "main"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
    (tmp_path / "file.ts").write_text("code")
    subprocess.run(["git", "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "update-ref", "refs/remotes/origin/main", "HEAD"], cwd=tmp_path, check=True)

    assert resolve_pr_base_ref("main", 10, str(tmp_path)) == "origin/main"


def test_check_pr_base_hygiene_rejects_drifted_head(tmp_path):
    subprocess.run(["git", "init", "-b", "main"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
    (tmp_path / "file.ts").write_text("code")
    subprocess.run(["git", "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "update-ref", "refs/remotes/origin/main", "HEAD"], cwd=tmp_path, check=True)
    (tmp_path / "extra.ts").write_text("drift")
    subprocess.run(["git", "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "commit", "-m", "drift"], cwd=tmp_path, capture_output=True, check=True)

    ok, message = check_pr_base_hygiene("main", 10, str(tmp_path))

    assert ok is False
    assert "not cleanly based" in message
