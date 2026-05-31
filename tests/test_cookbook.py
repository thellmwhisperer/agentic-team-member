"""Tests for tdd_cookbook — deterministic cookbook generator for the TDD agent."""
import textwrap

import pytest

from agentic_tdd_runner.cookbook import generate_cookbook


# ── Fixtures ────────────────────────────────────────────────────────


def _write_file(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return relative_path


# ── Tests ───────────────────────────────────────────────────────────


class TestMinimalTsFunction:
    """Minimal TS function with one import → generates correct mock.module."""

    def test_contains_mock_module_block(self, tmp_path):
        _write_file(tmp_path, "src/utils.ts", """\
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
        _write_file(tmp_path, "src/utils.ts", """\
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
        _write_file(tmp_path, "src/utils.ts", """\
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
        _write_file(tmp_path, "src/internal.ts", """\
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
        _write_file(tmp_path, "src/service.ts", """\
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


class TestBunMockHygiene:
    """Bun cookbook examples should not teach cast-only mock returns."""

    def test_uses_void_spies_without_cast_returns(self, tmp_path):
        _write_file(tmp_path, "src/service.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function process(): void {
              logger.info('start');
            }
        """)
        result = generate_cookbook("src/service.ts", "process", str(tmp_path))

        assert "mock(() => undefined)" in result
        assert "undefined as" not in result
        assert "as never" not in result
        assert "cast-only returns" in result


class TestModuleLocalMutable:
    """Module-local mutable binding -> test seam setter."""

    def test_includes_seam_setter(self, tmp_path):
        _write_file(tmp_path, "src/notifier.ts", """\
            let transport: any;

            export function sendMessage(channel: string, msg: string): void {
              transport.send(channel, msg);
            }
        """)
        result = generate_cookbook("src/notifier.ts", "sendMessage", str(tmp_path))
        assert "transport" in result
        assert "set" in result.lower() or "seam" in result.lower() or "inject" in result.lower()


class TestAssertionSurfaceScoring:
    """Assertion surface: say (120) > info (10) > get (-10)."""

    def test_picks_say_over_info(self, tmp_path):
        _write_file(tmp_path, "src/handler.ts", """\
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
        _write_file(tmp_path, "src/worker.py", """\
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
        _write_file(tmp_path, "src/math.ts", """\
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
        _write_file(tmp_path, "src/utils.ts", """\
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
        _write_file(tmp_path, "src/handler.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export const process = (name: string): string => {
              logger.info(name);
              return name.toUpperCase();
            };
        """)
        result = generate_cookbook("src/handler.ts", "process", str(tmp_path))
        assert "__setProcessForTests" not in result


class TestDirectCallableScaffold:
    """Direct callable (send(item)) must produce a scaffold with the spy defined."""

    def test_spy_variable_defined(self, tmp_path):
        _write_file(tmp_path, "src/notifier.py", """\
            from alerts import send

            def process(item):
                send(item)
        """)
        result = generate_cookbook("src/notifier.py", "process", str(tmp_path))
        assert "send_spy" in result
        # spy must be defined before assertion
        lines = result.splitlines()
        def_lines = [i for i, l in enumerate(lines) if "send_spy" in l and "=" in l and "assert" not in l.lower()]
        use_lines = [i for i, l in enumerate(lines) if "send_spy" in l and ("assert" in l.lower() or "expect" in l.lower())]
        assert def_lines, "send_spy must be defined"
        assert use_lines, "send_spy must be used in an assertion"
        assert def_lines[0] < use_lines[0], "spy definition must precede assertion"


class TestDirectImportWithMembers:
    """from pkg import logger; logger.info(x) — patch shape must be correct."""

    def test_patches_module_not_member(self, tmp_path):
        _write_file(tmp_path, "src/worker.py", """\
            from pkg import logger

            def process(item):
                logger.info(item)
                return item.upper()
        """)
        result = generate_cookbook("src/worker.py", "process", str(tmp_path))
        # Should NOT patch individual members like patch('pkg.info')
        assert "patch('pkg.info'" not in result
        # Should patch the object: patch('pkg.logger', ...)
        assert "patch('pkg.logger'" in result

    def test_direct_import_object_not_wrapped_as_factory(self, tmp_path):
        """Direct import (from pkg import logger) must patch with Mock(info=spy),
        not Mock(return_value=Mock(info=spy)) which is for factories."""
        _write_file(tmp_path, "src/worker.py", """\
            from pkg import logger

            def process(item):
                logger.info(item)
                return item.upper()
        """)
        result = generate_cookbook("src/worker.py", "process", str(tmp_path))
        # Must NOT use return_value — logger is an object, not a factory
        assert "return_value" not in result
        # Must use direct Mock(info=spy)
        assert "Mock(info=logger_info_spy)" in result


class TestVarExportMechanical:
    """var declarations must get mechanical export like const and let."""

    def test_var_target_gets_export(self, tmp_path):
        _write_file(tmp_path, "src/handler.ts", """\
            var process = function(item) {
              return item;
            };
        """)
        result = generate_cookbook("src/handler.ts", "process", str(tmp_path))
        assert "export" in result


class TestPythonClassMethod:
    """Class methods should be identified and the class name referenced."""

    def test_class_method_references_class(self, tmp_path):
        _write_file(tmp_path, "src/calc.py", """\
            class Calculator:
                def add(self, a, b):
                    return a + b
        """)
        result = generate_cookbook("src/calc.py", "add", str(tmp_path))
        assert "Calculator" in result
        assert "method" in result.lower()

    def test_scaffold_imports_class_not_method(self, tmp_path):
        _write_file(tmp_path, "src/calc.py", """\
            class Calculator:
                def add(self, a, b):
                    return a + b
        """)
        result = generate_cookbook("src/calc.py", "add", str(tmp_path))
        # Scaffold must import the class, not the bare method
        assert "import Calculator" in result, "scaffold must import the class"
        assert "import add" not in result, "scaffold must not import the bare method"
        # Scaffold must instantiate the class
        assert "Calculator()" in result


class TestArrowFunctionSignature:
    """Arrow function exports must have their signature detected."""

    def test_arrow_function_params_in_scaffold(self, tmp_path):
        _write_file(tmp_path, "src/handler.ts", """\
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


class TestModuleLevelDepsIncluded:
    """Cookbook must include module-level deps that run on import, not just function deps."""

    def test_includes_factory_calls_at_module_level(self, tmp_path):
        _write_file(tmp_path, "src/service.ts", """\
            import { env } from '../env';
            import { getLogger } from '../logger';
            import { getTokenManager } from './token';
            import { AIService } from '../ai';

            const logger = getLogger();
            const tokenManager = getTokenManager();

            export function process(): void {
              logger.event('start');
            }
        """)
        result = generate_cookbook("src/service.ts", "process", str(tmp_path))
        # Factory calls at module level need mocking (they execute on import)
        assert "../logger" in result  # used in function + factory call
        assert "./token" in result    # factory call at module level
        # Static imports without factory calls don't need mocking
        assert "../ai" not in result  # just a class import, no side effect


class TestMultiLineSignatureDetection:
    """TS functions with multi-line signatures must be fully captured."""

    def test_multiline_signature_captures_body(self, tmp_path):
        _write_file(tmp_path, "src/handler.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function processRenewal(
              channel: string,
              username: string,
              months: number,
            ): void {
              logger.event('renewal', { username, months });
              const response = 'hello';
            }
        """)
        result = generate_cookbook("src/handler.ts", "processRenewal", str(tmp_path))
        assert "mock.module(" in result
        assert "../logger" in result
        assert "logger" in result


class TestCookbookGuardrails:
    """Cookbook must warn the agent not to invent a new callable contract."""

    def test_warns_to_preserve_runtime_signature_before_testing(self, tmp_path):
        _write_file(tmp_path, "src/handler.ts", """\
            import eventBus from '@example/event-bus';

            let client: EventBusClient;

            client.on('renewal', processRenewal);

            function processRenewal(channel: string, username: string, months: number): void {
              client.say(channel, `${username} lleva ${months} meses`);
            }
        """)
        result = generate_cookbook("src/handler.ts", "processRenewal", str(tmp_path))
        assert "Do not change the target's runtime signature just to fit the test scaffold." in result
        assert "Write the first failing test against the real callable contract from source." in result
        assert "For callbacks, handlers, and framework listeners: preserve the production contract" in result


class TestSingleParamArrowFunction:
    """Single-param arrow functions without parens must be detected."""

    def test_single_param_arrow_detected(self, tmp_path):
        _write_file(tmp_path, "src/utils.ts", """\
            export const double = x => x * 2;
        """)
        result = generate_cookbook("src/utils.ts", "double", str(tmp_path))
        assert "x" in result
        assert "double" in result


class TestPythonReturnValueScaffold:
    """Python scaffold must not produce 'result = result = fn()'."""

    def test_no_double_assignment(self, tmp_path):
        _write_file(tmp_path, "src/worker.py", """\
            def process(item):
                return item.upper()
        """)
        result = generate_cookbook("src/worker.py", "process", str(tmp_path))
        assert "result = result =" not in result


class TestFindFunctionEndPython:
    """_find_function_end correctly bounds class methods, not just top-level."""

    def test_class_method_stops_at_next_sibling(self, tmp_path):
        _write_file(tmp_path, "src/calc.py", """\
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

        _write_file(tmp_path, "src/service.ts", """\
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


class TestBuildEpisodeContext:
    """build_episode_context returns structured data for phased runner."""

    def test_returns_mocks_text_and_paths(self, tmp_path):
        from agentic_tdd_runner.cookbook import build_episode_context

        _write_file(tmp_path, "src/handler.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            function handle(name: string): void {
              logger.info(name);
            }
        """)
        ctx = build_episode_context("src/handler.ts", "handle", str(tmp_path))
        assert ctx["source_file"] == "src/handler.ts"
        assert ctx["target_symbol"] == "handle"
        assert "test" in ctx["test_file"]
        assert "handle" in ctx["test_file"]
        assert "../logger" in ctx["mocks_text"]
        assert "mock.module(" in ctx["mocks_text"]

    def test_includes_pre_test_source_edits_for_unexported(self, tmp_path):
        from agentic_tdd_runner.cookbook import build_episode_context

        _write_file(tmp_path, "src/worker.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            function process(item: string): void {
              logger.info(item);
            }
        """)
        ctx = build_episode_context("src/worker.ts", "process", str(tmp_path))
        edits = ctx["pre_test_source_edits"]
        assert len(edits) >= 1
        assert any("export" in e["new"] for e in edits)

    def test_typescript_does_not_generate_production_test_seam_setters(self, tmp_path):
        from agentic_tdd_runner.cookbook import build_episode_context

        _write_file(tmp_path, "src/handler.ts", """\
            import eventBus from '@example/event-bus';

            let client: EventBusClient;

            function handle(channel: string): void {
              client.say(channel, 'ok');
            }
        """)
        ctx = build_episode_context("src/handler.ts", "handle", str(tmp_path))
        edit_text = "\n".join(edit["new"] for edit in ctx["pre_test_source_edits"])

        assert all(edit["kind"] != "mechanical_test_seam" for edit in ctx["pre_test_source_edits"])
        assert "__set" not in edit_text
        assert "### Test Seams" not in ctx["cookbook_text"]
        assert "Prefer a public caller/registration path, a module mock, or the smallest pure helper" in ctx["cookbook_text"]

    def test_includes_assertion_hint(self, tmp_path):
        from agentic_tdd_runner.cookbook import build_episode_context

        _write_file(tmp_path, "src/notifier.ts", """\
            import { getLogger } from '../logger';

            const logger = getLogger();

            export function notify(msg: string): void {
              logger.info(msg);
            }
        """)
        ctx = build_episode_context("src/notifier.ts", "notify", str(tmp_path))
        assert "assertion_hint" in ctx
        assert ctx["assertion_hint"]  # non-empty

    def test_episode_exposes_function_line_range(self, tmp_path):
        """Large repos (1500-line server.py) waste steps locating the symbol
        with sed/grep. Exposing the AST-resolved range lets the runner inject
        a precise location hint into the agent's first message.
        """
        from agentic_tdd_runner.cookbook import build_episode_context

        _write_file(tmp_path, "src/worker.py", """\
            # filler line 1
            # filler line 2

            def helper():
                return 1

            def process(item):
                # body
                return item.upper()
        """)
        ctx = build_episode_context("src/worker.py", "process", str(tmp_path))
        rng = ctx["function_line_range"]
        assert rng["start"] >= 7  # process starts after helper
        assert rng["end"] >= rng["start"]
        # Python `def` is a real definition match → source is "definition".
        assert rng["source"] == "definition"

    def test_function_line_range_marks_fallback_for_ts_class_method(self, tmp_path):
        """The parser only recognizes `function`, `const/let/var`, and `def`.
        TS/JS class methods fall back to the first textual occurrence of the
        symbol — which might be a comment or call site, not the definition.
        The episode must mark that case so the agent prompt can avoid emitting
        a misleading line hint."""
        from agentic_tdd_runner.cookbook import build_episode_context

        _write_file(tmp_path, "src/client.ts", """\
            // processRenewal: see related comment in callsite
            export class Client {
              callsite() { this.processRenewal(); }
              processRenewal(event: string) {
                return event;
              }
            }
        """)
        ctx = build_episode_context("src/client.ts", "processRenewal", str(tmp_path))
        assert ctx["function_line_range"]["source"] == "fallback"


class TestRegressionScopeGuidance:
    """Cookbook should bound test count without making the agent myopic."""

    def test_includes_grounded_extra_test_policy(self, tmp_path):
        _write_file(tmp_path, "src/math.ts", """\
            export function chooseMonths(streak: number, cumulative: number): number {
              return streak;
            }
        """)
        result = generate_cookbook("src/math.ts", "chooseMonths", str(tmp_path))

        assert "Write one focused regression test first" in result
        assert "Add up to two evidence-backed extra tests" in result
        assert "Do not invent domain edge cases" in result

    def test_includes_contrastive_fixture_policy(self, tmp_path):
        _write_file(tmp_path, "src/math.ts", """\
            export function chooseMonths(streak: number, cumulative: number): number {
              return streak;
            }
        """)
        result = generate_cookbook("src/math.ts", "chooseMonths", str(tmp_path))

        assert "contrastive fixtures" in result
        assert "issue or source shows competing inputs" in result
        assert "issue-grounded competing values" in result


class TestCallbackContractGuidance:
    """Callback targets should carry deterministic registration evidence."""

    def test_includes_callback_registration_evidence(self, tmp_path):
        _write_file(tmp_path, "src/events/processor.ts", """\
            import { createQueue } from '@example/job-queue';

            const queue = createQueue();

            function processJob(jobId: string, payload: unknown): void {
              queue.ack(jobId, payload);
            }

            queue.on('job.completed', processJob);
        """)
        result = generate_cookbook("src/events/processor.ts", "processJob", str(tmp_path))

        assert "### Callback Contract Evidence" in result
        assert "issue-provided callback contract" in result
        assert "Do not invent callback parameters" in result
        assert "queue.on('job.completed', processJob)" in result
        assert "TODO behavior" not in result
        assert "__todoValue" not in result

    def test_derives_callback_contract_facts_from_repo_profile(self, tmp_path):
        _write_file(tmp_path, ".atm/profile.toml", """\
            [profile]
            schema_version = "repo-profile.v1"

            [[event_frameworks]]
            id = "job-queue"
            kind = "callback_event"
            module = "@example/job-queue"
            imports = ["@example/job-queue"]
            registrations = ["on", "once"]
            dependency_contract = "job-events"

            [[dependency_contracts]]
            id = "job-events"
            module = "@example/job-queue"
            contract_mode = "inline"
            import_specs = ["@example/job-queue"]
            events = [
              { name = "job.completed", args = ["jobId", "payload", "metadata"], arg_sources = { payload = "message.payload" } },
            ]
        """)
        _write_file(tmp_path, "src/events/processor.ts", """\
            import { createQueue } from '@example/job-queue';

            const queue = createQueue();

            function processJob(jobId: string, payload: unknown): void {
              queue.ack(jobId, payload);
            }

            queue.on('job.completed', processJob);
        """)

        result = generate_cookbook("src/events/processor.ts", "processJob", str(tmp_path))

        assert "@example/job-queue source emits `job.completed(jobId, payload, metadata)`" in result
        assert "Argument 2 is `payload`, derived from `message.payload`." in result
        assert "Argument 1 is `jobId`" not in result
        assert "Argument 3 is `metadata`" not in result

    def test_derives_profile_contract_facts_for_detected_event_not_literal(self, tmp_path):
        _write_file(tmp_path, ".atm/profile.toml", """\
            [profile]
            schema_version = "repo-profile.v1"

            [[event_frameworks]]
            id = "event-bus"
            kind = "callback_event"
            module = "@example/event-bus"
            imports = ["@example/event-bus"]
            registrations = ["on"]
            dependency_contract = "event-bus-events"

            [[dependency_contracts]]
            id = "event-bus-events"
            module = "@example/event-bus"
            contract_mode = "inline"
            import_specs = ["@example/event-bus"]
            events = [
              { name = "alert.created", args = ["alertId: string", "metadata: AlertMetadata", "message: string"], arg_sources = { metadata = "event.metadata" } },
            ]
        """)
        _write_file(tmp_path, "src/events/alerts.ts", """\
            import { createBus } from '@example/event-bus';

            const bus = createBus();

            function handleAlert(alertId: string, metadata: AlertMetadata, message: string): void {
              bus.publish(alertId, message);
            }

            bus.on('alert.created', handleAlert);
        """)

        result = generate_cookbook("src/events/alerts.ts", "handleAlert", str(tmp_path))

        assert "bus.on('alert.created', handleAlert)" in result
        assert "@example/event-bus source emits `alert.created(alertId, metadata, message)`" in result
        assert "@example/event-bus type declarations expose `alert.created(alertId: string, metadata: AlertMetadata, message: string)`" in result
        assert "Argument 2 is `metadata`, derived from `event.metadata`." in result

    def test_callback_guidance_preserves_types_and_fallbacks(self, tmp_path):
        _write_file(tmp_path, "src/events.ts", """\
            const bus = { on(_event: string, _handler: unknown) {} };

            function handleEvent(count: number): number {
              return count;
            }

            bus.on('event', handleEvent);
        """)
        result = generate_cookbook("src/events.ts", "handleEvent", str(tmp_path))

        assert "use exported framework types or overloads" in result
        assert "empty-object casts" in result
        assert "preserve fallback values only when the real contract requires them" in result
        assert "Only inspect dependency type files" in result
        assert "node_modules/@types" not in result
        assert "metadata fallback" not in result


class TestRepoProfileGuidance:
    """Cookbook should render stable repo-profile facts without hardcoding them."""

    def test_includes_profile_mock_recipe_for_import_time_dependency(self, tmp_path):
        _write_file(tmp_path, ".atm/profile.toml", """\
            [[dependency_contracts]]
            id = "metrics-reporter"
            module = "src/metrics/reporter.ts"
            contract_mode = "inline"
            import_specs = ["../metrics/reporter"]
            side_effect = "import_time"
            reason = "opens reporter connection at import time"
            mock_recipe = "metrics-reporter"

            [[mock_recipes]]
            id = "metrics-reporter"
            module = "../metrics/reporter"
            exports = [
              { name = "getMetricsReporter", kind = "function_returns_object", members = ["start", "record", "flush"] },
              { name = "MetricsReporter", kind = "class", members = ["start", "flush"] },
            ]

            [[import_expectations]]
            module = "../metrics/reporter"
            expected_imports = ["../metrics/reporter"]
            applies_when = "import_time_dependency"
        """)
        _write_file(tmp_path, "src/jobs/processor.ts", """\
            import { getMetricsReporter } from '../metrics/reporter';

            const reporter = getMetricsReporter();

            export function processJob(jobId: string): void {
              reporter.record(jobId);
            }
        """)

        result = generate_cookbook("src/jobs/processor.ts", "processJob", str(tmp_path))

        assert "### Repo Profile Facts" in result
        assert "dependency `metrics-reporter` imports `src/metrics/reporter.ts`" in result
        assert "contract mode `inline`" in result
        assert "side effect `import_time`" in result
        assert "mock `../metrics/reporter` before importing the target" in result
        assert "getMetricsReporter (function_returns_object: start, record, flush)" in result
        assert "import expectation for module `../metrics/reporter`" in result

    def test_includes_profile_declared_event_framework(self, tmp_path):
        _write_file(tmp_path, ".atm/profile.toml", """\
            [[event_frameworks]]
            id = "event-bus"
            kind = "callback_event"
            module = "@example/event-bus"
            imports = ["@example/event-bus"]
            registrations = ["on", "subscribe"]
            contract_sources = ["docs/event-bus.md"]
        """)
        _write_file(tmp_path, "src/events.ts", """\
            import { bus } from '@example/event-bus';

            export function handleEvent(message: string): string {
              return message.trim();
            }

            bus.subscribe('message', handleEvent);
        """)

        result = generate_cookbook("src/events.ts", "handleEvent", str(tmp_path))

        assert "event framework `event-bus` (callback_event) uses module `@example/event-bus`" in result
        assert "registrations: on, subscribe" in result
        assert "contract sources: docs/event-bus.md" in result

    def test_repo_profile_facts_are_not_silently_truncated(self, tmp_path):
        expectations = "\n".join(
            f"""
            [[import_expectations]]
            module = "pkg-{index}"
            expected_imports = ["pkg-{index}"]
            """
            for index in range(14)
        )
        _write_file(tmp_path, ".atm/profile.toml", expectations)
        _write_file(tmp_path, "src/events.ts", """\
            export function handleEvent(message: string): string {
              return message.trim();
            }
        """)

        result = generate_cookbook("src/events.ts", "handleEvent", str(tmp_path))

        assert "import expectation for module `pkg-0`" in result
        assert "import expectation for module `pkg-13`" in result
