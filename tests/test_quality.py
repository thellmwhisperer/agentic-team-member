from agentic_tdd_runner.quality import detect_pr_target_violation


def test_pr_target_violation_when_diff_misses_target():
    episode = {"source_file": "src/personality/sanitizer.ts", "target_symbol": "wrapUserMessage"}
    changed = ["src/events/client.ts", "src/events/parseBotCommand.ts", "tmp_test.js"]
    violation = detect_pr_target_violation(changed, episode)
    assert violation is not None
    assert "sanitizer.ts" in violation


def test_pr_target_violation_when_only_neighbor_source_touched():
    episode = {"source_file": "src/personality/sanitizer.ts", "target_symbol": "wrapUserMessage"}
    changed = ["src/events/client.ts", "src/personality/wrapUserMessage.test.ts"]
    assert detect_pr_target_violation(changed, episode) is not None


def test_pr_target_violation_none_when_target_touched():
    episode = {"source_file": "src/personality/sanitizer.ts", "target_symbol": "wrapUserMessage"}
    changed = ["src/personality/sanitizer.ts", "src/personality/wrapUserMessage.test.ts"]
    assert detect_pr_target_violation(changed, episode) is None


def test_pr_target_violation_normalizes_leading_dot_slash():
    episode = {"source_file": "src/personality/sanitizer.ts"}
    changed = ["./src/personality/sanitizer.ts"]
    assert detect_pr_target_violation(changed, episode) is None


def test_pr_target_violation_none_without_target():
    assert detect_pr_target_violation(["whatever.ts"], {}) is None
    assert detect_pr_target_violation(["whatever.ts"], None) is None
