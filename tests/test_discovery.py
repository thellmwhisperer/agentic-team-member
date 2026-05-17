"""Tests for deterministic target discovery and semantic indexing."""
import json
import textwrap

from agentic_tdd_runner.discovery import (
    build_semantic_index,
    discover_target,
    load_or_build_semantic_index,
    rank_targets,
    write_semantic_index,
)


def _write_file(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return path


class TestSemanticIndex:
    def test_indexes_typescript_function_with_trigger_strings(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            export function initClient(client: any): void {
              client.on('message', handleMessage);
            }

            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              const isMention = lower.startsWith('@manolitozurrapa');
              const isOyeManolito = lower.startsWith('!oyemanolito');
              if (isMention || isOyeManolito) {
                console.log('reply');
              }
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        candidate = next(c for c in index["candidates"] if c["symbol"] == "handleMessage")

        assert candidate["source_path"] == "src/twitch/client.ts"
        assert candidate["kind"] == "function"
        assert "@manolitozurrapa" in candidate["strings"]
        assert "message" in candidate["terms"]
        assert "twitch" in candidate["path_tokens"]

    def test_skips_generated_and_environment_directories(self, tmp_path):
        _write_file(
            tmp_path,
            "src/client.ts",
            """\
            export function realTarget(): void {
              console.log('real');
            }
            """,
        )
        _write_file(
            tmp_path,
            "dist/generated.ts",
            """\
            export function generatedTarget(): void {
              console.log('generated');
            }
            """,
        )
        _write_file(
            tmp_path,
            ".venv/ignored.py",
            """\
            def ignored_target():
                return 'ignored'
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbols = {candidate["symbol"] for candidate in index["candidates"]}

        assert "realTarget" in symbols
        assert "generatedTarget" not in symbols
        assert "ignored_target" not in symbols

    def test_skips_source_files_with_decode_errors(self, tmp_path):
        _write_file(
            tmp_path,
            "src/client.ts",
            """\
            export function realTarget(): void {
              console.log('real');
            }
            """,
        )
        bad = tmp_path / "src" / "bad.ts"
        bad.write_bytes(b"\xff\xfe\x00not utf8")

        index = build_semantic_index(str(tmp_path))
        symbols = {candidate["symbol"] for candidate in index["candidates"]}

        assert symbols == {"realTarget"}

    def test_indexes_typescript_class_method(self, tmp_path):
        _write_file(
            tmp_path,
            "src/roles/RoleManager.ts",
            """\
            export class RoleManager {
              private actionTimers: Map<string, NodeJS.Timeout> = new Map();

              startActionTimers(sendMessage: (message: string) => void): void {
                for (const timer of this.actionTimers.values()) {
                  clearInterval(timer);
                }
                this.actionTimers.clear();
                sendMessage('started');
              }
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        candidate = next(c for c in index["candidates"] if c["symbol"] == "startActionTimers")

        assert candidate["source_path"] == "src/roles/RoleManager.ts"
        assert candidate["kind"] == "method"
        assert candidate["owner_class"] == "RoleManager"
        assert "action" in candidate["terms"]
        assert "timers" in candidate["terms"]


class TestDiscoverTarget:
    def test_rank_targets_orders_best_candidate_first(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            export function initClient(client: any): void {
              client.on('message', handleMessage);
            }

            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              const isMention = lower.startsWith('@manolitozurrapa');
              if (isMention) {
                console.log('reply');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/other.ts",
            """\
            export function mentionFallback(message: string): void {
              if (message.includes('@manolitozurrapa')) {
                console.log('fallback');
              }
            }
            """,
        )

        ranked = rank_targets(
            issue_text="Manolito no responde si @manolitozurrapa no va al principio del mensaje",
            project_root=str(tmp_path),
            limit=2,
        )

        assert [candidate["symbol"] for candidate in ranked] == ["handleMessage", "mentionFallback"]
        assert ranked[0]["score"] >= ranked[1]["score"]

    def test_ranks_mention_routing_issue_to_handle_message(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            export function initClient(client: any): void {
              client.on('message', handleMessage);
            }

            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              const isMention = lower.startsWith('@manolitozurrapa');
              const isOyeManolito = lower.startsWith('!oyemanolito');
              if (isMention || isOyeManolito) {
                console.log('reply');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/roles/RoleManager.ts",
            """\
            export class RoleManager {
              startActionTimers(): void {}
            }
            """,
        )

        target = discover_target(
            issue_text=(
                "Manolito solo responde cuando la mención @manolitozurrapa "
                "aparece como primera palabra del mensaje"
            ),
            project_root=str(tmp_path),
        )

        assert target is not None
        assert target["source_path"] == "src/twitch/client.ts"
        assert target["symbol"] == "handleMessage"

    def test_ranks_duplicate_timer_issue_to_start_action_timers(self, tmp_path):
        _write_file(
            tmp_path,
            "src/roles/RoleManager.ts",
            """\
            export class RoleManager {
              private actionTimers: Map<string, NodeJS.Timeout> = new Map();

              startActionTimers(sendMessage: (message: string) => void): void {
                for (const timer of this.actionTimers.values()) {
                  clearInterval(timer);
                }
                this.actionTimers.clear();
                sendMessage('started');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.startsWith('@manolitozurrapa')) {
                console.log('reply');
              }
            }
            """,
        )

        target = discover_target(
            issue_text="Action timers duplicate on reconnect and send duplicate messages",
            project_root=str(tmp_path),
        )

        assert target is not None
        assert target["source_path"] == "src/roles/RoleManager.ts"
        assert target["symbol"] == "startActionTimers"


class TestSemanticIndexPersistence:
    def test_writes_generated_semantic_layer_json(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.startsWith('@manolitozurrapa')) {
                console.log('reply');
              }
            }
            """,
        )

        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        written = write_semantic_index(str(tmp_path), output_path=output_path)

        assert written == output_path
        payload = json.loads(output_path.read_text())
        assert payload["version"] == 3
        assert "files" in payload
        assert "symbols" in payload
        assert any(c["symbol"] == "handleMessage" for c in payload["candidates"])

    def test_load_or_build_semantic_index_writes_default_generated_file(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.includes('@manolitozurrapa')) {
                console.log('reply');
              }
            }
            """,
        )

        payload = load_or_build_semantic_index(str(tmp_path))

        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        assert output_path.exists()
        assert payload["version"] == 3
        assert "files" in payload
        assert "symbols" in payload
        assert any(c["symbol"] == "handleMessage" for c in payload["candidates"])

    def test_load_or_build_semantic_index_reuses_existing_file(self, tmp_path, monkeypatch):
        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        output_path.parent.mkdir(parents=True, exist_ok=True)
        output_path.write_text(
            json.dumps(
                {
                    "version": 3,
                    "project_root": str(tmp_path),
                    "candidates": [
                        {
                            "source_path": "src/twitch/client.ts",
                            "symbol": "handleMessage",
                            "kind": "function",
                            "owner_class": None,
                            "line_start": 1,
                            "line_end": 5,
                            "path_tokens": ["src", "twitch", "client"],
                            "symbol_tokens": ["handle", "message"],
                            "string_tokens": ["manolitozurrapa"],
                            "strings": ["@manolitozurrapa"],
                            "terms": ["client", "handle", "manolitozurrapa", "message", "src", "twitch"],
                        }
                    ],
                }
            )
        )

        def fail_build(project_root):
            raise AssertionError(f"build_semantic_index should not run for {project_root}")

        monkeypatch.setattr("agentic_tdd_runner.discovery.build_semantic_index", fail_build)

        payload = load_or_build_semantic_index(str(tmp_path))

        assert payload["candidates"][0]["symbol"] == "handleMessage"

    def test_load_or_build_semantic_index_rebuilds_stale_version(self, tmp_path, monkeypatch):
        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        output_path.parent.mkdir(parents=True, exist_ok=True)
        output_path.write_text(
            json.dumps(
                {
                    "version": 1,
                    "project_root": str(tmp_path),
                    "candidates": [{"symbol": "staleTarget"}],
                }
            )
        )

        def fake_build(project_root):
            return {
                "version": 3,
                "project_root": project_root,
                "files": [],
                "symbols": [],
                "candidates": [{"symbol": "freshTarget"}],
            }

        monkeypatch.setattr("agentic_tdd_runner.discovery.build_semantic_index", fake_build)

        payload = load_or_build_semantic_index(str(tmp_path))

        assert payload["version"] == 3
        assert payload["candidates"] == [{"symbol": "freshTarget"}]
