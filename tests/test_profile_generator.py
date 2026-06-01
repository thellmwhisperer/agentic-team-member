"""Tests for deterministic `.atm/profile.toml` generation."""

import json
import textwrap

from agentic_tdd_runner import atm, profile_generator
from agentic_tdd_runner.profile_generator import (
    infer_repo_profile,
    render_repo_profile,
)
from agentic_tdd_runner.repo_profile import RunnerProfile, read_repo_profile


def _write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))


def _write_event_dependency(root, module_name, event_name, args):
    module_root = root / "node_modules" / module_name
    _write(module_root / "package.json", json.dumps({"main": "index.js"}))
    declarations = "\n".join(f"const {arg} = message.{arg};" for arg in args)
    _write(
        module_root / "index.js",
        f"""
        function dispatch(message) {{
          {declarations}
          bus.emit('{event_name}', {", ".join(args)});
        }}
        """,
    )


def _write_callback_project(root):
    _write(
        root / "package.json",
        json.dumps(
            {
                "packageManager": "bun@1.3.5",
                "scripts": {
                    "test": "bun test",
                    "typecheck": "tsc --noEmit",
                },
                "dependencies": {"@example/event-bus": "1.0.0"},
                "devDependencies": {
                    "@types/example__event-bus": "1.0.0",
                    "typescript": "5.9.3",
                },
            },
        ),
    )
    (root / "bun.lock").write_text("")
    _write(
        root / "src" / "events" / "processor.ts",
        """
        import { bus } from '@example/event-bus';
        import { getStreamSummaryManager } from '../managers/stream-summary';

        export function registerHandlers(): void {
          bus.on('renewal', processRenewal);
        }

        export function processRenewal(channel: string, username: string, count: number): void {
          getStreamSummaryManager().trackRenewal(username, count);
        }
        """,
    )
    _write(
        root / "node_modules" / "@example" / "event-bus" / "package.json",
        json.dumps({"main": "index.js"}),
    )
    _write(
        root / "node_modules" / "@example" / "event-bus" / "index.js",
        "module.exports = require('./src/runtime');",
    )
    _write(
        root / "node_modules" / "@example" / "event-bus" / "src" / "runtime.js",
        """
        function dispatchRenewal(message) {
          const channel = "#channel";
          const username = message.user.name;
          const retryCount = message.retry.count || 0;
          const payload = message.payload;
          const delivery = message.delivery;
          bus.emit('renewal', channel, username, retryCount, payload, delivery);
        }
        """,
    )
    _write(
        root / "node_modules" / "@types" / "example__event-bus" / "index.d.ts",
        """
        export interface Events {
          renewal(
            channel: string,
            username: string,
            totalCount: number,
            payload: RenewalPayload,
            delivery: DeliveryOptions,
          ): void;
        }

        export interface RenewalPayload {
          id: string;
        }

        export interface DeliveryOptions {
          mode?: string;
        }
        """,
    )


def test_infers_callback_contract_from_dependency_not_handler_signature(tmp_path):
    _write_callback_project(tmp_path)

    profile = infer_repo_profile(tmp_path)
    event_contract = profile.dependency_contracts[0].events[0]
    rendered = render_repo_profile(profile)

    assert profile.runner.test_runner == "bun:test"
    assert profile.runner.test_command == "bun test"
    assert profile.runner.typecheck_command == "bun run typecheck"
    assert profile.event_frameworks[0].module == "@example/event-bus"
    assert profile.event_frameworks[0].dependency_contract == "example-event-bus-events"
    assert event_contract.name == "renewal"
    assert event_contract.args == (
        "channel",
        "username",
        "retryCount",
        "payload",
        "delivery",
    )
    assert ("retryCount", "message.retry.count || 0") in event_contract.arg_sources
    assert 'args = ["channel", "username", "retryCount", "payload", "delivery"]' in rendered
    assert 'args = ["channel", "username", "count"]' not in rendered
    assert '"node_modules/@example/event-bus/src/runtime.js"' in rendered
    assert '"node_modules/@example/event-bus/index.js"' not in rendered

    path = tmp_path / ".atm" / "profile.toml"
    _write(path, rendered)
    reparsed = read_repo_profile(path)
    assert reparsed.dependency_contracts[0].events[0].args == event_contract.args


def test_event_registrations_are_attributed_to_the_referenced_import(tmp_path):
    _write(
        tmp_path / "package.json",
        json.dumps(
            {
                "dependencies": {
                    "@example/event-bus": "1.0.0",
                    "@example/metrics": "1.0.0",
                },
            },
        ),
    )
    _write(
        tmp_path / "src" / "events" / "processor.ts",
        """
        import { bus } from '@example/event-bus';
        import { metrics } from '@example/metrics';

        bus.on('renewal', processRenewal);
        """,
    )
    _write_event_dependency(tmp_path, "@example/event-bus", "renewal", ["username"])
    _write_event_dependency(tmp_path, "@example/metrics", "renewal", ["metricName"])

    profile = infer_repo_profile(tmp_path)

    assert [framework.module for framework in profile.event_frameworks] == ["@example/event-bus"]
    assert [contract.module for contract in profile.dependency_contracts] == ["@example/event-bus"]


def test_event_registration_attribution_follows_imported_factory_alias(tmp_path):
    _write(
        tmp_path / "package.json",
        json.dumps({"dependencies": {"@example/event-client": "1.0.0"}}),
    )
    _write(
        tmp_path / "src" / "events" / "processor.ts",
        """
        import EventClient from '@example/event-client';

        const client = new EventClient.Client();
        client.once('ready', handleReady);
        """,
    )
    _write_event_dependency(tmp_path, "@example/event-client", "ready", ["payload"])

    profile = infer_repo_profile(tmp_path)

    assert [framework.module for framework in profile.event_frameworks] == ["@example/event-client"]
    assert profile.dependency_contracts[0].events[0].name == "ready"


def test_event_registration_attribution_follows_imported_type_annotation(tmp_path):
    _write(
        tmp_path / "package.json",
        json.dumps({"dependencies": {"@example/event-client": "1.0.0"}}),
    )
    _write(
        tmp_path / "src" / "events" / "processor.ts",
        """
        import EventClient from '@example/event-client';

        let client: EventClient.Client;
        client.on('ready', handleReady);
        """,
    )
    _write_event_dependency(tmp_path, "@example/event-client", "ready", ["payload"])

    profile = infer_repo_profile(tmp_path)

    assert [framework.module for framework in profile.event_frameworks] == ["@example/event-client"]
    assert profile.dependency_contracts[0].events[0].name == "ready"


def test_scoped_dependency_contract_ids_include_scope(tmp_path):
    _write(
        tmp_path / "package.json",
        json.dumps(
            {
                "dependencies": {
                    "@scope-a/events": "1.0.0",
                    "@scope-b/events": "1.0.0",
                },
            },
        ),
    )
    _write(
        tmp_path / "src" / "events" / "processor.ts",
        """
        import { alpha } from '@scope-a/events';
        import { beta } from '@scope-b/events';

        alpha.on('ready', handleAlpha);
        beta.once('ready', handleBeta);
        """,
    )
    _write_event_dependency(tmp_path, "@scope-a/events", "ready", ["alphaPayload"])
    _write_event_dependency(tmp_path, "@scope-b/events", "ready", ["betaPayload"])

    profile = infer_repo_profile(tmp_path)

    assert [framework.id for framework in profile.event_frameworks] == [
        "scope-a-events",
        "scope-b-events",
    ]
    assert [contract.id for contract in profile.dependency_contracts] == [
        "scope-a-events-events",
        "scope-b-events-events",
    ]


def test_js_ts_source_iteration_prunes_skipped_directories(tmp_path, monkeypatch):
    (tmp_path / "src").mkdir()
    (tmp_path / "node_modules").mkdir()
    seen_after_prune = []

    def fake_walk(root):
        dirnames = ["src", "node_modules", "dist"]
        yield str(root), dirnames, []
        seen_after_prune.append(tuple(dirnames))
        if "node_modules" in dirnames:
            yield str(root / "node_modules"), [], ["poison.ts"]
        if "dist" in dirnames:
            yield str(root / "dist"), [], ["bundle.ts"]
        yield str(root / "src"), [], ["handler.ts"]

    monkeypatch.setattr(profile_generator.os, "walk", fake_walk)

    files = profile_generator._iter_js_ts_source_files(tmp_path)

    assert seen_after_prune == [("src",)]
    assert files == [tmp_path / "src" / "handler.ts"]


def test_profile_generator_does_not_run_js_ts_detector_without_node_manifest(tmp_path):
    _write(tmp_path / "pyproject.toml", "[project]\nname = 'service'\n")
    _write(
        tmp_path / "src" / "shadow.js",
        """
        import { bus } from '@example/event-bus';

        export function registerHandlers() {
          bus.on('renewal', processRenewal);
        }
        """,
    )
    _write(
        tmp_path / "node_modules" / "@example" / "event-bus" / "package.json",
        json.dumps({"main": "index.js"}),
    )
    _write(
        tmp_path / "node_modules" / "@example" / "event-bus" / "index.js",
        """
        function dispatchRenewal(message) {
          const channel = "#channel";
          const username = message.user.name;
          const retryCount = message.retry.count || 0;
          bus.emit('renewal', channel, username, retryCount);
        }
        """,
    )

    profile = infer_repo_profile(tmp_path)

    assert profile.runner == RunnerProfile()
    assert profile.event_frameworks == ()
    assert profile.dependency_contracts == ()
    assert profile.import_expectations == ()


def test_profile_print_does_not_write_profile(tmp_path, capsys):
    _write_callback_project(tmp_path)

    code = atm.main(["profile", "print", "--workdir", str(tmp_path)])

    assert code == 0
    assert not (tmp_path / ".atm" / "profile.toml").exists()
    assert 'args = ["channel", "username", "retryCount", "payload", "delivery"]' in capsys.readouterr().out

    generate_code = atm.main(["profile", "generate", "--workdir", str(tmp_path)])

    assert generate_code == 0
    assert "Wrote" in capsys.readouterr().out
    assert (tmp_path / ".atm" / "profile.toml").is_file()
    assert read_repo_profile(tmp_path / ".atm" / "profile.toml").runner.test_runner == "bun:test"


def test_profile_generate_refuses_existing_profile_and_update_rewrites(tmp_path, capsys):
    _write_callback_project(tmp_path)
    stale = tmp_path / ".atm" / "profile.toml"
    _write(
        stale,
        """
        [profile]
        schema_version = "repo-profile.v1"

        [[dependency_contracts]]
        id = "event-bus-events"
        module = "@example/event-bus"
        events = [
          { name = "renewal", args = ["channel", "username", "count"] },
        ]
        """,
    )

    generate_code = atm.main(["profile", "generate", "--workdir", str(tmp_path)])
    assert generate_code == 2
    assert 'args = ["channel", "username", "count"]' in stale.read_text()

    update_code = atm.main(["profile", "update", "--workdir", str(tmp_path)])

    assert update_code == 0
    assert "Wrote" in capsys.readouterr().out
    assert 'args = ["channel", "username", "retryCount", "payload", "delivery"]' in stale.read_text()
