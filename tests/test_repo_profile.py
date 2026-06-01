"""Tests for structured per-repo ATM profiles."""
import textwrap

import pytest

from agentic_tdd_runner.repo_profile import (
    RepoProfileError,
    load_repo_profile,
    read_repo_profile,
)


def _write_profile(tmp_path, content):
    path = tmp_path / ".atm" / "profile.toml"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return path


def test_missing_repo_profile_returns_empty_profile(tmp_path):
    profile = load_repo_profile(tmp_path)

    assert profile.is_empty is True
    assert profile.render_facts(source_text="") == []


def test_parses_structured_repo_profile(tmp_path):
    path = _write_profile(tmp_path, """\
        [profile]
        schema_version = "repo-profile.v1"

        [runner]
        test_runner = "bun:test"
        test_command = "bun test"
        typecheck_command = "bun run typecheck"

        [[event_frameworks]]
        id = "job-queue"
        kind = "callback_event"
        module = "@example/job-queue"
        imports = ["@example/job-queue"]
        registrations = ["on", "once"]
        dependency_contract = "job-queue-events"
        contract_sources = [
          "docs/job-queue-events.md",
        ]

        [[dependency_contracts]]
        id = "job-queue-events"
        module = "@example/job-queue"
        contract_mode = "inline"
        import_specs = ["@example/job-queue"]
        events = [
          { name = "job.completed", args = ["jobId", "payload", "metadata"], arg_sources = { payload = "message.payload" } },
        ]

        [[mock_recipes]]
        id = "metrics-reporter"
        module = "../metrics/reporter"
        exports = [
          { name = "getMetricsReporter", kind = "function_returns_object", members = ["start", "record", "flush"] },
          { name = "MetricsReporter", kind = "class", members = ["start", "flush"] },
        ]

        [[import_expectations]]
        module = "@example/job-queue"
        expected_imports = ["@example/job-queue"]
        applies_when = "callback_contract"

        [[test_seams]]
        source = "src/jobs/processor.ts"
        symbol = "processJob"
        preferred = ["extract pure status predicate"]
        avoid = ["exporting orchestration handlers only for tests"]
    """)

    profile = read_repo_profile(path)

    assert profile.runner.test_command == "bun test"
    assert profile.event_frameworks[0].module == "@example/job-queue"
    assert profile.event_frameworks[0].kind == "callback_event"
    assert profile.event_frameworks[0].registrations == ("on", "once")
    assert profile.dependency_contracts[0].contract_mode == "inline"
    assert profile.dependency_contracts[0].events[0].name == "job.completed"
    assert profile.dependency_contracts[0].events[0].arg_sources == (
        ("payload", "message.payload"),
    )
    assert profile.mock_recipes[0].exports[0].members == (
        "start",
        "record",
        "flush",
    )
    assert profile.import_expectations[0].expected_imports == ("@example/job-queue",)
    assert profile.test_seams[0].avoid == (
        "exporting orchestration handlers only for tests",
    )


def test_rejects_unknown_top_level_keys(tmp_path):
    path = _write_profile(tmp_path, """\
        notes = "free-form lore does not belong in the contract"
    """)

    with pytest.raises(RepoProfileError, match="unsupported keys: notes"):
        read_repo_profile(path)


def test_rejects_unknown_section_keys(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "metrics-reporter"
        module = "src/metrics/reporter.ts"
        freeform_prompt = "mock this somehow"
    """)

    with pytest.raises(RepoProfileError, match="freeform_prompt"):
        read_repo_profile(path)


def test_rejects_unknown_mock_recipe_reference(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "metrics-reporter"
        module = "src/metrics/reporter.ts"
        mock_recipe = "missing"
    """)

    with pytest.raises(RepoProfileError, match="unknown mock_recipe `missing`"):
        read_repo_profile(path)


def test_rejects_unknown_event_framework_dependency_contract(tmp_path):
    path = _write_profile(tmp_path, """\
        [[event_frameworks]]
        id = "job-queue"
        kind = "callback_event"
        module = "@example/job-queue"
        dependency_contract = "missing"
    """)

    with pytest.raises(RepoProfileError, match="unknown dependency_contract `missing`"):
        read_repo_profile(path)


def test_derived_dependency_contract_requires_sources(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "job-queue-events"
        module = "@example/job-queue"
        contract_mode = "derived"
    """)

    with pytest.raises(RepoProfileError, match="requires contract_sources"):
        read_repo_profile(path)


def test_render_facts_filters_by_target_source_imports(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "metrics-reporter"
        module = "src/metrics/reporter.ts"
        import_specs = ["../metrics/reporter"]
        contract_mode = "inline"
        side_effect = "import_time"
        mock_recipe = "metrics-reporter"

        [[dependency_contracts]]
        id = "email-service"
        module = "src/services/email.ts"
        import_specs = ["../services/email"]

        [[mock_recipes]]
        id = "metrics-reporter"
        module = "../metrics/reporter"
        exports = [
          { name = "getMetricsReporter", kind = "function_returns_object", members = ["record"] },
        ]
    """)
    profile = read_repo_profile(path)

    facts = profile.render_facts(
        source_path="src/jobs/processor.ts",
        symbol="processJob",
        source_text="import { getMetricsReporter } from '../metrics/reporter';",
    )

    rendered = "\n".join(facts)
    assert "metrics-reporter" in rendered
    assert "import_time" in rendered
    assert "record" in rendered
    assert "email-service" not in rendered


def test_render_facts_supports_inline_event_contracts(tmp_path):
    path = _write_profile(tmp_path, """\
        [[event_frameworks]]
        id = "job-queue"
        kind = "callback_event"
        module = "@example/job-queue"
        imports = ["@example/job-queue"]
        registrations = ["on", "once"]
        dependency_contract = "job-queue-events"

        [[dependency_contracts]]
        id = "job-queue-events"
        module = "@example/job-queue"
        contract_mode = "inline"
        import_specs = ["@example/job-queue"]
        events = [
          { name = "job.completed", args = ["jobId", "payload", "metadata"], arg_sources = { payload = "message.payload" } },
        ]
    """)
    profile = read_repo_profile(path)

    facts = profile.render_facts(
        source_text="import { queue } from '@example/job-queue';\nqueue.on('job.completed', processJob);",
    )

    rendered = "\n".join(facts)
    assert "event framework `job-queue` (callback_event) uses module `@example/job-queue`" in rendered
    assert "dependency contract `job-queue-events`" in rendered
    assert "dependency `job-queue-events` declares event `job.completed(jobId, payload, metadata)`" in rendered
    assert "argument `payload` comes from `message.payload`" in rendered


def test_render_facts_filters_python_source_imports_via_language_capability(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "python-bus"
        module = "src.bus"
        contract_mode = "inline"
        import_specs = ["src.bus"]
        events = [
          { name = "item.ready", args = ["payload"] },
        ]
    """)
    profile = read_repo_profile(path)

    facts = profile.render_facts(
        source_path="src/worker.py",
        source_text="from src.bus import bus\n",
    )

    rendered = "\n".join(facts)
    assert "dependency `python-bus` imports `src.bus`" in rendered
    assert "dependency `python-bus` declares event `item.ready(payload)`" in rendered


def test_render_facts_includes_import_expectations(tmp_path):
    path = _write_profile(tmp_path, """\
        [[import_expectations]]
        module = "@example/job-queue"
        expected_imports = ["@example/job-queue", "@example/job-queue/types"]
        applies_when = "callback_contract"
    """)
    profile = read_repo_profile(path)

    facts = profile.render_facts(source_text="")

    assert facts == [
        "import expectation for module `@example/job-queue`: expected imports: `@example/job-queue`, `@example/job-queue/types`; applies when `callback_contract`.",
    ]
