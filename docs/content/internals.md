---
title: Internals
description: How inner builds the bwrap command — architecture and low-level decisions for contributors
weight: 6
---



This page is for contributors who want to understand how `inner` works at a low level: how the bwrap command is assembled, why certain flags are applied, and what the design constraints are.

## Layered architecture

The pipeline from user intent to running process has three distinct layers:

```
Profile TOML  ──┐
CLI flags     ──┼──► RunConfig ──► BwrapIsolator.Build() ──► exec.Cmd ──► Launcher.Run()
                ┘
```

### Layer 1: Profile TOML → RunConfig

`config.Loader.Build()` reads `~/.config/inner/profiles/<name>.toml` and converts it to a `config.RunConfig` via `toRunConfig()`. `RunConfig` is the central contract between configuration and execution: it speaks in terms of **intent** (`Network bool`, `Entrypoint.Interactive bool`, `Allow []string`), never in bwrap-specific flags.

CLI overrides (`applyOverrides` in `cmd_run.go`) are applied on top of the loaded `RunConfig` before it reaches the isolator. This keeps the isolator free of any flag-parsing logic.

### Layer 2: RunConfig → bwrap args

`BwrapIsolator.Build(cfg RunConfig)` is a **pure function** with no side effects. It reads fields from `RunConfig` and appends the corresponding bwrap arguments. Every flag decision flows from a `RunConfig` field — there are no hardcoded profile names or special-case strings inside `Build`.

### Layer 3: exec.Cmd → process

`executor.Launcher.Run()` executes the command built by the isolator. It handles PTY attachment, signal forwarding, timeout, and logging. It knows nothing about bwrap internals.

---

## How bwrap flags are decided

`Build` assembles args in sections, in this order:

### Base filesystem

```
--ro-bind / /          bind host root read-only
--proc /proc           fresh /proc
--dev /dev             minimal devtmpfs
--bind /dev/pts /dev/pts       (always, see below)
--dev-bind /dev/ptmx /dev/ptmx (if present, see below)
```


`/dev/pts` is always bound read-write from the host, unconditionally. The reason: `bwrap --dev` creates a minimal devtmpfs that does not include the host's pseudo-terminal nodes. Interactive TUI apps (Node.js/claude, gemini) call `ttyname_r()` internally to resolve their controlling terminal path (e.g. `/dev/pts/3`). Without this bind the syscall returns `ENOENT` and the app cannot initialise its terminal handling. The bind is harmless for non-interactive runs.

`/dev/ptmx` is bound from the host if it exists. Modern Linux systems (e.g., Ubuntu, Debian) often use the `devpts` filesystem with `ptmxmode=000` on the `ptmx` node inside `/dev/pts`. In such cases, opening `/dev/ptmx` via the sandbox's default symlink to `pts/ptmx` would fail. Binding the host's `/dev/ptmx` ensures the kernel uses the global multiplexer node, which correctly routes to the bound `/dev/pts` instance. This prevents `forkpty(3) failed` errors when sandboxed agents attempt to spawn shell processes.

### Additional mounts

Profile mounts from `[mounts]` are emitted in two passes: `tmpfs` mounts first, then `rw`/`ro` binds. This ordering is mandatory because bwrap processes args left-to-right: a bind mount landing inside a tmpfs must come after the tmpfs declaration.

### Home directory

Two models are available, selected by `[sandbox] home` (see [profiles](../profiles/#home-filesystem-model)).

**`host-ro` (default).** The home directory is not mounted explicitly. It enters the sandbox as part of the base `--ro-bind / /`, which makes the entire host filesystem — including `$HOME` — visible read-only. Confidentiality then rests on the sensitive-resource denylist below.

**`isolated`.** `Build` emits `--tmpfs $HOME` immediately after the base filesystem section — before every other mount — and then re-binds the `[sandbox] home_allow` entries read-only:

```
--tmpfs /home/me                              empty writable home
--ro-bind /home/me/.local/bin /home/me/.local/bin   allowlist entry
```

The position in the argv is the whole mechanism: bwrap processes args left-to-right, so profile mounts, capability directories and the workdir bind — all emitted later — land *inside* the tmpfs instead of being erased by it, and bwrap creates their mount points in the tmpfs (they need not exist on the host). Allowlist entries missing on the host are skipped rather than passed to bwrap, which would abort the run.

An unresolvable or unsafe home (`/`, a relative path) is a hard error: silently continuing would produce a sandbox weaker than the profile claims.

To make the home directory (or a subdirectory of it) writable, a profile `[mounts]` entry with `mode = "rw"` is required:

```toml
[mounts]
"~" = "rw"          # make the whole home dir writable
"~/projects" = "rw" # or a specific subtree only
```

`~` is expanded to the real home path by `config.expandPath` before it reaches `Build`. The mount is then emitted as `--bind $HOME $HOME` (a writable bind on top of the read-only root).

Sensitive files within the home directory are hidden after profile mounts are applied (see [Sensitive resource hiding](#sensitive-resource-hiding) below). This ordering matters: a profile tmpfs that covers a sensitive path makes the hide step redundant, and `isUnderTmpfs` skips it to avoid a bind-inside-empty-tmpfs failure.

### Process isolation: `--unshare-pid`

```go
if cfg.PidNamespace {
    args = append(args, "--unshare-pid")
}
```

The decision is driven by `cfg.PidNamespace`, which defaults to **true** and is controlled per-profile via `[sandbox] pid_namespace = false`.

**Why it must be on by default:** the base mount is `--ro-bind / /` and `--proc /proc` mounts a fresh procfs of the *current* PID namespace. Without `--unshare-pid` the sandbox shares the host PID namespace, so every host process is visible under `/proc`. Running as the same UID, the sandboxed agent can then read `/proc/<pid>/environ` of any of the user's other processes — leaking secrets such as `AWS_SECRET_ACCESS_KEY` or `GITHUB_TOKEN` exported in another shell, which defeats the `--clearenv` hygiene — and can send signals to those processes. A private PID namespace closes that hole: inside the sandbox only the sandbox's own processes exist.

**Why it does not break TUI apps (the historical concern):** an earlier version skipped `--unshare-pid` for interactive runs, believing it forced bwrap to call `setsid()` and detach the controlling terminal, making TUI apps (claude, gemini — Node.js/libuv opens `/dev/tty` with `O_RDWR` during init) fail with `ENXIO` and hang. This was a misdiagnosis. Empirical testing (bubblewrap 0.9.0, under a real PTY) shows:

| Flags | `open("/dev/tty", O_RDWR)` + `tcgetattr` | Host processes visible |
|-------|------------------------------------------|------------------------|
| no `--unshare-pid` | OK | **all of them (leak)** |
| `--unshare-pid` | **OK** | isolated |
| `--unshare-pid --new-session` | **ENXIO** | isolated |

The flag that actually detaches the controlling terminal is `--new-session` (it calls `setsid()`), **which `inner` never emits**. With `--unshare-pid` alone, bwrap's internal fork inherits the session and the TTY keeps working.

**Invariants** (enforced by tests in `internal/isolator/bwrap_test.go`):

1. Never add `--new-session` — it breaks every interactive TUI (`ENXIO` on `open("/dev/tty")`).
2. Never add `--as-pid-1` — without bwrap's reaper as PID 1, zombies of double-forking children accumulate.
3. Keep the `pid_namespace = false` escape hatch working — it is the immediate rollback if a kernel/bwrap combination ever regresses.

### Network: `--unshare-net`

```go
if !cfg.Network {
    args = append(args, "--unshare-net")
}
```

Driven by `cfg.Network` (profile `[sandbox] network = true`, CLI `--network` / `--no-network`).

### Nested user namespaces: `--unshare-user` + caps

```go
if isAllowed(cfg.Allow, "nested-user-ns") {
    args = append(args, "--unshare-user")
    args = append(args, "--uid", ..., "--gid", ...)
    args = append(args, "--cap-add", "cap_setuid", "--cap-add", "cap_setgid")
    args = append(args, "--tmpfs", "/var/tmp")
    // --dev-bind /dev/net/tun (if present on host)
}
```

Driven by the presence of `"nested-user-ns"` in `cfg.Allow` (profile `[sandbox] allow = ["nested-user-ns"]`).

This key enables rootless container runtimes (podman, docker rootless) to work inside the sandbox. The full mechanism is a two-phase startup coordinated between bwrap and the host:

1. `Build` adds `--unshare-user` so bwrap creates a fresh user namespace.
2. After `Build` returns, `cmd_run.go` detects `nested-user-ns` in `rc.Allow` and calls `prepareNestedUserNs(cmd)`, which injects `--userns-block-fd` and `--info-fd` into `cmd.Args`.
3. When the process starts bwrap blocks (reading from the block pipe) and writes its child PID to the info pipe.
4. The `postStart` goroutine reads the PID, calls `newuidmap`/`newgidmap` from the **host** (where the setuid bit is fully effective) to install the full subuid range, then closes the block pipe.
5. Bwrap proceeds with a uid map that includes the complete subuid range, enabling nested rootless containers.

`/var/tmp` is overlaid with a tmpfs because podman uses it as scratch space during image pulls, and the host root is bound read-only. `/dev/net/tun` is bound explicitly because bwrap's minimal devtmpfs omits it, but it is required by pasta (podman's rootless network backend).

#### Cgroup manager: the injected `containers.conf`

Rootless podman defaults to `cgroup_manager = "systemd"`, which asks the user systemd manager over D-Bus to create a transient scope (`StartTransientUnit`) with cgroup delegation. That request always fails inside the sandbox, and the failure is *not* a cgroup error: polkit identifies the D-Bus caller by resolving its PID through `/proc` in the **host** PID namespace, while the sandbox has its own (see [`--unshare-pid`](#process-isolation---unshare-pid)). No local active session is found, so `allow_active` on `org.freedesktop.systemd1.manage-units` does not apply, the call falls back to `auth_admin`, and a non-interactive session gets:

```
Access denied as the requested operation requires interactive authentication
```

Two more independent blocks stack on top: profiles that tmpfs `/run/user/<uid>` also hide `systemd/`, so the private `systemctl --user` transport is unavailable; and `/sys/fs/cgroup` arrives read-only from `--ro-bind / /` with no cgroup namespace of our own.

The three ways to make podman's default work are all rejected on security grounds:

| Option | Why not |
|--------|---------|
| Share the host PID namespace | Undoes the `/proc/<pid>/environ` protection above |
| Bind `/run/user/<uid>/systemd/` | The user manager does not consult polkit for same-uid callers, so the sandbox could start arbitrary host units — a full escape |
| A permissive polkit rule | Changes the **host** security posture to accommodate a sandbox |

So `Build` instead binds a generated `containers.conf` fragment (`internal/containers`) at `/tmp/inner/containers.conf` and exports `CONTAINERS_CONF_OVERRIDE`, which podman merges on top of the user's own files. The generation happens in `applyContainersConf` (`cmd_run.go`, mirrored in `cmd_verify.go`) so `Build` stays side-effect free, matching the shim-dir and gitconfig patterns. The `--setenv` is emitted inside the `nested-user-ns` block, i.e. *before* the `[env] set` loop, so a profile that sets `CONTAINERS_CONF_OVERRIDE` itself still wins (bwrap honours the last `--setenv` for a key).

Consequence: per-container resource limits (`--memory`, `--cpus`, `--pids-limit`) are unavailable inside the sandbox. This is consistent with `wrapWithLimits`, which already skips the `systemd-run --scope` wrapper when `nested-user-ns` is active. `[sandbox] cgroup_manager = "systemd"` opts out of the injection.

### Environment

```go
if !cfg.Env.InheritAll {
    args = append(args, "--clearenv")
    for _, key := range cfg.Env.Inherit { ... }
}
for key, val := range cfg.Env.Set { ... }
```

Driven by `cfg.Env` (profile `[env]` section, CLI `-e KEY=VAL`). The host environment is always cleared unless `InheritAll` is set (`inherit_all = true` in the profile). Explicit `set` values always override inherited ones.

### Clipboard / display server

```go
if cfg.Clipboard {
    switch b.info.Display { ... }
}
```

Driven by `cfg.Clipboard` (profile `[sandbox] clipboard = true`) and the host display server detected at startup by `runtime.Detect()`. The isolator never queries the environment directly — it reads from `RuntimeInfo`, which is populated once at construction time.

### Sensitive resource hiding

The following resources are hidden by default:

| Key | Path | Method |
|-----|------|--------|
| `ssh-keys` | `~/.ssh` | `--tmpfs` (empty dir) |
| `gpg-keys` | `~/.gnupg` | `--tmpfs` (empty dir) |
| `git-credentials` | `~/.git-credentials` | `--bind /dev/null` |
| `netrc` | `~/.netrc` | `--bind /dev/null` |
| `docker-socket` | `/var/run/docker.sock` | `--bind /dev/null` |
| `podman-socket` | `/run/user/<uid>/podman/podman.sock` | `--bind /dev/null` |
| `bash-history` | `~/.bash_history` | `--bind /dev/null` |
| `zsh-history` | `~/.zsh_history` | `--bind /dev/null` |
| `aws-credentials` | `~/.aws` | `--tmpfs` (empty dir) |
| `gcloud-credentials` | `~/.config/gcloud` | `--tmpfs` (empty dir) |
| `kube-config` | `~/.kube` | `--tmpfs` (empty dir) |
| `azure-credentials` | `~/.azure` | `--tmpfs` (empty dir) |
| `docker-config` | `~/.docker/config.json` | `--bind /dev/null` |
| `npmrc` | `~/.npmrc` | `--bind /dev/null` |
| `pypirc` | `~/.pypirc` | `--bind /dev/null` |
| `cargo-credentials` | `~/.cargo/credentials`, `~/.cargo/credentials.toml` | `--bind /dev/null` |
| `gh-config` | `~/.config/gh` | `--tmpfs` (empty dir) |
| `terraform-credentials` | `~/.terraform.d` | `--tmpfs` (empty dir) |
| `maven-settings` | `~/.m2/settings.xml`, `~/.m2/settings-security.xml` | `--bind /dev/null` |
| `gradle-properties` | `~/.gradle/gradle.properties` | `--bind /dev/null` |
| `helm-config` | `~/.config/helm` | `--tmpfs` (empty dir) |
| `pgpass` | `~/.pgpass` | `--bind /dev/null` |
| `mysql-config` | `~/.my.cnf` | `--bind /dev/null` |
| `password-store` | `~/.password-store` | `--tmpfs` (empty dir) |
| `keyrings` | `~/.local/share/keyrings` | `--tmpfs` (empty dir) |
| `onepassword-config` | `~/.config/op` | `--tmpfs` (empty dir) |
| `browser-profiles` | `~/.mozilla`, `~/.config/google-chrome`, `~/.config/chromium`, `~/.config/BraveSoftware`, `~/.config/microsoft-edge`, `~/.config/vivaldi`, `~/.config/opera` | `--tmpfs` (empty dir) |

A key may cover several paths (`browser-profiles`, `maven-settings`): all of
them are hidden, and listing the key in `allow` un-hides all of them.

Only the credential files of `~/.m2` and `~/.gradle` are hidden, not the whole
directory: the rest is the local artifact cache, and hiding it would break
offline builds for no security gain.

**This is a denylist, and a denylist rots.** It protects only the paths someone
thought of; a tool that invents a new token path is readable until the list is
updated. `home = "isolated"` (see [profiles](../profiles/#home-filesystem-model)) is the
allowlist model and the real answer for profiles that do not need the host home
— this table is the floor for profiles that stay on `home = "host-ro"`.
Two tests guard it: `TestSensitiveResources_coverWellKnownSecrets` (a canary
list of paths that must be covered) and `inner verify`, which derives one check
per key from this same table.

Each entry is skipped when:

- its key is listed in `cfg.Allow` (user explicitly opted in), or
- the path does not exist on the host (nothing to hide), or
- a profile-level `tmpfs` mount already covers the path (a bind inside an empty tmpfs would fail and is redundant), or
- `home = "isolated"` already replaced the subtree with an empty tmpfs **and** nothing re-exposes the path (`isReexposedInHome`): materialising an empty `~/.ssh` inside an isolated home would be misleading noise. A resource carried back in by a mount or a `home_allow` entry — a profile mounting `~/.cargo` also brings `~/.cargo/credentials` — is still hidden, so `[sandbox] allow` remains the single switch governing sensitive resources in both home modes. Paths outside `$HOME` (the docker socket) are unaffected.

The allow key check (`isAllowed`) is a simple `slices.Contains` — no special-casing. The table itself lives in `config.SensitiveResources` (`internal/config/types.go`) so that the profile validator can reason about the same list: adding a new sensitive resource requires one entry there, and adding a new allow key one entry in `config.ValidAllowKeys`.

### Shim directory

If `cfg.ShimDir` is non-empty (populated by `cmd_run.go` after `shim.Builder.Build()`), the shim directory is bind-mounted at `/tmp/inner-shims` and prepended to `PATH`. `/tmp` is used (not `/run`) because it is already a writable tmpfs at this point and `--dir` can create subdirectories there without touching the read-only root.

### Git config injection

If `cfg.GitConfigPath` is non-empty (set after `git.Sanitize()`), the sanitized gitconfig is bind-mounted at the fixed in-sandbox path `/etc/inner/gitconfig` and `GIT_CONFIG_GLOBAL` is set to that path. The host temp path is never exposed inside the sandbox, so the host `/tmp` layout is not leaked.

---

## The TUI raw-mode decision (`ForceRawMode`)

`executor.RunOptions.ForceRawMode` is driven by `cfg.Entrypoint.TUI`, a bool declared in the profile:

```toml
[entrypoint]
interactive = true
tui         = true
```

When `ForceRawMode` is true, `Launcher.runInteractive` puts the host terminal in raw mode **before** the child starts.

This is intentionally separate from `Entrypoint.Interactive`. The reason: TUI apps built on Node.js/libuv (claude, gemini) send terminal capability queries (`DA`, `XTVERSION`, etc.) during module initialisation, before they call `setRawMode` themselves. In cooked mode the kernel's TTY line discipline buffers the response until a newline arrives; the subsequent `TCSAFLUSH` that the app issues when it takes over the terminal then discards the buffered response, and the app hangs waiting for a reply that never comes.

Plain interactive shells (bash, zsh) must NOT receive pre-raw mode: they configure the terminal themselves via readline, and pre-raw mode breaks bracketed-paste echo, making pasted text invisible.

`ForceRawMode` is only consumed inside the `opts.Interactive` branch of `Launcher.Run` — it is silently ignored for non-interactive runs. The bwrap command is identical regardless; only the Launcher's pre-start terminal configuration differs.

---

## Adding a new sandbox capability

To expose a new sensitive resource:

1. Add an entry to `config.SensitiveResources` (`internal/config/types.go`); `BwrapIsolator.Build` and the profile validator both read it.
2. Add the key to `config.ValidAllowKeys` (`internal/config/types.go`).
3. Document it in `docs/content/profiles.md` under `[sandbox].allow`.

To add a new bwrap-level capability gated by a profile key:

1. Add the key to `config.ValidAllowKeys`.
2. Add an `isAllowed(cfg.Allow, "new-key")` block in `Build`.
3. If post-start host-side work is needed (like `nested-user-ns`), add it in `cmd_run.go:runSandbox` after `iso.Build()`.
