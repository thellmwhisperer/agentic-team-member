"""Tests for deterministic P1 -> P2 contract building."""
from agentic_tdd_runner.compiler import build_p1p2_contract


def _handle_resub_facts():
    return {
        "target": {
            "symbol": "handleResub",
            "kind": "function",
            "source_path": "src/twitch/client.ts",
            "line_start": 770,
            "line_end": 777,
            "signature": "handleResub(channel: string, username: string, months: number): void",
        },
        "test_file": {
            "path": "src/twitch/client-resub.test.ts",
            "runner": "bun:test",
        },
        "pre_test_source_edits": [
            {
                "kind": "mechanical_export",
                "path": "src/twitch/client.ts",
                "old": "function handleResub(",
                "new": "export function handleResub(",
            }
        ],
        "module_load_dependencies": [
            {
                "binding": "logger",
                "origin_kind": "factory_result",
                "source_module": "../logger",
                "required_shape": {
                    "getLogger": ["event", "response"],
                    "log": ["info", "error", "warn"],
                },
                "strategy": "mock_module",
            },
            {
                "binding": "streamSummaryManager",
                "origin_kind": "factory_result",
                "source_module": "../managers/stream-summary",
                "required_shape": {
                    "getStreamSummaryManager": ["trackResub"],
                },
                "strategy": "mock_module",
            },
        ],
        "execution_dependencies": [
            {
                "binding": "memoryManager",
                "origin_kind": "module_local_mutable",
                "required_shape": {"getEmote": []},
                "strategy": "set_test_seam",
                "observed_members": ["getEmote"],
            },
            {
                "binding": "client",
                "origin_kind": "module_local_mutable",
                "required_shape": {"say": []},
                "strategy": "set_test_seam",
                "observed_members": ["say"],
            },
        ],
        "injection_plan": [
            {
                "binding": "memoryManager",
                "strategy": "set_test_seam",
                "steps": ["inject a stub with getEmote()"],
                "blocks_p2_if_missing": True,
                "seam_available": False,
            },
            {
                "binding": "client",
                "strategy": "set_test_seam",
                "steps": ["inject a spy for say(channel, message)"],
                "blocks_p2_if_missing": True,
                "seam_available": False,
            },
        ],
        "assertion_surface": {
            "kind": "outbound_call_arguments",
            "binding": "client",
            "member": "say",
            "assertion_shape": "toHaveBeenCalledWith(channel, expectedMessage)",
        },
        "pattern_files": [
            {
                "path": "src/api/api.test.ts",
                "why_selected": "logger mock shape",
                "reusable_shapes": ["mock.module('../logger', ...)"],
                "non_reusable_noise": ["HTTP assertions"],
            }
        ],
    }


def test_computes_source_import_path():
    contract = build_p1p2_contract(_handle_resub_facts())
    assert contract["test_file"]["source_import_path"] == "./client"


def test_marks_contract_not_ready_when_framework_seam_is_missing():
    contract = build_p1p2_contract(_handle_resub_facts())
    assert contract["ready_for_p2"] is False
    assert contract["gaps"] == [
        {
            "kind": "missing_test_seam",
            "message": "memoryManager requires injection strategy 'set_test_seam' but no framework seam is available",
            "owner": "framework",
        },
        {
            "kind": "missing_test_seam",
            "message": "client requires injection strategy 'set_test_seam' but no framework seam is available",
            "owner": "framework",
        },
    ]


def test_renders_module_mocks_and_source_import():
    contract = build_p1p2_contract(_handle_resub_facts())
    block = contract["scaffold"]["module_mocks_block"]
    assert "mock.module('../logger'" in block
    assert "getLogger: () => ({" in block
    assert "log: {" in block
    assert "event: mock(() => {})" in block
    assert "info: mock(() => {})" in block
    assert "const { handleResub } = await import('./client');" in block


def test_renders_arrange_act_and_assert_blocks():
    contract = build_p1p2_contract(_handle_resub_facts())
    scaffold = contract["scaffold"]
    assert "const client_say_spy = mock(() => undefined);" in scaffold["arrange_block"]
    assert "const channel = /* TODO */;" in scaffold["arrange_block"]
    assert scaffold["act_block"] == "handleResub(channel, username, months);"
    assert (
        scaffold["assert_block"]
        == "expect(client_say_spy).toHaveBeenCalledWith(/* TODO: channel */, expected_message);"
    )


def test_rendered_test_contains_minimal_template():
    contract = build_p1p2_contract(_handle_resub_facts())
    rendered = contract["scaffold"]["rendered_test"]
    assert "import { describe, expect, mock, test } from 'bun:test';" in rendered
    assert "describe('handleResub'" in rendered
    assert "test('TODO behavior'" in rendered
    assert "handleResub(channel, username, months);" in rendered
