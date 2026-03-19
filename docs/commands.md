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
| `--interactive` | `-i` | bool | profile default | Force interactive mode (allocate PTY) |
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
inner run -p agent-interactive

# Mount a project directory as /workspace
inner run -p agent-interactive -w ~/projects/myapp

# Override network for a single run
inner run --network -- curl https://example.com

# Override network off for a single run
inner run -p agent-interactive --no-network

# Mount an extra read-only directory
inner run -m ~/shared/libs:/libs:ro

# Pass environment variables
inner run -e DEBUG=true -e LOG_LEVEL=info

# Add extra arguments to the entrypoint
inner run -p default -- ls -la /workspace

# Send a prompt to the agent
inner run -p one-shot -w ~/myproject --prompt "write unit tests for all public functions"

# Set a 5-minute timeout
inner run -p one-shot --timeout 300 --prompt "summarize the codebase"

# Preview the bwrap command without running
inner run -p agent-interactive --dry-run
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
agent-interactive    Claude Code interactive, network enabled
one-shot             Claude Code non-interactive, dangerously-skip-permissions
agent-containers     Claude Code with Podman rootless containers
```

### `inner profile show`

Display the TOML content of a profile:

```bash
inner profile show agent-interactive
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
inner profile clone agent-interactive my-agent
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
inner verify -p agent-interactive

# Show TOML snippets to fix failures
inner verify -p agent-interactive --suggest
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
profile agent-containers: installed
profile agent-interactive: installed
profile default: installed
profile one-shot: installed
profile shell: installed
```

Example output when everything already exists:

```
dir: /home/alice/.inner
config: already exists (skipped)
profile agent-containers: already exists (skipped)
profile agent-interactive: already exists (skipped)
profile default: already exists (skipped)
profile one-shot: already exists (skipped)
profile shell: already exists (skipped)
```

### Resetting to defaults

If you have not customized any files in `~/.inner/` and want to start fresh (for example,
to pick up updated built-in profiles after an upgrade), it is safe to delete the entire
directory and re-run `inner init`:

```bash
rm -rf ~/.inner
inner init
```

**What to preserve before deleting:**

| Path | Preserve if... |
|------|----------------|
| `~/.inner/config.toml` | You have changed `log_dir` or other settings |
| `~/.inner/profiles/*.toml` | You have edited or added custom profiles |
| `~/.inner/logs/` | You want to keep run history |

The `~/.inner/directives/` directory holds custom verification directives; back it up if
you have added any.

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
- `~/.inner/profiles/` directory exists
- `~/.inner/logs/` directory exists
- `ANTHROPIC_API_KEY` environment variable set
- `claude` binary found on `$PATH`
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
