"""Shell command validation for agent tool execution."""

import os
import shlex


ALLOWED_COMMANDS = frozenset({
    "git", "grep", "rg", "find", "ls", "cat", "head", "tail", "wc",
    "bun", "node", "npm", "npx", "pnpm", "yarn", "deno",
    "python", "python3", "pip", "pip3", "pytest",
    "echo", "sort", "uniq", "diff", "tr", "cut", "tee",
    "sed", "awk", "xargs", "dirname", "basename",
    "tree", "file", "which", "true", "false", "test", "env",
})

# Flags that allow arbitrary code execution on otherwise safe binaries.
BLOCKED_FLAGS = {
    "python": {"-c"},
    "python3": {"-c"},
    "node": {"-e", "--eval"},
    "deno": {"eval"},
    "bun": {"-e", "--eval", "-p", "--print"},
}


def split_shell_segments(command: str) -> list[str]:
    """Split shell command chains on unquoted shell operators."""
    segments: list[str] = []
    buf: list[str] = []
    quote: str | None = None
    escaped = False
    i = 0
    while i < len(command):
        ch = command[i]
        if escaped:
            buf.append(ch)
            escaped = False
            i += 1
            continue
        if ch == "\\" and quote != "'":
            buf.append(ch)
            escaped = True
            i += 1
            continue
        if quote:
            buf.append(ch)
            if ch == quote:
                quote = None
            i += 1
            continue
        if ch in {"'", '"'}:
            quote = ch
            buf.append(ch)
            i += 1
            continue
        if command.startswith("&&", i) or command.startswith("||", i):
            segment = "".join(buf).strip()
            if segment:
                segments.append(segment)
            buf = []
            i += 2
            continue
        if ch in {"|", ";"}:
            segment = "".join(buf).strip()
            if segment:
                segments.append(segment)
            buf = []
            i += 1
            continue
        buf.append(ch)
        i += 1
    segment = "".join(buf).strip()
    if segment:
        segments.append(segment)
    return segments


def has_shell_command_substitution(command: str) -> bool:
    """Return true when shell command substitution can execute."""
    quote: str | None = None
    escaped = False
    i = 0
    while i < len(command):
        ch = command[i]
        if escaped:
            escaped = False
            i += 1
            continue
        if ch == "\\" and quote != "'":
            escaped = True
            i += 1
            continue
        if quote:
            if ch == quote:
                quote = None
            elif quote != "'" and ch == "`":
                return True
            elif quote != "'" and command.startswith("$(", i):
                return True
            i += 1
            continue
        if ch in {"'", '"'}:
            quote = ch
            i += 1
            continue
        if ch == "`" or command.startswith("$(", i):
            return True
        i += 1
    return False


def validate_command(command: str) -> None:
    """Validate that all commands in a pipeline/chain use allowed binaries."""
    if not command or not command.strip():
        raise ValueError("empty command")
    # Reject newlines; they bypass shell operator splitting.
    if "\n" in command:
        raise ValueError("newlines not allowed in commands")
    if has_shell_command_substitution(command):
        raise ValueError("command substitution is not allowed")
    for part in split_shell_segments(command):
        part = part.strip()
        if not part:
            continue
        try:
            tokens = shlex.split(part)
        except ValueError:
            tokens = part.split()
        if not tokens:
            continue
        binary = os.path.basename(tokens[0])
        if binary not in ALLOWED_COMMANDS:
            raise ValueError(
                f"command '{binary}' is not allowed. "
                f"Allowed: {', '.join(sorted(ALLOWED_COMMANDS))}"
            )
        blocked = BLOCKED_FLAGS.get(binary, set())
        if blocked:
            for token in tokens[1:]:
                if token in blocked:
                    raise ValueError(
                        f"flag '{token}' not allowed with '{binary}'"
                    )
        if binary == "env":
            for token in tokens[1:]:
                if "=" in token:
                    continue
                wrapped = os.path.basename(token)
                if wrapped not in ALLOWED_COMMANDS:
                    raise ValueError(
                        f"command '{wrapped}' (via env) is not allowed"
                    )
                wrapped_blocked = BLOCKED_FLAGS.get(wrapped, set())
                remaining = tokens[tokens.index(token) + 1:]
                for flag in remaining:
                    if flag in wrapped_blocked:
                        raise ValueError(
                            f"flag '{flag}' not allowed with '{wrapped}' (via env)"
                        )
                break
