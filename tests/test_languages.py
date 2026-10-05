"""Language detection by file extension, recovered from the pre-prune registry tests (0e9a197)."""
from agentic_tdd_runner.languages import EXTENSIONS, language_for


def test_typescript_resolved_by_ts_extension():
    assert language_for("src/events/client.ts") == "typescript"


def test_python_resolved_by_py_extension():
    assert language_for("src/worker.py") == "python"


def test_unknown_extension_returns_none():
    assert language_for("main.go") is None


def test_supported_extensions_list_both():
    extensions = {ext for exts in EXTENSIONS.values() for ext in exts}
    assert ".ts" in extensions
    assert ".py" in extensions
