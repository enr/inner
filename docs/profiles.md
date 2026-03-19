---
title: Profiles
description: Profile TOML configuration reference for inner sandboxes
---

# Profiles

A **profile** is a TOML file that fully describes a sandbox environment. Profiles are stored in `~/.inner/profiles/<name>.toml`.

## Built-in Profiles

| Name | Description |
|------|-------------|
| `default` | Interactive bash shell, no network, package managers blocked |
| `shell` | Explicit bash shell, no network |
| `agent-interactive` | Claude Code interactive session, network enabled |
| `one-shot` | Claude Code non-interactive, `--dangerously-skip-permissions` |
| `agent-containers` | Claude Code with Podman rootless container support |

Inspect any built-in profile:

```bash
inner profile show agent-interactive
```

---

## Profile Structure

```toml
schema_version = "1"
name = "my-profile"
description = "Human-readable description"

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
| `docker-socket` | `/var/run/docker.sock` |
| `podman-socket` | `/run/user/$UID/podman/podman.sock` |
| `nested-user-ns` | Unprivileged user namespaces inside sandbox (required for Podman) |
| `netrc` | `~/.netrc` |

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

The `--workdir` flag at runtime is a shorthand for adding a `rw` mount at `/workspace`.

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

```toml
[entrypoint]
cmd         = "claude"
args        = ["--dangerously-skip-permissions"]
interactive = false
```

Arguments appended via `--` on the command line, or via `--prompt`, are added after `args`.

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

## Managing Profiles

```bash
# List all profiles
inner profile list

# Show profile content
inner profile show default

# Create a new profile (opens $EDITOR)
inner profile new my-profile

# Edit an existing profile
inner profile edit my-profile

# Clone a profile as a starting point
inner profile clone agent-interactive my-agent

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
