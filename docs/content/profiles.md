---
title: Profiles
description: Profile TOML configuration reference for inner sandboxes
weight: 3
---

# Profiles

A **profile** is a TOML file that fully describes a sandbox environment. Profiles are stored in `~/.inner/profiles/<name>.toml`.

**Local (per-directory) profiles** can be placed in `.inner/profiles/` inside the current working directory. They are discovered automatically and shown with a `[local]` tag in `inner profile list`. When a local profile has the same name as a global one, the **local profile takes precedence** — `inner run -p foo` will load `.inner/profiles/foo.toml` rather than `~/.inner/profiles/foo.toml`. The shadowed global profile is hidden in the default list view and visible only with `inner profile list --wide`.

Alternatively, you can pass a **file path** directly to `-p`/`--profile`. If the value points to an existing file it is loaded as-is; otherwise it is treated as a profile name looked up in the profiles directory. This lets you use local or project-specific profile files without installing them in `~/.inner/profiles/`:

```bash
inner run -p profiles/inner-dev.toml
inner run -p /tmp/my-test-profile.toml
```

## Built-in Profiles

| Name | Description |
|------|-------------|
| `shell` | Interactive bash shell, no network — **default for new installations** |
| `default` | Interactive bash shell, no network, package managers blocked |
| `claude-interactive` | ⚠ experimental — Claude Code interactive session, network enabled |
| `claude-one-shot` | Claude Code non-interactive, `--dangerously-skip-permissions` |
| `claude-containers` | Claude Code with Podman rootless container support |
| `gemini-interactive` | Gemini CLI interactive session, network enabled |
| `gemini-one-shot` | Gemini CLI non-interactive, `--yolo` |
| `shell-containers` | Interactive bash shell with Podman rootless container support |

Inspect any built-in profile:

```bash
inner profile show claude-interactive
```

---

## Profile Structure

```toml
schema_version = "1"
name          = "my-profile"
description   = "Human-readable description"
extends       = ""     # optional: name or path of a base profile to inherit from
experimental  = false  # set to true to prevent inner run from starting

[sandbox]
# ...

[mounts]
# ...

[env]
# ...

[git]
# ...

[entrypoint]
# ...

[output]
# ...

[noop]
# ...

[verify.custom]
# ...
```

---

## Profile Inheritance (`extends`)

A profile can inherit from a base profile using the `extends` field. The child profile starts as a complete copy of the base and then applies its own fields on top — only the fields explicitly declared in the child file take effect.

```toml
extends = "claude"          # name of a profile in ~/.inner/profiles/
# or
extends = "~/my-base.toml"  # explicit file path (~ and absolute paths supported)
```

### Merge semantics

| Type | Behaviour |
|------|-----------|
| Scalar (`bool`, `string`, `int`) | Child wins only when the key is explicitly present in the child file. A missing key keeps the base value, including `false` booleans. |
| Slice (`allow`, `env.inherit`, `noop.block`, `git.strip_sections`) | Union: base items first, then any new items from the child. Duplicates are removed. |
| Map (`mounts`, `env.set`, `noop.rewrite`, `git.overrides`) | Merge: all base entries kept; child entries added or override matching keys. |
| `verify.custom.checks` | Append: base checks first, child checks after. |

### Example

```toml
# ~/.inner/profiles/claude.toml  (base)
[sandbox]
allow = ["ssh-keys"]

[entrypoint]
cmd         = "claude"
interactive = true
tui         = true

[output]
timeout_seconds = 3600
```

```toml
# ~/.inner/profiles/claude-net.toml  (child)
extends     = "claude"
description = "Claude with network access"

[sandbox]
network = true
allow   = ["docker-socket"]  # merged with base: ["ssh-keys", "docker-socket"]

[output]
timeout_seconds = 7200       # overrides base value
```

### Chaining

Inheritance chains are supported — `A extends B extends C` produces the result of applying B on C, then A on that. Cycles are detected and reported as an error.

### `extends` resolution

If the value contains `/`, starts with `~`, or is an absolute path, it is treated as a file path (with `~` expansion). Otherwise it is looked up by name in `~/.inner/profiles/`.

---

## Top-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `schema_version` | string | — | Must be `"1"` |
| `name` | string | — | Profile identifier |
| `description` | string | `""` | Human-readable description shown in `inner profile list` |
| `extends` | string | `""` | Name or path of the base profile to inherit from (see above) |
| `workspaces_path` | string | `""` | Override the global `workspaces_path` for this profile (see [`[mounts]` — workspace directories](#workspace-directories-workspaces_path)) |
| `experimental` | bool | `false` | When `true`, `inner run` refuses to start with an error. The profile remains visible in `inner profile list` with an `[experimental]` prefix. Use this to keep a work-in-progress profile in the repository without accidentally running it. |

---

## `[sandbox]`

Controls top-level sandbox behavior.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `network` | bool | `false` | Allow network access |
| `clipboard` | bool | `false` | Forward clipboard (requires display server) |
| `allow` | list | `[]` | Explicitly permit sensitive resources (see below) |

### `allow` — sensitive resource opt-in

By default `inner` blocks access to sensitive host resources. To grant access, list items in `allow`:

| Value | Resource unlocked |
|-------|-------------------|
| `ssh-keys` | `~/.ssh/` directory (read-only) |
| `git-credentials` | Git credential store / helpers |
| `gpg-keys` | GPG keyring |
| `docker-socket` | `/var/run/docker.sock` (Docker daemon socket) |
| `podman-socket` | `/run/user/$UID/podman/podman.sock` (Podman rootless socket, per-user path) |
| `nested-user-ns` | Unprivileged user namespaces inside sandbox (required for Podman) |
| `netrc` | `~/.netrc` |
| `bash-history` | `~/.bash_history` |
| `zsh-history` | `~/.zsh_history` |

The two socket entries are distinct because they point to different paths: `docker-socket` is the system-level Docker daemon socket, while `podman-socket` is the per-user Podman rootless socket.

> **Note on `DOCKER_HOST`:** unlocking `podman-socket` only bind-mounts the socket file into the sandbox. The `DOCKER_HOST` variable is **not** set automatically — you must add it explicitly in `[env]` so that Docker-compatible clients find the socket:
>
> ```toml
> [sandbox]
> allow = ["podman-socket", "nested-user-ns"]
>
> [env]
> set = { "DOCKER_HOST" = "unix:///run/user/${UID}/podman/podman.sock" }
> ```
>
> `inner` expands `${UID}` to the current user ID at runtime. The built-in `claude-containers` profile already includes this setup.

```toml
[sandbox]
network = true
allow = ["ssh-keys", "git-credentials"]
```

---

## `[mounts]`

Mount host paths into the sandbox. Keys are host paths, values are mount descriptors.

```toml
[mounts]
"~/projects/myapp" = { dest = "/workspace", mode = "rw" }
"~/shared/data"    = { dest = "/data",      mode = "ro" }
"/tmp/build"       = { dest = "/build",     mode = "rw" }
```

| Key in value | Type | Description |
|------|------|-------------|
| `dest` | string | Destination path inside the sandbox |
| `mode` | string | `ro` (read-only) or `rw` (read-write) |

Paths support `~` expansion.

The `--workdir` flag at runtime is a shorthand for adding a `rw` mount of PATH at the same path inside the sandbox (e.g. `-w ~/my-project` mounts `~/my-project` → `~/my-project`).

### `dest` path requirements

`inner` uses `--ro-bind / /` to bind the host root into the sandbox as read-only. This is the **deny-by-default** security model: the entire host filesystem is visible but immutable; only the paths you explicitly mount are writable. A consequence is that every mount destination must exist as a directory on the host before `bwrap` starts — it cannot create new directories inside a read-only root.

| `dest` form | Host requirement |
|-------------|-----------------|
| Path that already exists on the host (e.g. `~/projects`) | No action needed — bwrap binds over it directly |
| Path that does not yet exist on the host | Must be pre-created; use the `${workspaces_path}` token (see below) |

### Workspace directories (`${workspaces_path}`) {#workspace-directories-workspaces_path}

When a mount destination does not exist on the host, use the `${workspaces_path}` token in `dest`. `inner` will pre-create the directory before running `bwrap` and remove it (if empty) after the sandbox exits.

```toml
# ~/.inner/config.toml
workspaces_path = "~/.inner/workspaces"
```

```toml
# profile.toml
[mounts]
"~/projects/myapp" = { dest = "${workspaces_path}/myapp", mode = "rw" }
```

At runtime `inner` resolves `${workspaces_path}` → `~/.inner/workspaces` and runs `mkdir -p ~/.inner/workspaces/myapp` before launching `bwrap`.

**`dest` can be any path under `workspaces_path`**, including nested subdirectories — `inner` uses `os.MkdirAll` so intermediate directories are created automatically. The only requirement is that `workspaces_path` itself already exists on the host.

```toml
# All of these are valid:
"~/src/a" = { dest = "${workspaces_path}/a",       mode = "rw" }
"~/src/b" = { dest = "${workspaces_path}/foo/bar",  mode = "rw" }
"~/src/c" = { dest = "${workspaces_path}/x/y/z",    mode = "rw" }
```

> **Note:** the `${workspaces_path}` token is expanded in mount `dest` fields, in `entrypoint.workdir`, and in alias values (using the global/local config `workspaces_path`). It is never expanded in mount source (key) paths.

#### `workspaces_path` precedence

The effective `workspaces_path` is resolved in this order (highest priority first):

1. **Profile** `workspaces_path` field — overrides everything for that specific profile
2. **Local config** `workspaces_path` (`.inner/config.toml` in the current working directory)
3. **Global config** `workspaces_path` (`~/.inner/config.toml`)

This lets you set a default in `~/.inner/config.toml`, override it per-project in `.inner/config.toml`, and further override it in a specific profile when needed.

```toml
# ~/.inner/config.toml  (global default)
workspaces_path = "~/.inner/workspaces"
```

```toml
# /my-project/.inner/config.toml  (per-project override)
workspaces_path = "/my-project/.workspaces"
```

```toml
# /my-project/.inner/profiles/custom.toml  (per-profile override)
workspaces_path = "/tmp/custom-workspaces"
```

#### Concurrent run protection

`inner` writes a lock file (`.inner-<pid>.lock`) into `workspaces_path` for the duration of each run. If two `inner` invocations attempt to use the same workspace directory simultaneously, the second one fails with an error listing which process holds the lock. Stale lock files (from crashed runs) are detected via PID liveness check and removed automatically on the next run.

---

## `[env]`

Controls environment variable inheritance and injection.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `clearenv` | bool | `true` | Clear all host environment variables |
| `inherit` | list | `[]` | Variables to pass through from the host |
| `set` | table | `{}` | Variables to set explicitly in the sandbox |

```toml
[env]
clearenv = true
inherit  = ["TERM", "LANG", "HOME", "USER"]
set      = { "CI" = "true", "LOG_LEVEL" = "debug" }
```

When `clearenv = false` all host variables are inherited and `set` acts as overrides.

---

## `[git]`

Sanitize the git configuration injected into the sandbox.

| Key | Type | Description |
|-----|------|-------------|
| `strip_sections` | list | Remove entire sections from `~/.gitconfig` (e.g. `["credential"]`) |
| `overrides` | table | Override individual git config keys inside the sandbox |

```toml
[git]
strip_sections = ["credential", "url"]
overrides      = { "push.default" = "nothing" }
```

`push.default = "nothing"` is a useful default to prevent accidental pushes from an agent session.

---

## `[entrypoint]`

Defines what runs inside the sandbox.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `cmd` | string | `$SHELL` | Binary to execute |
| `args` | list | `[]` | Arguments passed to `cmd` |
| `interactive` | bool | `true` | Whether to allocate a PTY |
| `tui` | bool | `false` | Mark as a TUI app that probes terminal capabilities at startup (see below) |
| `workdir` | string | `""` | Default working directory inside the sandbox. Supports `~` and `${workspaces_path}`. Overridable with `--workdir`/`-w` at runtime; falls back to the caller's cwd if unset. |

```toml
[entrypoint]
cmd         = "claude"
args        = ["--dangerously-skip-permissions"]
interactive = false
```

Arguments appended via `--` on the command line, or via `--prompt`, are added after `args`.

### `tui` — terminal initialisation

Some applications (claude, gemini) are built on runtimes like Node.js/libuv that send terminal capability queries (`DA`, `XTVERSION`, etc.) during module initialisation, **before** the app calls `setRawMode` itself. In the default cooked terminal mode the kernel's TTY line discipline buffers those responses until a newline arrives; the subsequent `TCSAFLUSH` that the app issues when it takes over the terminal discards the buffer, and the app hangs waiting for a reply that never comes.

Setting `tui = true` tells the launcher to put the host terminal in raw mode **before** the child starts, so those early queries are answered immediately.

Plain interactive shells (`bash`, `zsh`) must **not** set this: they configure the terminal themselves via readline, and pre-raw mode breaks bracketed-paste echo.

---

## `[output]`

Controls logging and runtime behavior.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `summary` | bool | `false` | Print execution summary after the run |
| `log` | string | `~/.inner/logs/` | Directory for run logs |
| `timeout_seconds` | int | `0` | Kill sandbox after N seconds (`0` = no limit) |

---

## `[noop]`

Shim binaries inside the sandbox to block or redirect commands.

| Key | Type | Description |
|-----|------|-------------|
| `block` | list | Commands that exit 1 with an error message |
| `rewrite` | table | Commands that delegate to a replacement binary |

```toml
[noop]
block   = ["apt-get", "apt", "yum", "dnf", "brew", "pacman", "yay"]
rewrite = { "docker" = "podman", "docker-compose" = "podman-compose" }
```

Shims are generated as shell scripts placed on `$PATH` inside the sandbox, so they transparently intercept calls by name.

---

## `[verify.custom]`

Define additional security checks run by `inner verify`.

```toml
[verify.custom]
checks = [
  { name = "no-aws-creds",  cmd = "test ! -f /root/.aws/credentials", severity = "critical" },
  { name = "no-kubeconfig", cmd = "test ! -f /root/.kube/config",     severity = "high"     },
]
```

| Field | Values | Description |
|-------|--------|-------------|
| `name` | string | Human-readable check identifier |
| `cmd` | string | Shell command run inside the sandbox; exit 0 = pass |
| `severity` | `critical` \| `high` \| `medium` \| `info` | How failures are reported |

---

## Claude Code sandbox (`~/.claude`)

The agent profiles (`claude-interactive`, `claude-one-shot`) declare a mount for `~/.claude`:

```toml
[mounts]
"~/.claude" = { dest = "~/.claude", mode = "rw" }
```

This looks like the entire directory is shared, but `inner` intercepts it at runtime and
**replaces the mount with a sanitized temporary clone**. The real `~/.claude` is never
mounted into the sandbox and is never modified by the agent.

### What the clone contains

| Path | Source | Why |
|------|--------|-----|
| `.credentials.json` | copied from `~/.claude` | Required — auth token for Anthropic API |
| `settings.json` | copied from `~/.claude` | Optional — user preferences (theme, model, …) |
| `skills/` | copied from `~/.claude` | Optional — user-defined skill definitions |
| `sessions/`, `cache/`, `projects/`, `tasks/`, `history/`, … | created empty | Fresh state for this run |

The files are **copied** from the originals before the sandbox starts, so the originals
remain untouched even if the agent writes to its own copies.

### Lifecycle

```
inner run -p claude-interactive
  └─ prepareClaude()
       ├─ create /tmp/inner-claude-XXXXXX/
       ├─ copy .credentials.json  (from ~/.claude)
       ├─ copy settings.json      (from ~/.claude, if present)
       ├─ copy skills/            (from ~/.claude, if present)
       └─ mkdir sessions/ cache/ projects/ tasks/ …  (empty)
  └─ mount /tmp/inner-claude-XXXXXX -> ~/.claude inside sandbox
  └─ [agent runs, writes sessions/tasks/cache to the clone]
  └─ sandbox exits -> rm -rf /tmp/inner-claude-XXXXXX
     ~/.claude on the host is untouched
```

### Consequences

- The agent **can authenticate** (credentials are present in the clone).
- The agent **cannot read** previous sessions, history, or project state from the host.
- Any session data or tasks the agent writes **disappear** when the sandbox exits.
- The host `~/.claude` stays **pristine** regardless of what the agent does.

If you need session data to persist across runs, copy the relevant files out of the
sandbox before it exits, or review captured output with `inner log show`.

---

## Gemini CLI sandbox (`~/.gemini`)

The `gemini-interactive` profile declares a mount for `~/.gemini`:

```toml
[mounts]
"~/.gemini" = { dest = "~/.gemini", mode = "rw" }
```

Like `~/.claude`, this mount is intercepted at runtime and **replaced with a
sanitized temporary clone**. The real `~/.gemini` is never mounted into the sandbox.

### Authentication

Gemini CLI authenticates via the `GEMINI_API_KEY` environment variable, which is
inherited from the host through `env.inherit`. No credentials file needs to be copied.

### What the clone contains

| Path | Source | Why |
|------|--------|-----|
| `settings.json` | copied from `~/.gemini` | Optional — user preferences |
| (everything else) | empty | Fresh state for this run |

### Lifecycle

```
inner run -p gemini-interactive
  └─ prepareGemini()
       ├─ create /tmp/inner-gemini-XXXXXX/
       └─ copy settings.json  (from ~/.gemini, if present)
  └─ mount /tmp/inner-gemini-XXXXXX -> ~/.gemini inside sandbox
  └─ [agent runs, writes to the clone]
  └─ sandbox exits -> rm -rf /tmp/inner-gemini-XXXXXX
     ~/.gemini on the host is untouched
```

### Consequences

- The agent **can authenticate** (via `GEMINI_API_KEY` env var).
- The agent **cannot read** previous sessions or history from the host.
- Any data the agent writes **disappears** when the sandbox exits.
- The host `~/.gemini` stays **pristine** regardless of what the agent does.

---

## Default Profile

`inner run` (without `-p`) uses the profile configured in `~/.inner/config.toml`:

```toml
# ~/.inner/config.toml
default_profile = "shell"
```

New installations set `default_profile = "shell"` automatically. To change it:

```bash
inner config edit   # opens config.toml in $EDITOR
```

Or edit directly:

```toml
default_profile = "claude-interactive"
```

The precedence is: **`-p` flag > `default_profile` in config.toml > `"default"`** (hard fallback).

---

## Managing Profiles

```bash
# List all profiles
inner profile list

# List with scope, path, and shadowed profiles
inner profile list --wide

# Show profile content
inner profile show default

# Create a new profile (opens $EDITOR)
inner profile new my-profile

# Edit an existing profile
inner profile edit my-profile

# Clone a profile as a starting point
inner profile clone claude-interactive my-agent

# Validate all profiles
inner profile validate --all
```

---

## Example: Minimal Custom Profile

```toml
schema_version = "1"
name           = "python-sandbox"
description    = "Isolated Python environment, no network"

[sandbox]
network = false

[mounts]
"~/projects/myapp" = { dest = "/workspace", mode = "rw" }

[env]
clearenv = true
inherit  = ["TERM", "LANG", "HOME"]
set      = { "PYTHONDONTWRITEBYTECODE" = "1" }

[git]
strip_sections = ["credential"]
overrides      = { "push.default" = "nothing" }

[entrypoint]
cmd         = "bash"
interactive = true

[noop]
block = ["pip", "pip3", "pip install"]
```
