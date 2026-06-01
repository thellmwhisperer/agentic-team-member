"""Deterministic intent routing before expensive or noisy tool calls."""

from __future__ import annotations

from dataclasses import dataclass

from agentic_tdd_runner.languages import get_language, plugins
from agentic_tdd_runner.runner_facts import RunnerFacts


@dataclass(frozen=True)
class RouterDecision:
    """A deterministic answer to a model intent."""

    event: str
    message: str
    data: dict


class IntentRouter:
    """Answer known runner/setup questions before the model burns tool calls."""

    def __init__(self, runner_facts: RunnerFacts | None) -> None:
        self.runner_facts = runner_facts
        self.pending_test_file: str | None = None
        self.pending_missing_globals: set[str] = set()
        self.pending_mock_api_file: str | None = None
        self.pending_import_side_effect_file: str | None = None

    def review_tool_call(self, name: str, args: dict) -> RouterDecision | None:
        support = _language_intent_router_support(self.runner_facts)
        review_fn = getattr(support, "intent_router_review_tool_call", None)
        if not callable(review_fn):
            return None
        payload = review_fn(self._state(), name, args, self.runner_facts)
        if not payload:
            return None
        return RouterDecision(
            event=payload["event"],
            message=payload["message"],
            data=payload["data"],
        )

    def observe_tool_result(
        self,
        name: str,
        args: dict,
        result: str,
        *,
        applied: bool | None,
    ) -> None:
        support = _language_intent_router_support(self.runner_facts)
        is_edit_to_path = getattr(support, "intent_router_is_edit_to_path", None)
        if callable(is_edit_to_path):
            if applied is True and is_edit_to_path(name, args, self.pending_test_file):
                self.pending_test_file = None
                self.pending_missing_globals.clear()
            if applied is True and is_edit_to_path(name, args, self.pending_mock_api_file):
                self.pending_mock_api_file = None
            if applied is True and is_edit_to_path(
                name, args, self.pending_import_side_effect_file
            ):
                self.pending_import_side_effect_file = None

        observe_fn = getattr(support, "intent_router_observe_tool_result", None)
        if not callable(observe_fn):
            return
        observations = observe_fn(name, args, result, self.runner_facts)
        missing = observations.get("missing_globals") or {}
        if missing.get("test_file") and missing.get("names"):
            self.pending_test_file = missing["test_file"]
            self.pending_missing_globals = set(missing["names"])
        if observations.get("mock_api_file"):
            self.pending_mock_api_file = observations["mock_api_file"]
        if observations.get("import_side_effect_file"):
            self.pending_import_side_effect_file = observations["import_side_effect_file"]

    def _state(self) -> dict:
        return {
            "pending_test_file": self.pending_test_file,
            "pending_missing_globals": set(self.pending_missing_globals),
            "pending_mock_api_file": self.pending_mock_api_file,
            "pending_import_side_effect_file": self.pending_import_side_effect_file,
        }


def _language_intent_router_support(runner_facts: RunnerFacts | None) -> object | None:
    for path in (
        getattr(runner_facts, "recommended_test_file", None),
        getattr(runner_facts, "source_file", None),
    ):
        if isinstance(path, str) and path:
            language = get_language(path)
            if language:
                return language
    test_runner = getattr(runner_facts, "test_runner", None)
    if isinstance(test_runner, str) and test_runner:
        for plugin in plugins():
            supports_fn = getattr(plugin, "supports_test_runner", None)
            if callable(supports_fn) and supports_fn(test_runner):
                return plugin
    return None
