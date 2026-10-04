"""Fourth gate for follow-ups: does this gap serve the issue, or widen it?

The three gates before it are mechanical (red test fails on behavior, the test calls code that
exists in the base, the cited criterion is in the issue). None of them can tell "the zcode hook
has the same bug" from "the Claude session hook has the same bug": same words, different issue.
A System One model (TypeSafe's Jev) answers that with a probability in ~250 ms.

Validated 4-oct-2026 on la-roca #300: 12/12 against hand labels, the trap cases (same defect in
another product) landed on "adjacent" with 0.99+. See workspace/.tmp/jev/report-follow-ups-2026-10-04.txt.

Configuration, in the agent TOML:

    [follow_ups.judge]
    enabled = true
    model = "jev-latest"
    threshold = 0.6              # Noul probability below which the follow-up is not chained
    api_key_file = "~/.config/typesafe/api_key"   # TYPESAFE_API_KEY in the environment wins

With `enabled = true` and no key, the gate fails closed: the follow-up is kept but not chained.
"""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from pathlib import Path
from typing import Callable

ENDPOINT = "https://api.typesafe.ai/v1/systemone"
DEFAULTS = {"enabled": False, "model": "jev-latest", "threshold": 0.6,
            "api_key_file": "~/.config/typesafe/api_key", "timeout": 30}

QUESTIONS = {
    "serves": {
        "type": "noul",
        "instructions": "Does fixing `follow_up` satisfy one of the acceptance criteria of `issue`, as opposed to "
                        "being a separate issue that merely resembles it?",
        "criteria": {
            "true": "The follow-up is a remaining case of the defect the issue describes, and one of the issue's "
                    "acceptance criteria is not met until it is fixed.",
            "false": "The follow-up is a different defect, a different product or hook, a new feature, or a "
                     "nicety; the issue's criteria are met without it.",
        },
    },
    "fit": {
        "type": "choice",
        "instructions": "How does `follow_up` relate to `issue`?",
        "criteria": {
            "within": "A remaining case of the same defect in the same lifecycle the issue names; an acceptance criterion requires it.",
            "adjacent": "Same kind of defect, but in a product, hook or path the issue does not name.",
            "outside": "A different concern: feature, cosmetics, housekeeping, or unrelated bug.",
        },
    },
}


def settings(config: dict) -> dict:
    return {**DEFAULTS, **(config.get("follow_ups", {}).get("judge", {}) or {})}


def api_key(cfg: dict) -> str | None:
    key = os.environ.get("TYPESAFE_API_KEY", "").strip()
    if key:
        return key
    path = Path(os.path.expanduser(str(cfg.get("api_key_file") or "")))
    try:
        return path.read_text().strip() or None
    except OSError:
        return None


def post_json(url: str, body: dict, key: str, timeout: int) -> dict:
    req = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST",
                                 headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def judge_follow_up(issue: dict, follow_up: dict, config: dict, *,
                    post: Callable[[str, dict, str, int], dict] = post_json) -> dict:
    """Return {"enabled", "ok", "serves", "fit", "probabilities", "threshold", "reason"}.

    `ok` is True when the gate lets the follow-up through: judge disabled, or serves >= threshold.
    A disabled judge is not a verdict; the record says so and the caller chains on the other gates."""
    cfg = settings(config)
    threshold = float(cfg["threshold"])
    if not cfg["enabled"]:
        return {"enabled": False, "ok": True, "reason": "judge disabled"}
    key = api_key(cfg)
    if not key:
        return {"enabled": True, "ok": False, "threshold": threshold,
                "reason": "judge enabled but no API key (TYPESAFE_API_KEY or api_key_file)"}
    state = {"issue": {"title": issue.get("title"), "body": issue.get("body")},
             "follow_up": {"title": follow_up.get("title"), "paths": follow_up.get("paths") or [],
                           "criterion": follow_up.get("criterion")}}
    body = {"state": state, "model": cfg["model"], "questions": QUESTIONS}
    try:
        out = post(ENDPOINT, body, key, int(cfg["timeout"]))
        answers = out["answers"]
        serves = float(answers["serves"]["noul"])
        fit = answers["fit"]
    except (urllib.error.URLError, OSError, KeyError, TypeError, ValueError) as exc:
        return {"enabled": True, "ok": False, "threshold": threshold, "reason": f"judge call failed: {exc}"[:200]}
    ok = serves >= threshold
    return {"enabled": True, "ok": ok, "serves": serves, "fit": fit.get("choice"),
            "probabilities": fit.get("probabilities") or {}, "threshold": threshold, "model": out.get("model"),
            "reason": f"judge: serves={serves:.2f} fit={fit.get('choice')} threshold={threshold:.2f}"
                      + ("" if ok else " (below threshold)")}
