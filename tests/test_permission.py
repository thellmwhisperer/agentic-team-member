"""Tests for permission-driven runtime gates."""

from agentic_tdd_runner import permission


def _context():
    return {
        "phase": "test",
        "test_file_created": False,
        "source_file": "src/twitch/client.ts",
        "test_file": "src/twitch/handleResub.test.ts",
        "source_import_path": "./client",
        "target_symbol": "handleResub",
        "runner": "bun:test",
        "test_command": "bun test",
        "contract_facts": [
            "line 115: `client.on('resub', handleResub)`",
            "tmi.js source emits `resub(channel, username, streakMonths, msg, tags, methods)`.",
            "The userstate/tags argument exposes both `msg-param-streak-months` and `msg-param-cumulative-months`.",
        ],
        "runner_facts": [
            "test runner: bun:test",
            "test API import: `import { beforeEach, describe, expect, mock, test } from \"bun:test\";`",
        ],
        "source_signature": "function handleResub(channel: string, username: string, months: number): void {",
        "source_seams": [
            "`client` -> call `__setClientForTests({ say })` before invoking target",
        ],
        "source_mocks": [
            "mock.module('../managers/stream-summary', () => ({",
        ],
        "module_mock_block": "\n".join([
            "const streamSummaryManager_trackResub_spy = mock(() => undefined);",
            "",
            "mock.module('../managers/stream-summary', () => (",
            "{",
            "  getStreamSummaryManager: () => ({",
            "    trackResub: streamSummaryManager_trackResub_spy,",
            "  }),",
            "}",
            "));",
        ]),
    }


def test_understand_contract_answers_known_facts_without_granting_exploration():
    review = permission.answer_harness({"intent": "understand_contract"}, _context())

    assert review.allowed is True
    assert review.grant is None
    assert "HARNESS ANSWER" in review.message
    assert "Do not read source" in review.message
    assert "msg-param-cumulative-months" in review.message
    assert "intent `write_regression_test`" in review.message


def test_blocks_tools_until_model_declares_intent():
    review = permission.review_tool_call(
        "read_file",
        {"path": "src/twitch/client.ts"},
        grant=None,
        context=_context(),
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )

    assert review is not None
    assert review.allowed is False
    assert "PERMISSION REQUIRED" in review.message


def test_write_test_grant_allows_only_recommended_test_file():
    grant = permission.answer_harness({"intent": "write_regression_test"}, _context())
    allowed = permission.review_tool_call(
        "create_file",
        {"path": "src/twitch/handleResub.test.ts"},
        grant="write_test",
        context=_context(),
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )
    blocked = permission.review_tool_call(
        "str_replace_editor",
        {"path": "src/twitch/client.ts"},
        grant="write_test",
        context=_context(),
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )

    assert grant.grant == "write_test"
    assert "Do not read source before writing" in grant.message
    assert "Target callable currently has source signature" in grant.message
    assert "msg-param-cumulative-months" in grant.message
    assert "__setClientForTests" in grant.message
    assert "Suggested regression test skeleton" in grant.message
    assert "type ResubHandler" in grant.message
    assert 'await import("./client")' in grant.message
    assert 'const getEmote = mock((): string => "teseoLove");' in grant.message
    assert "const streamSummaryManager_trackResub_spy" in grant.message
    assert "for (const spy of [streamSummaryManager_trackResub_spy]) spy.mockClear();" in grant.message
    assert "streamSummaryManager_trackResub_spy" in grant.message
    assert allowed is None
    assert blocked is not None
    assert "PERMISSION DENIED" in blocked.message


def test_edit_source_denied_until_test_exists():
    review = permission.answer_harness({"intent": "edit_source"}, _context())

    assert review.allowed is False
    assert review.grant is None
    assert "before editing source" in review.message


def test_edit_source_grant_includes_exact_target_snippet_after_test_exists():
    context = _context()
    context["test_file_created"] = True
    context["source_snippet"] = "\n".join([
        "export function handleResub(channel: string, username: string, months: number): void {",
        "  logger.event('resub', { username, months });",
        "  streamSummaryManager.trackResub(username, months);",
        "}",
    ])
    context["source_imports"] = "import tmi, { type ChatUserstate } from 'tmi.js';"

    review = permission.answer_harness({"intent": "edit_source"}, context)

    assert review.allowed is True
    assert review.grant == "write_source"
    assert "Do not call read_file or rg" in review.message
    assert "Current target snippet for exact str_replace" in review.message
    assert "streamSummaryManager.trackResub(username, months);" in review.message
    assert "Existing relevant imports" in review.message
    assert "import tmi, { type ChatUserstate } from 'tmi.js';" in review.message
    assert "msg-param-cumulative-months" in review.message


def test_build_permission_context_extracts_current_target_snippet(tmp_path):
    source = tmp_path / "src" / "twitch" / "client.ts"
    source.parent.mkdir(parents=True)
    source.write_text(
        "\n".join([
            "import tmi, { type ChatUserstate } from 'tmi.js';",
            "",
            "function other(): void {",
            "}",
            "",
            "export function handleResub(channel: string, username: string, months: number): void {",
            "  logger.event('resub', { username, months });",
            "  streamSummaryManager.trackResub(username, months);",
            "}",
            "",
            "function after(): void {",
            "}",
        ])
    )

    context = permission.build_permission_context(
        episode={
            "source_file": "src/twitch/client.ts",
            "test_file": "src/twitch/handleResub.test.ts",
            "target_symbol": "handleResub",
            "cookbook_text": "\n".join([
                "### Callback Contract Evidence",
                "- tmi.js source emits `resub(channel, username, streakMonths, msg, tags, methods)`.",
            ]),
        },
        config={},
        phase="fix",
        test_file_created=True,
        workdir=str(tmp_path),
    )

    assert context["source_snippet"] == "\n".join([
        "export function handleResub(channel: string, username: string, months: number): void {",
        "  logger.event('resub', { username, months });",
        "  streamSummaryManager.trackResub(username, months);",
        "}",
    ])
    assert context["source_imports"] == "import tmi, { type ChatUserstate } from 'tmi.js';"
