from __future__ import annotations

import sys
from pathlib import Path


TARGET_ROOT = Path(__file__).resolve().parents[1]
for path in (TARGET_ROOT / "src", TARGET_ROOT):
    sys.path.insert(0, str(path))
