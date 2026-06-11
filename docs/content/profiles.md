---
title: Profiles
description: Profile TOML configuration reference for inner sandboxes
weight: 3
---



A **profile** is a TOML file that fully describes a sandbox environment. Profiles are stored in `~/.config/inner/profiles/<name>.toml`.

**Local (per-directory) profiles** can be placed in `.config/inner/profiles/` inside the current working directory. They are discovered automatically and shown with a `[local]` tag in `inner profile list`. When a local profile has the same name as a global one, the **local profile takes precedence** — `inner run -p foo` will load `.config/inner/profiles/foo.toml` rather than `~/.config/inner/profiles/foo.toml`. The shadowed global profile is hidden in the default list view and visible only with `inner profile list --wide`.

Alternatively, you can pass a **file path** directly to `-p`/`--profile`. If the value points to an existing file it is loaded as-is; otherwise it is treated as a profile name looked up in the profiles directory. This lets you use local or project-specific profile files without installing them in `~/.config/inner/profiles/`:

```bash
inner run -p profiles/inner-dev.toml
inner run -p /tmp/my-test-profile.toml
```

You can also pass an **HTTP/HTTPS URL**. `inner` downloads the TOML, validates it, and uses it for that single run — nothing is written to disk:

```bash
inner run -p https://raw.githubusercontent.com/acme/profiles/main/claude-restricted.toml
```

To permanently install a remote profile, use [`inner profile install`](#inner-profile-install).

## Built-in Profiles

These profiles are embedded in the binary and installed automatically on first run (`inner init`):

| Name | Description |
|------|-------------|
| `shell` | Interactive bash shell, no network — **default for new installations** |
| `shell-oneshot` | Run a single shell command in sandbox (no network) |
| `claude-interactive` | Claude Code interactive session, network enabled |
| `claude-one-shot` | Claude Code non-interactive, `--dangerously-skip-permissions` |
| `gemini-interactive` | Gemini CLI interactive session, network enabled |
| `gemini-one-shot` | Gemini CLI non-interactive, `--yolo` |
| `cursor-interactive` | Cursor Agent interactive session, network enabled |

Inspect any built-in profile:

```bash
inner profile show claude-interactive
```

## Contrib Profiles

Additional profiles are available in the [`contrib/profiles/`](https://github.com/enr/inner/tree/main/contrib/profiles) folder of the repository. They cover more specific setups (container support, language toolchains) and can be installed on demand with `inner profile install`:

| Name | Description | Requires |
|------|-------------|----------|
| `shell-containers` | Interactive bash shell with Podman rootless support | Podman |
| `shell-with-claude` | Interactive bash shell with Claude Code available | — |
| `claude-containers` | Claude Code agent with Podman rootless support | Podman |
| `java-maven` | Interactive shell with Java + Maven + Podman | `shell-containers` |
| `gradle-java` | Interactive shell with Java + Gradle + Podman | `shell-containers` |

Install a contrib profile (name is derived from the URL automatically):

```bash
# Base URL for contrib profiles
BASE=https://raw.githubusercontent.com/enr/inner/main/contrib/profiles

inner profile install $BASE/shell-containers.toml
inner profile install $BASE/shell-with-claude.toml
inner profile install $BASE/claude-containers.toml

# Java toolchains depend on shell-containers — install that first
inner profile install $BASE/shell-containers.toml
inner profile install $BASE/java-maven.toml
inner profile install $BASE/gradle-java.toml
```

Or use a profile directly without installing it:

```bash
inner run -p $BASE/shell-containers.toml
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
extends = "claude"          # name of a profile in ~/.config/inner/profiles/
# or
extends = "~/my-base.toml"  # explicit file path (~ and absolute paths supported)
```

### Merge semantics

| Type | Behaviour |
|------|-----------|
| Scalar (`bool`, `string`, `int`) | Child wins only when the key is explicitly present in the child file. A missing key keeps the base value, including `false` booleans. |
| Slice (`allow`, `capabilities`, `env.inherit`, `noop.block`, `git.strip_sections`) | Union: base items first, then any new items from the child. Duplicates are removed. |
| Map (`mounts`, `env.set`, `noop.rewrite`, `git.overrides`) | Merge: all base entries kept; child entries added or override matching keys. |
| `verify.custom.checks` | Append: base checks first, child checks after. |

### Example

```toml
# ~/.config/inner/profiles/claude.toml  (base)
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
# ~/.config/inner/profiles/claude-net.toml  (child)
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

If the value contains `/`, starts with `~`, or is an absolute path, it is treated as a file path (with `~` expansion). Otherwise it is looked up by name in `~/.config/inner/profiles/`.

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
| `capabilities` | list | `[]` | Named integrations to activate for this sandbox (e.g. `["claude"]`). Each capability injects preconfigured mounts at runtime without requiring explicit `[mounts]` entries. See [Capabilities](#capabilities). |

---

## `[sandbox]`

Controls top-level sandbox behavior.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `network` | bool | `false` | Allow network access |
| `clipboard` | bool | `false` | Forward clipboard (requires display server) |
| `pid_namespace` | bool | `true` | Give the sandbox a private PID namespace (`--unshare-pid`). See below. |
| `allow` | list | `[]` | Explicitly permit sensitive resources (see below) |

### `pid_namespace` — process isolation

When `true` (the default) the sandbox runs in its own PID namespace and cannot
see host processes. This prevents a sandboxed agent from reading
`/proc/<pid>/environ` of your other processes (which would leak secrets like
`AWS_*` / `GITHUB_TOKEN` exported in another shell) or signalling them.

Leave this on. Set it to `false` **only** as an emergency escape hatch if an
interactive TUI app misbehaves on an unusual kernel/bubblewrap combination:

```toml
[sandbox]
pid_namespace = false   # NOT recommended — host processes become visible
```

PID isolation does **not** break TUI apps: `inner` never passes
`--new-session` (the flag that would detach the controlling terminal), so
`--unshare-pid` is safe for claude, gemini, and interactive shells.

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
| `mode` | string | `ro` (read-only), `rw` (read-write), `safe-rw` (copy-on-run, see below), or `tmpfs` (in-memory filesystem) |

Source paths (keys) support `~` expansion, `$VAR`/`${VAR}` environment variable substitution, and the `${workdir}` token described below.

The `--workdir` flag at runtime is a shorthand for adding a `rw` mount of PATH at the same path inside the sandbox (e.g. `-w ~/my-project` mounts `~/my-project` → `~/my-project`).

### `safe-rw` — copy-on-run {#safe-rw}

When `mode = "safe-rw"`, `inner` copies the source directory to a temporary directory before the sandbox starts, binds the copy as `rw`, then deletes the temporary directory when the sandbox exits. The original source is never mounted and is never modified.

```toml
[mounts]
"~/projects/myapp" = { dest = "/workspace", mode = "safe-rw" }
```

Use this when you want an agent to have full write access to a directory without risking changes to the host copy. It is the generic equivalent of what the built-in `claude` capability does automatically for `~/.claude`.

> **Note:** The validator requires the source to exist on the host at validation time (so the copy can succeed at run time). Missing sources produce a validation error.

---

### Portable source paths (`${workdir}`) {#portable-source-paths-workdir}

When a profile lives inside a project's `.config/inner/profiles/` directory, hard-coding the host source path ties the profile to a specific machine. Use the `${workdir}` token in the mount source to refer portably to the directory from which `inner` is invoked (i.e. the project root that contains `.config/inner/`):

```toml
# .config/inner/profiles/my-project.toml
[mounts]
"${workdir}" = { dest = "~/projects/myapp", mode = "rw" }
```

On any machine, `${workdir}` expands to the cwd at the time `inner run` is called — no per-developer edits needed.

The token can also appear as part of a longer path:

```toml
"${workdir}/src" = { dest = "/workspace/src", mode = "rw" }
```

> **Note:** `${workdir}` is only meaningful in mount **source** (key) paths. It is not expanded in `dest` fields or elsewhere — use `${workspaces_path}` for dest-side indirection.

### `dest` path requirements

`inner` uses `--ro-bind / /` to bind the host root into the sandbox as read-only. This is the **deny-by-default** security model: the entire host filesystem is visible but immutable; only the paths you explicitly mount are writable. A consequence is that every mount destination must exist as a directory on the host before `bwrap` starts — it cannot create new directories inside a read-only root.

| `dest` form | Host requirement |
|-------------|-----------------|
| Path that already exists on the host (e.g. `~/projects`) | No action needed — bwrap binds over it directly |
| Path that does not yet exist on the host | Must be pre-created; use the `${workspaces_path}` token (see below) |

`inner profile validate` (and `inner run`) checks that every `dest` exists on the host before launching `bwrap`. A missing dest produces a clear error:

```
profile [error] mount dest "/nonexistent/path" does not exist on host (expanded: "/nonexistent/path")
```

Dests that use the `${workspaces_path}` token are exempt — `inner` creates them automatically (see [Workspace directories](#workspace-directories-workspaces_path)).

### Workspace directories (`${workspaces_path}`) {#workspace-directories-workspaces_path}

When a mount destination does not exist on the host, use the `${workspaces_path}` token in `dest`. `inner` will pre-create the directory before running `bwrap` and remove it (if empty) after the sandbox exits.

```toml
# ~/.config/inner/config.toml
workspaces_path = "~/.config/inner/workspaces"
```

```toml
# profile.toml
[mounts]
"~/projects/myapp" = { dest = "${workspaces_path}/myapp", mode = "rw" }
```

At runtime `inner` resolves `${workspaces_path}` → `~/.config/inner/workspaces` and runs `mkdir -p ~/.config/inner/workspaces/myapp` before launching `bwrap`.

**`dest` can be any path under `workspaces_path`**, including nested subdirectories — `inner` uses `os.MkdirAll` so intermediate directories are created automatically. The only requirement is that `workspaces_path` itself already exists on the host.

```toml
# All of these are valid:
"~/src/a" = { dest = "${workspaces_path}/a",       mode = "rw" }
"~/src/b" = { dest = "${workspaces_path}/foo/bar",  mode = "rw" }
"~/src/c" = { dest = "${workspaces_path}/x/y/z",    mode = "rw" }
```

> **Note:** the `${workspaces_path}` token is expanded in mount `dest` fields, in `entrypoint.workdir`, and in alias values. It is not expanded in mount source (key) paths — use `${workdir}` there instead.

#### `workspaces_path` precedence

The effective `workspaces_path` is resolved in this order (highest priority first):

1. **Profile** `workspaces_path` field — overrides everything for that specific profile
2. **Local config** `workspaces_path` (`.config/inner.toml` in the current working directory)
3. **Global config** `workspaces_path` (`~/.config/inner/config.toml`)

This lets you set a default in `~/.config/inner/config.toml`, override it per-project in `.config/inner.toml`, and further override it in a specific profile when needed.

```toml
# ~/.config/inner/config.toml  (global default)
workspaces_path = "~/.config/inner/workspaces"
```

```toml
# /my-project/.config/inner.toml  (per-project override)
workspaces_path = "/my-project/.workspaces"
```

```toml
# /my-project/.config/inner/profiles/custom.toml  (per-profile override)
workspaces_path = "/tmp/custom-workspaces"
```

#### Concurrent run protection

`inner` writes a lock file (`.inner-<pid>.lock`) into `workspaces_path` for the duration of each run. If two `inner` invocations attempt to use the same workspace directory simultaneously, the second one fails with an error listing which process holds the lock. Stale lock files (from crashed runs) are detected via PID liveness check and removed automatically on the next run.

---

## `[env]`

Controls environment variable inheritance and injection.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `inherit_all` | bool | `false` | Inherit the full host environment (opt-in; leaks secrets) |
| `inherit` | list | `[]` | Variables to pass through from the host |
| `set` | table | `{}` | Variables to set explicitly in the sandbox |

```toml
[env]
inherit  = ["TERM", "LANG", "HOME", "USER"]
set      = { "CI" = "true", "LOG_LEVEL" = "debug" }
```

The host environment is cleared by default. Set `inherit_all = true` to inherit all host variables (not recommended — leaks secrets such as `AWS_*`, `GITHUB_TOKEN`, `SSH_AUTH_SOCK`). Use `inherit` to selectively pass through individual variables.

> **Note:** the legacy `clearenv` TOML key is accepted for backward compatibility but has no effect; clearing is always the default.

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
| `cursor_fix` | string | `""` | Cursor-repair strategy after a TUI app exits (see below) |
| `workdir` | string | `""` | Default working directory inside the sandbox. Supports `~` and `${workspaces_path}`. Overridable with `--workdir`/`-w` at runtime; falls back to the caller's cwd if unset. |
| `history` | list | `[]` | Commands pre-loaded into the shell history. Recall them with the up-arrow key immediately after entering the sandbox. Currently supported for interactive bash sessions; silently ignored for other shells. |

```toml
[entrypoint]
cmd         = "claude"
args        = ["--dangerously-skip-permissions"]
interactive = false
```

Arguments appended via `--` on the command line, or via `--arg`, are added after `args`.

### `history` — pre-populated shell history

Use `history` to pre-load a list of commands into the shell history so users can recall them immediately with the up-arrow key:

```toml
[entrypoint]
cmd         = "/bin/bash"
interactive = true
history     = [
  "git status",
  "mvn clean install -DskipTests",
  "claude --dangerously-skip-permissions",
]
```

Commands are injected oldest-first, so the **last entry in the list sits at the top of the history stack** and is recalled first with up-arrow. The feature is currently implemented for interactive bash sessions; it is silently ignored for other shells.

### `tui` — terminal initialisation

Some applications (claude, gemini) are built on runtimes like Node.js/libuv that send terminal capability queries (`DA`, `XTVERSION`, etc.) during module initialisation, **before** the app calls `setRawMode` itself. In the default cooked terminal mode the kernel's TTY line discipline buffers those responses until a newline arrives; the subsequent `TCSAFLUSH` that the app issues when it takes over the terminal discards the buffer, and the app hangs waiting for a reply that never comes.

Setting `tui = true` tells the launcher to put the host terminal in raw mode **before** the child starts, so those early queries are answered immediately.

Plain interactive shells (`bash`, `zsh`) must **not** set this: they configure the terminal themselves via readline, and pre-raw mode breaks bracketed-paste echo.

### `cursor_fix` — cursor repair after TUI exit

When a TUI application (e.g. claude) is interrupted with CTRL+C it may leave the cursor in the middle of its rendered area instead of moving it below. The `cursor_fix` field selects a repair strategy:

| Value | Effect |
|-------|--------|
| `""` (default) | No repair — cursor position is unchanged after exit |
| `"newlines"` | Prints `\r\n\r\n\r\n` after inner's child process exits. Suitable when the entrypoint **is** the TUI app but `tui = true` is not used (rare). |
| `"shell"` | For bash/zsh entrypoints that **run** TUI children interactively. Does two things: injects a `PROMPT_COMMAND` into the sandbox so bash moves the cursor and clears stale TUI content before each prompt; and prints `\r\n` after the shell itself exits. |

**Use `cursor_fix = "shell"` when the entrypoint is a shell (bash/zsh) and the user will launch TUI apps inside it.** This is the value used by the built-in `shell-with-claude` profile.

```toml
[entrypoint]
cmd        = "/bin/bash"
interactive = true
tui        = false        # bash manages the terminal itself
cursor_fix = "shell"      # fix cursor after claude (or any TUI child) exits
```

The `PROMPT_COMMAND` injection prepends `printf '\r\n\033[J'` to any existing `PROMPT_COMMAND` value — it resets the cursor to column 0, moves down one line, and clears any stale TUI content below. User-defined `PROMPT_COMMAND` entries set via `[env.set]` are preserved.

**Note:** `cursor_fix` and `tui` address different problems and can coexist, but for plain shell entrypoints only `cursor_fix = "shell"` is needed — do **not** set `tui = true` for bash/zsh.

---

## `[output]`

Controls logging and runtime behavior.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `summary` | bool | `false` | Print execution summary after the run |
| `log` | string | `~/.config/inner/logs/` | Directory for run logs |
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

## Capabilities {#capabilities}

A **capability** is a named integration that injects preconfigured mounts at runtime. Instead of listing the required host paths manually in `[mounts]`, a profile just declares which capabilities it needs:

```toml
capabilities = ["claude"]
```

`inner` resolves the capability at startup, copies the relevant directories to sandboxed temporary locations, and binds them as `rw` mounts — all without any explicit `[mounts]` entries.

### Known capabilities

| Name | What it injects |
|------|----------------|
| `claude` | `~/.claude` (sanitized clone) and `~/.claude.json` (copy, if present). Runs `claude auth status` on the host before copying credentials to unlock the OS credential store and refresh any expired OAuth token. |
| `gemini` | `~/.gemini` (sanitized clone with `settings.json` only) |
| `cursor` | `~/.cursor` (sanitized clone) and `~/.config/cursor` (sanitized clone) |

Capabilities compose with `extends`: if a base profile declares `capabilities = ["claude"]` every child profile inherits it automatically (union merge, no duplicates).

### Inspecting a capability

```bash
inner profile show claude-interactive --explain
```

The `--explain` flag appends a `── capability: <name> ───` section to the output that lists the mounts injected, any pre-run actions (e.g. token refresh), and notes about the capability's behaviour.

### Validator checks

- Unknown capability names are a **validation error**.
- If a capability's primary host directory does not exist (e.g. `~/.claude` is absent), `inner profile validate` emits a **warning** — the run-time handler will produce a clearer error when it actually needs the directory.
- Declaring a capability **and** an explicit mount whose `dest` overlaps with one of the capability's injected destinations is a **validation error** (would cause a double-bind in bwrap).

---

## Claude Code sandbox (`~/.claude`)

The agent profiles (`claude-interactive`, `claude-one-shot`) activate the `claude` capability:

```toml
capabilities = ["claude"]
```

At runtime, `inner` **creates a sanitized temporary clone** of `~/.claude` and binds that into the sandbox. The real `~/.claude` is never mounted and is never modified by the agent.

### What the clone contains

| Path | Source | Why |
|------|--------|-----|
| `.credentials.json` | copied from `~/.claude` | Required — auth token for Anthropic API |
| `settings.json` | copied from `~/.claude`, stripped | Optional — user preferences (theme, model, …); `mcpServers` and `enabledPlugins` keys are removed to prevent MCP servers from being spawned inside the sandbox, where they would hang |
| `skills/` | copied from `~/.claude` | Optional — user-defined skill definitions |
| `sessions/`, `cache/`, `projects/`, `tasks/`, `history/`, … | created empty | Fresh state for this run |

The files are **copied** from the originals before the sandbox starts, so the originals
remain untouched even if the agent writes to its own copies.

### Credential store unlock and token refresh

Before copying `.credentials.json` into the sandbox, `inner` always runs `claude auth status` on the host. This step serves two purposes:

1. **OS credential store unlock.** On Linux and macOS, Claude stores its OAuth token in the OS native credential store (libsecret/gnome-keyring on Linux, Keychain on macOS). If the store is locked when the sandbox process starts, Claude cannot read its credentials even though `.credentials.json` is present. Running `claude auth status` on the host — before the sandbox is created — prompts the OS to unlock the store so the token is accessible.

2. **Token refresh.** If the stored OAuth token is expired, Claude's startup sequence refreshes it silently during the `auth status` call, ensuring the freshest token is captured in the sandbox copy.

`inner` then checks whether the token in `.credentials.json` is still expired after the unlock attempt. If so, it aborts with an actionable error instead of launching a sandbox that would immediately receive a 401:

```
Claude OAuth token is expired and could not be refreshed automatically.
Run 'claude' on the host machine to renew it, then relaunch inner.
```

To manually renew the token, run `claude auth login` on the host. If the OAuth **refresh token** is still valid, Claude will refresh silently without opening a browser; otherwise the full browser-based login flow runs.

### Lifecycle

```
inner run -p claude-interactive
  └─ applyClaude()
       ├─ claude auth status   (host; unlocks OS credential store, refreshes token)
       ├─ prepareClaude()
       │    ├─ create /tmp/inner-claude-XXXXXX/
       │    ├─ copy .credentials.json  (from ~/.claude)
       │    ├─ copy settings.json      (from ~/.claude, if present)
       │    ├─ copy skills/            (from ~/.claude, if present)
       │    └─ mkdir sessions/ cache/ projects/ tasks/ …  (empty)
       └─ copy ~/.claude.json -> /tmp/inner-claudejson-XXXXXX  (if present)
  └─ mount /tmp/inner-claude-XXXXXX    -> ~/.claude      inside sandbox
  └─ mount /tmp/inner-claudejson-XXXXXX -> ~/.claude.json inside sandbox
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

The `gemini-interactive` profile activates the `gemini` capability:

```toml
capabilities = ["gemini"]
```

Like `claude`, at runtime `inner` **creates a sanitized temporary clone** of `~/.gemini` and binds that into the sandbox. The real `~/.gemini` is never mounted.

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

`inner run` (without `-p`) uses the profile configured in `~/.config/inner/config.toml`:

```toml
# ~/.config/inner/config.toml
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

# Download and install a profile from a URL
inner profile install https://example.com/my-profile.toml

# Install with a custom local name
inner profile install https://example.com/my-profile.toml --name my-custom-name

# Overwrite an existing profile
inner profile install https://example.com/my-profile.toml --force
```

### `inner profile install` {#inner-profile-install}

Downloads a profile TOML from an HTTP/HTTPS URL and installs it in `~/.config/inner/profiles/`.

```
inner profile install URL [--name NAME] [--force]
```

| Flag | Description |
|------|-------------|
| `--name NAME` | Override the profile name (default: derived from the last URL path segment, `.toml` stripped) |
| `--force` | Overwrite an existing profile with the same name |

The TOML is validated before writing to disk. The downloaded profile is subject to the same constraints as any local profile — review it before running with `inner run -p <url>` or `inner profile show` after installing.

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
