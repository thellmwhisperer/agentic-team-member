import subprocess
from datetime import datetime

from agentic_tdd_runner.delivery import branch_name, deliver


def git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)


def make_repo(path):
    path.mkdir()
    git(path, "init", "-q")
    git(path, "config", "user.name", "test")
    git(path, "config", "user.email", "test@example.com")
    (path / "tracked.txt").write_text("base\n")
    git(path, "add", ".")
    git(path, "commit", "-qm", "base")


def test_delivery_refuses_source_without_origin_before_changing_clone(tmp_path):
    source = tmp_path / "source"
    clone = tmp_path / "clone"
    make_repo(source)
    git(tmp_path, "clone", "-q", str(source), str(clone))
    original_branch = git(clone, "branch", "--show-current").stdout.strip()
    (clone / "tracked.txt").write_text("change\n")

    result = deliver(str(clone), str(source), "change", 1)

    assert result["ok"] is False
    assert "branch preparation failed" in result["error"]
    assert git(clone, "branch", "--show-current").stdout.strip() == original_branch
    assert git(clone, "remote", "get-url", "origin").stdout.strip() == str(source)


def test_delivery_resolves_relative_source_origin(tmp_path):
    source = tmp_path / "source"
    clone = tmp_path / "clone"
    make_repo(source)
    git(source, "remote", "add", "origin", "../server.git")
    git(tmp_path, "clone", "-q", str(source), str(clone))

    result = deliver(str(clone), str(source), "change", 1)

    assert git(clone, "remote", "get-url", "origin").stdout.strip() == str(tmp_path / "server.git")
    assert result["branch"].startswith("atm/change-")


def test_branch_name_is_unique_for_same_title_and_time():
    now = datetime(2026, 10, 5, 12, 0, 0)

    assert branch_name("same title", now) != branch_name("same title", now)
