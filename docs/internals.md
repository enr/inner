---
title: Internals
description: How inner builds the bwrap command — architecture and low-level decisions for contributors
---

# Internals

This page is for contributors who want to understand how `inner` works at a low level: how the bwrap command is assembled, why certain flags are applied, and what the design constraints are.

## Layered architecture

The pipeline from user intent to running process has three distinct layers:

```
Profile TOML  ──┐
CLI flags     ──┼──► RunConfig ──► BwrapIsolator.Build() ──► exec.Cmd ──► Launcher.Run()
                ┘
```

### Layer 1: Profile TOML → RunConfig

`config.Loader.Build()` reads `~/.inner/profiles/<name>.toml` and converts it to a `config.RunConfig` via `toRunConfig()`. `RunConfig` is the central contract between configuration and execution: it speaks in terms of **intent** (`Network bool`, `Entrypoint.Interactive bool`, `Allow []string`), never in bwrap-specific flags.

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
--dev-bind /dev/pts /dev/pts   (always, see below)
--tmpfs /tmp           empty writable /tmp
```

`/dev/pts` is always bound read-write from the host, unconditionally. The reason: `bwrap --dev` creates a minimal devtmpfs that does not include `/dev/pts`. Interactive TUI apps (Node.js/claude, gemini) call `ttyname_r()` internally to resolve their controlling terminal path (e.g. `/dev/pts/3`). Without this bind the syscall returns `ENOENT` and the app cannot initialise its terminal handling. The bind is harmless for non-interactive runs.

### Additional mounts

Profile mounts from `[mounts]` are emitted in two passes: `tmpfs` mounts first, then `rw`/`ro` binds. This ordering is mandatory because bwrap processes args left-to-right: a bind mount landing inside a tmpfs must come after the tmpfs declaration.

### Home directory

The home directory is not mounted explicitly. It enters the sandbox as part of the base `--ro-bind / /`, which makes the entire host filesystem — including `$HOME` — visible read-only.

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
if !cfg.Entrypoint.Interactive {
    args = append(args, "--unshare-pid")
}
```

The decision is driven solely by `cfg.Entrypoint.Interactive`, a bool set in the profile (`[entrypoint] interactive = true`) or overridden at the CLI (`-i` / `--no-interactive`).

**Why it cannot be unconditional:** when `--unshare-pid` is active bwrap forks internally and the child calls `setsid()`, creating a new session with no controlling terminal. Interactive TUI apps call `open("/dev/tty")` at startup to initialise raw mode; with no controlling terminal that call fails with `ENXIO` and the app hangs silently with no output.

There is no check for a specific profile name — any profile or CLI invocation that sets `interactive = true` gets this behaviour.

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

### Environment

```go
if cfg.Env.Clear {
    args = append(args, "--clearenv")
    for _, key := range cfg.Env.Inherit { ... }
}
for key, val := range cfg.Env.Set { ... }
```

Driven by `cfg.Env` (profile `[env]` section, CLI `-e KEY=VAL`). Explicit `set` values always override inherited ones.

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

Each entry is skipped when:

- its key is listed in `cfg.Allow` (user explicitly opted in), or
- the path does not exist on the host (nothing to hide), or
- a profile-level `tmpfs` mount already covers the path (a bind inside an empty tmpfs would fail and is redundant).

The allow key check (`isAllowed`) is a simple `slices.Contains` — no special-casing. Adding a new sensitive resource requires one entry in the `sensitive` slice; adding a new allow key requires one entry in `config.ValidAllowKeys`.

### Shim directory

If `cfg.ShimDir` is non-empty (populated by `cmd_run.go` after `shim.Builder.Build()`), the shim directory is bind-mounted at `/tmp/inner-shims` and prepended to `PATH`. `/tmp` is used (not `/run`) because it is already a writable tmpfs at this point and `--dir` can create subdirectories there without touching the read-only root.

### Git config injection

If `cfg.GitConfigPath` is non-empty (set after `git.Sanitize()`), the sanitized gitconfig is bind-mounted at its temp path and `GIT_CONFIG_GLOBAL` is set to point to it.

---

## The TUI raw-mode decision (`ForceRawMode`)

This is the one place in the codebase where a specific application name drives behaviour:

```go
// cmd_run.go
func isTUIApp(cmd string) bool {
    switch filepath.Base(cmd) {
    case "claude", "gemini":
        return true
    }
    return false
}
```

`isTUIApp` feeds `executor.RunOptions.ForceRawMode`. When true, `Launcher.runInteractive` puts the host terminal in raw mode **before** the child starts.

This is intentionally separate from `Entrypoint.Interactive`. The reason: TUI apps built on Node.js/libuv (claude, gemini) send terminal capability queries (`DA`, `XTVERSION`, etc.) during module initialisation, before they call `setRawMode` themselves. In cooked mode the kernel's TTY line discipline buffers the response until a newline arrives; the subsequent `TCSAFLUSH` that the app issues when it takes over the terminal then discards the buffered response, and the app hangs waiting for a reply that never comes.

Plain interactive shells (bash, zsh) must NOT receive pre-raw mode: they configure the terminal themselves via readline, and pre-raw mode breaks bracketed-paste echo, making pasted text invisible.

The check is isolated to `cmd_run.go` and does not reach the isolator. The bwrap command is identical regardless — only the Launcher's pre-start terminal configuration differs.

---

## Adding a new sandbox capability

To expose a new sensitive resource:

1. Add an entry to the `sensitive` slice in `BwrapIsolator.Build` (`internal/isolator/bwrap.go`).
2. Add the key to `config.ValidAllowKeys` (`internal/config/types.go`).
3. Document it in `docs/profiles.md` under `[sandbox].allow`.

To add a new bwrap-level capability gated by a profile key:

1. Add the key to `config.ValidAllowKeys`.
2. Add an `isAllowed(cfg.Allow, "new-key")` block in `Build`.
3. If post-start host-side work is needed (like `nested-user-ns`), add it in `cmd_run.go:runSandbox` after `iso.Build()`.
