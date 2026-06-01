"""TypeScript/JavaScript repo-profile detector registration."""
from __future__ import annotations

import re
from typing import Any


def profile_detectors() -> list[Any]:
    return []


def source_imports_spec(source_text: str, spec: str) -> bool:
    escaped = re.escape(spec)
    patterns = (
        rf"\bfrom\s+['\"]{escaped}['\"]",
        rf"\bimport\s+[^;\n]*\s+from\s+['\"]{escaped}['\"]",
        rf"\brequire\(\s*['\"]{escaped}['\"]\s*\)",
    )
    return any(re.search(pattern, source_text) for pattern in patterns)
