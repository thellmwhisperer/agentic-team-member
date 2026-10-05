"""Red/green verification tests, recovered from tests/test_agent.py before the prune (0e9a197).

They drove verification through an agent.py wrapper bound to a module-level WORKDIR and
config. RUN and verify_red_green below keep that shape, so the bodies are unchanged.
"""
import subprocess
import shutil
from types import SimpleNamespace

import pytest

from agentic_tdd_runner import verification
from agentic_tdd_runner.verification import single_test_argv, verification_infra_error

GIT = shutil.which("git") or "git"
RUN = SimpleNamespace(workdir=None, config=None)


def verify_red_green(test_file):
    return verification.verify_red_green(
        test_file, workdir=RUN.workdir, config=RUN.config, emit=lambda msg: None, log=lambda event, data: None,
    )


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
        assert verification_infra_error(output) == "pytest is unavailable in the verification environment"

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
        assert verification_infra_error(output) == expected

    def test_ignores_project_module_missing_with_pytest_banner(self):
        output = (
            "============================= test session starts ==============================\n"
            "platform darwin -- Python 3.12.0, pytest-8.4.2\n"
            "collected 1 item\n\n"
            "src/test_worker.py F                                                     [100%]\n\n"
            "E   ModuleNotFoundError: No module named 'my_module'\n"
        )
        assert verification_infra_error(output) is None

    def test_ignores_regular_file_not_found_with_pytest_banner(self):
        output = (
            "============================= test session starts ==============================\n"
            "platform darwin -- Python 3.12.0, pytest-8.4.2\n"
            "collected 1 item\n\n"
            "tests/test_worker.py F                                                   [100%]\n\n"
            "E   FileNotFoundError: [Errno 2] No such file or directory: 'fixtures/missing.json'\n"
        )
        assert verification_infra_error(output) is None


class TestFindTestFile:

    def test_verify_red_green_preserves_untracked_test(self, tmp_path, monkeypatch):
        """An untracked test file must survive the stash cycle in verify_red_green."""

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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        verify_red_green("src/math.test.ts")

        assert (src / "math.test.ts").exists(), "Untracked test file disappeared"
        assert (src / "math.ts").read_text() == "fixed", "Source fix not restored"

    def test_stash_popped_after_red_phase_exception(self, tmp_path, monkeypatch):
        """git stash must be popped even if the red-phase test run times out."""
        from unittest.mock import patch as mock_patch

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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

        sp_run = subprocess.run
        sp_run([GIT, "init"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "config", "user.email", "t@t"], cwd=tmp_path, capture_output=True)
        sp_run([GIT, "config", "user.name", "t"], cwd=tmp_path, capture_output=True)
        (tmp_path / "tracked.txt").write_text("base")
        sp_run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        sp_run([GIT, "commit", "-m", "base"], cwd=tmp_path, capture_output=True, check=True)

        (tmp_path / "tracked.txt").write_text("stashed")
        sp_run([GIT, "stash", "push", "-m", "keep-existing"], cwd=tmp_path, capture_output=True, check=True)

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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
        assert single_test_argv("test_worker.py", {}) == ["python3", "-m", "pytest", "test_worker.py"]

    def test_verify_red_green_rejects_red_phase_that_fails_on_missing_test_seam(self, tmp_path, monkeypatch):
        """A red phase that dies on missing __setXForTests is not a valid proof of the bug."""
        from unittest.mock import patch as mock_patch

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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

    def test_verify_red_green_detects_scaffold_marker_past_500_chars(self, tmp_path, monkeypatch):
        """The scaffold-failure guard must inspect the FULL output, not the 500-char preview.

        Real test runners (pytest, bun test) print banners, collected items, and
        traceback frames before the actual error. The __setXForTests marker can
        easily fall past char 500. If the guard only sees the truncated preview,
        invalid red phases sneak through and the LLM is told its broken test was
        verified."""
        from unittest.mock import patch as mock_patch

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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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
        """ReferenceError / NameError around __setXForTests seams both produce
        the exact substring 'is not defined'. These are the same invalid-red-phase
        bug as the __setXForTests check, just a different runtime phrasing. Must
        be rejected, not verified."""
        from unittest.mock import patch as mock_patch

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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        monkeypatch.setattr(RUN, "config", {
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

