"""Tests for ATM-formatted issue intake."""

from agentic_tdd_runner.issue_intake import parse_issue_contract


def test_drops_forbidden_any_inside_fix_approach():
    issue = """Bug: handleResub reports 0 months

## Symptom
The bot reports 0 months.

## Fix approach
1. Add `message: string` and `userstate: any` params to the signature
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "userstate: any" not in contract.model_text
    assert "dropped Fix approach before prompting because it contained forbidden guidance" in contract.warnings


def test_rejects_forbidden_any_in_model_facing_section():
    issue = """Bug: handleResub reports 0 months

## Expected behavior
Use `userstate: any` and report 6 months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is True
    assert "model-facing issue section 'Expected behavior'" in contract.rejection_reason


def test_extracts_target_hints_without_prompting_mechanical_export_details():
    issue = """Bug: handleResub reports '0 meses' for all resubscriptions

## Where
`src/twitch/client.ts` -> `handleResub()` (line 770, not exported)

## Root cause
The handler appears to use streak months as cumulative months.

## Expected behavior
A user subscribed for 6 months should produce "6 meses".
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert contract.source_hint == "src/twitch/client.ts"
    assert contract.symbol_hint == "handleResub"
    assert "`src/twitch/client.ts` -> `handleResub()`" in contract.model_text
    assert "not exported" not in contract.model_text
    assert "Reporter hypothesis" not in contract.model_text
    assert "streak months as cumulative months" not in contract.model_text
    assert contract.reporter_hypotheses == [
        "The handler appears to use streak months as cumulative months."
    ]


def test_drops_fix_approach_and_keeps_test_approach_as_acceptance_only():
    issue = """Bug: handleResub reports 0 months

## Symptom
The bot reports 0 months.

## Fix approach
1. Export handleResub so it can be tested
2. Read cumulative months from the resub userstate

## Test approach
- Export handleResub, import it in the test
- Mock logger, memoryManager, and client
- Call handleResub with a synthetic resub payload
- Verify the response message contains "6 meses", not "0 meses"
- Verify streamSummaryManager.trackResub receives 6, not 0
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "Fix approach" not in contract.model_text
    assert "Export handleResub" not in contract.model_text
    assert "Mock logger" not in contract.model_text
    assert "Call handleResub" not in contract.model_text
    assert "Verify the response message contains" in contract.model_text
    assert "Verify streamSummaryManager.trackResub receives 6" in contract.model_text
    assert "dropped Fix approach before prompting" in contract.warnings


def test_preserves_common_github_issue_sections_for_issue_first_discovery():
    issue = """fix: API retry logic and error resilience

## Description
Add retry logic with exponential backoff for all external API calls.

## Tasks
- Apply retry logic to YouTube Analytics API calls
- Apply retry logic to YouTube Data API calls
- Log retry attempts for debugging

## Notes
- Only retry transient errors like 429, 500, and 503.
- Do not retry auth failures like 401.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "## Description" in contract.model_text
    assert "exponential backoff" in contract.model_text
    assert "## Tasks" in contract.model_text
    assert "YouTube Analytics API calls" in contract.model_text
    assert "## Notes" in contract.model_text
    assert "429, 500, and 503" in contract.model_text
    assert "dropped unsupported issue section" not in "\n".join(contract.warnings)


def test_only_dropped_sections_do_not_fall_back_to_raw_issue_text():
    issue = """## Fix approach
1. Export handleResub so it can be tested
2. Add `userstate: any` to the signature
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is True
    assert contract.model_text == ""
    assert "Fix approach" not in contract.model_text
    assert "userstate: any" not in contract.model_text
    assert contract.rejection_reason == "issue has no model-facing content after sanitization"
    assert "dropped Fix approach before prompting" in contract.warnings[0]


def test_numbered_test_approach_steps_become_acceptance_checks():
    issue = """Bug: handleResub reports 0 months

## Symptom
The bot reports 0 months.

## Test approach
1. Verify the response message contains "6 meses"
2) Ensure streamSummaryManager.trackResub receives 6
3. Mock logger and client
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "Verify the response message contains" in contract.model_text
    assert "Ensure streamSummaryManager.trackResub receives 6" in contract.model_text
    assert "Mock logger" not in contract.model_text


def test_unheaded_issue_body_strips_mechanical_testing_details():
    issue = """Bug: handleResub reports 0 months.
Export handleResub so it can be tested.
Expected behavior: report 6 months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "Expected behavior" in contract.model_text
    assert "Export handleResub" not in contract.model_text


def test_root_cause_is_audit_only_even_when_it_contains_the_fix():
    issue = """Bug: handleResub reports 0 months

## Symptom
The bot reports 0 months for cumulative resubs.

## Root cause
tmi.js emits resub(channel, username, months, message, userstate, methods), but
handleResub only accepts three params and must read
userstate['msg-param-cumulative-months'].

## Expected behavior
The bot should report cumulative subscription months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "tmi.js emits resub" not in contract.model_text
    assert "msg-param-cumulative-months" not in contract.model_text
    assert "three params" not in contract.model_text
    assert "The bot should report cumulative subscription months" in contract.model_text
    assert contract.reporter_hypotheses == [
        (
            "tmi.js emits resub(channel, username, months, message, userstate, methods), but\n"
            "handleResub only accepts three params and must read\n"
            "userstate['msg-param-cumulative-months']."
        )
    ]
    assert contract.to_log_dict()["reporter_hypotheses"] == contract.reporter_hypotheses


def test_rejected_contract_still_preserves_reporter_hypotheses():
    issue = """Bug: handleResub reports 0 months

## Root cause
The handler appears to read the wrong month value.

## Expected behavior
Use `userstate: any` and report 6 months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is True
    assert contract.model_text == ""
    assert "model-facing issue section 'Expected behavior'" in contract.rejection_reason
    assert contract.reporter_hypotheses == [
        "The handler appears to read the wrong month value."
    ]
    assert contract.to_log_dict()["reporter_hypotheses"] == contract.reporter_hypotheses


def test_suspected_root_cause_is_not_checked_as_model_facing_guidance():
    issue = """Bug: fails

## Suspected root cause
Use `as any` to force the payload through.

## Symptom
The API rejects valid payloads.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "as any" not in contract.model_text
    assert contract.reporter_hypotheses == ["Use `as any` to force the payload through."]
