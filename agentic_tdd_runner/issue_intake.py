"""Parse ATM-formatted issues into a safe execution contract."""

from __future__ import annotations

import re
from dataclasses import asdict, dataclass, field


_SECTION_RE = re.compile(r"^##+\s+(.+?)\s*$", re.MULTILINE)

_IMPLEMENTATION_SECTIONS = {
    "fix",
    "fix approach",
    "implementation",
    "implementation approach",
    "proposed fix",
    "solution",
    "test approach",
}

_MODEL_SECTION_TITLES = {
    "acceptance criteria": "Acceptance criteria",
    "acceptance test": "Acceptance test",
    "context": "Context",
    "expected behavior": "Expected behavior",
    "observed examples": "Observed examples",
    "root cause": "Reporter hypothesis",
    "suspected area": "Suspected area",
    "suspected root cause": "Reporter hypothesis",
    "symptom": "Symptom",
    "where": "Suspected area",
}

_FORBIDDEN_GUIDANCE = (
    (re.compile(r"\bas\s+any\b", re.IGNORECASE), "`as any`"),
    (re.compile(r":\s*any\b", re.IGNORECASE), "`: any`"),
    (re.compile(r"@ts-ignore\b", re.IGNORECASE), "`@ts-ignore`"),
    (re.compile(r"@ts-expect-error\b", re.IGNORECASE), "`@ts-expect-error`"),
    (re.compile(r"eslint-disable\b", re.IGNORECASE), "`eslint-disable`"),
)


@dataclass
class IssueContract:
    raw_text: str
    model_text: str
    source_hint: str | None = None
    symbol_hint: str | None = None
    rejected: bool = False
    rejection_reason: str = ""
    warnings: list[str] = field(default_factory=list)

    def to_log_dict(self) -> dict:
        data = asdict(self)
        data.pop("raw_text", None)
        data["raw_length"] = len(self.raw_text)
        data["model_length"] = len(self.model_text)
        return data


def parse_issue_contract(issue_text: str) -> IssueContract:
    """Extract target hints and sanitize issue text before it reaches the model."""
    sections = _split_sections(issue_text)
    rejected_reason = _forbidden_guidance_reason(sections)
    if rejected_reason:
        return IssueContract(
            raw_text=issue_text,
            model_text="",
            rejected=True,
            rejection_reason=rejected_reason,
        )

    source_hint, symbol_hint = _extract_target_hint(issue_text)
    model_text, warnings = _build_model_text(sections, fallback=issue_text)

    return IssueContract(
        raw_text=issue_text,
        model_text=model_text,
        source_hint=source_hint,
        symbol_hint=symbol_hint,
        warnings=warnings,
    )


def _split_sections(text: str) -> list[tuple[str, str]]:
    matches = list(_SECTION_RE.finditer(text))
    if not matches:
        return [("", text.strip())]

    sections: list[tuple[str, str]] = []
    preamble = text[:matches[0].start()].strip()
    if preamble:
        sections.append(("", preamble))

    for index, match in enumerate(matches):
        start = match.end()
        end = matches[index + 1].start() if index + 1 < len(matches) else len(text)
        sections.append((match.group(1).strip(), text[start:end].strip()))
    return sections


def _forbidden_guidance_reason(sections: list[tuple[str, str]]) -> str:
    for title, body in sections:
        if title and _normalize_title(title) in _IMPLEMENTATION_SECTIONS:
            continue
        haystack = f"{title}\n{body}"
        for pattern, label in _FORBIDDEN_GUIDANCE:
            if pattern.search(haystack):
                section = title or "issue body"
                return f"model-facing issue section '{section}' contains forbidden TypeScript escape {label}"
    return ""


def _build_model_text(sections: list[tuple[str, str]], *, fallback: str) -> tuple[str, list[str]]:
    parts: list[str] = []
    warnings: list[str] = []
    saw_structured_section = False

    for title, body in sections:
        normalized = _normalize_title(title)
        body = body.strip()
        if not body:
            continue

        if not title:
            parts.append(body)
            continue

        if normalized == "fix approach":
            if _contains_forbidden_guidance(body):
                warnings.append("dropped Fix approach before prompting because it contained forbidden guidance")
            else:
                warnings.append("dropped Fix approach before prompting")
            continue

        if normalized == "test approach":
            acceptance = _test_approach_to_acceptance(body)
            if acceptance:
                parts.append(f"## Acceptance test\n{acceptance}")
                saw_structured_section = True
            warnings.append("converted Test approach to acceptance-only checks")
            continue

        model_title = _MODEL_SECTION_TITLES.get(normalized)
        if not model_title:
            warnings.append(f"dropped unsupported issue section: {title}")
            continue

        if normalized in {"where", "suspected area"}:
            target_text = _target_text_for_model(body)
            if not target_text:
                warnings.append(f"dropped unsupported issue section: {title}")
                continue
            body = target_text
        else:
            body = _strip_mechanical_testing_details(body)

        parts.append(f"## {model_title}\n{body}")
        saw_structured_section = True

    model_text = "\n\n".join(part.strip() for part in parts if part.strip()).strip()
    if not model_text:
        return fallback.strip(), warnings
    if not saw_structured_section and len(parts) == 1:
        return model_text, warnings
    return model_text, warnings


def _test_approach_to_acceptance(body: str) -> str:
    kept: list[str] = []
    for raw_line in body.splitlines():
        line = raw_line.strip()
        if not line:
            continue
        text = re.sub(r"^[-*]\s*", "", line).strip()
        lowered = text.lower()
        if not text:
            continue
        if lowered.startswith(("export ", "import ", "mock ", "call ")):
            continue
        if " so it can be tested" in lowered:
            continue
        if lowered.startswith(("verify ", "assert ", "expect ", "prove ", "ensure ")):
            kept.append(f"- {text}")
    return "\n".join(kept)


def _extract_target_hint(text: str) -> tuple[str | None, str | None]:
    path_match = re.search(r"`?([\w./-]+\.(?:ts|tsx|js|jsx|py))`?", text)
    source_hint = path_match.group(1) if path_match else None

    symbol_hint = None
    if source_hint:
        after_path = text[path_match.end(): path_match.end() + 200]
        arrow_match = re.search(r"(?:->|→|::|#)\s*`?([A-Za-z_$][\w$]*)\s*(?:\(\))?`?", after_path)
        if arrow_match:
            symbol_hint = arrow_match.group(1)
        else:
            nearby_call = re.search(r"`([A-Za-z_$][\w$]*)\s*\(\)`", after_path)
            if nearby_call:
                symbol_hint = nearby_call.group(1)

    return source_hint, symbol_hint


def _contains_forbidden_guidance(text: str) -> bool:
    return any(pattern.search(text) for pattern, _label in _FORBIDDEN_GUIDANCE)


def _target_text_for_model(body: str) -> str:
    source_hint, symbol_hint = _extract_target_hint(body)
    if source_hint and symbol_hint:
        return f"`{source_hint}` -> `{symbol_hint}()`"
    if source_hint:
        return f"`{source_hint}`"
    return _strip_mechanical_testing_details(body)


def _strip_mechanical_testing_details(text: str) -> str:
    text = re.sub(r"\s*\([^)]*\bnot exported\b[^)]*\)", "", text, flags=re.IGNORECASE)
    text = re.sub(r"\bnot exported\b", "", text, flags=re.IGNORECASE)
    lines: list[str] = []
    for raw_line in text.splitlines():
        line = raw_line.rstrip()
        lowered = line.strip().lower()
        if " so it can be tested" in lowered:
            continue
        if lowered.startswith(("- export ", "* export ", "export ")):
            continue
        lines.append(line)
    return "\n".join(lines).strip()


def _normalize_title(title: str) -> str:
    return re.sub(r"\s+", " ", title.strip().lower())
