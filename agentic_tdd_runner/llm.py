"""LLM client helpers for the agent runner."""

import requests


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
    if "thinking_budget_tokens" in llm:
        payload["thinking_budget_tokens"] = llm["thinking_budget_tokens"]
    if include_tools:
        payload["tools"] = config["tools"]
    resp = requests.post(llm["url"], json=payload, timeout=config["timeouts"]["llm_request"])
    resp.raise_for_status()
    return resp.json()
