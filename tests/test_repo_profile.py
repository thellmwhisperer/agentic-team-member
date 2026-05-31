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
        id = "tmi"
        kind = "callback_event"
        module = "tmi.js"
        imports = ["tmi.js"]
        registrations = ["on", "once"]
        dependency_contract = "tmi-events"
        contract_sources = [
          "node_modules/tmi.js/lib/client.js",
          "node_modules/@types/tmi.js/index.d.ts",
        ]

        [[dependency_contracts]]
        id = "tmi-events"
        module = "tmi.js"
        contract_mode = "inline"
        import_specs = ["tmi.js"]
        events = [
          { name = "resub", args = ["channel", "username", "streakMonths", "msg", "tags", "methods"], arg_sources = { streakMonths = "tags['msg-param-streak-months']" } },
        ]

        [[mock_recipes]]
        id = "stream-summary"
        module = "../managers/stream-summary"
        exports = [
          { name = "getStreamSummaryManager", kind = "function_returns_object", members = ["startPeriodicSummaries", "trackResub", "generateFinalSummary"] },
          { name = "StreamSummaryManager", kind = "class", members = ["startPeriodicSummaries", "generateFinalSummary"] },
        ]

        [[import_expectations]]
        module = "tmi.js"
        expected_imports = ["tmi.js"]
        applies_when = "callback_contract"

        [[test_seams]]
        source = "src/twitch/client.ts"
        symbol = "handleMessage"
        preferred = ["extract pure mention detection helper"]
        avoid = ["exporting large runtime handlers only for tests"]
    """)

    profile = read_repo_profile(path)

    assert profile.runner.test_command == "bun test"
    assert profile.event_frameworks[0].module == "tmi.js"
    assert profile.event_frameworks[0].kind == "callback_event"
    assert profile.event_frameworks[0].registrations == ("on", "once")
    assert profile.dependency_contracts[0].contract_mode == "inline"
    assert profile.dependency_contracts[0].events[0].name == "resub"
    assert profile.dependency_contracts[0].events[0].arg_sources == (
        ("streakMonths", "tags['msg-param-streak-months']"),
    )
    assert profile.mock_recipes[0].exports[0].members == (
        "startPeriodicSummaries",
        "trackResub",
        "generateFinalSummary",
    )
    assert profile.import_expectations[0].expected_imports == ("tmi.js",)
    assert profile.test_seams[0].avoid == (
        "exporting large runtime handlers only for tests",
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
        id = "stream-summary"
        module = "src/managers/stream-summary.ts"
        freeform_prompt = "mock this somehow"
    """)

    with pytest.raises(RepoProfileError, match="freeform_prompt"):
        read_repo_profile(path)


def test_rejects_unknown_mock_recipe_reference(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "stream-summary"
        module = "src/managers/stream-summary.ts"
        mock_recipe = "missing"
    """)

    with pytest.raises(RepoProfileError, match="unknown mock_recipe `missing`"):
        read_repo_profile(path)


def test_rejects_unknown_event_framework_dependency_contract(tmp_path):
    path = _write_profile(tmp_path, """\
        [[event_frameworks]]
        id = "tmi"
        kind = "callback_event"
        module = "tmi.js"
        dependency_contract = "missing"
    """)

    with pytest.raises(RepoProfileError, match="unknown dependency_contract `missing`"):
        read_repo_profile(path)


def test_derived_dependency_contract_requires_sources(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "tmi-events"
        module = "tmi.js"
        contract_mode = "derived"
    """)

    with pytest.raises(RepoProfileError, match="requires contract_sources"):
        read_repo_profile(path)


def test_render_facts_filters_by_target_source_imports(tmp_path):
    path = _write_profile(tmp_path, """\
        [[dependency_contracts]]
        id = "stream-summary"
        module = "src/managers/stream-summary.ts"
        import_specs = ["../managers/stream-summary"]
        contract_mode = "inline"
        side_effect = "import_time"
        mock_recipe = "stream-summary"

        [[dependency_contracts]]
        id = "discord"
        module = "src/services/discord.ts"
        import_specs = ["../services/discord"]

        [[mock_recipes]]
        id = "stream-summary"
        module = "../managers/stream-summary"
        exports = [
          { name = "getStreamSummaryManager", kind = "function_returns_object", members = ["trackResub"] },
        ]
    """)
    profile = read_repo_profile(path)

    facts = profile.render_facts(
        source_path="src/twitch/client.ts",
        symbol="handleMessage",
        source_text="import { getStreamSummaryManager } from '../managers/stream-summary';",
    )

    rendered = "\n".join(facts)
    assert "stream-summary" in rendered
    assert "import_time" in rendered
    assert "trackResub" in rendered
    assert "discord" not in rendered


def test_render_facts_supports_inline_event_contracts(tmp_path):
    path = _write_profile(tmp_path, """\
        [[event_frameworks]]
        id = "tmi"
        kind = "callback_event"
        module = "tmi.js"
        imports = ["tmi.js"]
        registrations = ["on", "once"]
        dependency_contract = "tmi-events"

        [[dependency_contracts]]
        id = "tmi-events"
        module = "tmi.js"
        contract_mode = "inline"
        import_specs = ["tmi.js"]
        events = [
          { name = "resub", args = ["channel", "username", "streakMonths"], arg_sources = { streakMonths = "tags['msg-param-streak-months']" } },
        ]
    """)
    profile = read_repo_profile(path)

    facts = profile.render_facts(
        source_text="import tmi from 'tmi.js';\nclient.on('resub', handleResub);",
    )

    rendered = "\n".join(facts)
    assert "event framework `tmi` (callback_event) uses module `tmi.js`" in rendered
    assert "dependency contract `tmi-events`" in rendered
    assert "dependency `tmi-events` declares event `resub(channel, username, streakMonths)`" in rendered
    assert "argument `streakMonths` comes from `tags['msg-param-streak-months']`" in rendered
