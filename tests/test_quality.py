from agentic_tdd_runner.quality import run_quality_checks


def test_quality_unknown_language_does_not_use_typescript_fallback(tmp_path):
    test_file = tmp_path / "tests" / "worker_spec.rb"
    test_file.parent.mkdir(parents=True)
    test_file.write_text("value = '{} as any'\n")

    ok, msg = run_quality_checks(
        "tests/worker_spec.rb",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.endswith("_spec.rb"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: ["tests/worker_spec.rb"],
    )

    assert ok is True
    assert msg == "No quality checks configured for unknown language"


def test_quality_unknown_language_keeps_duplicated_setup_check(tmp_path):
    test_file = tmp_path / "tests" / "worker_spec.rb"
    test_file.parent.mkdir(parents=True)
    repeated_setup = "notifier_send_spy = mock()"
    test_file.write_text("\n".join([
        "describe 'worker' do",
        repeated_setup,
        repeated_setup,
        "end",
    ]))

    ok, msg = run_quality_checks(
        "tests/worker_spec.rb",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.endswith("_spec.rb"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: ["tests/worker_spec.rb"],
    )

    assert ok is False
    assert "Duplicated setup" in msg
    assert "notifier_send_spy" in msg
    assert "as any" not in msg


def test_quality_unknown_language_does_not_use_typescript_jest_mock_setup(tmp_path):
    test_file = tmp_path / "tests" / "worker_spec.rb"
    test_file.parent.mkdir(parents=True)
    repeated_setup = "notifier = jest.fn(() => undefined)"
    test_file.write_text("\n".join([
        "describe 'worker' do",
        repeated_setup,
        repeated_setup,
        "end",
    ]))

    ok, msg = run_quality_checks(
        "tests/worker_spec.rb",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.endswith("_spec.rb"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: ["tests/worker_spec.rb"],
    )

    assert ok is True
    assert msg == "No quality checks configured for unknown language"


def test_quality_typescript_uses_language_mock_setup_detection(tmp_path):
    test_file = tmp_path / "src" / "worker.test.ts"
    test_file.parent.mkdir(parents=True)
    repeated_setup = "notifier = jest.fn(() => undefined)"
    test_file.write_text("\n".join([
        "test('one', () => {",
        repeated_setup,
        "});",
        "test('two', () => {",
        repeated_setup,
        "});",
    ]))

    ok, msg = run_quality_checks(
        "src/worker.test.ts",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: ["src/worker.test.ts"],
    )

    assert ok is False
    assert "Duplicated setup" in msg
    assert "notifier" in msg


def test_quality_typescript_detects_bun_module_mock_setup(tmp_path):
    test_file = tmp_path / "src" / "worker.test.ts"
    test_file.parent.mkdir(parents=True)
    repeated_setup = "mock.module('../logger', () => ({}));"
    test_file.write_text("\n".join([
        "test('one', () => {",
        repeated_setup,
        "});",
        "test('two', () => {",
        repeated_setup,
        "});",
    ]))

    ok, msg = run_quality_checks(
        "src/worker.test.ts",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "typescript": {"checks": [], "forbidden": []},
            },
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: ["src/worker.test.ts"],
    )

    assert ok is False
    assert "Duplicated setup" in msg


def test_quality_python_filters_changed_files_to_python_extensions(tmp_path):
    py_test = tmp_path / "tests" / "test_worker.py"
    ts_file = tmp_path / "src" / "worker.ts"
    py_test.parent.mkdir(parents=True)
    ts_file.parent.mkdir(parents=True)
    py_test.write_text("def test_worker():\n    assert True\n")
    ts_file.write_text("const value = {} as any;\n")

    ok, msg = run_quality_checks(
        "tests/test_worker.py",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "python": {"checks": [], "forbidden": ["type: ignore"]},
                "typescript": {"checks": [], "forbidden": ["as any"]},
            },
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.startswith("tests/test_"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: ["tests/test_worker.py", "src/worker.ts"],
    )

    assert ok is True
    assert msg == "All quality checks passed"


def _committed_repo(tmp_path, rel, text):
    import subprocess
    git = ["git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.hooksPath=/dev/null"]
    path = tmp_path / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    subprocess.run([*git, "init", "-q"], cwd=tmp_path, check=True)
    subprocess.run([*git, "add", "."], cwd=tmp_path, check=True)
    subprocess.run([*git, "commit", "-q", "-m", "base"], cwd=tmp_path, check=True)


def _python_forbidden_check(tmp_path, changed):
    return run_quality_checks(
        changed[0],
        workdir=str(tmp_path),
        config={
            "quality": {"enabled": True, "python": {"checks": [], "forbidden": [MARKER]}},
            "timeouts": {"tool_execution": 10},
        },
        log=lambda _event, _data: None,
        is_test_file_path=lambda path: path.startswith("tests/test_"),
        detect_quality_tools_fn=lambda _lang_name: [],
        get_changed_files_fn=lambda: changed,
    )


MARKER = "no" + "qa"  # spelled apart so this file never carries the forbidden pattern itself


def test_quality_forbidden_ignores_patterns_already_on_the_base_commit(tmp_path):
    _committed_repo(tmp_path, "tests/test_worker.py", f"import os  # {MARKER}\n")
    with (tmp_path / "tests" / "test_worker.py").open("a") as fh:
        fh.write("\n\ndef test_worker():\n    assert os\n")

    ok, msg = _python_forbidden_check(tmp_path, ["tests/test_worker.py"])

    assert ok is True, msg


def test_quality_forbidden_reports_added_lines_and_new_files_with_line_numbers(tmp_path):
    _committed_repo(tmp_path, "tests/test_worker.py", f"import os  # {MARKER}\n")
    with (tmp_path / "tests" / "test_worker.py").open("a") as fh:
        fh.write(f"import sys  # {MARKER}\n")
    (tmp_path / "tests" / "test_new.py").write_text(f"x = 1\ny = 2  # {MARKER}\n")

    ok, msg = _python_forbidden_check(tmp_path, ["tests/test_worker.py", "tests/test_new.py"])

    assert ok is False
    assert f"tests/test_worker.py:2 '{MARKER}'" in msg
    assert f"tests/test_worker.py:1 '{MARKER}'" not in msg
    assert f"tests/test_new.py:2 '{MARKER}'" in msg
