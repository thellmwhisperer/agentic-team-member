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
            "src/twitch/client.ts",
            """\
            export function initTwitch(): void {
              client.on('message', handleMessage);
            }

            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.startsWith('@manolitozurrapa')) {
                client.say('#canal', 'hola');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/mentions.test.ts",
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

        file_fact = _get_file(index, "src/twitch/client.ts")
        assert file_fact["language"] == "typescript"
        assert "twitch" in file_fact["domains"]
        assert "event.message" in file_fact["domains"]
        assert "routing.mention" in file_fact["domains"]
        assert file_fact["symbols"] == ["handleMessage", "initTwitch"]
        assert file_fact["nearby_tests"] == ["src/twitch/mentions.test.ts"]

        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")
        assert symbol_fact["qualified_name"] == "handleMessage"
        assert "twitch" in symbol_fact["domains"]
        assert "event.message" in symbol_fact["domains"]
        assert "routing.mention" in symbol_fact["domains"]
        assert all(not domain.startswith("twitch.") for domain in symbol_fact["domains"])
        assert symbol_fact["nearby_tests"] == ["src/twitch/mentions.test.ts"]
        assert symbol_fact["calls"] == ["client.say"]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"}
        ]
        assert symbol_fact["routes"] == [
            {
                "condition": "lower.startsWith('@manolitozurrapa')",
                "guard_patterns": ["startsWith('@manolitozurrapa')"],
                "label": None,
                "triggers": ["@manolitozurrapa"],
            }
        ]

    def test_nearby_tests_prefers_symbol_and_source_stem_matches(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            export function initTwitch(): void {
              client.on('message', handleMessage);
            }

            async function handleMessage(message: string): Promise<void> {
              if (message.startsWith('@manolitozurrapa')) {
                client.say('#canal', 'hola');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/client.test.ts",
            """\
            import { handleMessage } from './client';

            test('client test', () => {
              expect(handleMessage).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/handleMessage.test.ts",
            """\
            import { handleMessage } from './client';

            test('handleMessage test', () => {
              expect(handleMessage).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/mentions.test.ts",
            """\
            test('mention routing', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")

        assert symbol_fact["nearby_tests"] == [
            "src/twitch/handleMessage.test.ts",
            "src/twitch/client.test.ts",
            "src/twitch/mentions.test.ts",
        ]

    def test_nearby_tests_uses_semantic_overlap_when_no_symbol_named_test_exists(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();
              const isMention = lower.startsWith('@manolitozurrapa');
              const isOyeManolito = lower.startsWith('!oyemanolito');

              if (isOyeManolito || isMention) {
                const argsLowerCmd = trimmed.toLowerCase();
                if (argsLowerCmd === 'habla') {
                  setDiceMode(true);
                }
                if (argsLowerCmd === 'calla' || argsLowerCmd === 'callate') {
                  setDiceMode(false);
                }
                if (getVoiceUser('teseo')?.ttsPrefix) {
                  client.say('#canal', 'hola');
                }
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/dice-mode.test.ts",
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
            "src/twitch/voice-users.test.ts",
            """\
            test('voice users should have tts prefix', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/reconnect.test.ts",
            """\
            test('reconnect client disables auto reconnect', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")

        assert symbol_fact["nearby_tests"] == [
            "src/twitch/dice-mode.test.ts",
            "src/twitch/voice-users.test.ts",
            "src/twitch/reconnect.test.ts",
        ]

    def test_nearby_tests_deemphasizes_generic_code_tokens(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();

              if (getVoiceUser('teseo')?.ttsPrefix) {
                setDiceMode(true);
                client.say('#canal', 'hola');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/reconnect.test.ts",
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
            "src/twitch/voice-users.test.ts",
            """\
            test('voice users should have tts prefix', () => {
              const voiceUsers = [{ username: 'teseo', ttsPrefix: '!dice' }];
              expect(voiceUsers[0]?.ttsPrefix).toBe('!dice');
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")

        assert symbol_fact["nearby_tests"] == [
            "src/twitch/voice-users.test.ts",
            "src/twitch/reconnect.test.ts",
        ]

    def test_nearby_tests_ignores_cross_module_noise_without_strong_seams(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const lower = message.toLowerCase();
              if (lower.startsWith('@manolitozurrapa')) {
                client.say('#canal', 'hola');
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/dice-mode.test.ts",
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
            "src/managers/stream-summary.test.ts",
            """\
            test('stream summary noise', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")

        assert symbol_fact["nearby_tests"] == ["src/twitch/dice-mode.test.ts"]


class TestSemanticLayerGoldens:
    def test_handle_message_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            export function initTwitch(): void {
              client.on('message', handleMessage);
            }

            async function handleMessage(
              channel: string,
              userstate: ChatUserstate,
              message: string,
              self: boolean,
            ): Promise<void> {
              if (self) return;

              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();
              // --- @manolitozurrapa or !oyemanolito ---
              const isMention = lower.startsWith('@manolitozurrapa');
              const isOyeManolito = lower.startsWith('!oyemanolito');

              if (isOyeManolito || isMention) {
                client.say(channel, `@${userstate.username} hola illo`);
              }
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/handleMessage.test.ts",
            """\
            import { test, expect } from 'bun:test';

            test('handleMessage', () => {
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")

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
        assert symbol_fact["triggers"] == ["!oyemanolito", "@manolitozurrapa"]
        assert symbol_fact["guard_patterns"] == [
            "startsWith('!oyemanolito')",
            "startsWith('@manolitozurrapa')",
        ]
        assert symbol_fact["observables"] == ["client.say"]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"},
        ]
        assert symbol_fact["nearby_tests"] == ["src/twitch/handleMessage.test.ts"]
        assert symbol_fact["routes"] == [
            {
                "condition": "isOyeManolito || isMention",
                "guard_patterns": [
                    "startsWith('!oyemanolito')",
                    "startsWith('@manolitozurrapa')",
                ],
                "label": "@manolitozurrapa or !oyemanolito",
                "triggers": ["!oyemanolito", "@manolitozurrapa"],
            }
        ]

    def test_handle_message_triggers_exclude_nested_branch_literals(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleMessage(message: string): Promise<void> {
              const trimmed = message.trim();
              const lower = trimmed.toLowerCase();

              if (lower === '!mismensajes') {
                client.say('#canal', 'ok');
                return;
              }

              // --- @manolitozurrapa or !oyemanolito ---
              const isMention = lower.startsWith('@manolitozurrapa');
              const isOyeManolito = lower.startsWith('!oyemanolito');

              if (isOyeManolito || isMention) {
                const argsLowerCmd = trimmed.toLowerCase();
                if (argsLowerCmd === 'calla' || argsLowerCmd === 'callate') {
                  client.say('#canal', 'vale');
                }
              }
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleMessage")

        assert "routing.mention" in symbol_fact["domains"]
        assert "routing.command" in symbol_fact["domains"]
        assert symbol_fact["triggers"] == ["!mismensajes", "!oyemanolito", "@manolitozurrapa"]
        assert symbol_fact["guard_patterns"] == [
            "== '!mismensajes'",
            "startsWith('!oyemanolito')",
            "startsWith('@manolitozurrapa')",
        ]
        assert all("calla" not in route["condition"] for route in symbol_fact["routes"])

    def test_handle_resub_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            export function initTwitch(): void {
              client.on('resub', handleResub);
            }

            function handleResub(channel: string, username: string, months: number): void {
              logger.event('resub', { username, months });
              streamSummaryManager.trackResub(username, months);
              client.say(channel, `@${username} ${months}`);
            }
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleResub")

        assert "event.resub" in symbol_fact["domains"]
        assert "feature.resub" in symbol_fact["domains"]
        assert symbol_fact["entrypoints"] == [
            {
                "kind": "event",
                "emitter": "client.on",
                "name": "resub",
                "line": 2,
            }
        ]
        assert symbol_fact["triggers"] == []
        assert symbol_fact["observables"] == [
            "client.say",
            "streamSummaryManager.trackResub",
        ]
        assert symbol_fact["telemetry"] == ["logger.event"]
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"},
            {"kind": "module_object", "members": ["trackResub"], "name": "streamSummaryManager"},
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
                  this.executeAction('piropo', ['reina'], sendMessage);
                }, 1000);

                this.actionTimers.set('piropo', timer);
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
            "src/twitch/client.ts",
            """\
            function handleResub(channel: string, username: string, months: number): void {
              logger.event('resub', { username, months });
              streamSummaryManager.trackResub(username, months);
              client.say(channel, `@${username} ${months}`);
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/twitch/token.test.ts",
            """\
            test('token manager boilerplate', () => {
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/stream-summary.test.ts",
            """\
            test('trackResub increments event count', () => {
              manager.trackResub('testuser', 12);
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
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleResub")

        assert symbol_fact["nearby_tests"] == [
            "src/managers/stream-summary.test.ts",
            "src/twitch/token.test.ts",
        ]

    def test_handle_search_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleSearch(
              channel: string,
              userstate: ChatUserstate,
              query: string,
            ): Promise<void> {
              const username = (userstate.username || '').toLowerCase();

              if (!searchService.hasApiKey()) {
                logger.error('search_no_api_key', new Error('missing'));
                client.say(channel, `@${userstate.username} No hay API de búsqueda configurada illo`);
                return;
              }

              streamSummaryManager.trackSearch(username, query);
              const results = await searchService.search(query);
              const response = await aiService.askWithSearch(query, results, username, 'es');
              client.say(channel, `@${userstate.username} ${response}`);
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/stream-summary.test.ts",
            """\
            test('trackSearch increments event count', () => {
              manager.trackSearch('testuser', 'algo');
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/services/search.test.ts",
            """\
            test('search service search returns results', async () => {
              const results = await searchService.search('algo');
              expect(results).toBeDefined();
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleSearch")

        assert "feature.search" in symbol_fact["domains"]
        assert symbol_fact["observables"] == [
            "aiService.askWithSearch",
            "client.say",
            "searchService.search",
            "streamSummaryManager.trackSearch",
        ]
        assert symbol_fact["telemetry"] == ["logger.error"]
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["askWithSearch"], "name": "aiService"},
            {"kind": "module_object", "members": ["say"], "name": "client"},
            {"kind": "module_object", "members": ["search"], "name": "searchService"},
            {"kind": "module_object", "members": ["trackSearch"], "name": "streamSummaryManager"},
        ]
        assert symbol_fact["nearby_tests"] == [
            "src/managers/stream-summary.test.ts",
            "src/services/search.test.ts",
        ]

    def test_handle_clip_semantic_facts(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleClip(
              channel: string,
              userstate: ChatUserstate,
              args: string,
            ): Promise<void> {
              const username = (userstate.username || '').toLowerCase();

              if (!args) {
                client.say(channel, `@${userstate.username} Dime el título del clip`);
                return;
              }

              const broadcasterId = await twitchService.getBroadcasterId('canal');
              const clip = await twitchService.createClip(broadcasterId, 30, args);
              const clipUrl = twitchService.getClipUrl(clip.id);
              client.say(channel, `Clip creado: ${clipUrl}`);
              streamSummaryManager.trackClip(username, clipUrl, args, 30);

              if (discordService.hasWebhook()) {
                await discordService.sendClip({
                  url: clipUrl,
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
            "src/managers/stream-summary.test.ts",
            """\
            test('trackClip increments event count', () => {
              manager.trackClip('testuser', 'https://clip', 'algo', 30);
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/services/discord.test.ts",
            """\
            test('sendClip sends payload', async () => {
              await discordService.sendClip({ url: 'https://clip' });
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleClip")

        assert "feature.clip" in symbol_fact["domains"]
        assert symbol_fact["observables"] == [
            "client.say",
            "discordService.sendClip",
            "streamSummaryManager.trackClip",
            "twitchService.createClip",
            "twitchService.getBroadcasterId",
            "twitchService.getClipUrl",
        ]
        assert symbol_fact["telemetry"] == []
        assert symbol_fact["test_seams"] == [
            {"kind": "module_object", "members": ["say"], "name": "client"},
            {"kind": "module_object", "members": ["sendClip"], "name": "discordService"},
            {"kind": "module_object", "members": ["trackClip"], "name": "streamSummaryManager"},
            {"kind": "module_object", "members": ["createClip", "getBroadcasterId", "getClipUrl"], "name": "twitchService"},
        ]
        assert symbol_fact["nearby_tests"] == [
            "src/managers/stream-summary.test.ts",
            "src/services/discord.test.ts",
        ]

    def test_handle_search_nearby_tests_prioritize_feature_service_before_ai_consumers(self, tmp_path):
        _write_file(
            tmp_path,
            "src/twitch/client.ts",
            """\
            async function handleSearch(
              channel: string,
              userstate: ChatUserstate,
              query: string,
            ): Promise<void> {
              const username = (userstate.username || '').toLowerCase();

              streamSummaryManager.trackSearch(username, query);
              const results = await searchService.search(query);
              const response = await aiService.askWithSearch(query, results, username, 'es');
              client.say(channel, `@${userstate.username} ${response}`);
            }
            """,
        )
        _write_file(
            tmp_path,
            "src/managers/stream-summary.test.ts",
            """\
            test('trackSearch increments event count', () => {
              manager.trackSearch('testuser', 'algo');
              expect(true).toBe(true);
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/services/search.test.ts",
            """\
            test('search service search returns results', async () => {
              const results = await searchService.search('algo');
              expect(results).toBeDefined();
            });
            """,
        )
        _write_file(
            tmp_path,
            "src/personality/ai.test.ts",
            """\
            test('askWithSearch includes search results in prompt', async () => {
              await aiService.askWithSearch('algo', [], 'user', 'es');
              expect(true).toBe(true);
            });
            """,
        )

        index = build_semantic_index(str(tmp_path))
        symbol_fact = _get_symbol(index, "src/twitch/client.ts", "handleSearch")

        assert symbol_fact["nearby_tests"] == [
            "src/managers/stream-summary.test.ts",
            "src/services/search.test.ts",
            "src/personality/ai.test.ts",
        ]
