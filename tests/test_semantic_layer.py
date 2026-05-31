"""Tests for the deterministic semantic layer contract."""
import textwrap

from agentic_tdd_runner.discovery import build_semantic_index


def _write_file(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return path


def _get_symbol(index: dict, source_path: str, symbol: str) -> dict:
    return next(
        item
        for item in index["symbols"]
        if item["source_path"] == source_path and item["symbol"] == symbol
    )


def _get_file(index: dict, path: str) -> dict:
    return next(item for item in index["files"] if item["path"] == path)


class TestSemanticLayerSchema:
    def test_build_semantic_index_emits_file_and_symbol_tables(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            export function initRouter(): void {
              server.on('request', routeRequest);
            }

            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              if (lower.startsWith('/api/tasks')) {
                response.send('ok');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/http/router.test.ts",
            """\
            import { test, expect } from 'bun:test';

            test('endpoint routing', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))

        assert index["version"] == 5
        assert "files" in index
        assert "symbols" in index
        assert "candidates" in index

        file_fact = _get_file(index, "src/http/router.ts")
        assert file_fact["language"] == "typescript"
        assert "http" in file_fact["domains"]
        assert "event.request" in file_fact["domains"]
        assert "routing.endpoint" in file_fact["domains"]
        assert file_fact["symbols"] == ["initRouter", "routeRequest"]
        assert file_fact["nearby_tests"] == ["src/http/router.test.ts"]

        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")
        assert symbol_fact["qualified_name"] == "routeRequest"
        assert "http" in symbol_fact["domains"]
        assert "event.request" in symbol_fact["domains"]
        assert "routing.endpoint" in symbol_fact["domains"]
        assert all(not domain.startswith("events.") for domain in symbol_fact["domains"])
        assert symbol_fact["nearby_tests"] == ["src/http/router.test.ts"]
        assert symbol_fact["calls"] == ["response.send"]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["send"], "name": "response"}
        ]
        assert symbol_fact["routes"] == [
            {
                "condition": "lower.startsWith('/api/tasks')",
                "guard_patterns": ["startsWith('/api/tasks')"],
                "label": None,
                "triggers": ["/api/tasks"],
            }
        ]

    def test_nearby_tests_prefers_symbol_and_source_stem_matches(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            export function initRouter(): void {
              server.on('request', routeRequest);
            }

            async function routeRequest(path: string): Promise<void> {
              if (path.startsWith('/api/tasks')) {
                response.send('ok');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/http/router.test.ts",
            """\
            import { routeRequest } from './router';

            test('router test', () => {
              expect(routeRequest).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/http/routeRequest.test.ts",
            """\
            import { routeRequest } from './router';

            test('routeRequest test', () => {
              expect(routeRequest).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/http/routes.test.ts",
            """\
            test('endpoint routing', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")

        assert symbol_fact["nearby_tests"] == [
            "src/http/routeRequest.test.ts",
            "src/http/router.test.ts",
            "src/http/routes.test.ts",
        ]

    def test_nearby_tests_uses_semantic_overlap_when_no_symbol_named_test_exists(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const trimmed = path.trim();
              const lower = trimmed.toLowerCase();
              const isTasksEndpoint = lower.startsWith('/api/tasks');
              const isJobsEndpoint = lower.startsWith('/api/jobs');

              if (isJobsEndpoint || isTasksEndpoint) {
                const argsLowerCmd = trimmed.toLowerCase();
                if (argsLowerCmd === 'enable') {
                  setRetryMode(true);
                }
                if (argsLowerCmd === 'disable' || argsLowerCmd === 'stop') {
                  setRetryMode(false);
                }
                if (getFeatureFlag('retry-policy')?.enabled) {
                  response.send('ok');
                }
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/http/retry-mode.test.ts",
            """\
            test('enable route should activate retry mode', () => {
              expect(true).toBe(true);
            });

            test('disable route should deactivate retry mode', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/http/feature-flags.test.ts",
            """\
            test('feature flags should expose retry policy', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/http/reconnect.test.ts",
            """\
            test('reconnect transport disables auto reconnect', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")

        assert symbol_fact["nearby_tests"] == [
            "src/http/retry-mode.test.ts",
            "src/http/feature-flags.test.ts",
            "src/http/reconnect.test.ts",
        ]

    def test_nearby_tests_deemphasizes_generic_code_tokens(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const trimmed = path.trim();
              const lower = trimmed.toLowerCase();

              if (getFeatureFlag('retry-policy')?.enabled) {
                setRetryMode(true);
                response.send('ok');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/http/reconnect.test.ts",
            """\
            test('generic reconnect boilerplate', async () => {
              const client = await Promise.resolve({ reconnect: false });
              const processInfo = { number: 1, string: 'ok', boolean: true };
              expect(client).toBeDefined();
              expect(processInfo).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/http/feature-flags.test.ts",
            """\
            test('feature flags should expose retry policy', () => {
              const featureFlags = [{ key: 'retry-policy', enabled: true }];
              expect(featureFlags[0]?.enabled).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")

        assert symbol_fact["nearby_tests"] == [
            "src/http/feature-flags.test.ts",
            "src/http/reconnect.test.ts",
        ]

    def test_nearby_tests_ignores_cross_module_noise_without_strong_seams(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const lower = path.toLowerCase();
              if (lower.startsWith('/api/tasks')) {
                response.send('ok');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/http/retry-mode.test.ts",
            """\
            test('retry mode nearby', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/logger/chat.test.ts",
            """\
            test('chat logger noise', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/metrics-summary.test.ts",
            """\
            test('metrics summary noise', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")

        assert symbol_fact["nearby_tests"] == ["src/http/retry-mode.test.ts"]


class TestSemanticLayerGoldens:
    def test_handle_message_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            export function initRouter(): void {
              server.on('request', routeRequest);
            }

            async function routeRequest(
              path: string,
              request: RequestPayload,
            ): Promise<void> {
              const trimmed = path.trim();
              const lower = trimmed.toLowerCase();
              // --- /api/tasks or /api/jobs ---
              const isTasksEndpoint = lower.startsWith('/api/tasks');
              const isJobsEndpoint = lower.startsWith('/api/jobs');

              if (isJobsEndpoint || isTasksEndpoint) {
                response.send(request.id, `ok ${request.userId}`);
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/http/routeRequest.test.ts",
            """\
            import { test, expect } from 'bun:test';

            test('routeRequest', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")

        assert "event.request" in symbol_fact["domains"]
        assert "routing.endpoint" in symbol_fact["domains"]
        assert symbol_fact["entrypoints"] == [
            {
                "kind": "event",
                "emitter": "server.on",
                "name": "request",
                "line": 2,
            }
        ]
        assert symbol_fact["triggers"] == ["/api/jobs", "/api/tasks"]
        assert symbol_fact["guard_patterns"] == [
            "startsWith('/api/jobs')",
            "startsWith('/api/tasks')",
        ]
        assert symbol_fact["observables"] == ["response.send"]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["send"], "name": "response"},
        ]
        assert symbol_fact["nearby_tests"] == ["src/http/routeRequest.test.ts"]
        assert symbol_fact["routes"] == [
            {
                "condition": "isJobsEndpoint || isTasksEndpoint",
                "guard_patterns": [
                    "startsWith('/api/jobs')",
                    "startsWith('/api/tasks')",
                ],
                "label": "/api/tasks or /api/jobs",
                "triggers": ["/api/jobs", "/api/tasks"],
            }
        ]

    def test_handle_message_triggers_exclude_nested_branch_literals(self, tmp_path):
        _write_file(
            tmp_path,
            "src/http/router.ts",
            """\
            async function routeRequest(path: string): Promise<void> {
              const trimmed = path.trim();
              const lower = trimmed.toLowerCase();

              if (lower === '/health') {
                response.send('ok');
                return;
              }

              // --- /api/tasks or /api/jobs ---
              const isTasksEndpoint = lower.startsWith('/api/tasks');
              const isJobsEndpoint = lower.startsWith('/api/jobs');

              if (isJobsEndpoint || isTasksEndpoint) {
                const argsLowerCmd = trimmed.toLowerCase();
                if (argsLowerCmd === 'disable' || argsLowerCmd === 'stop') {
                  response.send('done');
                }
              }
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/http/router.ts", "routeRequest")

        assert "routing.endpoint" in symbol_fact["domains"]
        assert symbol_fact["triggers"] == ["/api/jobs", "/api/tasks", "/health"]
        assert symbol_fact["guard_patterns"] == [
            "== '/health'",
            "startsWith('/api/jobs')",
            "startsWith('/api/tasks')",
        ]
        assert all("disable" not in route["condition"] for route in symbol_fact["routes"])

    def test_handle_renewal_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            export function initEventBus(): void {
              client.on('renewal', processRenewal);
            }

            function processRenewal(channel: string, username: string, months: number): void {
              logger.event('renewal', { username, months });
              metricsSummaryManager.trackRenewal(username, months);
              response.send(channel, `${username} ${months}`);
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "processRenewal")

        assert "event.renewal" in symbol_fact["domains"]
        assert "feature.renewal" in symbol_fact["domains"]
        assert symbol_fact["entrypoints"] == [
            {
                "kind": "event",
                "emitter": "client.on",
                "name": "renewal",
                "line": 2,
            }
        ]
        assert symbol_fact["triggers"] == []
        assert symbol_fact["observables"] == [
            "metricsSummaryManager.trackRenewal",
            "response.send",
        ]
        assert symbol_fact["telemetry"] == ["logger.event"]
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["trackRenewal"], "name": "metricsSummaryManager"},
            {"kind": "module_object", "members": ["send"], "name": "response"},
        ]
        assert symbol_fact["routes"] == []

    def test_start_action_timers_semantic_facts(self, tmp_path):
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

                const timer = setInterval(() => {
                  this.executeAction('action', ['target'], sendMessage);
                }, 1000);

                this.actionTimers.set('action', timer);
              }

              private executeAction(
                actionType: string,
                roleNames: string[],
                sendMessage: (message: string) => void,
              ): void {
                sendMessage(`${actionType}:${roleNames.length}`);
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/roles/RoleManager.test.ts",
            """\
            import { test, expect } from 'bun:test';

            test('startActionTimers', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/roles/RoleManager.ts", "startActionTimers")

        assert "roles" in symbol_fact["domains"]
        assert "timer.management" in symbol_fact["domains"]
        assert symbol_fact["entrypoints"] == []
        assert symbol_fact["triggers"] == []
        assert symbol_fact["calls"] == [
            "clearInterval",
            "setInterval",
            "this.actionTimers.clear",
            "this.actionTimers.set",
            "this.actionTimers.values",
            "this.executeAction",
        ]
        assert symbol_fact["observables"] == [
            "clearInterval",
            "setInterval",
            "this.actionTimers.clear",
            "this.actionTimers.set",
            "this.executeAction",
        ]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "function", "members": [], "name": "clearInterval"},
            {"kind": "function", "members": [], "name": "setInterval"},
            {"kind": "instance_object", "members": ["clear", "set", "values"], "name": "this.actionTimers"},
            {"kind": "instance_method", "members": [], "name": "this.executeAction"},
        ]
        assert symbol_fact["nearby_tests"] == ["src/roles/RoleManager.test.ts"]
        assert symbol_fact["routes"] == []

    def test_nearby_tests_can_surface_cross_module_semantic_matches(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            function processRenewal(channel: string, username: string, months: number): void {
              logger.event('renewal', { username, months });
              metricsSummaryManager.trackRenewal(username, months);
              client.say(channel, `@${username} ${months}`);
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/token.test.ts",
            """\
            test('token manager boilerplate', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/metrics-summary.test.ts",
            """\
            test('trackRenewal increments event count', () => {
              manager.trackRenewal('testuser', 12);
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/logger/logger.test.ts",
            """\
            test('logger.event exists', () => {
              logger.event('sub', { username: 'testuser' });
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "processRenewal")

        assert symbol_fact["nearby_tests"] == [
            "src/managers/metrics-summary.test.ts",
            "src/events/token.test.ts",
        ]

    def test_handle_search_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function handleSearch(
              channel: string,
              eventPayload: ChatEventPayload,
              query: string,
            ): Promise<void> {
              const username = (eventPayload.username || '').toLowerCase();

              if (!searchService.hasApiKey()) {
                logger.error('search_no_api_key', new Error('missing'));
                client.say(channel, `@${eventPayload.username} Search API is not configured`);
                return;
              }

              metricsSummaryManager.trackSearch(username, query);
              const results = await searchService.search(query);
              const response = await aiService.askWithSearch(query, results, username, 'es');
              client.say(channel, `@${eventPayload.username} ${response}`);
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/metrics-summary.test.ts",
            """\
            test('trackSearch increments event count', () => {
              manager.trackSearch('testuser', 'query');
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/services/search.test.ts",
            """\
            test('search service search returns results', async () => {
              const results = await searchService.search('query');
              expect(results).toBeDefined();
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "handleSearch")

        assert "feature.search" in symbol_fact["domains"]
        assert symbol_fact["observables"] == [
            "aiService.askWithSearch",
            "client.say",
            "metricsSummaryManager.trackSearch",
            "searchService.search",
        ]
        assert symbol_fact["telemetry"] == ["logger.error"]
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["askWithSearch"], "name": "aiService"},
            {"kind": "module_object", "members": ["say"], "name": "client"},
            {"kind": "module_object", "members": ["trackSearch"], "name": "metricsSummaryManager"},
            {"kind": "module_object", "members": ["search"], "name": "searchService"},
        ]
        assert symbol_fact["nearby_tests"] == [
            "src/managers/metrics-summary.test.ts",
            "src/services/search.test.ts",
        ]

    def test_handle_asset_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function handleAsset(
              channel: string,
              eventPayload: ChatEventPayload,
              args: string,
            ): Promise<void> {
              const username = (eventPayload.username || '').toLowerCase();

              if (!args) {
                client.say(channel, `@${eventPayload.username} Provide the asset title`);
                return;
              }

              const ownerId = await mediaService.getOwnerId('room');
              const asset = await mediaService.createAsset(ownerId, 30, args);
              const assetUrl = mediaService.getAssetUrl(asset.id);
              client.say(channel, `Asset created: ${assetUrl}`);
              metricsSummaryManager.trackAsset(username, assetUrl, args, 30);

              if (webhookService.hasWebhook()) {
                await webhookService.sendAsset({
                  url: assetUrl,
                  title: args,
                  creator: username,
                  duration: 30,
                });
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/metrics-summary.test.ts",
            """\
            test('trackAsset increments event count', () => {
              manager.trackAsset('testuser', 'https://asset', 'query', 30);
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/services/webhook.test.ts",
            """\
            test('sendAsset sends payload', async () => {
              await webhookService.sendAsset({ url: 'https://asset' });
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "handleAsset")

        assert "feature.asset" in symbol_fact["domains"]
        assert symbol_fact["observables"] == [
            "client.say",
            "mediaService.createAsset",
            "mediaService.getAssetUrl",
            "mediaService.getOwnerId",
            "metricsSummaryManager.trackAsset",
            "webhookService.sendAsset",
        ]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"},
            {"kind": "module_object", "members": ["createAsset", "getAssetUrl", "getOwnerId"], "name": "mediaService"},
            {"kind": "module_object", "members": ["trackAsset"], "name": "metricsSummaryManager"},
            {"kind": "module_object", "members": ["sendAsset"], "name": "webhookService"},
        ]
        assert symbol_fact["nearby_tests"] == [
            "src/managers/metrics-summary.test.ts",
            "src/services/webhook.test.ts",
        ]

    def test_handle_search_nearby_tests_prioritize_feature_service_before_ai_consumers(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function handleSearch(
              channel: string,
              eventPayload: ChatEventPayload,
              query: string,
            ): Promise<void> {
              const username = (eventPayload.username || '').toLowerCase();

              metricsSummaryManager.trackSearch(username, query);
              const results = await searchService.search(query);
              const response = await aiService.askWithSearch(query, results, username, 'es');
              client.say(channel, `@${eventPayload.username} ${response}`);
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/metrics-summary.test.ts",
            """\
            test('trackSearch increments event count', () => {
              manager.trackSearch('testuser', 'query');
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/services/search.test.ts",
            """\
            test('search service search returns results', async () => {
              const results = await searchService.search('query');
              expect(results).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/assistants/ai.test.ts",
            """\
            test('askWithSearch includes search results in prompt', async () => {
              await aiService.askWithSearch('query', [], 'user', 'es');
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "handleSearch")

        assert symbol_fact["nearby_tests"] == [
            "src/managers/metrics-summary.test.ts",
            "src/services/search.test.ts",
            "src/assistants/ai.test.ts",
        ]
