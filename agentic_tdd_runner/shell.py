"""Shell command validation for agent tool execution."""

import os


DEFAULT_COMMAND_PATH_DIRS = (
    "~/bin",
    "~/.local/bin",
    "~/.bun/bin",
    "~/.cargo/bin",
    "/opt/homebrew/bin",
    "/usr/local/bin",
    "/usr/bin",
    "/bin",
    "/usr/sbin",
    "/sbin",
)

def build_command_env(config: dict | None = None) -> dict[str, str]:
    """Return an env with common developer tool directories on PATH.

    Agent prompts may recommend tools such as rg, gh, bun, or pnpm. Runs launched
    from GUI shells often have a narrower PATH than interactive terminals, so
    append well-known developer bin dirs while preserving the caller's PATH
    precedence.
    """
    env = os.environ.copy()
    path_dirs: list[str] = []

    tools_config = (config or {}).get("tools", {})
    configured_dirs = (
        tools_config.get("path_dirs", [])
        if isinstance(tools_config, dict)
        else []
    )
    if isinstance(configured_dirs, str):
        configured_dirs = [configured_dirs]
    elif not isinstance(configured_dirs, list):
        configured_dirs = []

    for path in (env.get("PATH") or "").split(os.pathsep):
        if path:
            path_dirs.append(path)
    for path in [*configured_dirs, *DEFAULT_COMMAND_PATH_DIRS]:
        if not isinstance(path, str) or not path:
            continue
        expanded = os.path.expanduser(path)
        if expanded not in path_dirs:
            path_dirs.append(expanded)

    env["PATH"] = os.pathsep.join(path_dirs)
    return env


