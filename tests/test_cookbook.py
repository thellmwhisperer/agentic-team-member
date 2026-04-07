"""Tests for tdd_cookbook — deterministic cookbook generator for the TDD agent."""
import textwrap

import pytest

from agentic_tdd_runner.cookbook import generate_cookbook


# ── Fixtures ────────────────────────────────────────────────────────


def _write_ts(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return relative_path


# ── Tests ───────────────────────────────────────────────────────────


class TestMinimalTsFunction:
    """Minimal TS function with one import → generates correct mock.module."""

    def test_contains_mock_module_block(self, tmp_path):
        _write_ts(tmp_path, "src/utils.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function greet(name: string): void {
              logger.info(`Hello ${name}`);
            }
        """)
        result = generate_cookbook("src/utils.ts", "greet", str(tmp_path))
        assert "mock.module(" in result
        assert "../logger" in result

    def test_contains_test_scaffold(self, tmp_path):
        _write_ts(tmp_path, "src/utils.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function greet(name: string): void {
              logger.info(`Hello ${name}`);
            }
        """)
        result = generate_cookbook("src/utils.ts", "greet", str(tmp_path))
        assert "describe(" in result or "test(" in result
        assert "greet" in result

    def test_contains_import_path(self, tmp_path):
        _write_ts(tmp_path, "src/utils.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function greet(name: string): void {
              logger.info(`Hello ${name}`);
            }
        """)
        result = generate_cookbook("src/utils.ts", "greet", str(tmp_path))
        # Test file at src/utils.test.ts imports from ./utils
        assert "./utils" in result or "../utils" in result or "src/utils" in result


class TestUnexportedFunction:
    """Non-exported function → cookbook includes export instruction."""

    def test_includes_export_edit(self, tmp_path):
        _write_ts(tmp_path, "src/internal.ts", """\
            function secret(): string {
              return 'hidden';
            }
        """)
        result = generate_cookbook("src/internal.ts", "secret", str(tmp_path))
        assert "export" in result
        assert "function secret" in result


class TestFactoryDependency:
    """Factory pattern: const logger = getLogger() → mock with factory shape."""

    def test_mock_has_factory_shape(self, tmp_path):
        _write_ts(tmp_path, "src/service.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function process(): void {
              logger.event('start');
              logger.response('ok', 'done');
            }
        """)
        result = generate_cookbook("src/service.ts", "process", str(tmp_path))
        assert "mock.module(" in result
        assert "getLogger" in result
        # Factory shape: getLogger returns object with observed members
        assert "event" in result
        assert "response" in result


class TestModuleLocalMutable:
    """Module-local mutable binding (let client) → test seam setter."""

    def test_includes_seam_setter(self, tmp_path):
        _write_ts(tmp_path, "src/bot.ts", """\
            let client: any;

            export function sendMessage(channel: string, msg: string): void {
              client.say(channel, msg);
            }
        """)
        result = generate_cookbook("src/bot.ts", "sendMessage", str(tmp_path))
        # Should mention test seam or setter for client
        assert "client" in result
        assert "set" in result.lower() or "seam" in result.lower() or "inject" in result.lower()


class TestAssertionSurfaceScoring:
    """Assertion surface: say (120) > info (10) > get (-10)."""

    def test_picks_say_over_info(self, tmp_path):
        _write_ts(tmp_path, "src/handler.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();
            let client: any;

            export function handle(channel: string): void {
              logger.info('handling');
              client.say(channel, 'hello');
            }
        """)
        result = generate_cookbook("src/handler.ts", "handle", str(tmp_path))
        # Assertion should be on client.say, not logger.info
        assert "say" in result
        assert "toHaveBeenCalledWith" in result or "assert_called" in result


class TestPythonSource:
    """Python source → generates pytest scaffold, not bun:test."""

    def test_uses_pytest(self, tmp_path):
        _write_ts(tmp_path, "src/worker.py", """\
            from src.logger import get_logger

            logger = get_logger()

            def process_item(item):
                logger.info(f"Processing {item}")
                return item.upper()
        """)
        result = generate_cookbook("src/worker.py", "process_item", str(tmp_path))
        assert "def test_" in result or "pytest" in result.lower()
        assert "mock.module(" not in result  # bun-only


class TestNoDependencies:
    """Pure function with no deps → clean scaffold, no mocks section."""

    def test_no_mock_blocks(self, tmp_path):
        _write_ts(tmp_path, "src/math.ts", """\
            export function add(a: number, b: number): number {
              return a + b;
            }
        """)
        result = generate_cookbook("src/math.ts", "add", str(tmp_path))
        assert "mock.module(" not in result
        assert "add" in result
        assert "describe(" in result or "test(" in result


class TestAssertionPrefersReturnOverParamCall:
    """When a function returns a value, prefer return_value over spying on param methods."""

    def test_return_value_over_param_method(self, tmp_path):
        _write_ts(tmp_path, "src/utils.ts", """\
            export function process(item: string): string {
              return item.toUpperCase();
            }
        """)
        result = generate_cookbook("src/utils.ts", "process", str(tmp_path))
        # Should NOT generate a spy on item.toUpperCase
        assert "item_toUpperCase_spy" not in result
        # Should use return value assertion
        assert "result" in result.lower() or "return" in result.lower() or "toBe" in result


class TestDiscoverDependenciesExcludesTarget:
    """_discover_dependencies must not treat the target symbol as a dependency."""

    def test_const_export_not_treated_as_seam(self, tmp_path):
        _write_ts(tmp_path, "src/handler.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export const process = (name: string): string => {
              logger.info(name);
              return name.toUpperCase();
            };
        """)
        result = generate_cookbook("src/handler.ts", "process", str(tmp_path))
        assert "__setProcessForTests" not in result


class TestPythonClassMethod:
    """Class methods should be identified and the class name referenced."""

    def test_class_method_references_class(self, tmp_path):
        _write_ts(tmp_path, "src/calc.py", """\
            class Calculator:
                def add(self, a, b):
                    return a + b
        """)
        result = generate_cookbook("src/calc.py", "add", str(tmp_path))
        # Must reference the owning class
        assert "Calculator" in result
        # Must indicate it's a method, not a standalone function
        assert "method" in result.lower()


class TestArrowFunctionSignature:
    """Arrow function exports must have their signature detected."""

    def test_arrow_function_params_in_scaffold(self, tmp_path):
        _write_ts(tmp_path, "src/handler.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export const process = (name: string, age: number): string => {
              logger.info(name);
              return name.toUpperCase();
            };
        """)
        result = generate_cookbook("src/handler.ts", "process", str(tmp_path))
        # Scaffold should have the params, not just process()
        assert "name" in result
        assert "age" in result


class TestSingleParamArrowFunction:
    """Single-param arrow functions without parens must be detected."""

    def test_single_param_arrow_detected(self, tmp_path):
        _write_ts(tmp_path, "src/utils.ts", """\
            export const double = x => x * 2;
        """)
        result = generate_cookbook("src/utils.ts", "double", str(tmp_path))
        assert "x" in result
        assert "double" in result


class TestPythonReturnValueScaffold:
    """Python scaffold must not produce 'result = result = fn()'."""

    def test_no_double_assignment(self, tmp_path):
        _write_ts(tmp_path, "src/worker.py", """\
            def process(item):
                return item.upper()
        """)
        result = generate_cookbook("src/worker.py", "process", str(tmp_path))
        assert "result = result =" not in result


class TestFindFunctionEndPython:
    """_find_function_end correctly bounds class methods, not just top-level."""

    def test_class_method_stops_at_next_sibling(self, tmp_path):
        _write_ts(tmp_path, "src/calc.py", """\
            class Calculator:
                def add(self, a, b):
                    return a + b

                def subtract(self, a, b):
                    return a - b
        """)
        from agentic_tdd_runner.cookbook import _find_function_end
        source = (tmp_path / "src/calc.py").read_text()
        # add starts at line 2, subtract at line 5
        end = _find_function_end(source, 2)
        assert end is not None
        assert end <= 5  # must stop before or at subtract


class TestBuildSystemPrompt:
    """build_system_prompt injects cookbook into the base prompt."""

    def test_injects_cookbook_section(self, tmp_path):
        from agentic_tdd_runner.cookbook import build_system_prompt

        _write_ts(tmp_path, "src/service.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function process(): void {
              logger.event('start');
            }
        """)
        base = "You are a senior software engineer."
        issue = "Fix the bug in process()"
        prompt = build_system_prompt(
            base_prompt=base,
            issue_text=issue,
            source_path="src/service.ts",
            symbol="process",
            project_root=str(tmp_path),
        )
        # Base prompt preserved
        assert "senior software engineer" in prompt
        # Cookbook injected
        assert "Mock Cookbook" in prompt
        assert "mock.module(" in prompt
        assert "../logger" in prompt

    def test_returns_base_when_no_source(self):
        from agentic_tdd_runner.cookbook import build_system_prompt

        base = "You are a senior software engineer."
        prompt = build_system_prompt(
            base_prompt=base,
            issue_text="Fix the bug",
        )
        assert prompt == base
        assert "Mock Cookbook" not in prompt
