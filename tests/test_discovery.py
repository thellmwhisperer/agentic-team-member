"""Tests for deterministic target discovery and semantic indexing."""
import json
import textwrap

from agentic_tdd_runner.discovery import (
    build_semantic_index,
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
            "src/http/router.ts",
            """\
            export function initRouter(server: any): void {
              server.on('request', routeRequest);
            }

            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              const isTasksEndpoint = lower.startsWith('/api/tasks');
              const isJobsEndpoint = lower.startsWith('/api/jobs');
              if (isTasksEndpoint || isJobsEndpoint) {
                console.log('reply');
              }
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        candidate = next(c for c in index["candidates"] if c["symbol"] == "routeRequest")

        assert candidate["source_path"] == "src/http/router.ts"
        assert candidate["kind"] == "function"
        assert "/api/tasks" in candidate["strings"]
        assert "request" in candidate["terms"]
        assert "http" in candidate["path_tokens"]

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
        _write_file(
            tmp_path,
            ".worktrees/old-run/src/contaminated.ts",
            """\
            export function contaminatedTarget(): void {
              console.log('contaminated');
            }
            """,
        )
        _write_file(
            tmp_path,
            ".atm/generated.ts",
            """\
            export function atmGeneratedTarget(): void {
              console.log('generated');
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbols = {candidate["symbol"] for candidate in index["candidates"]}

        assert "realTarget" in symbols
        assert "generatedTarget" not in symbols
        assert "ignored_target" not in symbols
        assert "contaminatedTarget" not in symbols
        assert "atmGeneratedTarget" not in symbols

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
            "src/http/router.ts",
            """\
            export function initRouter(server: any): void {
              server.on('request', routeRequest);
            }

            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              const isTasksEndpoint = lower.startsWith('/api/tasks');
              if (isTasksEndpoint) {
                console.log('reply');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/other.ts",
            """\
            export function routeFallback(path: string): void {
              if (path.includes('/api/tasks')) {
                console.log('fallback');
              }
            }
            """,
        )

        ranked = rank_targets(
            issue_text="Router misses /api/tasks away from the path start",
            project_root=str(tmp_path),
            limit=3,
        )

        ranked_symbols = [candidate["symbol"] for candidate in ranked]
        assert ranked_symbols[0] == "routeRequest"
        assert "routeFallback" in ranked_symbols
        assert ranked[0]["score"] >= ranked[1]["score"]

    def test_ranks_endpoint_routing_issue_to_route_request(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            export function initRouter(server: any): void {
              server.on('request', routeRequest);
            }

            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              const isTasksEndpoint = lower.startsWith('/api/tasks');
              const isJobsEndpoint = lower.startsWith('/api/jobs');
              if (isTasksEndpoint || isJobsEndpoint) {
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

        target = rank_targets(
            issue_text=(
                "Router only responds when the /api/tasks endpoint "
                "appears as the first segment of the request path"
            ),
            project_root=str(tmp_path),
            limit=1,
        )[0]

        assert target is not None
        assert target["source_path"] == "src/http/router.ts"
        assert target["symbol"] == "routeRequest"

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
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              if (lower.startsWith('/api/tasks')) {
                console.log('reply');
              }
            }
            """,
        )

        target = rank_targets(
            issue_text="Action timers duplicate on reconnect and send duplicate messages",
            project_root=str(tmp_path),
            limit=1,
        )[0]

        assert target is not None
        assert target["source_path"] == "src/roles/RoleManager.ts"
        assert target["symbol"] == "startActionTimers"

    def test_api_retry_issue_prefers_api_boundary_over_report_reader(self, tmp_path):
        _write_file(
            tmp_path,
            "src/report.ts",
            """\
            import { existsSync, readdirSync, readFileSync } from 'node:fs';

            export function loadLatestYouTube(dir: string): unknown {
              if (!existsSync(dir)) return undefined;
              const latest = readdirSync(dir).filter((file) => file.endsWith('.json'))[0];
              return latest ? JSON.parse(readFileSync(`${dir}/${latest}`, 'utf-8')) : undefined;
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/providers/youtube.ts",
            """\
            export async function fetchYouTubeData(client: any, analytics: any): Promise<unknown> {
              const report = await analytics.reports.query({ ids: 'channel==MINE' });
              const videos = await client.videos.update({ part: ['snippet'] });
              return { report, videos };
            }
            """,
        )

        ranked = rank_targets(
            issue_text=(
                "Add retry logic with exponential backoff for external API calls. "
                "Apply retry logic to YouTube Analytics API calls and YouTube Data API uploads/updates. "
                "Retry 429, 500 and 503 transient failures."
            ),
            project_root=str(tmp_path),
            limit=2,
        )

        assert ranked[0]["source_path"] == "src/providers/youtube.ts"
        assert ranked[0]["symbol"] == "fetchYouTubeData"
        assert ranked[0]["issue_shape"] == "api_retry"
        assert all(candidate["symbol"] != "loadLatestYouTube" or candidate["score"] < ranked[0]["score"] for candidate in ranked)

    def test_generic_api_issue_does_not_trigger_api_retry_shape(self, tmp_path):
        _write_file(
            tmp_path,
            "src/providers/youtube.ts",
            """\
            export async function fetchYouTubeData(client: any, analytics: any): Promise<unknown> {
              const report = await analytics.reports.query({ ids: 'channel==MINE' });
              const videos = await client.videos.update({ part: ['snippet'] });
              return { report, videos };
            }
            """,
        )

        ranked = rank_targets(
            issue_text=(
                "External API analytics responses map video totals incorrectly. "
                "The server returns data, but the provider builds the wrong report rows."
            ),
            project_root=str(tmp_path),
            limit=1,
        )

        assert ranked[0]["source_path"] == "src/providers/youtube.ts"
        assert "issue_shape" not in ranked[0]

    def test_api_rate_limit_issue_can_use_quota_or_throttle_signal(self, tmp_path):
        _write_file(
            tmp_path,
            "src/providers/youtube.ts",
            """\
            export async function fetchYouTubeData(client: any): Promise<unknown> {
              return client.videos.list({ part: ['snippet'] });
            }
            """,
        )

        ranked = rank_targets(
            issue_text="The external API is throttled when hitting quota limits",
            project_root=str(tmp_path),
            limit=1,
        )

        assert ranked[0]["source_path"] == "src/providers/youtube.ts"
        assert ranked[0]["issue_shape"] == "api_retry"

    def test_status_like_numbers_do_not_trigger_api_retry_shape(self, tmp_path):
        _write_file(
            tmp_path,
            "src/report.ts",
            """\
            export function summarizeRows(rows: unknown[]): number {
              return rows.length;
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/pricing-api.ts",
            """\
            export function formatPricingApiAmount(value: number): string {
              return `$${value}`;
            }
            """,
        )

        rows_ranked = rank_targets(
            issue_text="The report returns 500 rows but should cap the table at 100 rows",
            project_root=str(tmp_path),
            limit=1,
        )
        price_ranked = rank_targets(
            issue_text="The external pricing API displays $500 in the wrong field",
            project_root=str(tmp_path),
            limit=1,
        )

        assert rows_ranked[0]["source_path"] == "src/report.ts"
        assert "issue_shape" not in rows_ranked[0]
        assert price_ranked[0]["source_path"] == "src/pricing-api.ts"
        assert "issue_shape" not in price_ranked[0]


class TestSemanticIndexPersistence:
    def test_writes_generated_semantic_layer_json(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              if (lower.startsWith('/api/tasks')) {
                console.log('reply');
              }
            }
            """,
        )

        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        written = write_semantic_index(str(tmp_path), output_path=output_path)

        assert written == output_path
        payload = json.loads(output_path.read_text())
        assert payload["version"] == 5
        assert "files" in payload
        assert "symbols" in payload
        assert any(c["symbol"] == "routeRequest" for c in payload["candidates"])

    def test_load_or_build_semantic_index_writes_default_generated_file(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              if (lower.includes('/api/tasks')) {
                console.log('reply');
              }
            }
            """,
        )

        payload = load_or_build_semantic_index(str(tmp_path))

        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        assert output_path.exists()
        assert payload["version"] == 5
        assert "files" in payload
        assert "symbols" in payload
        assert any(c["symbol"] == "routeRequest" for c in payload["candidates"])

    def test_load_or_build_semantic_index_reuses_existing_file(self, tmp_path, monkeypatch):
        output_path = tmp_path / ".atm" / "semantic-index.generated.json"
        output_path.parent.mkdir(parents=True, exist_ok=True)
        output_path.write_text(
            json.dumps(
                {
                    "version": 5,
                    "project_root": str(tmp_path),
                    "candidates": [
                        {
                            "source_path": "src/http/router.ts",
                            "symbol": "routeRequest",
                            "kind": "function",
                            "owner_class": None,
                            "line_start": 1,
                            "line_end": 5,
                            "path_tokens": ["src", "http", "router"],
                            "symbol_tokens": ["route", "request"],
                            "string_tokens": ["api", "tasks"],
                            "strings": ["/api/tasks"],
                            "terms": ["api", "http", "request", "route", "router", "src", "tasks"],
                        }
                    ],
                }
            )
        )

        def fail_build(project_root):
            raise AssertionError(f"build_semantic_index should not run for {project_root}")

        monkeypatch.setattr("agentic_tdd_runner.discovery.build_semantic_index", fail_build)

        payload = load_or_build_semantic_index(str(tmp_path))

        assert payload["candidates"][0]["symbol"] == "routeRequest"

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
                "version": 5,
                "project_root": project_root,
                "files": [],
                "symbols": [],
                "candidates": [{"symbol": "freshTarget"}],
            }

        monkeypatch.setattr("agentic_tdd_runner.discovery.build_semantic_index", fake_build)

        payload = load_or_build_semantic_index(str(tmp_path))

        assert payload["version"] == 5
        assert payload["candidates"] == [{"symbol": "freshTarget"}]
