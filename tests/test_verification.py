import subprocess

from agentic_tdd_runner import verification

CALC_TEST = "from calc import add\n\n\ndef test_add():\n    assert add(2, 3) == 5\n"


def _repo(tmp_path, committed_add):
    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "calc.py").write_text(f"def add(a, b):\n    return {committed_add}\n")
    git = ["git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.hooksPath=/dev/null"]
    subprocess.run([*git, "init", "-q", "-b", "main"], cwd=repo, check=True)
    subprocess.run([*git, "add", "."], cwd=repo, check=True)
    subprocess.run([*git, "commit", "-q", "-m", "init"], cwd=repo, check=True)
    (repo / "calc.py").write_text("def add(a, b):\n    return a + b\n")
    (repo / "test_calc.py").write_text(CALC_TEST)
    return repo


def _verify(repo):
    return verification.verify_red_green(
        "test_calc.py", workdir=str(repo), config={"timeouts": {"test_run": 60}},
        emit=lambda s: None, log=lambda n, d: None,
    )


def test_real_red_green_pair_is_verified(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    repo = _repo(tmp_path, "a - b")
    ok, msg = _verify(repo)
    assert ok, msg
    assert msg.startswith("VERIFIED")
    assert (repo / "calc.py").read_text() == "def add(a, b):\n    return a + b\n"


def test_test_that_passes_without_the_fix_is_rejected(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    repo = _repo(tmp_path, "a + b")
    ok, msg = _verify(repo)
    assert not ok
    assert "passes even WITHOUT" in msg
