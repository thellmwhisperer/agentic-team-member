"""Prompt construction helpers for the agent runner."""


def build_system_prompt(base_system_prompt: str, episode: dict | None = None) -> str:
    """Return the system prompt, optionally enriched with episode cookbook text."""
    system_prompt = base_system_prompt.strip()
    if episode:
        system_prompt = f"{system_prompt}\n\n{episode['cookbook_text']}"
    return system_prompt


def function_line_hint(episode: dict) -> str:
    """Return a line hint only when the cookbook matched a real definition."""
    rng = episode.get("function_line_range") or {}
    if rng.get("source") == "definition" and rng.get("start") and rng.get("end"):
        return f" (lines {rng['start']}-{rng['end']})"
    return ""


def build_phase1_message(episode: dict, issue_text_for_model: str) -> str:
    """Build the first user message for phased source/symbol runs."""
    line_hint = function_line_hint(episode)
    return (
        f"Read {episode['source_file']} and understand the bug below. "
        f"Focus on the function `{episode['target_symbol']}`{line_hint}. "
        f"Then create a failing test in {episode['test_file']} that reproduces it.\n\n"
        f"Bug:\n{issue_text_for_model}"
    )


def build_initial_messages(
    *,
    base_system_prompt: str,
    issue_text_for_model: str,
    episode: dict | None = None,
) -> list[dict]:
    """Build the initial system/user message pair for the runtime loop."""
    system_prompt = build_system_prompt(base_system_prompt, episode)
    if episode:
        user_prompt = build_phase1_message(episode, issue_text_for_model)
    else:
        user_prompt = f"Fix this bug:\n\n{issue_text_for_model}"
    return [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": user_prompt},
    ]
