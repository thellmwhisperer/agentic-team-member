"""Tests for LLM client helpers."""

import requests

from agentic_tdd_runner import llm


def _config(llm_overrides=None):
    return {
        "llm": {
            "model": "test-model",
            "url": "http://localhost:9999/v1/chat/completions",
            "temperature": 0.6,
            **(llm_overrides or {}),
        },
        "timeouts": {"llm_request": 10},
        "tools": [{"type": "function", "function": {"name": "test_tool"}}],
    }


class FakeResponse:
    status_code = 200

    def raise_for_status(self):
        pass

    def json(self):
        return {"choices": [{"message": {"content": "hi"}}]}


def test_chat_completion_includes_thinking_budget_tokens_when_configured(monkeypatch):
    captured = {}

    def capture_post(url, json=None, timeout=None):
        captured["url"] = url
        captured["timeout"] = timeout
        captured["payload"] = json
        return FakeResponse()

    monkeypatch.setattr("agentic_tdd_runner.llm.requests.post", capture_post)

    llm.chat_completion(
        [{"role": "user", "content": "hello"}],
        _config({"thinking_budget_tokens": 0}),
    )

    assert captured["url"] == "http://localhost:9999/v1/chat/completions"
    assert captured["timeout"] == 10
    assert captured["payload"]["thinking_budget_tokens"] == 0
    assert captured["payload"]["tools"] == [{"type": "function", "function": {"name": "test_tool"}}]


def test_chat_completion_omits_thinking_budget_tokens_when_not_configured(monkeypatch):
    captured = {}

    def capture_post(url, json=None, timeout=None):
        captured.update(json)
        return FakeResponse()

    monkeypatch.setattr("agentic_tdd_runner.llm.requests.post", capture_post)

    llm.chat_completion([{"role": "user", "content": "hello"}], _config())

    assert "thinking_budget_tokens" not in captured


def test_chat_completion_omits_tools_when_requested(monkeypatch):
    captured = {}

    def capture_post(url, json=None, timeout=None):
        captured.update(json)
        return FakeResponse()

    monkeypatch.setattr("agentic_tdd_runner.llm.requests.post", capture_post)

    llm.chat_completion(
        [{"role": "user", "content": "hello"}],
        _config(),
        include_tools=False,
    )

    assert "tools" not in captured


def test_is_llm_timeout_error_accepts_requests_timeout():
    assert llm.is_llm_timeout_error(requests.exceptions.ReadTimeout("read timed out")) is True


def test_is_llm_timeout_error_accepts_timeout_text():
    assert llm.is_llm_timeout_error(RuntimeError("operation timed out")) is True


def test_is_llm_timeout_error_rejects_unrelated_errors():
    assert llm.is_llm_timeout_error(RuntimeError("boom")) is False
