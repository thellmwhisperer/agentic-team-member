"""Tests for ATM-formatted issue intake."""

from agentic_tdd_runner.issue_intake import parse_issue_contract


def test_drops_forbidden_any_inside_fix_approach():
    issue = """Bug: processRenewal reports 0 months

## Symptom
The bot reports 0 months.

## Fix approach
1. Add `message: string` and `eventPayload: any` params to the signature
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "eventPayload: any" not in contract.model_text
    assert "dropped Fix approach before prompting because it contained forbidden guidance" in contract.warnings


def test_rejects_forbidden_any_in_model_facing_section():
    issue = """Bug: processRenewal reports 0 months

## Expected behavior
Use `eventPayload: any` and report 6 months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is True
    assert "model-facing issue section 'Expected behavior'" in contract.rejection_reason


def test_extracts_target_hints_without_prompting_mechanical_export_details():
    issue = """Bug: processRenewal reports '0 meses' for all renewal events

## Where
`src/events/client.ts` -> `processRenewal()` (line 770, not exported)

## Root cause
The handler appears to use streak months as cumulative months.

## Expected behavior
A user subscribed for 6 months should produce "6 meses".
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert contract.source_hint == "src/events/client.ts"
    assert contract.symbol_hint == "processRenewal"
    assert "`src/events/client.ts` -> `processRenewal()`" in contract.model_text
    assert "not exported" not in contract.model_text
    assert "Reporter hypothesis" not in contract.model_text
    assert "streak months as cumulative months" not in contract.model_text
    assert contract.reporter_hypotheses == [
        "The handler appears to use streak months as cumulative months."
    ]


def test_drops_fix_approach_and_keeps_test_approach_as_acceptance_only():
    issue = """Bug: processRenewal reports 0 months

## Symptom
The bot reports 0 months.

## Fix approach
1. Export processRenewal so it can be tested
2. Read cumulative months from the renewal eventPayload

## Test approach
- Export processRenewal, import it in the test
- Mock logger, memoryManager, and client
- Call processRenewal with a synthetic renewal payload
- Verify the response message contains "6 meses", not "0 meses"
- Verify metricsSummaryManager.trackRenewal receives 6, not 0
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "Fix approach" not in contract.model_text
    assert "Export processRenewal" not in contract.model_text
    assert "Mock logger" not in contract.model_text
    assert "Call processRenewal" not in contract.model_text
    assert "Verify the response message contains" in contract.model_text
    assert "Verify metricsSummaryManager.trackRenewal receives 6" in contract.model_text
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
1. Export processRenewal so it can be tested
2. Add `eventPayload: any` to the signature
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is True
    assert contract.model_text == ""
    assert "Fix approach" not in contract.model_text
    assert "eventPayload: any" not in contract.model_text
    assert contract.rejection_reason == "issue has no model-facing content after sanitization"
    assert "dropped Fix approach before prompting" in contract.warnings[0]


def test_numbered_test_approach_steps_become_acceptance_checks():
    issue = """Bug: processRenewal reports 0 months

## Symptom
The bot reports 0 months.

## Test approach
1. Verify the response message contains "6 meses"
2) Ensure metricsSummaryManager.trackRenewal receives 6
3. Mock logger and client
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "Verify the response message contains" in contract.model_text
    assert "Ensure metricsSummaryManager.trackRenewal receives 6" in contract.model_text
    assert "Mock logger" not in contract.model_text


def test_unheaded_issue_body_strips_mechanical_testing_details():
    issue = """Bug: processRenewal reports 0 months.
Export processRenewal so it can be tested.
Expected behavior: report 6 months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "Expected behavior" in contract.model_text
    assert "Export processRenewal" not in contract.model_text


def test_root_cause_is_audit_only_even_when_it_contains_the_fix():
    issue = """Bug: processRenewal reports 0 months

## Symptom
The bot reports 0 months for cumulative renewals.

## Root cause
@example/event-bus emits renewal(channel, username, months, message, eventPayload, methods), but
processRenewal only accepts three params and must read
eventPayload['event-total-count'].

## Expected behavior
The bot should report cumulative subscription months.
"""

    contract = parse_issue_contract(issue)

    assert contract.rejected is False
    assert "@example/event-bus emits renewal" not in contract.model_text
    assert "event-total-count" not in contract.model_text
    assert "three params" not in contract.model_text
    assert "The bot should report cumulative subscription months" in contract.model_text
    assert contract.reporter_hypotheses == [
        (
            "@example/event-bus emits renewal(channel, username, months, message, eventPayload, methods), but\n"
            "processRenewal only accepts three params and must read\n"
            "eventPayload['event-total-count']."
        )
    ]
    assert contract.to_log_dict()["reporter_hypotheses"] == contract.reporter_hypotheses


def test_rejected_contract_still_preserves_reporter_hypotheses():
    issue = """Bug: processRenewal reports 0 months

## Root cause
The handler appears to read the wrong month value.

## Expected behavior
Use `eventPayload: any` and report 6 months.
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
