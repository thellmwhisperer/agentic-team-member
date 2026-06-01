"""Tests for prompt construction helpers."""

from agentic_tdd_runner.prompts import (
    build_initial_messages,
    build_phase1_message,
    build_system_prompt,
    function_line_hint,
)


def _episode(**overrides):
    episode = {
        "source_file": "src/client.ts",
        "target_symbol": "processRenewal",
        "test_file": "src/client.test.ts",
        "function_line_range": {"start": 12, "end": 34, "source": "definition"},
        "cookbook_text": "## Cookbook\nUse real imports.\n",
    }
    episode.update(overrides)
    return episode


def test_build_initial_messages_without_episode_uses_generic_bug_prompt():
    messages = build_initial_messages(
        base_system_prompt="  system prompt  ",
        issue_text_for_model="bug text",
    )

    assert messages == [
        {"role": "system", "content": "system prompt"},
        {"role": "user", "content": "Fix this bug:\n\nbug text"},
    ]


def test_build_initial_messages_with_episode_injects_cookbook_and_phase1_prompt():
    messages = build_initial_messages(
        base_system_prompt="system prompt",
        issue_text_for_model="bug text",
        episode=_episode(),
    )

    assert messages[0]["content"] == "system prompt\n\n## Cookbook\nUse real imports.\n"
    assert "Read src/client.ts" in messages[1]["content"]
    assert "Start with `processRenewal` (lines 12-34) as a discovery hypothesis" in messages[1]["content"]
    assert "src/client.test.ts" in messages[1]["content"]
    assert "Bug:\nbug text" in messages[1]["content"]


def test_build_initial_messages_with_episode_injects_runner_facts():
    messages = build_initial_messages(
        base_system_prompt="system prompt",
        issue_text_for_model="bug text",
        episode=_episode(runner_facts_text="## Runner Facts\n- test runner: bun:test"),
    )

    assert messages[0]["content"] == (
        "system prompt\n\n"
        "## Cookbook\nUse real imports.\n"
        "\n\n"
        "## Runner Facts\n- test runner: bun:test"
    )


def test_build_initial_messages_prepends_recon_before_target_cookbook():
    messages = build_initial_messages(
        base_system_prompt="system prompt",
        issue_text_for_model="bug text",
        episode=_episode(
            recon_cookbook_text="## Recon Cookbook\nTrace consumers first.\n",
        ),
    )

    assert messages[0]["content"] == (
        "system prompt\n\n"
        "## Recon Cookbook\nTrace consumers first.\n"
        "\n\n"
        "## Cookbook\nUse real imports.\n"
    )


def test_function_line_hint_only_uses_definition_ranges():
    assert function_line_hint(_episode()) == " (lines 12-34)"
    assert function_line_hint(_episode(function_line_range={"start": 1, "end": 2, "source": "fallback"})) == ""
    assert function_line_hint(_episode(function_line_range={"start": None, "end": 2, "source": "definition"})) == ""
    assert function_line_hint(_episode(function_line_range=None)) == ""


def test_build_phase1_message_omits_fallback_line_hint():
    msg = build_phase1_message(
        _episode(function_line_range={"start": 1, "end": 2, "source": "fallback"}),
        "bug text",
    )

    assert "lines 1-2" not in msg
    assert "Start with `processRenewal` as a discovery hypothesis" in msg


def test_build_phase1_message_includes_ranked_candidates_when_available():
    msg = build_phase1_message(
        _episode(discovery_candidates=[
            {"source_path": "src/report.ts", "symbol": "loadLatestYouTube", "score": 31},
            {"source_path": "src/providers/youtube.ts", "symbol": "fetchYouTubeData", "score": 28},
        ]),
        "retry API calls",
    )

    assert "ranked hypotheses, not ground truth" in msg
    assert "src/report.ts::loadLatestYouTube score=31" in msg
    assert "src/providers/youtube.ts::fetchYouTubeData score=28" in msg
    assert "only reads local files" in msg


def test_build_system_prompt_without_episode_strips_base_prompt():
    assert build_system_prompt("  system prompt\n") == "system prompt"


def test_build_system_prompt_injects_permission_instructions_only_when_enabled():
    disabled = build_system_prompt("system prompt", permission_driven=False)
    enabled = build_system_prompt("system prompt", permission_driven=True)

    assert "Permission-Driven Mode" not in disabled
    assert "ask_harness" not in disabled
    assert "Permission-Driven Mode" in enabled
    assert "ask_harness" in enabled
