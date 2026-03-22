---
title: Command Reference
description: All inner commands, subcommands, flags, and options
weight: 2
---

# Command Reference

## Global Synopsis

```
inner <command> [subcommand] [flags] [-- extra-args]
```

---

## `inner run`

Execute a command inside a sandbox defined by a profile.

```
inner run [flags] [-- extra-args]
```

### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--profile` | `-p` | string | configured default (see `inner config`) | Profile to use |
| `--workdir` | `-w` | path | — | Mount PATH read-write inside the sandbox (at the same path); also sets the initial working directory |
| `--network` | | bool | profile default | Enable network access |
| `--no-network` | | bool | — | Disable network access |
| `--interactive` | `-i` | bool | profile default | Force interactive mode (connect stdin/stdout directly to the sandbox) |
| `--no-interactive` | | bool | — | Force non-interactive mode |
| `--mount` | `-m` | string | — | Additional mount in `SRC:DEST[:MODE]` format (mode: `ro`\|`rw`). Repeatable. |
| `--env` | `-e` | string | — | Set an environment variable as `KEY=VAL`. Repeatable. |
| `--entrypoint` | | string | — | Override the entrypoint command (resets profile args, like `docker --entrypoint`). |
| `--arg` | `-a` | string | — | Append an argument to the entrypoint command. Repeatable. |
| `--args-file` | | path | — | Read the file and append its entire content as a single entrypoint argument. |
| `--timeout` | | int | `0` | Timeout in seconds (`0` = no timeout) |
| `--dry-run` | | bool | false | Print the `bwrap` command without executing |

### Extra arguments

There are three ways to pass additional arguments to the entrypoint:

| Method | When to use |
|--------|-------------|
| `-- arg1 arg2` | Multiple distinct arguments from the command line |
| `--arg TEXT` | Single argument; can be repeated; works well in scripts |
| `--args-file PATH` | Content of a file as a single argument (e.g. an issue description) |

All three methods can be combined. The order of appending is:
`[profile args]` → `--arg` values → `-- extra-args` (which includes `--args-file` content).

**`--args-file` limits:** files larger than 512 KB produce a warning; files larger than 2 MB are
rejected. Binary files (content containing null bytes) are always rejected because they would be
silently truncated by the kernel at the `execve` boundary.

### Examples

```bash
# Start interactive shell (default profile)
inner run

# Use a specific profile
inner run -p claude-interactive

# Mount a project directory read-write
inner run -p claude-interactive -w ~/projects/myapp

# Override network for a single run
inner run --network -- curl https://example.com

# Override network off for a single run
inner run -p claude-interactive --no-network

# Mount an extra read-only directory
inner run -m ~/shared/libs:/libs:ro

# Pass environment variables
inner run -e DEBUG=true -e LOG_LEVEL=info

# Add extra arguments to the entrypoint via --
inner run -p default -- ls -la /workspace

# Send a prompt to an agent profile
inner run -p claude-one-shot -w ~/myproject --arg "write unit tests for all public functions"

# Pass a saved issue as the agent prompt
inner run -p claude-one-shot -w ~/myproject --args-file ~/issues/042-login-bug.md

# Combine profile flags with --arg and an issue file
inner run -p claude-one-shot -w ~/myproject --timeout 300 --args-file ~/issues/042-login-bug.md

# Override entrypoint: reuse a profile's sandbox config with a different binary
inner run -p claude-one-shot --entrypoint /usr/bin/gemini --arg "explain this codebase"

# Run a one-shot shell command in a sandbox
inner run -p shell-oneshot --arg "ls -la ~/project"

# Preview the bwrap command without running
inner run -p claude-interactive --dry-run
```

---

## `inner profile`

Manage sandbox profiles stored in `~/.inner/profiles/`.

### `inner profile list`

List all available profiles:

```bash
inner profile list
```

Output:

```
  NAME                DESCRIPTION
* shell               Bash shell, no network
  claude-interactive  Claude Code interactive, network enabled
  claude-one-shot     Claude Code non-interactive, dangerously-skip-permissions
  claude-containers   Claude Code with Podman rootless containers
  my-project          Project-specific profile  [local]
```

- `*` marks the profile that will be used when `-p` is not specified (the effective default, accounting for any local `default_profile` override).
- `[local]` marks profiles found in `.inner/profiles/` of the current directory rather than `~/.inner/profiles/`.

### `inner profile show`

Display the TOML content of a profile:

```bash
inner profile show claude-interactive
```

### `inner profile new`

Create a new profile from a template, then open it in `$EDITOR`:

```bash
inner profile new myprofile
```

Creates `~/.inner/profiles/myprofile.toml`.

### `inner profile edit`

Open an existing profile in `$EDITOR`:

```bash
inner profile edit myprofile
```

### `inner profile validate`

Validate one or all profiles for structural correctness:

```bash
# Validate a single profile
inner profile validate myprofile

# Validate all profiles
inner profile validate --all
```

| Flag | Description |
|------|-------------|
| `--all` | Validate all profiles in the profiles directory |

### `inner profile clone`

Clone a profile under a new name:

```bash
inner profile clone claude-interactive my-agent
```

---

## `inner verify`

Run security checks **inside** a sandbox to detect exposed sensitive resources and
misconfigurations.

```
inner verify [flags]
```

### How it works

`inner verify` is always run from the **host**. It:

1. Builds a real sandbox using the given profile (same as `inner run`).
2. Launches `inner verify --inside` **inside** that sandbox.
3. The checks execute within the sandboxed environment and their output is forwarded to the terminal.

This means the checks probe what a real agent run would actually see — not the host environment.

### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--profile` | `-p` | string | `default` | Profile to verify |
| `--suggest` | | bool | false | Print TOML snippets for failed checks |

### What it checks

| Check | Severity | Description |
|-------|----------|-------------|
| user is not root | CRITICAL | Process must not run as uid 0 |
| /usr is read-only | CRITICAL | Base filesystem must not be writable |
| git credentials not exposed | CRITICAL | `~/.git-credentials` must be absent or empty |
| `~/.ssh` not accessible | HIGH | No private key files visible in `~/.ssh/` |
| `~/.gnupg` not accessible | HIGH | `~/.gnupg/` must be empty |
| no secrets in env vars | HIGH | No env var names containing `PASSWORD`, `SECRET`, `TOKEN`, etc. |
| docker socket not accessible | MEDIUM | `/var/run/docker.sock` must not be reachable |
| `~/.netrc` not accessible | MEDIUM | `~/.netrc` must be absent or empty |
| shims active in PATH | MEDIUM | Shim directory must be first in `PATH` |
| network restricted | MEDIUM | TCP connection to 8.8.8.8:53 must fail |

Custom checks can be added to a profile under `[verify.custom]` — see [Profiles](../profiles/).

### Output symbols

| Symbol | Meaning |
|--------|---------|
| `[ok]` | Check passed |
| `[!!]` | Check failed |
| `[--]` | Resource explicitly allowed via `[sandbox].allow` — downgraded to INFO, does not count as failure |

### Examples

```bash
# Verify the default profile
inner verify

# Verify a specific profile
inner verify -p claude-interactive

# Show TOML snippets to fix failures
inner verify -p claude-interactive --suggest
```

Exit code is `1` if any check fails and is not overridden by `[sandbox].allow`.

---

## `inner log`

Manage execution logs stored in `~/.inner/logs/` (or the path set in global config).

### `inner log list`

Show all run logs as a table with ID, date, and size:

```bash
inner log list
```

### `inner log show`

Display a log file, paginated through `$PAGER` (default: `less`):

```bash
inner log show <run-id>
```

### `inner log clean`

Delete old log files:

```bash
inner log clean
```

| Flag | Default | Description |
|------|---------|-------------|
| `--older-than` | `30` | Delete logs older than this many days |
| `--dry-run` | false | Preview deletions without removing files |

```bash
# Preview what would be deleted (older than 7 days)
inner log clean --dry-run --older-than 7

# Actually delete logs older than 14 days
inner log clean --older-than 14
```

---

## `inner init`

Initialize (or re-initialize) the `~/.inner` directory:

```bash
inner init           # initialize ~/.inner (global, default)
inner init --local   # create .inner/ in the current directory
```

### Global init (default)

Creates `~/.inner/` and its subdirectories, installs the built-in profiles, and writes a
starter `config.toml` if none exists. Already-present files are never overwritten.

Example output on a fresh install:

```
dir: /home/alice/.inner
created dirs: /home/alice/.inner/profiles, /home/alice/.inner/logs, /home/alice/.inner/directives
config: created
profile claude-containers: installed
profile claude-interactive: installed
profile default: installed
profile claude-one-shot: installed
profile shell: installed
```

Example output when everything already exists:

```
dir: /home/alice/.inner
config: already exists (skipped)
profile claude-containers: already exists (skipped)
profile claude-interactive: already exists (skipped)
profile default: already exists (skipped)
profile claude-one-shot: already exists (skipped)
profile shell: already exists (skipped)
```

To reset to defaults (for example, to pick up updated built-in profiles after an upgrade)
use [`inner reset`](#inner-reset).

### Local init (`--local`)

Creates a `.inner/` directory in the current directory with a starter `config.toml` and an
empty `profiles/` folder — so the directory structure is immediately visible and ready to
customize:

```
inner init --local
```

Example output:

```
dir: /home/alice/projects/myapp
created dirs: /home/alice/projects/myapp/.inner/profiles
config: created
```

The created layout:

```
.inner/
├── config.toml   # local config template (all options commented out)
└── profiles/     # place project-specific profiles here
```

`--local` is idempotent: running it again in the same directory skips existing files.

**Tip:** commit `.inner/` to the project repository. Team members get the directory structure
and local defaults without any manual setup. To further customize, see
[`inner config edit --local`](#inner-config) and the [profiles guide](../profiles/).

### Flags

| Flag | Description |
|------|-------------|
| `--local` | Create `.inner/` in the current directory instead of initializing `~/.inner` |

---

## `inner reset`

Archive the current `~/.inner` contents and reinitialize with default profiles and config:

```bash
inner reset          # asks for confirmation
inner reset --force  # skips confirmation (useful in scripts)
```

The command:

1. Moves everything inside `~/.inner/` (except `backups/`) into `~/.inner/backups/<datetime>/`
2. Runs `inner init` to recreate default profiles and a starter `config.toml`

The `~/.inner` directory itself is never deleted — only its contents are archived.

Example output:

```
backup saved to: /home/alice/.inner/backups/20260319-142301
profile claude-containers: installed
profile claude-interactive: installed
profile claude-one-shot: installed
profile default: installed
profile gemini-interactive: installed
profile shell: installed
config: created

reset complete.
to undo: mv /home/alice/.inner/backups/20260319-142301/* /home/alice/.inner/
```

### Undoing a reset

The undo command is printed at the end of each reset:

```bash
mv ~/.inner/backups/20260319-142301/* ~/.inner/
```

### Managing backups

Backups accumulate under `~/.inner/backups/`. Remove old ones manually when no longer needed:

```bash
ls ~/.inner/backups/
rm -rf ~/.inner/backups/20260319-142301
```

---

## `inner config`

Manage configuration. `inner` supports two levels of configuration:

| Level | File | Scope |
|-------|------|-------|
| **Global** | `~/.inner/config.toml` | All directories |
| **Local** | `.inner/config.toml` (in the current directory) | This directory only |

Local config is merged on top of global. Fields set in the local file override the global value; unset fields fall back to global defaults.

### `inner config show`

Print both the global and local configuration (if present):

```bash
inner config show
```

Example output when a local config exists:

```
# Global: /home/alice/.inner/config.toml
log_dir = "~/.inner/logs/"
default_profile = "shell"

# Local: /home/alice/projects/myapp/.inner/config.toml
default_profile = "claude-interactive"
```

When a file is absent a placeholder is shown instead of an error.

### `inner config edit`

Open a config file in `$EDITOR`. By default, the global config is opened:

```bash
inner config edit           # opens ~/.inner/config.toml  (default)
inner config edit --global  # same as above (explicit)
inner config edit --local   # opens .inner/config.toml in the current directory
```

`--local` creates `.inner/config.toml` (and the `.inner/` directory) in the current directory if they do not yet exist.

| Flag | Description |
|------|-------------|
| `--global` | Edit the global config `~/.inner/config.toml` (default) |
| `--local` | Edit the local config `.inner/config.toml` in the current directory |

**Config options (apply to both global and local files):**

| Key | Default | Description |
|-----|---------|-------------|
| `default_profile` | `"default"` | Profile used when `-p` is not specified |
| `log_dir` | `~/.inner/logs/` | Directory where run logs are written |

**Tip:** commit `.inner/config.toml` to a project repository to give all contributors a consistent default profile without touching their personal `~/.inner/config.toml`.

---

## `inner doctor`

Check that the host environment satisfies all requirements:

```bash
inner doctor
```

Checks performed:

- `bwrap` binary found and version reported
- Unprivileged user namespaces supported
- `~/.inner/profiles/` directory exists and each profile is validated (unknown `allow` keys, missing mounts, etc.)
- `~/.inner/logs/` directory exists
- `ANTHROPIC_API_KEY` environment variable set
- `claude` binary found on `$PATH`
- `GEMINI_API_KEY` environment variable set
- `gemini` binary found on `$PATH`
- Display server available (Wayland or X11, for clipboard forwarding)

---

## `inner completion`

Generate shell completion scripts. Provided automatically by Cobra.

```
inner completion [bash|zsh|fish|powershell]
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `bash` | Generate completion script for Bash |
| `zsh` | Generate completion script for Zsh |
| `fish` | Generate completion script for Fish |
| `powershell` | Generate completion script for PowerShell |

### Setup

**Bash:**

```bash
# Load for the current session
source <(inner completion bash)

# Load permanently
inner completion bash > /etc/bash_completion.d/inner
```

**Zsh:**

```zsh
# Load permanently (requires compinit)
inner completion zsh > "${fpath[1]}/_inner"
```

**Fish:**

```fish
inner completion fish > ~/.config/fish/completions/inner.fish
```

**PowerShell:**

```powershell
inner completion powershell | Out-String | Invoke-Expression
```

---

## `inner version`

Print version, build time, and git commit:

```bash
inner version
```

Example output:

```
inner v0.3.1 (built 2025-11-01T12:00:00Z, commit abc1234)
```
