"""Quality gate tests, recovered from tests/test_agent.py before the prune (0e9a197).

They drove quality through agent.py wrappers bound to a module-level WORKDIR and
config. RUN and run_quality_checks below keep that shape, so the bodies are unchanged.
"""
import subprocess
import shutil
from types import SimpleNamespace

import pytest

from agentic_tdd_runner import quality
from agentic_tdd_runner.paths import is_test_file_path

GIT = shutil.which("git") or "git"
RUN = SimpleNamespace(workdir=None, config=None)


def is_test_file_path_in_run(path):
    return is_test_file_path(path, RUN.config)


def run_quality_checks(test_file):
    return quality.run_quality_checks(
        test_file, workdir=RUN.workdir, config=RUN.config, log=lambda event, data: None,
        is_test_file_path=is_test_file_path_in_run,
    )


class TestIsTestFilePath:
    """Fallback test filename heuristics should avoid broad substring matches."""

    def test_fallback_accepts_conventional_test_name(self, monkeypatch):
        monkeypatch.setattr(RUN, "config", {"runner": {"test_file_patterns": []}})
        assert is_test_file_path_in_run("src/test_worker.py") is True

    def test_fallback_rejects_non_test_substring_name(self, monkeypatch):
        monkeypatch.setattr(RUN, "config", {"runner": {"test_file_patterns": []}})
        assert is_test_file_path_in_run("src/contest.py") is False


class TestDetectQualityTools:
    """detect_quality_tools reads package.json/pyproject.toml to find lint/format tools."""

    def test_detects_biome_from_package_json(self, tmp_path, monkeypatch):
        import json
        (tmp_path / "package.json").write_text(json.dumps({
            "devDependencies": {"@biomejs/biome": "^2.0"},
            "scripts": {"lint": "biome check src", "typecheck": "tsc --noEmit"},
        }))
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        checks = quality.detect_quality_tools("typescript", RUN.workdir)
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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        checks = quality.detect_quality_tools("typescript", RUN.workdir)
        names = [c["name"] for c in checks]
        assert "lint" in names
        assert "format" in names
        lint_cmd = next(c for c in checks if c["name"] == "lint")
        assert "eslint" in lint_cmd["command"]
        format_cmd = next(c for c in checks if c["name"] == "format")
        assert "prettier" in format_cmd["command"]

    def test_detects_ruff_from_pyproject(self, tmp_path, monkeypatch):
        (tmp_path / "pyproject.toml").write_text('[tool.ruff]\nline-length = 88\n')
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        checks = quality.detect_quality_tools("python", RUN.workdir)
        names = [c["name"] for c in checks]
        assert "lint" in names
        lint_cmd = next(c for c in checks if c["name"] == "lint")
        assert "ruff" in lint_cmd["command"]

    def test_returns_empty_when_no_tools_found(self, tmp_path, monkeypatch):
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        checks = quality.detect_quality_tools("typescript", RUN.workdir)
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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        checks = quality.detect_quality_tools("typescript", RUN.workdir)
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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        checks = quality.detect_quality_tools("typescript", RUN.workdir)
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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))

        checks = quality.detect_quality_tools("typescript", RUN.workdir)

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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))

        checks = quality.detect_quality_tools("typescript", RUN.workdir)

        by_name = {check["name"]: check for check in checks}
        assert by_name["typecheck"]["command"] == "pnpm exec tsc --noEmit"
        assert by_name["lint"]["command"] == "pnpm exec biome check {changed_files}"
        assert by_name["lint"]["fix"] == "pnpm exec biome check {changed_files} --fix"


class TestTypecheckOwnershipHint:
    """Typecheck ownership should not misattribute common basenames."""

    def test_matches_exact_changed_path(self):
        msg = quality.typecheck_ownership_hint(
            "typecheck",
            "src/file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts"],
        )
        assert msg is not None
        assert "belongs to this fix" in msg

    def test_matches_unique_basename_when_output_omits_directory(self):
        msg = quality.typecheck_ownership_hint(
            "typecheck",
            "file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts"],
        )
        assert msg is not None
        assert "belongs to this fix" in msg

    def test_rejects_ambiguous_basename_when_output_omits_directory(self):
        msg = quality.typecheck_ownership_hint(
            "typecheck",
            "file.ts(1,1): error TS2322: broken\n",
            ["src/file.ts", "tests/file.ts"],
        )
        assert msg is None

    def test_rejects_directory_path_when_only_basename_matches(self):
        msg = quality.typecheck_ownership_hint(
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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        files = quality.get_changed_files(RUN.workdir)
        assert "tracked.ts" in files
        assert "new.ts" in files

    def test_includes_staged_files(self, tmp_path, monkeypatch):
        self._init_repo(tmp_path)
        (tmp_path / "initial.ts").write_text("original")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "initial.ts").write_text("staged change")
        subprocess.run([GIT, "add", "initial.ts"], cwd=tmp_path, capture_output=True, check=True)
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        files = quality.get_changed_files(RUN.workdir)
        assert "initial.ts" in files, "Staged files must be included in quality gate"

    def test_excludes_deleted_files(self, tmp_path, monkeypatch):
        self._init_repo(tmp_path)
        (tmp_path / "keep.ts").write_text("keep")
        (tmp_path / "deleted.ts").write_text("gone soon")
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "init"], cwd=tmp_path, capture_output=True, check=True)
        (tmp_path / "deleted.ts").unlink()
        (tmp_path / "keep.ts").write_text("modified")
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))
        files = quality.get_changed_files(RUN.workdir)
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
        monkeypatch.setattr(RUN, "workdir", str(tmp_path))

    def test_passes_when_disabled(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        monkeypatch.setattr(RUN, "config", {
            "quality": {"enabled": False},
            "timeouts": {"tool_execution": 10},
        })
        ok, msg = run_quality_checks("src/file.test.ts")
        assert ok is True

    def test_detects_forbidden_pattern(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("const x = {} as any;")
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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

    def test_rejects_tests_that_assert_source_text(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text(
            "import { expect, test } from 'bun:test';\n"
            "import fs from 'fs';\n"
            "test('implementation text', () => {\n"
            "  const source = fs.readFileSync('src/file.ts', 'utf-8');\n"
            "  expect(source).toContain('includes');\n"
            "});\n"
        )
        monkeypatch.setattr(RUN, "config", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is False
        assert "reads production source text" in msg
        assert "import and exercise the real behavior" in msg

    def test_allows_tests_that_read_fixture_source_text(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "__fixtures__").mkdir()
        (tmp_path / "src" / "__fixtures__" / "case.ts").write_text(
            "export const fixture = 'includes';\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text(
            "import { expect, test } from 'bun:test';\n"
            "import fs from 'fs';\n"
            "test('fixture text', () => {\n"
            "  const source = fs.readFileSync('src/__fixtures__/case.ts', 'utf-8');\n"
            "  expect(source).toContain('includes');\n"
            "});\n"
        )
        monkeypatch.setattr(RUN, "config", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True
        assert msg == "All quality checks passed"

    def test_allows_tests_that_read_test_source_text(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.spec.ts").write_text("test('existing spec', () => {});\n")
        (tmp_path / "src" / "file.test.ts").write_text(
            "import { expect, test } from 'bun:test';\n"
            "import fs from 'fs';\n"
            "test('test helper text', () => {\n"
            "  const source = fs.readFileSync('src/file.spec.ts', 'utf-8');\n"
            "  expect(source).toContain('existing spec');\n"
            "});\n"
        )
        monkeypatch.setattr(RUN, "config", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is True
        assert msg == "All quality checks passed"

    def test_passes_clean_code(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        so the model sees type names like RenewalEventPayload without exploring node_modules."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("clean")

        tsc_output = (
            "src/file.ts(123,10): error TS2769: No overload matches this call.\n"
            "  Overload 1 of 2, '(event: \"renewal\", listener: (..., "
            "eventPayload: RenewalEventPayload, methods: DeliveryOptions) => void): Client'\n"
            "  gave the following error.\n"
            "    Argument of type '(a: string) => void' is not assignable.\n"
        )
        # Write a script that emits the tsc output and exits 1
        script = tmp_path / "fake-tsc.sh"
        script.write_text(f"#!/bin/sh\ncat <<'TSCEOF'\n{tsc_output}TSCEOF\nexit 1\n")
        script.chmod(0o755)

        monkeypatch.setattr(RUN, "config", {
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
        assert "RenewalEventPayload" in msg, f"Type name from continuation line missing: {msg}"
        assert "DeliveryOptions" in msg

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

        monkeypatch.setattr(RUN, "config", {
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

        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
            "    const notifier_send_spy = mock(() => undefined);\n"
            "    __setClientForTests(notifier_send_spy);\n"
            "    processRenewal(channel, username, 0, message, eventPayload);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    const notifier_send_spy = mock(() => undefined);\n"
            "    __setClientForTests(notifier_send_spy);\n"
            "    processRenewal(channel, username, 1, message, eventPayload);\n"
            "  });\n"
            "});\n"
        )
        monkeypatch.setattr(RUN, "config", {
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
        assert "notifier_send_spy" in msg

    def test_filters_changed_files_by_language_extensions(self, tmp_path, monkeypatch):
        """Only files matching the active language's extensions are scanned."""
        self._setup_repo(tmp_path, monkeypatch)
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        (tmp_path / "config.toml").write_text('value = "as any"')
        (tmp_path / "helper.py").write_text("x = 'as any'")
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('c', () => {\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "});\n"
        )

        class FakeResponse:
            def raise_for_status(self):
                return None

            def json(self):
                return {"message": {"content": "NO"}}

        monkeypatch.setattr("agentic_tdd_runner.quality.requests.post", lambda *a, **kw: FakeResponse())
        monkeypatch.setattr(RUN, "config", {
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
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('c', () => {\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "});\n"
        )
        monkeypatch.setattr(RUN, "config", {
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
        assert quality.is_obvious_assert_line("toHaveBeenCalledWith(channel, expected_message);") is True

    def test_duplicated_setup_treats_function_under_test_call_as_act(self):
        assert quality.is_obvious_act_line("processRenewal(channel, username, retryCount, message, eventPayload);") is True

    def test_duplicated_setup_judge_prompt_explicitly_bans_act_assert_in_before_each(self):
        prompt = quality.build_duplicated_setup_judge_prompt(
            "src/file.test.ts",
            "describe('x', () => {})\n",
            [
                "processRenewal(channel, username, retryCount, message, eventPayload);",
                "expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);",
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
            "    const notifier_send_spy = mock(() => undefined);\n"
            "    __setClientForTests(notifier_send_spy);\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('b', () => {\n"
            "    const notifier_send_spy = mock(() => undefined);\n"
            "    __setClientForTests(notifier_send_spy);\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "  test('c', () => {\n"
            "    const notifier_send_spy = mock(() => undefined);\n"
            "    __setClientForTests(notifier_send_spy);\n"
            "    processRenewal(channel, username, retryCount, message, eventPayload);\n"
            "    expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_message);\n"
            "  });\n"
            "});\n"
        )
        monkeypatch.setattr(RUN, "config", {
            "quality": {
                "enabled": True, "max_fix_rounds": 3,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
            "prompt": {"quality_failed": "FAIL: {details}"},
        })

        ok, msg = run_quality_checks("src/file.test.ts")

        assert ok is False
        assert "notifier_send_spy" in msg
        assert "processRenewal" not in msg

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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
        monkeypatch.setattr(RUN, "config", {
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
            "  logger.event('renewal', { username, months });\n"
            "}\n"
        )
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "payload baseline"], cwd=tmp_path, capture_output=True, check=True)

        source.write_text(
            "export function f(logger, username, totalCount) {\n"
            "  logger.event('renewal', { username, totalCount });\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
        assert "totalCount" in msg

    def test_allows_side_effect_payload_value_change_with_same_key(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(logger, username, months) {\n"
            "  logger.event('renewal', { username, months });\n"
            "}\n"
        )
        subprocess.run([GIT, "add", "-A"], cwd=tmp_path, capture_output=True, check=True)
        subprocess.run([GIT, "commit", "-m", "payload baseline"], cwd=tmp_path, capture_output=True, check=True)

        source.write_text(
            "export function f(logger, username, totalCount) {\n"
            "  logger.event('renewal', { username, months: totalCount });\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
            "export function f(_retryCount: number, eventPayload: Record<string, string>) {\n"
            "  const months = parseInt(eventPayload['event-total-count'] || '0', 10);\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
        assert "_retryCount" in msg

    def test_rejects_plain_callback_value_metadata_fallback_without_invalid_guard(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(retryCount: number, eventPayload: Record<string, string>) {\n"
            "  const raw = eventPayload['event-total-count'];\n"
            "  const months = typeof raw === 'string' ? parseInt(raw, 10) : retryCount;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
        assert "retryCount" in msg

    def test_allows_parsed_metadata_with_original_fallback(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_retryCount: number, eventPayload: Record<string, string>) {\n"
            "  const parsed = Number(eventPayload['event-total-count']);\n"
            "  const months = Number.isNaN(parsed) ? _retryCount : parsed;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
            "export function f(_retryCount: number, eventPayload: Record<string, string>) {\n"
            "  const months = Number(eventPayload['event-total-count']) || _retryCount;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
        assert "_retryCount" in msg

    def test_rejects_parse_default_that_does_not_handle_invalid_metadata(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_retryCount: number, eventPayload: Record<string, string>) {\n"
            "  const months = parseInt(\n"
            "    String(eventPayload['event-total-count'] ?? _retryCount),\n"
            "    10,\n"
            "  );\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
        assert "_retryCount" in msg

    def test_allows_nan_guarded_metadata_fallback(self, tmp_path, monkeypatch):
        self._setup_repo(tmp_path, monkeypatch)
        source = tmp_path / "src" / "file.ts"
        source.write_text(
            "export function f(_retryCount: number, eventPayload: Record<string, string>) {\n"
            "  const parsed = Number(eventPayload['event-total-count']);\n"
            "  const months = Number.isFinite(parsed) ? parsed : _retryCount;\n"
            "  return months;\n"
            "}\n"
        )
        (tmp_path / "src" / "file.test.ts").write_text("const x: number = 1;")
        monkeypatch.setattr(RUN, "config", {
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
            "import type { DeliveryOptions } from '@example/event-bus';\n"
            "const methods = {} as DeliveryOptions;\n"
        )
        monkeypatch.setattr(RUN, "config", {
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
        assert "{} as DeliveryOptions" in msg

