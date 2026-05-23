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
            "tmi.js type declarations expose `resub(channel: string, username: string, months: number, message: string, userstate: SubUserstate, methods: SubMethods)`.",
            "The userstate/tags argument exposes both `msg-param-streak-months` and `msg-param-cumulative-months`.",
        ],
        "runner_facts": [
            "test runner: bun:test",
            "test API import: `import { beforeEach, describe, expect, mock, test } from \"bun:test\";`",
        ],
        "source_signature": "function handleResub(channel: string, username: string, months: number): void {",
        "source_snippet": "\n".join([
            "export function handleResub(channel: string, username: string, months: number): void {",
            "  logger.event('resub', { username, months });",
            "  streamSummaryManager.trackResub(username, months);",
            "}",
        ]),
        "source_imports": "import tmi, { type SubUserstate } from 'tmi.js';",
        "source_seams": [
            "`client` -> call `__setClientForTests({ say })` before invoking target",
            "`memoryManager` -> call `__setMemoryManagerForTests({ getEmote })` before invoking target",
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
        "referenced_type_shapes": [
            {
                "module": "tmi.js",
                "name": "SubUserstate",
                "kind": "interface",
                "fields": [
                    {"name": "message-type", "optional": True, "type": '"sub" | "resub" | undefined'},
                    {"name": "msg-param-streak-months", "optional": True, "type": "string | boolean | undefined"},
                    {"name": "msg-param-cumulative-months", "optional": True, "type": "string | boolean | undefined"},
                ],
            },
            {
                "module": "tmi.js",
                "name": "SubMethods",
                "kind": "interface",
                "fields": [
                    {"name": "prime", "optional": True, "type": "boolean"},
                    {"name": "plan", "optional": True, "type": "SubMethodsPlan"},
                    {"name": "planName", "optional": True, "type": "string"},
                ],
            },
        ],
    }


def test_understand_contract_answers_known_facts_without_granting_exploration():
    review = permission.answer_harness({"intent": "understand_contract"}, _context())

    assert review.allowed is True
    assert review.grant is None
    assert "HARNESS ANSWER" in review.message
    assert "Do not call read_file" in review.message
    assert "msg-param-cumulative-months" in review.message
    assert "Referenced type shapes" in review.message
    assert "SubUserstate" in review.message
    assert "msg-param-streak-months" in review.message
    assert "Regression red-case guidance" in review.message
    assert "Do not make the third callback number `6`" in review.message
    assert "intent `write_regression_test`" in review.message


def test_understand_contract_serves_target_body_when_question_asks_for_implementation():
    review = permission.answer_harness(
        {
            "intent": "understand_contract",
            "question": "What does handleResub do internally? Why does it report 0 months?",
        },
        _context(),
    )

    assert review.allowed is True
    assert review.grant is None
    assert "Target implementation snippet supplied by the harness" in review.message
    assert "streamSummaryManager.trackResub(username, months);" in review.message
    assert "Relevant existing imports" in review.message


def test_informational_harness_answer_preserves_existing_grant():
    current = permission.merge_grant_after_harness_answer(
        "write_test",
        permission.answer_harness(
            {
                "intent": "understand_contract",
                "question": "What does handleResub do internally?",
            },
            _context(),
        ),
    )

    assert current == "write_test"


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
    assert "Referenced type shapes" in grant.message
    assert "Regression red-case guidance" in grant.message
    assert "runtime streak argument at `0`" in grant.message
    assert "Suggested regression test skeleton" in grant.message
    assert 'import type { SubMethods, SubUserstate } from "tmi.js";' in grant.message
    assert 'type TargetHandler = ClientModule["handleResub"];' in grant.message
    assert "type CallbackContract = (" in grant.message
    assert 'await import("./client")' in grant.message
    assert "targetHandler = clientModule.handleResub;" in grant.message
    assert 'let __setClientForTests: ClientModule["__setClientForTests"];' in grant.message
    assert 'const userstate: SubUserstate = {' in grant.message
    assert '"msg-param-streak-months": "0",' in grant.message
    assert '"msg-param-cumulative-months": "6",' in grant.message
    assert 'const methods: SubMethods = {' in grant.message
    assert 'const sayResult: [string] = [""];' in grant.message
    assert "const say_spy = mock((_channel: string, _message: string) => Promise.resolve(sayResult));" in grant.message
    assert "__setClientForTests({ say: say_spy });" in grant.message
    assert '__setMemoryManagerForTests({ getEmote: () => "teseLove" });' in grant.message
    assert 'callbackHandler(channel, username, months, message, userstate, methods);' in grant.message
    assert "const streamSummaryManager_trackResub_spy" in grant.message
    assert "for (const spy of [streamSummaryManager_trackResub_spy]) spy.mockClear();" in grant.message
    assert "streamSummaryManager_trackResub_spy" in grant.message
    assert allowed is None
    assert blocked is not None
    assert "PERMISSION DENIED" in blocked.message


def test_callback_skeleton_adds_contrastive_cumulative_field_from_type_shape():
    context = _context()
    context["contract_facts"] = [
        "line 115: `client.on('subgift', handleGift)`",
        "tmi.js source emits `subgift(channel, username, streakMonths, msg, tags, methods)`.",
        "tmi.js type declarations expose `subgift(channel: string, username: string, months: number, message: string, userstate: SubUserstate, methods: SubMethods)`.",
    ]
    context["referenced_type_shapes"][0]["fields"].insert(
        2,
        {"name": "msg-param-should-share-streak", "optional": True, "type": "boolean | undefined"},
    )
    context["target_symbol"] = "handleGift"
    context["test_file"] = "src/twitch/handleGift.test.ts"

    grant = permission.answer_harness({"intent": "write_regression_test"}, context)

    assert grant.grant == "write_test"
    assert 'const months: number = 0;' in grant.message
    assert '"msg-param-streak-months": "0",' in grant.message
    assert '"msg-param-cumulative-months": "6",' in grant.message
    assert '"msg-param-should-share-streak"' not in grant.message
    assert "Do not make the third callback number `6`" in grant.message


def test_write_grants_allow_reading_the_same_file_for_resync():
    test_read = permission.review_tool_call(
        "read_file",
        {"path": "src/twitch/handleResub.test.ts"},
        grant="write_test",
        context=_context(),
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )
    source_read = permission.review_tool_call(
        "read_file",
        {"path": "src/twitch/client.ts"},
        grant="write_source",
        context=_context(),
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )
    unrelated_read = permission.review_tool_call(
        "read_file",
        {"path": "src/twitch/other.ts"},
        grant="write_source",
        context=_context(),
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )

    assert test_read is None
    assert source_read is None
    assert unrelated_read is not None
    assert "PERMISSION DENIED" in unrelated_read.message


def test_write_grants_survive_same_file_reads_and_edits():
    assert permission.consume_grant(
        "read_file",
        {"path": "src/twitch/handleResub.test.ts"},
        "write_test",
        _context(),
    ) == "write_test"
    assert permission.consume_grant(
        "str_replace_editor",
        {"path": "src/twitch/client.ts"},
        "write_source",
        _context(),
    ) == "write_source"
    assert permission.consume_grant(
        "run_command",
        {"command": "bun test"},
        "run_test",
        _context(),
    ) is None


def test_write_test_skeleton_is_not_coupled_to_one_handler_name():
    context = _context()
    context.update({
        "test_file": "src/twitch/handleCheer.test.ts",
        "target_symbol": "handleCheer",
        "source_signature": "function handleCheer(channel: string, userstate: ChatUserstate): void {",
        "source_seams": [
            "`notifier` -> call `__setNotifierForTests({ send })` before invoking target",
        ],
        "module_mock_block": "\n".join([
            "const notifier_send_spy = mock(() => undefined);",
            "",
            "mock.module('../notifier', () => ({",
            "  getNotifier: () => ({",
            "    send: notifier_send_spy,",
            "  }),",
            "}));",
        ]),
        "contract_facts": [
            "line 42: `client.on('cheer', handleCheer)`",
            "tmi.js source emits `cheer(channel, userstate, message)`.",
            "tmi.js type declarations expose `cheer(channel: string, userstate: ChatUserstate, message: string)`.",
        ],
        "referenced_type_shapes": [
            {
                "module": "tmi.js",
                "name": "ChatUserstate",
                "kind": "interface",
                "fields": [
                    {"name": "bits", "optional": True, "type": "string | undefined"},
                    {"name": "user-id", "optional": True, "type": "string | undefined"},
                ],
            },
        ],
    })

    grant = permission.answer_harness({"intent": "write_regression_test"}, context)

    assert grant.grant == "write_test"
    assert "Suggested regression test skeleton" in grant.message
    assert 'type TargetHandler = ClientModule["handleCheer"];' in grant.message
    assert "targetHandler = clientModule.handleCheer;" in grant.message
    assert 'let __setNotifierForTests: ClientModule["__setNotifierForTests"];' in grant.message
    assert "const notifier_send_spy" in grant.message
    assert "const userstate: ChatUserstate = {" in grant.message
    assert 'bits: "",' in grant.message


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
    assert "You may read_file this same source path" in review.message
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
    types = tmp_path / "node_modules" / "@types" / "tmi.js" / "index.d.ts"
    types.parent.mkdir(parents=True)
    types.write_text(
        "\n".join([
            "interface SubUserstate {",
            '  "msg-param-cumulative-months"?: string | boolean | undefined;',
            '  "msg-param-streak-months"?: string | boolean | undefined;',
            "}",
            "interface SubMethods {",
            "  prime?: boolean;",
            "  plan?: SubMethodsPlan;",
            "  planName?: string;",
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
                "- tmi.js type declarations expose `resub(channel: string, username: string, months: number, message: string, userstate: SubUserstate, methods: SubMethods)`.",
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
    assert context["referenced_type_shapes"][0]["name"] == "SubMethods"
    assert context["referenced_type_shapes"][1]["name"] == "SubUserstate"
    assert context["referenced_type_shapes"][1]["fields"][0]["name"] == "msg-param-cumulative-months"
