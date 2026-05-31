"""Tests for the compiler — contract building from analysis facts."""
from agentic_tdd_runner.compiler import build_contract
from agentic_tdd_runner.languages.signature import parse_signature_params as _parse_signature_params


def _handle_renewal_facts():
    return {
        "target": {
            "symbol": "processRenewal",
            "kind": "function",
            "source_path": "src/events/client.ts",
            "line_start": 770,
            "line_end": 777,
            "signature": "processRenewal(channel: string, username: string, months: number): void",
        },
        "test_file": {
            "path": "src/events/client-renewal.test.ts",
            "runner": "bun:test",
        },
        "pre_test_source_edits": [
            {
                "kind": "mechanical_export",
                "path": "src/events/client.ts",
                "old": "function processRenewal(",
                "new": "export function processRenewal(",
            }
        ],
        "module_load_dependencies": [
            {
                "binding": "logger",
                "origin_kind": "factory_result",
                "source_module": "../logger",
                "required_shape": {
                    "getLogger": ["event", "response"],
                    "log": ["info", "error", "warn"],
                },
                "strategy": "mock_module",
            },
            {
                "binding": "metricsSummaryManager",
                "origin_kind": "factory_result",
                "source_module": "../managers/metrics-summary",
                "required_shape": {
                    "getMetricsSummaryManager": ["trackRenewal"],
                },
                "strategy": "mock_module",
            },
        ],
        "execution_dependencies": [
            {
                "binding": "memoryManager",
                "origin_kind": "module_local_mutable",
                "required_shape": {"getEmote": []},
                "strategy": "set_test_seam",
                "observed_members": ["getEmote"],
            },
            {
                "binding": "notifier",
                "origin_kind": "module_local_mutable",
                "required_shape": {"send": []},
                "strategy": "set_test_seam",
                "observed_members": ["send"],
            },
        ],
        "injection_plan": [
            {
                "binding": "memoryManager",
                "strategy": "set_test_seam",
                "steps": ["inject a stub with getEmote()"],
                "blocks_if_missing": True,
                "seam_available": False,
            },
            {
                "binding": "notifier",
                "strategy": "set_test_seam",
                "steps": ["inject a spy for send(channel, message)"],
                "blocks_if_missing": True,
                "seam_available": False,
            },
        ],
        "assertion_surface": {
            "kind": "outbound_call_arguments",
            "binding": "notifier",
            "member": "send",
            "assertion_shape": "toHaveBeenCalledWith(channel, expectedMessage)",
        },
        "pattern_files": [
            {
                "path": "src/api/api.test.ts",
                "why_selected": "logger mock shape",
                "reusable_shapes": ["mock.module('../logger', ...)"],
                "non_reusable_noise": ["HTTP assertions"],
            }
        ],
    }


def test_computes_source_import_path():
    contract = build_contract(_handle_renewal_facts())
    assert contract["test_file"]["source_import_path"] == "./client"


def test_marks_contract_not_ready_when_framework_seam_is_missing():
    contract = build_contract(_handle_renewal_facts())
    assert contract["ready"] is False
    assert contract["gaps"] == [
        {
            "kind": "missing_test_seam",
            "message": "memoryManager requires injection strategy 'set_test_seam' but no framework seam is available",
            "owner": "framework",
        },
        {
            "kind": "missing_test_seam",
            "message": "notifier requires injection strategy 'set_test_seam' but no framework seam is available",
            "owner": "framework",
        },
    ]


def test_renders_module_mocks_and_source_import():
    contract = build_contract(_handle_renewal_facts())
    block = contract["scaffold"]["module_mocks_block"]
    assert "mock.module('../logger'" in block
    assert "const logger_event_spy = mock(() => undefined);" in block
    assert "const metricsSummaryManager_trackRenewal_spy = mock(() => undefined);" in block
    assert "getLogger: () => ({" in block
    assert "log: {" in block
    assert "event: logger_event_spy" in block
    assert "info: logger_info_spy" in block
    assert "trackRenewal: metricsSummaryManager_trackRenewal_spy" in block
    assert "const { processRenewal } = await import('./client');" in block


def test_module_mock_spies_are_scoped_per_dependency():
    facts = _handle_renewal_facts()
    facts["module_load_dependencies"] = [
        {
            "binding": "primaryLogger",
            "origin_kind": "factory_result",
            "source_module": "../primary-logger",
            "required_shape": {"getLogger": ["info"]},
            "strategy": "mock_module",
        },
        {
            "binding": "secondaryLogger",
            "origin_kind": "factory_result",
            "source_module": "../secondary-logger",
            "required_shape": {"getLogger": ["info"]},
            "strategy": "mock_module",
        },
    ]

    block = build_contract(facts)["scaffold"]["module_mocks_block"]

    assert "const primaryLogger_info_spy = mock(() => undefined);" in block
    assert "const secondaryLogger_info_spy = mock(() => undefined);" in block
    primary_block = block.split("mock.module('../primary-logger'", 1)[1].split("mock.module('../secondary-logger'", 1)[0]
    secondary_block = block.split("mock.module('../secondary-logger'", 1)[1]
    assert "info: primaryLogger_info_spy" in primary_block
    assert "info: secondaryLogger_info_spy" in secondary_block
    assert "info: primaryLogger_info_spy" not in secondary_block


def test_renders_arrange_act_and_assert_blocks():
    contract = build_contract(_handle_renewal_facts())
    scaffold = contract["scaffold"]
    assert "const notifier_send_spy = mock(() => undefined);" in scaffold["arrange_block"]
    assert "const channel = __todoValue('value_for_channel');" in scaffold["arrange_block"]
    assert "const expected_value = __todoValue('expected_assertion_value');" in scaffold["arrange_block"]
    assert scaffold["act_block"] == "processRenewal(channel, username, months);"
    assert (
        scaffold["assert_block"]
        == "expect(notifier_send_spy).toHaveBeenCalledWith(channel, expected_value);"
    )


def test_rendered_test_contains_minimal_template():
    contract = build_contract(_handle_renewal_facts())
    rendered = contract["scaffold"]["rendered_test"]
    assert "import { describe, expect, mock, test } from 'bun:test';" in rendered
    assert "describe('processRenewal'" in rendered
    assert "test('TODO behavior'" in rendered
    assert "processRenewal(channel, username, months);" in rendered


def test_bun_scaffold_does_not_emit_cast_only_mock_returns():
    contract = build_contract(_handle_renewal_facts())
    rendered = contract["scaffold"]["rendered_test"]
    assert "mock(() => undefined)" in rendered
    assert "undefined as" not in rendered
    assert "as never" not in rendered


def test_bun_scaffold_uses_valid_todo_values():
    contract = build_contract(_handle_renewal_facts())
    rendered = contract["scaffold"]["rendered_test"]
    assert "const __todoValue = (slot: string) =>" in rendered
    assert "/* TODO */" not in rendered
    assert "/* TODO: channel */" not in rendered


def test_bun_scaffold_uses_first_param_not_hardcoded_channel_for_assertion():
    facts = _handle_renewal_facts()
    facts["target"]["signature"] = "sendAlert(roomId: string, message: string): void"
    contract = build_contract(facts)
    assert (
        contract["scaffold"]["assert_block"]
        == "expect(notifier_send_spy).toHaveBeenCalledWith(roomId, expected_value);"
    )


def test_bun_scaffold_omits_mock_import_when_unused():
    facts = _handle_renewal_facts()
    facts["module_load_dependencies"] = []
    facts["execution_dependencies"] = []
    facts["injection_plan"] = []
    facts["assertion_surface"] = {
        "kind": "return_value",
        "binding": "processRenewal",
        "member": "return",
    }

    rendered = build_contract(facts)["scaffold"]["rendered_test"]

    assert "import { describe, expect, test } from 'bun:test';" in rendered
    assert "import { describe, expect, mock, test } from 'bun:test';" not in rendered


def test_renderer_uses_typescript_signature_parser_for_optional_params():
    facts = _handle_renewal_facts()
    facts["target"]["signature"] = "processRenewal(channel?: string, months: number): void"

    contract = build_contract(facts)
    scaffold = contract["scaffold"]

    assert "const channel = __todoValue('value_for_channel');" in scaffold["arrange_block"]
    assert "const channel? = __todoValue('value_for_channel?');" not in scaffold["arrange_block"]
    assert scaffold["act_block"] == "processRenewal(channel, months);"


class TestPytestScaffoldModuleLoadDeps:
    """pytest scaffold must patch module_load dependencies."""

    def _pytest_facts(self):
        return {
            "target": {
                "symbol": "process",
                "kind": "function",
                "source_path": "src/worker.py",
                "line_start": 5,
                "line_end": 8,
                "signature": "process(item)",
            },
            "test_file": {
                "path": "src/test_process.py",
                "runner": "pytest",
            },
            "pre_test_source_edits": [],
            "module_load_dependencies": [{
                "binding": "logger",
                "origin_kind": "factory_result",
                "source_module": "src.logger",
                "required_shape": {"get_logger": ["info"]},
                "strategy": "mock_module",
            }],
            "execution_dependencies": [{
                "binding": "client",
                "origin_kind": "module_local_mutable",
                "required_shape": {"say": []},
                "strategy": "set_test_seam",
                "observed_members": ["say"],
            }],
            "injection_plan": [{
                "binding": "client",
                "strategy": "set_test_seam",
                "setter_name": "__set_client_for_tests",
                "steps": ["inject a spy for say(channel, message)"],
                "blocks_if_missing": False,
                "seam_available": True,
            }],
            "assertion_surface": {
                "kind": "outbound_call_arguments",
                "binding": "client",
                "member": "say",
                "assertion_shape": "toHaveBeenCalledWith(...)",
            },
            "pattern_files": [],
        }

    def test_pytest_uses_mock_not_dict(self):
        """Execution dep doubles must use Mock() for attribute access, not dict."""
        contract = build_contract(self._pytest_facts())
        rendered = contract["scaffold"]["rendered_test"]
        # Should use Mock() not dict literal for the double
        assert "Mock(" in rendered
        assert "client_test_double = {" not in rendered

    def test_pytest_patches_module_load_deps(self):
        """Module-load deps should be patched in the pytest scaffold."""
        contract = build_contract(self._pytest_facts())
        rendered = contract["scaffold"]["rendered_test"]
        assert "patch" in rendered or "mock" in rendered.lower()

    def test_pytest_imports_after_patches_active(self):
        """Target import must happen after patches are active, not at module level."""
        contract = build_contract(self._pytest_facts())
        rendered = contract["scaffold"]["rendered_test"]
        lines = rendered.splitlines()
        import_line = next(
            (i for i, line in enumerate(lines) if "from src.worker import" in line), None
        )
        def_line = next(
            (i for i, line in enumerate(lines) if line.strip().startswith("def test_")), None
        )
        assert import_line is not None and def_line is not None
        assert import_line > def_line, (
            f"Target import at line {import_line} must be inside the test function "
            f"(after def at line {def_line}), not at module level"
        )

    def test_pytest_spy_defined_for_assertion_surface(self):
        """When assertion targets a module_load dep, the spy variable must exist."""
        facts = self._pytest_facts()
        # Make assertion target the module_load dep instead of execution dep
        facts["assertion_surface"] = {
            "kind": "outbound_call_arguments",
            "binding": "logger",
            "member": "info",
            "assertion_shape": "toHaveBeenCalledWith(...)",
        }
        contract = build_contract(facts)
        rendered = contract["scaffold"]["rendered_test"]
        # The spy must be a named variable, not anonymous inside patch()
        assert "logger_info_spy" in rendered
        # And it must be defined before the assertion
        spy_def = rendered.index("logger_info_spy")
        assert_usage = rendered.index("logger_info_spy.assert_called_with")
        assert spy_def < assert_usage


class TestBunScaffoldSpyBinding:
    """When assertion targets a module_load dep, the spy must be a named variable."""

    def _bun_facts_with_module_assertion(self):
        return {
            "target": {
                "symbol": "process",
                "kind": "function",
                "source_path": "src/service.ts",
                "line_start": 5,
                "line_end": 8,
                "signature": "process()",
            },
            "test_file": {
                "path": "src/service.test.ts",
                "runner": "bun:test",
            },
            "pre_test_source_edits": [],
            "module_load_dependencies": [{
                "binding": "logger",
                "origin_kind": "factory_result",
                "source_module": "../logger",
                "required_shape": {"getLogger": ["event", "response"]},
                "strategy": "mock_module",
            }],
            "execution_dependencies": [],
            "injection_plan": [],
            "assertion_surface": {
                "kind": "outbound_call_arguments",
                "binding": "logger",
                "member": "event",
                "assertion_shape": "toHaveBeenCalledWith(...)",
            },
            "pattern_files": [],
        }

    def test_spy_variable_defined_before_use(self):
        contract = build_contract(self._bun_facts_with_module_assertion())
        rendered = contract["scaffold"]["rendered_test"]
        assert "const logger_event_spy = mock(() =>" in rendered
        # The spy must be defined before the assertion uses it
        spy_pos = rendered.index("const logger_event_spy")
        assert_pos = rendered.index("expect(logger_event_spy)")
        assert spy_pos < assert_pos


class TestParseSignatureParams:
    """_parse_signature_params must handle complex type annotations."""

    def test_simple_params(self):
        assert _parse_signature_params("foo(a, b)") == ["a", "b"]

    def test_typed_params(self):
        assert _parse_signature_params("foo(a: string, b: number)") == ["a", "b"]

    def test_generic_type_params(self):
        result = _parse_signature_params("foo(a: dict[str, int], b: int)")
        assert result == ["a", "b"]

    def test_empty_params(self):
        assert _parse_signature_params("foo()") == []

    def test_python_defaults(self):
        result = _parse_signature_params("foo(a: str, b=10)")
        assert result == ["a", "b"]

    def test_string_default_with_comma(self):
        result = _parse_signature_params("process(message: str = 'a,b')")
        assert result == ["message"]

    def test_double_quote_string_default(self):
        result = _parse_signature_params('process(sep: str = ",", count: int = 1)')
        assert result == ["sep", "count"]

    def test_return_function_type_does_not_overcapture(self):
        assert _parse_signature_params("foo(): (x: number) => void") == []

    def test_rest_params_strip_ellipsis(self):
        assert _parse_signature_params("foo(...args: string[])") == ["args"]

    def test_parenthesis_inside_string_default(self):
        assert _parse_signature_params('foo(pattern: str = ")")') == ["pattern"]
