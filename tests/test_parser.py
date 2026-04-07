"""Tests for compiler parser functions."""
from agentic_tdd_runner.compiler.parser import _find_symbol_line


class TestFindSymbolLine:
    def test_finds_function_definition_not_reference(self):
        source = (
            "// setup\n"
            "client.on('resub', handleResub);\n"
            "\n"
            "function handleResub(channel, username, months) {\n"
            "  // body\n"
            "}\n"
        )
        assert _find_symbol_line(source, "handleResub") == 4

    def test_finds_export_function(self):
        source = "export function process(item) {\n  return item;\n}\n"
        assert _find_symbol_line(source, "process") == 1

    def test_finds_python_def(self):
        source = "logger = get_logger()\n\ndef process(item):\n    return item\n"
        assert _find_symbol_line(source, "process") == 3

    def test_finds_arrow_function(self):
        source = "const other = 1;\nexport const process = (x) => x * 2;\n"
        assert _find_symbol_line(source, "process") == 2
