"""Tests for pull request helper behavior."""

import subprocess

from agentic_tdd_runner.pr import (
    build_pr_fallback,
    check_pr_base_hygiene,
    collect_pr_changed_files,
    create_pr,
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


def test_create_pr_stages_only_delta_after_baseline(tmp_path, monkeypatch):
    subprocess.run(["git", "init", "-b", "main"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
    (tmp_path / "preexisting.ts").write_text("original")
    (tmp_path / "file.ts").write_text("original")
    subprocess.run(["git", "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "update-ref", "refs/remotes/origin/main", "HEAD"], cwd=tmp_path, check=True)

    (tmp_path / "preexisting.ts").write_text("dirty before run")
    baseline = collect_pr_changed_files(str(tmp_path), 10)
    (tmp_path / "file.ts").write_text("agent fix")
    (tmp_path / "file.test.ts").write_text("agent test")

    add_commands = []
    original_run = subprocess.run

    def track_run(*args, **kwargs):
        cmd = args[0] if args else kwargs.get("args", [])
        if isinstance(cmd, list):
            if len(cmd) > 1 and cmd[1] == "add":
                add_commands.append(cmd)
            if cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd):
                return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
        return original_run(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", track_run)

    result = create_pr(
        [],
        {},
        "file.test.ts",
        3,
        workdir=str(tmp_path),
        config={
            "pr": {"base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "timeouts": {"pr_create": 10},
        },
        emit=lambda _msg: None,
        log=lambda _event, _data: None,
        chat=lambda _messages, include_tools=True: {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        },
        get_changed_files_fn=lambda: [],
        baseline_changed_files=baseline,
    )

    assert result == "https://github.com/test/pr/1"
    assert len(add_commands) == 1
    staged_files = add_commands[0][3:]
    assert "file.ts" in staged_files
    assert "file.test.ts" in staged_files
    assert "preexisting.ts" not in staged_files
