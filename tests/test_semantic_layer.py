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
            "src/events/client.ts",
            """\
            export function initEventBus(): void {
              client.on('message', routeMessage);
            }

            async function routeMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.startsWith('@dispatcher')) {
                client.say('#room', 'hello');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/mentions.test.ts",
            """\
            import { test, expect } from 'bun:test';

            test('mention routing', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))

        assert index["version"] == 5
        assert "files" in index
        assert "symbols" in index
        assert "candidates" in index

        file_fact = _get_file(index, "src/events/client.ts")
        assert file_fact["language"] == "typescript"
        assert "events" in file_fact["domains"]
        assert "event.message" in file_fact["domains"]
        assert "routing.mention" in file_fact["domains"]
        assert file_fact["symbols"] == ["initEventBus", "routeMessage"]
        assert file_fact["nearby_tests"] == ["src/events/mentions.test.ts"]

        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")
        assert symbol_fact["qualified_name"] == "routeMessage"
        assert "events" in symbol_fact["domains"]
        assert "event.message" in symbol_fact["domains"]
        assert "routing.mention" in symbol_fact["domains"]
        assert all(not domain.startswith("events.") for domain in symbol_fact["domains"])
        assert symbol_fact["nearby_tests"] == ["src/events/mentions.test.ts"]
        assert symbol_fact["calls"] == ["client.say"]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"}
        ]
        assert symbol_fact["routes"] == [
            {
                "condition": "lower.startsWith('@dispatcher')",
                "guard_patterns": ["startsWith('@dispatcher')"],
                "label": None,
                "triggers": ["@dispatcher"],
            }
        ]

    def test_nearby_tests_prefers_symbol_and_source_stem_matches(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            export function initEventBus(): void {
              client.on('message', routeMessage);
            }

            async function routeMessage(message: string): Promise<void> {
              if (message.startsWith('@dispatcher')) {
                client.say('#room', 'hello');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/client.test.ts",
            """\
            import { routeMessage } from './client';

            test('client test', () => {
              expect(routeMessage).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/events/routeMessage.test.ts",
            """\
            import { routeMessage } from './client';

            test('routeMessage test', () => {
              expect(routeMessage).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/events/mentions.test.ts",
            """\
            test('mention routing', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")

        assert symbol_fact["nearby_tests"] == [
            "src/events/routeMessage.test.ts",
            "src/events/client.test.ts",
            "src/events/mentions.test.ts",
        ]

    def test_nearby_tests_uses_semantic_overlap_when_no_symbol_named_test_exists(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function routeMessage(message: string): Promise<void> {
              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();
              const isMention = lower.startsWith('@dispatcher');
              const isOyeDispatcher = lower.startsWith('!dispatch');

              if (isOyeDispatcher || isMention) {
                const argsLowerCmd = trimmed.toLowerCase();
                if (argsLowerCmd === 'habla') {
                  setDiceMode(true);
                }
                if (argsLowerCmd === 'calla' || argsLowerCmd === 'callate') {
                  setDiceMode(false);
                }
                if (getVoiceUser('teseo')?.ttsPrefix) {
                  client.say('#room', 'hello');
                }
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/dice-mode.test.ts",
            """\
            test('habla command should activate dice mode', () => {
              expect(true).toBe(true);
            });

            test('calla command should deactivate dice mode', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/events/voice-users.test.ts",
            """\
            test('voice users should have tts prefix', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/events/reconnect.test.ts",
            """\
            test('reconnect client disables auto reconnect', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")

        assert symbol_fact["nearby_tests"] == [
            "src/events/dice-mode.test.ts",
            "src/events/voice-users.test.ts",
            "src/events/reconnect.test.ts",
        ]

    def test_nearby_tests_deemphasizes_generic_code_tokens(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function routeMessage(message: string): Promise<void> {
              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();

              if (getVoiceUser('teseo')?.ttsPrefix) {
                setDiceMode(true);
                client.say('#room', 'hello');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/reconnect.test.ts",
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
            "src/events/voice-users.test.ts",
            """\
            test('voice users should have tts prefix', () => {
              const voiceUsers = [{ username: 'teseo', ttsPrefix: '!dice' }];
              expect(voiceUsers[0]?.ttsPrefix).toBe('!dice');
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")

        assert symbol_fact["nearby_tests"] == [
            "src/events/voice-users.test.ts",
            "src/events/reconnect.test.ts",
        ]

    def test_nearby_tests_ignores_cross_module_noise_without_strong_seams(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function routeMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.startsWith('@dispatcher')) {
                client.say('#room', 'hello');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/dice-mode.test.ts",
            """\
            test('dice mode nearby', () => {
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
        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")

        assert symbol_fact["nearby_tests"] == ["src/events/dice-mode.test.ts"]


class TestSemanticLayerGoldens:
    def test_handle_message_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            export function initEventBus(): void {
              client.on('message', routeMessage);
            }

            async function routeMessage(
              channel: string,
              eventPayload: ChatEventPayload,
              message: string,
              self: boolean,
            ): Promise<void> {
              if (self) return;

              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();
              // --- @dispatcher or !dispatch ---
              const isMention = lower.startsWith('@dispatcher');
              const isOyeDispatcher = lower.startsWith('!dispatch');

              if (isOyeDispatcher || isMention) {
                client.say(channel, `@${eventPayload.username} hello illo`);
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/events/routeMessage.test.ts",
            """\
            import { test, expect } from 'bun:test';

            test('routeMessage', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")

        assert "event.message" in symbol_fact["domains"]
        assert "routing.mention" in symbol_fact["domains"]
        assert "routing.command" in symbol_fact["domains"]
        assert symbol_fact["entrypoints"] == [
            {
                "kind": "event",
                "emitter": "client.on",
                "name": "message",
                "line": 2,
            }
        ]
        assert symbol_fact["triggers"] == ["!dispatch", "@dispatcher"]
        assert symbol_fact["guard_patterns"] == [
            "startsWith('!dispatch')",
            "startsWith('@dispatcher')",
        ]
        assert symbol_fact["observables"] == ["client.say"]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"},
        ]
        assert symbol_fact["nearby_tests"] == ["src/events/routeMessage.test.ts"]
        assert symbol_fact["routes"] == [
            {
                "condition": "isOyeDispatcher || isMention",
                "guard_patterns": [
                    "startsWith('!dispatch')",
                    "startsWith('@dispatcher')",
                ],
                "label": "@dispatcher or !dispatch",
                "triggers": ["!dispatch", "@dispatcher"],
            }
        ]

    def test_handle_message_triggers_exclude_nested_branch_literals(self, tmp_path):
        _write_file(
            tmp_path,
            "src/events/client.ts",
            """\
            async function routeMessage(message: string): Promise<void> {
              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();

              if (lower === '!mytasks') {
                client.say('#room', 'ok');
                return;
              }

              // --- @dispatcher or !dispatch ---
              const isMention = lower.startsWith('@dispatcher');
              const isOyeDispatcher = lower.startsWith('!dispatch');

              if (isOyeDispatcher || isMention) {
                const argsLowerCmd = trimmed.toLowerCase();
                if (argsLowerCmd === 'calla' || argsLowerCmd === 'callate') {
                  client.say('#room', 'done');
                }
              }
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/events/client.ts", "routeMessage")

        assert "routing.mention" in symbol_fact["domains"]
        assert "routing.command" in symbol_fact["domains"]
        assert symbol_fact["triggers"] == ["!dispatch", "!mytasks", "@dispatcher"]
        assert symbol_fact["guard_patterns"] == [
            "== '!mytasks'",
            "startsWith('!dispatch')",
            "startsWith('@dispatcher')",
        ]
        assert all("calla" not in route["condition"] for route in symbol_fact["routes"])

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
              client.say(channel, `@${username} ${months}`);
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
            "client.say",
            "metricsSummaryManager.trackRenewal",
        ]
        assert symbol_fact["telemetry"] == ["logger.event"]
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"},
            {"kind": "module_object", "members": ["trackRenewal"], "name": "metricsSummaryManager"},
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
            "src/personality/ai.test.ts",
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
            "src/personality/ai.test.ts",
        ]
