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
