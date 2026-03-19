---
title: Command Reference
description: All inner commands, subcommands, flags, and options
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
| `--profile` | `-p` | string | `default` | Profile to use |
| `--workdir` | `-w` | path | — | Mount PATH read-write inside the sandbox (at the same path); also sets the initial working directory |
| `--network` | | bool | profile default | Enable network access |
| `--no-network` | | bool | — | Disable network access |
| `--interactive` | `-i` | bool | profile default | Force interactive mode (connect stdin/stdout directly to the sandbox) |
| `--no-interactive` | | bool | — | Force non-interactive mode |
| `--mount` | `-m` | string | — | Additional mount in `SRC:DEST[:MODE]` format (mode: `ro`\|`rw`). Repeatable. |
| `--env` | `-e` | string | — | Set an environment variable as `KEY=VAL`. Repeatable. |
| `--prompt` | | string | — | Append text to entrypoint arguments |
| `--timeout` | | int | `0` | Timeout in seconds (`0` = no timeout) |
| `--dry-run` | | bool | false | Print the `bwrap` command without executing |

### Extra arguments

Arguments after `--` are appended verbatim to the entrypoint defined in the profile:

```bash
inner run -p default -- python script.py --verbose
```

### Examples

```bash
# Start interactive shell (default profile)
inner run

# Use a specific profile
inner run -p claude-interactive

# Mount a project directory as /workspace
inner run -p claude-interactive -w ~/projects/myapp

# Override network for a single run
inner run --network -- curl https://example.com

# Override network off for a single run
inner run -p claude-interactive --no-network

# Mount an extra read-only directory
inner run -m ~/shared/libs:/libs:ro

# Pass environment variables
inner run -e DEBUG=true -e LOG_LEVEL=info

# Add extra arguments to the entrypoint
inner run -p default -- ls -la /workspace

# Send a prompt to the agent
inner run -p claude-one-shot -w ~/myproject --prompt "write unit tests for all public functions"

# Set a 5-minute timeout
inner run -p claude-one-shot --timeout 300 --prompt "summarize the codebase"

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
default              Interactive shell, no network
shell                Bash shell, no network
claude-interactive    Claude Code interactive, network enabled
claude-one-shot             Claude Code non-interactive, dangerously-skip-permissions
claude-containers     Claude Code with Podman rootless containers
```

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

Run security checks inside a sandbox to detect exposed sensitive resources.

```
inner verify [flags]
```

### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--profile` | `-p` | string | `default` | Profile to verify |
| `--suggest` | | bool | false | Print TOML fix snippets for failed checks |

### What it checks

- SSH keys (`~/.ssh/`)
- Git credentials and credential helpers
- GPG keys
- Docker socket
- Podman socket
- `.netrc`

Custom checks can be added to a profile under `[verify.custom]` — see [Profiles](profiles.md).

### Examples

```bash
# Verify the default profile
inner verify

# Verify a specific profile
inner verify -p claude-interactive

# Show TOML snippets to fix failures
inner verify -p claude-interactive --suggest
```

Exit code is `1` if any check fails.

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
inner init
```

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

Manage global configuration at `~/.inner/config.toml`.

### `inner config show`

Print the current global configuration:

```bash
inner config show
```

### `inner config edit`

Open `~/.inner/config.toml` in `$EDITOR`:

```bash
inner config edit
```

**Global config options:**

| Key | Default | Description |
|-----|---------|-------------|
| `log_dir` | `~/.inner/logs/` | Directory where run logs are written |

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

## `inner version`

Print version, build time, and git commit:

```bash
inner version
```

Example output:

```
inner v0.3.1 (built 2025-11-01T12:00:00Z, commit abc1234)
```
