"""Tests for pull request helper behavior."""

import os
import subprocess

from agentic_tdd_runner.pr import (
    build_pr_content_messages,
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
    assert body.startswith("## Summary\n\n- Fix")
    assert "\n## Changed files\n\n- `src/file.ts`" in body
    for line in body.splitlines():
        assert not line.startswith("        ")


def test_build_pr_content_messages_removes_tool_blocks_for_text_only_request():
    messages = [
        {"role": "system", "content": "You are ATM."},
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [{"id": "call_1", "type": "function"}],
        },
        {"role": "tool", "tool_call_id": "call_1", "content": "file contents"},
    ]

    result = build_pr_content_messages(
        messages,
        {"role": "assistant", "content": "DONE"},
        pr_prompt="Generate PR",
        changed_files=["src/file.ts"],
    )

    assert result == [
        {"role": "system", "content": "You are ATM."},
        {
            "role": "user",
            "content": (
                "Generate PR\n\n"
                "Changed files:\n"
                "- src/file.ts\n\n"
                "Final agent message:\n"
                "DONE"
            ),
        },
    ]


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
    preflight_envs = []
    original_run = subprocess.run

    def track_run(*args, **kwargs):
        cmd = args[0] if args else kwargs.get("args", [])
        if isinstance(cmd, list):
            if cmd[0] == "git" and cmd[1] in {"rev-parse", "rev-list", "diff", "ls-files"}:
                preflight_envs.append(kwargs.get("env"))
            if len(cmd) > 1 and cmd[1] == "add":
                add_commands.append(cmd)
            if cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd):
                return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
        return original_run(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", track_run)
    logged = []

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
            "tools": {"path_dirs": ["/custom/git/bin"]},
        },
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
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
    assert preflight_envs
    assert all(
        "/custom/git/bin" in env["PATH"].split(os.pathsep)
        for env in preflight_envs
    )
    pr_events = [event for event, _data in logged if event.startswith("pr")]
    assert "pr_excluded_preexisting_changes" in pr_events
    assert [event for event in pr_events if event != "pr_excluded_preexisting_changes"] == [
        "pr_start",
        "pr_content_start",
        "pr_git_start",
        "pr",
        "pr_done",
    ]
    assert logged[-1][1]["url"] == "https://github.com/test/pr/1"


def test_create_pr_logs_content_fallback_when_llm_returns_incomplete_body(tmp_path, monkeypatch):
    subprocess.run(["git", "init", "-b", "main"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
    (tmp_path / "file.ts").write_text("original")
    subprocess.run(["git", "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "update-ref", "refs/remotes/origin/main", "HEAD"], cwd=tmp_path, check=True)

    (tmp_path / "file.ts").write_text("agent fix")
    (tmp_path / "file.test.ts").write_text("agent test")

    original_run = subprocess.run

    def fake_remote_run(*args, **kwargs):
        cmd = args[0] if args else kwargs.get("args", [])
        if isinstance(cmd, list) and (cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd)):
            return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/2\n")
        return original_run(*args, **kwargs)

    monkeypatch.setattr(subprocess, "run", fake_remote_run)
    logged = []

    result = create_pr(
        [],
        {},
        "file.test.ts",
        4,
        workdir=str(tmp_path),
        config={
            "pr": {"base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "timeouts": {"pr_create": 10},
        },
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=lambda _messages, include_tools=True: {
            "choices": [{"message": {"content": "PR_TITLE: fix"}}],
        },
        get_changed_files_fn=lambda: [],
    )

    assert result == "https://github.com/test/pr/2"
    fallback_events = [
        data for event, data in logged
        if event == "pr_content_fallback"
    ]
    assert fallback_events == [{
        "reason": "incomplete_content",
        "raw_response": "PR_TITLE: fix",
        "got_title": True,
        "got_body": False,
    }]
    assert logged[-1][0] == "pr_done"


def test_create_pr_logs_changed_files_os_error(tmp_path, monkeypatch):
    subprocess.run(["git", "init", "-b", "main"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
    subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
    (tmp_path / "file.ts").write_text("original")
    subprocess.run(["git", "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
    subprocess.run(["git", "update-ref", "refs/remotes/origin/main", "HEAD"], cwd=tmp_path, check=True)

    def fail_changed_files(*_args, **_kwargs):
        raise OSError("git executable not found")

    monkeypatch.setattr(
        "agentic_tdd_runner.pr.collect_pr_changed_files",
        fail_changed_files,
    )
    emitted = []
    logged = []

    result = create_pr(
        [],
        {},
        "file.test.ts",
        5,
        workdir=str(tmp_path),
        config={
            "pr": {"base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "timeouts": {"pr_create": 10},
        },
        emit=emitted.append,
        log=lambda event, data: logged.append((event, data)),
        chat=lambda _messages, include_tools=True: {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        },
        get_changed_files_fn=lambda: [],
    )

    assert result is None
    assert any("Tool missing" in msg for msg in emitted)
    assert ("pr_error", {
        "stage": "changed_files",
        "error": "git executable not found",
    }) in logged
