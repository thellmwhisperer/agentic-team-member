"""Tests for deterministic bug-state review."""

from agentic_tdd_runner.state_reviewer import BugStateReviewer


def test_blocks_dependency_lookup_when_contract_is_already_known():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=True,
    )

    review = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "SubMethods" node_modules/tmi.js'},
        allow_dependency_contract_lookup=False,
    )

    assert review is not None
    assert review.event == "state_review_blocked"
    assert "CONTRACT" in review.message
    assert "already has concrete contract evidence" in review.message


def test_pending_forbidden_pattern_blocks_exploration_until_same_file_is_edited():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/twitch/handleResub.test.ts"},
        (
            "OK: created src/twitch/handleResub.test.ts\n\n"
            "[Reactive forbidden] Potential quality issues detected before DONE:\n"
            "[Forbidden] src/twitch/handleResub.test.ts: 1 forbidden patterns\n"
            "  src/twitch/handleResub.test.ts:110 '{} as'"
        ),
        applied=True,
    )

    blocked = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "SubMethods" node_modules/tmi.js'},
        allow_dependency_contract_lookup=True,
    )
    allowed_edit = reviewer.review_tool_call(
        "str_replace_editor",
        {
            "path": "src/twitch/handleResub.test.ts",
            "old_str": "{} as SubMethods",
            "new_str": "methods",
        },
        allow_dependency_contract_lookup=True,
    )

    assert blocked is not None
    assert "QUALITY_REPAIR" in blocked.message
    assert "src/twitch/handleResub.test.ts" in blocked.message
    assert allowed_edit is None


def test_successful_edit_clears_pending_forbidden_pattern_when_feedback_is_clean():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/twitch/handleResub.test.ts"},
        "[Reactive forbidden]\n  src/twitch/handleResub.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/twitch/handleResub.test.ts"},
        "OK: replaced in src/twitch/handleResub.test.ts",
        applied=True,
    )

    review = reviewer.review_tool_call(
        "rg",
        {"pattern": "SubMethods", "path": "node_modules/tmi.js"},
        allow_dependency_contract_lookup=True,
    )

    assert review is None
