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

## The trust boundary: profiles downloaded from a URL

A local profile is trusted: the user wrote it, or installed it deliberately. A
profile fetched by `inner run <https://…>` is not — and it controls **every**
input of layer 1: the entrypoint that executes, the network, whether the host
environment is forwarded (`inherit_all`), which credentials stay visible
(`[sandbox] allow`), the mounts. Without a boundary, one URL means "run this
command on my machine with my secrets", which is precisely what the sandbox
exists to prevent.

The boundary lives in `cmd/inner/remote_profile.go` and is applied in
`runSandbox` **after** `Loader.Build()` (so it sees the merged config, `extends`
included) and **before** `applyOverrides` (so the user's own CLI flags are
applied on top of the hardened profile, never underneath it):

```
fetch bytes ─► --sha256 pin ─► temp file ─► Loader.Build ─► gateRemoteProfile ─► applyOverrides ─► …
                                                             │
                                          hardenRemoteProfile ┴ consent prompt
```

### What the hardening removes

`hardenRemoteProfile(rc *config.RunConfig)` mutates the built `RunConfig` and
returns one line per change, which the gate prints:

| Setting requested by the profile | Result | Why |
|----------------------------------|--------|-----|
| `[env] inherit_all = true` | ignored | forwards every exported host secret (`AWS_*`, `GITHUB_TOKEN`, …) |
| `[env] inherit = [...]` with a secret-looking name (`*TOKEN*`, `*SECRET*`, `*PASSWORD*`, `*PASSWD*`, `*CREDENTIAL*`, `*KEY*`, `*AUTH*`, `*SESSION*`) | entry dropped | otherwise the profile just names the secret it wants instead of asking for all of them |
| `[sandbox] allow` key in `config.CredentialAllowKeys` | key dropped | un-hides a readable credential from the [hide table](#sensitive-resource-hiding) |
| `[sandbox] allow = ["docker-socket" \| "podman-socket" \| "nested-user-ns"]` | key dropped | a container socket is root on the host; nested user namespaces grant caps |
| `[sandbox] pid_namespace = false` | ignored | host `/proc/<pid>/environ` is readable, defeating `--clearenv` (see [process isolation](#process-isolation---unshare-pid)) |

The remaining allow keys (`env-secrets`, `shims-active`, `network-policy`) only
downgrade an `inner verify` check and carry no host privilege, so they survive.

### What the hardening deliberately keeps

`network`, the entrypoint and its args, mounts and capabilities are **not**
stripped. They are what a profile exists to describe — forcing `network = false`
would break every remote agent profile — and the hardening above already removes
the host secrets that make an open network an exfiltration channel. They are
reported by `remoteProfileRequests` and gated by consent instead.

### Consent and pinning

`gateRemoteProfile` prints the source URL, the sha256 of the exact bytes
fetched, each hardening applied, and what the profile still asks for; then it
blocks on `run this downloaded profile? [y/N]`.

- `--yes` does **not** answer it. That flag skips routine confirmations (keyring
  unlock, implicit workdir); treating it as blanket trust for downloaded code
  would make the gate disappear from every script that already passes it.
- `--allow-remote` is the explicit consent, and the only way through in a
  non-interactive run: with no terminal on stdin and no flag, the run is
  **refused**, never silently accepted (`stdinIsTerminal`, overridable in tests).
- `--trust-remote` skips the hardening *and* counts as consent.
- `--dry-run` prints the summary and proceeds — nothing executes.
- `--sha256 <digest>` (bare or `sha256:`-prefixed, any case) is checked against
  the fetched bytes before they are ever parsed; a mismatch aborts. The digest is
  printed after every download, so pinning is a copy-paste. Passing `--sha256`
  with a local profile is an error, not a silent no-op.

### Why `inner profile install` is not gated

`inner profile install <url>` writes the TOML into `~/.config/inner/profiles/`,
and from that moment it is a **local** profile: `inner run -p <name>` applies
neither the hardening nor the prompt. That is intentional — installing is the
explicit act of trust, and re-asking on every run would train the user to answer
`y`. To keep the decision informed, the install prints the sha256 and every
validation warning the profile produces, and accepts the same `--sha256` pin.

---

## How bwrap flags are decided

`Build` assembles args in sections, in this order:

### Base filesystem

```
--ro-bind / /          bind host root read-only
--proc /proc           fresh /proc
--dev /dev             minimal devtmpfs
--bind /dev/pts /dev/pts       (only if the host's /dev/pts/ptmx is openable, see below)
--tmpfs /tmp           empty writable /tmp
```


`/dev/pts` is bound read-write from the host **only when the host's pty multiplexer, `/dev/pts/ptmx`, can be opened by the user running `inner`**. The bind exists because interactive TUI apps (Node.js/claude, gemini) call `ttyname_r()` internally to resolve their controlling terminal path (e.g. `/dev/pts/3`), and with bwrap's own fresh `devpts` instance that path does not exist inside the sandbox.

The condition exists because the bind shadows the `devpts` instance `--dev` mounted, so every pty allocation inside the sandbox is routed through the host's node. Many distributions (Arch, Debian, Ubuntu) mount `devpts` with `ptmxmode=000`, which makes that node unopenable and turns every `forkpty(3)` into `EACCES`. The node cannot be substituted either:

- `--dev-bind /dev/ptmx /dev/ptmx` is rejected by bwrap >= 0.11 with `Can't mount on symlink destination /dev/ptmx`, since `--dev` creates `/dev/ptmx` as a symlink to `pts/ptmx`;
- on older bwrap versions `mount(2)` resolved that symlink and bound the host node at `/dev/pts/ptmx`, where the kernel can no longer find the sibling `devpts` instance and `open()` returns `ENOENT`.

So on such hosts the host `/dev/pts` is left alone and bwrap's own `devpts` is kept: `forkpty(3)` works, and the inherited terminal stays reachable as `/dev/tty` and `/dev/console` (`ttyname()` reports `/dev/console` rather than the host `/dev/pts/N`).

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
if cfg.EffectiveNetworkMode() != config.NetworkFull {
    args = append(args, "--unshare-net")
}
```

Only `"full"` keeps the host network namespace. `"off"`, `"allowlist"`, and any
value a given binary does not recognise all get a private namespace — a mode
that cannot be enforced must never silently mean "open network".

Driven by `[sandbox] network_mode` (or the legacy `network` bool) and the CLI
`--network` / `--no-network`, both of which move the mode rather than the bool.

### Allowlist mode: the proxy, the socket and the relay

`network_mode = "allowlist"` is `--unshare-net` plus exactly one way out.

```
sandbox (own netns)                    │  host
                                       │
  entrypoint                           │
    └─ HTTP_PROXY=http://127.0.0.1:10108
         └─ inner __net-relay  ────────┼──▶ /tmp/inner/net-proxy.sock
                (loopback listener)    │      └─ netproxy.Proxy
                                       │           └─ upstream
```

**Why a relay at all.** `HTTP_PROXY` must name a TCP address; no HTTP client
speaks to a Unix socket through it. But a TCP port cannot cross a network
namespace, while a bind-mounted socket can. The relay is `inner` re-invoked
inside the sandbox as a hidden `__net-relay` subcommand: it listens on loopback
in the sandbox's own namespace, forwards each connection to the socket, and
wraps the real entrypoint so the sandbox still has one process to wait for.

A read-only bind is enough for the socket. The kernel's `EROFS` check in
`inode_permission` covers regular files, directories and symlinks — not sockets
— so `connect(2)` still succeeds through a `--ro-bind`.

**Signals through the relay.** The relay and the entrypoint share the foreground
process group, so the tty driver already delivers `SIGINT`, `SIGQUIT`, `SIGTSTP`
and `SIGWINCH` to both. The relay forwards only `SIGTERM` and `SIGHUP` —
forwarding the rest would give the entrypoint a *second* copy of every Ctrl-C,
and a TUI whose quit gesture is "press Ctrl-C twice" would read one keypress as
two. For the same reason the child is not given its own process group.

**The decision chain**, in `internal/netproxy`, in this order:

1. parse and normalise the target (lower-case, strip a trailing dot, reject
   non-ASCII);
2. match it against the allow list — **before resolving anything**;
3. resolve, once;
4. reject if any resolved address is on the always-deny list;
5. dial the validated address literal, never the name again.

Step 2 before step 3 is the load-bearing part. Resolving first would make the
proxy a DNS exfiltration channel: a request for
`<secret-in-base32>.attacker.com` would send that name to the attacker's
nameserver, and refusing the TCP connection afterwards would be too late. The
API enforces the order structurally — `AllowsHost` takes a parsed name,
`AllowsAddr` takes a `net.IP` — so getting it backwards requires writing code
that visibly does that.

Step 5 is what closes DNS rebinding: the address checked is the address dialled.

**Why the always-deny list is large.** The proxy runs on the host, in the host
network namespace. Enabling it does not only narrow what the sandbox can reach —
it re-attaches a previously fully-isolated namespace to the host's stack.
Loopback, RFC1918, ULA, link-local, CGNAT and every non-global-unicast address
are refused regardless of configuration, because otherwise the feature would
*hand out* access to the user's localhost services rather than take access away.
The test is positive (reject unless the address is ordinary global unicast), so
a range nobody thought of fails closed.

**Environment injection** happens after the `--clearenv` block in `bwrap.go`,
for the same reason the `containers.conf` injection does: bwrap applies
`--clearenv` when it parses it and wipes every earlier `--setenv`. All four
spellings of the proxy variable are set, `NO_PROXY` is forced empty, and
`NODE_USE_ENV_PROXY=1` is set because Node 26+ otherwise ignores the proxy
environment for `fetch()`.

**Refusals share the user's terminal.** The proxy is a host-side goroutine, so
its `stderr` is the terminal the sandboxed process is using. A full-screen TUI
positions the cursor and redraws regions on its own terms and has no idea a
second writer exists, so a refusal written mid-frame is painted into its output
and stays there. `netproxy.DenyLog` handles both halves of that: it counts and
suppresses repeats of a line already reported (a tool retrying a blocked
telemetry endpoint emits one every few seconds), and when the entrypoint is a
TUI *and* our stderr is a terminal it holds every line until `Flush`, called by
the proxy cleanup once the child has exited. Deferring changes only what reaches
the terminal — the sandbox still gets its `403` at the moment of the request.

The deferred summary replays the lines the live path would have written,
verbatim plus an attempt count, so the two forms are recognisably the same
output. That contract is why `Proxy.deny` composes its message and writes it in
a single call: `DenyLog` identifies a refusal *by its line*.

**No session token.** The socket lives in a `0700` temp directory owned by the
user, so the processes that can reach the proxy are exactly the ones that could
run `inner` in the first place. A token would authenticate that set to itself.
This would need revisiting if the proxy ever grew a TCP listener on the host.

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
