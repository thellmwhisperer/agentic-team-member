"""Tests for compiler parser functions."""
from agentic_tdd_runner.compiler.parser import _extract_signature, _find_symbol_line


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

    def test_commented_out_decoy_does_not_mask_real_definition(self):
        """CodeRabbit (PR #14, comment 3106068384): without a line-start anchor,
        the TS/JS definition patterns match commented-out code like
        `// function handleResub()`. The commented decoy at line 1 then wins
        over the real definition below, sending the agent to the wrong
        region. With ^\\s*, the real definition at line 3 is the one that
        classifies as 'definition'."""
        source = (
            "// function handleResub(channel) {}  // old stub, kept for history\n"
            "\n"
            "export function handleResub(channel) {\n"
            "  return channel;\n"
            "}\n"
        )
        assert _find_symbol_line(source, "handleResub") == 3

    def test_commented_out_const_decoy_does_not_mask_real_definition(self):
        source = (
            "// const process = (x) => x + 1;\n"
            "export const process = (x) => x * 2;\n"
        )
        assert _find_symbol_line(source, "process") == 2


class TestExtractSignature:
    def test_multiline_ts_function(self):
        source = (
            "export function handleResub(\n"
            "  channel: string,\n"
            "  username: string,\n"
            "  months: number,\n"
            "): void {\n"
            "  // body\n"
            "}\n"
        )
        sig = _extract_signature(source, "handleResub")
        assert "channel" in sig
        assert "username" in sig
        assert "months" in sig

    def test_multiline_python_def(self):
        source = (
            "def process(\n"
            "    item: str,\n"
            "    count: int,\n"
            ") -> str:\n"
            "    return item\n"
        )
        sig = _extract_signature(source, "process")
        assert "item" in sig
        assert "count" in sig

    def test_single_line_still_works(self):
        source = "export function greet(name: string): void {\n"
        sig = _extract_signature(source, "greet")
        assert "name" in sig
