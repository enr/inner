---
title: Command Reference
description: All inner commands, subcommands, flags, and options
weight: 2
---



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
| `--workdir` | `-w` | path | profile `entrypoint.workdir`, then cwd | Mount PATH read-write inside the sandbox (at the same path); also sets the initial working directory. Overrides `entrypoint.workdir` from the profile. |
| `--network` | | bool | profile default | Enable network access |
| `--no-network` | | bool | — | Disable network access |
| `--interactive` | `-i` | bool | profile default | Force interactive mode (connect stdin/stdout directly to the sandbox) |
| `--no-interactive` | | bool | — | Force non-interactive mode |
| `--mount` | `-m` | string | — | Additional mount in `SRC:DEST[:MODE]` format (mode: `ro`\|`rw`). Repeatable. |
| `--env` | `-e` | string | — | Set an environment variable as `KEY=VAL`. `~`, `$VAR` and `${VAR}` in VAL are expanded against the host environment (same rules as profile `[env] set` values). Repeatable. |
| `--entrypoint` | | string | — | Override the entrypoint command (resets profile args, like `docker --entrypoint`). |
| `--arg` | `-a` | string | — | Append an argument to the entrypoint command. Repeatable. |
| `--args-file` | | path | — | Read the file and append its entire content as a single entrypoint argument. |
| `--timeout` | | int | `0` | Timeout in seconds (`0` = no timeout) |
| `--limit-memory` | | string | resolved (see profiles) | Override the memory cap (`"512M"`, `"4G"`). Highest-priority source in the [resource-limit chain](profiles.md#sandbox-limits-resource-limits). |
| `--limit-cpu` | | string | resolved (see profiles) | Override the CPU cap (`"200%"` or `"2.0"` cores). |
| `--limit-pids` | | int | resolved (see profiles) | Override the max process count (`0` = no override, keep the resolved value). |
| `--dry-run` | | bool | false | Print the resolved config and `bwrap` command without executing |

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

# Cap resources for a single run (overrides profile/config defaults)
inner run -p claude-one-shot --limit-memory 2G --limit-cpu 100% --limit-pids 256

# Preview the resolved config and bwrap command without running
inner run -p claude-interactive --dry-run

# Use a profile from a URL (downloaded for this run only, not saved)
inner run -p https://raw.githubusercontent.com/acme/profiles/main/claude-restricted.toml
```

`--dry-run` prints the resolved profile, config file paths, effective sandbox settings, and the
`bwrap` command that would be executed. The local config line is always shown so you can confirm
whether a `.config/inner.toml` in the current directory is being picked up:

```
profile:       claude-interactive
profile path:  ~/.config/inner/profiles/claude-interactive.toml
global config: ~/.config/inner/config.toml
local config:  ~/projects/myapp/.config/inner.toml (missing)

entrypoint: /usr/bin/claude
interactive: true
network:     true
...

resource limits:
  memory: 4G
  cpu:    200% (systemd CPUQuota)
  pids:   512

bwrap command:
  bwrap --ro-bind / / ...
```

The `resource limits` block shows the **effective** caps for this exact run
(after the full resolution chain and any `--limit-*` flags). See
[`[sandbox.limits]`](profiles.md#sandbox-limits-resource-limits) for how the
values are resolved and auto-detected.

---

## `inner profile`

Manage sandbox profiles stored in `~/.config/inner/profiles/`.

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
- `[local]` marks profiles found in `.config/inner/profiles/` of the current directory rather than `~/.config/inner/profiles/`.
- When a local profile has the same name as a global one, the global profile is **hidden** (the local takes precedence). Use `--wide` to reveal it.

| Flag | Short | Description |
|------|-------|-------------|
| `--wide` | `-w` | Show SCOPE and PATH columns; also display shadowed global profiles (marked `[shadowed]`) |

Wide output example (when a local `shell` profile shadows the global one):

```
  NAME   SCOPE   DESCRIPTION                   PATH
  shell  global  Bash shell, no network  [shadowed]  ~/.config/inner/profiles/shell.toml
* shell  local   Project shell variant  [local]      ~/projects/myapp/.config/inner/profiles/shell.toml
  claude-interactive  global  Claude Code interactive    ~/.config/inner/profiles/claude-interactive.toml
```

### `inner profile show`

Display the TOML content of a profile:

```bash
inner profile show claude-interactive
```

| Flag | Description |
|------|-------------|
| `--explain` | Append an explanation of each declared capability (injected mounts, pre-run actions, notes) |

```bash
# Show profile content with capability details
inner profile show claude-interactive --explain
```

### `inner profile new`

Create a new profile from a template, then open it in `$EDITOR`:

```bash
inner profile new myprofile
```

Creates `~/.config/inner/profiles/myprofile.toml`.

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

### `inner profile install`

Download and install a profile from an HTTP/HTTPS URL into `~/.config/inner/profiles/`:

```bash
inner profile install URL [--name NAME] [--force]
```

| Flag | Type | Description |
|------|------|-------------|
| `--name` | string | Override the profile name (default: last URL path segment, `.toml` stripped) |
| `--force` | bool | Overwrite an existing profile with the same name |

```bash
# Install under the name derived from the URL
inner profile install https://example.com/my-profile.toml

# Install under a custom name
inner profile install https://example.com/my-profile.toml --name restricted

# Overwrite an existing local profile
inner profile install https://example.com/my-profile.toml --force
```

The TOML is validated before being written to disk. Use `inner run -p URL` to try a remote profile without installing it first.

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

Custom checks can be added to a profile under `[verify.custom]` — see [Profiles](/inner/profiles/).

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

Manage execution logs stored in `~/.config/inner/logs/` (or the path set in global config).

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

Initialize (or re-initialize) the `~/.config/inner` directory:

```bash
inner init           # initialize ~/.config/inner (global, default)
inner init --local   # create .config/inner/ in the current directory
```

### Global init (default)

Creates `~/.config/inner/` and its subdirectories, installs the built-in profiles, and writes a
starter `config.toml` if none exists. Already-present files are never overwritten.

Example output on a fresh install:

```
dir: /home/alice/.config/inner
created dirs: /home/alice/.config/inner/profiles, /home/alice/.config/inner/logs, /home/alice/.config/inner/directives
config: created
profile claude-containers: installed
profile claude-interactive: installed
profile claude-one-shot: installed
profile gemini-interactive: installed
profile gemini-one-shot: installed
profile shell: installed
profile shell-containers: installed
profile shell-oneshot: installed
profile shell-with-claude: installed
```

Example output when everything already exists:

```
dir: /home/alice/.config/inner
config: already exists (skipped)
profile claude-containers: already exists (skipped)
profile claude-interactive: already exists (skipped)
profile claude-one-shot: already exists (skipped)
profile gemini-interactive: already exists (skipped)
profile gemini-one-shot: already exists (skipped)
profile shell: already exists (skipped)
profile shell-containers: already exists (skipped)
profile shell-oneshot: already exists (skipped)
profile shell-with-claude: already exists (skipped)
```

To reset to defaults (for example, to pick up updated built-in profiles after an upgrade)
use [`inner reset`](#inner-reset).

### Local init (`--local`)

Creates `.config/inner/` and `.config/inner.toml` in the current directory — the directory
structure is immediately visible and ready to customize:

```
inner init --local
```

Example output:

```
dir: /home/alice/projects/myapp
created dirs: /home/alice/projects/myapp/.config/inner/profiles
config: created
```

The created layout:

```
.config/
├── inner.toml        # local config template (all options commented out)
└── inner/
    └── profiles/     # place project-specific profiles here
```

`--local` is idempotent: running it again in the same directory skips existing files.

**Tip:** commit `.config/inner.toml` and `.config/inner/profiles/` to the project repository.
Team members get the directory structure and local defaults without any manual setup. To further
customize, see [`inner config edit --local`](#inner-config) and the [profiles guide](/inner/profiles/).

### Flags

| Flag | Description |
|------|-------------|
| `--local` | Create `.config/inner/` in the current directory instead of initializing `~/.config/inner` |

---

## `inner reset`

Restore all built-in profiles to the versions embedded in the binary:

```bash
inner reset          # asks for confirmation
inner reset --force  # skips confirmation (useful in scripts)
```

The command overwrites every file in `~/.config/inner/profiles/` whose name matches a
built-in profile. Files that do not match any built-in name — user-created
profiles, cloned profiles under a custom name — are left completely untouched.

Use this after upgrading `inner` to pick up changes to the built-in profiles
without losing your own customizations.

Example output:

```
profile claude-containers: reset
profile claude-interactive: reset
profile claude-one-shot: reset
profile gemini-interactive: reset
profile gemini-one-shot: reset
profile shell: reset
profile shell-containers: reset
profile shell-oneshot: reset
profile shell-with-claude: reset

reset complete.
```

### What is affected

| File | Outcome |
|------|---------|
| Built-in profile (e.g. `shell.toml`) | Overwritten with the embedded version |
| User-created profile (e.g. `my-agent.toml`) | Left untouched |
| Cloned profile under a new name | Left untouched |
| `config.toml` | Left untouched |

### Preserving a customized built-in profile

If you have modified a built-in profile and want to keep your changes after a reset,
clone it under a new name first:

```bash
inner profile clone shell my-shell
inner reset
```

`my-shell.toml` is not a built-in name, so it survives the reset unchanged.

---

## `inner config`

Manage configuration. `inner` uses a layered config hierarchy — each layer is merged on top of the previous:

| Level | File(s) | Scope |
|-------|---------|-------|
| **System** | `/etc/inner/config.toml` | All users (admin-set) |
| **Global** | `~/.config/inner/config.toml` | All directories for this user |
| **Project** | Per-directory, from filesystem root to cwd — see below | This project and its parents |

Fields set in a higher-precedence layer override lower-precedence values; unset fields fall back to the next layer.

**Project config files** — `inner` walks from the filesystem root to the current working directory and loads these files (lowest → highest precedence) in each directory it passes through:

```
.config/inner.toml               # committed project config
.config/inner/config.toml        # alternative committed location
.config/inner.local.toml         # local overrides (not committed)
.config/inner/config.local.toml  # alternative local override location
inner.toml                       # top-level committed config
inner.local.toml                 # top-level local overrides (not committed)
```

### `inner config show`

Print all active configuration layers (if present):

```bash
inner config show
```

Example output when a project config exists:

```
# Global: /home/alice/.config/inner/config.toml
log_dir = "~/.config/inner/logs/"
default_profile = "shell"

# Local: /home/alice/projects/myapp/.config/inner.toml
default_profile = "claude-interactive"

# Resolved default limits (global+project config, before profile/CLI override):
#   memory: 4G
#   cpu   : 200%
#   pids:   512  # (auto-detected)
```

When a file is absent a placeholder is shown instead of an error.

The trailing **Resolved default limits** block shows the effective resource
caps derived from the merged global + project config, *before* any profile
`[sandbox.limits]` or `--limit-*` flag is applied. Values that come from
[auto-detection](profiles.md#sandbox-limits-resource-limits) (rather than an
explicit `[default_limits]` entry) are annotated `# (auto-detected)`.

### `inner config edit`

Open a config file in `$EDITOR`. By default, the global config is opened:

```bash
inner config edit           # opens ~/.config/inner/config.toml  (default)
inner config edit --global  # same as above (explicit)
inner config edit --local   # opens .config/inner.toml in the current directory
```

`--local` creates `.config/inner.toml` in the current directory if it does not yet exist.

| Flag | Description |
|------|-------------|
| `--global` | Edit the global config `~/.config/inner/config.toml` (default) |
| `--local` | Edit the project config `.config/inner.toml` in the current directory |

**Config options (apply to all layers):**

| Key | Default | Description |
|-----|---------|-------------|
| `default_profile` | `"default"` | Profile used when `-p` is not specified |
| `log_dir` | `~/.config/inner/logs/` | Directory where run logs are written |
| `workspaces_path` | — | Host directory where workspace mount-point directories are pre-created. Required when a profile mount uses `${workspaces_path}` in its `dest` field. Local config overrides global. |
| `[default_limits]` | auto-detected | Fallback resource limits (`memory`, `cpu`, `pids`) applied to every run unless a profile or `--limit-*` flag overrides them. Project config overrides user config, field by field. See [`[sandbox.limits]`](profiles.md#sandbox-limits-resource-limits). |

**Default resource limits** can be set machine-wide (user config) or
per-repository (project config):

```toml
# ~/.config/inner/config.toml  — applies to every project
[default_limits]
memory = "4G"
cpu    = "200%"
pids   = 512
```

```toml
# /my-project/.config/inner.toml  — tighter caps for this repo only
[default_limits]
memory = "2G"     # only memory is overridden; cpu and pids keep the user-config values
```

**Tip:** commit `.config/inner.toml` to a project repository to give all contributors a consistent default profile without touching their personal `~/.config/inner/config.toml`. Use `.config/inner.local.toml` (gitignored) for personal overrides like credentials.

---

## `inner doctor`

Check that the host environment satisfies all requirements:

```bash
inner doctor
```

Checks performed:

- `bwrap` binary found and version reported
- Unprivileged user namespaces supported
- `~/.config/inner/profiles/` directory exists and each profile is validated (unknown `allow` keys, missing mounts, etc.)
- `~/.config/inner/logs/` directory exists
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
inner v0.1.3 (built 2025-11-01T12:00:00Z, commit abc1234)
```
