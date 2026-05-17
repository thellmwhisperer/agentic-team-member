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
        "target_symbol": "handleResub",
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
    assert "Focus on the function `handleResub` (lines 12-34)" in messages[1]["content"]
    assert "src/client.test.ts" in messages[1]["content"]
    assert "Bug:\nbug text" in messages[1]["content"]


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
    assert "Focus on the function `handleResub`." in msg


def test_build_system_prompt_without_episode_strips_base_prompt():
    assert build_system_prompt("  system prompt\n") == "system prompt"
