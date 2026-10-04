"""Python repo-profile detector registration."""
from __future__ import annotations

import re


def source_imports_spec(source_text: str, spec: str) -> bool:
    escaped = re.escape(spec)
    if re.search(rf"^from\s+{escaped}\s+import\s+", source_text, flags=re.MULTILINE):
        return True
    for line in source_text.splitlines():
        stripped = line.strip()
        if not stripped.startswith("import "):
            continue
        imports = stripped.removeprefix("import ").split("#", 1)[0]
        for part in imports.split(","):
            module = part.strip().split(" as ", 1)[0].strip()
            if module == spec:
                return True
    return False
