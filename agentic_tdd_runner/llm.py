"""LLM client helpers for the agent runner."""

import requests


def _coerce_int(value) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        value = value.strip()
        if value.lstrip("-").isdigit():
            return int(value)
    return None


def resolve_thinking_budget_tokens(config: dict) -> int | None:
    """Resolve the thinking budget for the current runtime step.

    Static ``[llm].thinking_budget_tokens`` remains supported. When
    ``[llm.thinking_budget].enabled`` is true, the runtime phase set by the
    agent loop selects a per-phase budget instead.
    """
    llm = config.get("llm", {})
    static_budget = _coerce_int(llm.get("thinking_budget_tokens"))
    budget_cfg = llm.get("thinking_budget", {})
    if not isinstance(budget_cfg, dict) or not budget_cfg.get("enabled", False):
        return static_budget

    runtime = config.get("_runtime", {})
    phase = str(runtime.get("thinking_phase") or "default")

    if runtime.get("completion_rejected") and _coerce_int(budget_cfg.get("recover")) is not None:
        return _coerce_int(budget_cfg.get("recover"))

    late_ratio_raw = budget_cfg.get("late_step_ratio", 0.7)
    try:
        late_ratio = float(late_ratio_raw)
    except (TypeError, ValueError):
        late_ratio = 0.7
    step = _coerce_int(runtime.get("step"))
    max_steps = _coerce_int(runtime.get("max_steps"))
    if (
        step is not None
        and max_steps
        and late_ratio > 0
        and step / max_steps >= late_ratio
        and _coerce_int(budget_cfg.get("late")) is not None
    ):
        return _coerce_int(budget_cfg.get("late"))

    phase_budget = _coerce_int(budget_cfg.get(phase))
    if phase_budget is not None:
        return phase_budget

    default_budget = _coerce_int(budget_cfg.get("default"))
    if default_budget is not None:
        return default_budget

    return static_budget


def is_llm_timeout_error(exc: Exception) -> bool:
    if isinstance(exc, requests.exceptions.Timeout):
        return True
    return "timed out" in str(exc).lower()


def chat_completion(messages: list, config: dict, include_tools: bool = True) -> dict:
    llm = config["llm"]
    payload = {
        "model": llm["model"],
        "messages": messages,
        "temperature": llm.get("temperature", 0.6),
        "top_p": llm.get("top_p", 0.95),
        "top_k": llm.get("top_k", 20),
        "cache_prompt": True,
    }
    thinking_budget_tokens = resolve_thinking_budget_tokens(config)
    if thinking_budget_tokens is not None:
        payload["thinking_budget_tokens"] = thinking_budget_tokens
    if include_tools:
        payload["tools"] = config["tools"]
    resp = requests.post(llm["url"], json=payload, timeout=config["timeouts"]["llm_request"])
    resp.raise_for_status()
    return resp.json()
