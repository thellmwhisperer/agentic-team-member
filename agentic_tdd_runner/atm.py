"""Small ATM command surface for repository utilities."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from agentic_tdd_runner.profile_generator import (
    generate_profile,
    infer_repo_profile,
    render_repo_profile,
)


def main(argv: list[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    if args.command == "profile":
        return _run_profile_command(args)
    parser.print_help()
    return 2


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="atm")
    subparsers = parser.add_subparsers(dest="command")

    profile = subparsers.add_parser("profile", help="Generate and maintain .atm/profile.toml")
    profile_subparsers = profile.add_subparsers(dest="profile_command", required=True)
    for name in ("generate", "update", "print"):
        command = profile_subparsers.add_parser(name)
        command.add_argument("--workdir", required=True, help="Project root to inspect")
    return parser


def _run_profile_command(args: argparse.Namespace) -> int:
    workdir = Path(args.workdir).expanduser().resolve()
    if args.profile_command == "print":
        sys.stdout.write(render_repo_profile(infer_repo_profile(workdir)))
        return 0

    update = args.profile_command == "update"
    try:
        profile_path = generate_profile(workdir, update=update)
    except FileExistsError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    print(f"Wrote {profile_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
