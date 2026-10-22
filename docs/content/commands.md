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
| `--mount` | `-m` | string | — | Additional mount in `SRC:DEST[:MODE]` format (mode: `ro`\|`rw`\|`safe-rw`, default `ro`). `tmpfs` has no host source and cannot be expressed this way — declare it in the profile's `[mounts]` table instead. Repeatable. |
| `--publish` | | string | — | Forward a host port into the sandbox: `[HOST_IP:]PORT[:SANDBOX_PORT]`, host side `127.0.0.1` by default. Added to the profile's `[sandbox] publish`; ignored under `network_mode = "full"`. Repeatable. |
| `--env` | `-e` | string | — | Set an environment variable as `KEY=VAL`. `~`, `$VAR` and `${VAR}` in VAL are expanded against the host environment (same rules as profile `[env] set` values). Repeatable. |
| `--entrypoint` | | string | — | Override the entrypoint command (resets profile args, like `docker --entrypoint`). |
| `--arg` | `-a` | string | — | Append an argument to the entrypoint command. Repeatable. |
| `--args-file` | | path | — | Read the file and append its entire content as a single entrypoint argument. |
| `--timeout` | | int | `0` | Timeout in seconds (`0` = no timeout) |
| `--limit-memory` | | string | resolved (see profiles) | Override the memory cap (`"512M"`, `"4G"`). Highest-priority source in the [resource-limit chain](profiles.md#sandbox-limits-resource-limits). |
| `--limit-cpu` | | string | resolved (see profiles) | Override the CPU cap (`"200%"` or `"2.0"` cores). |
| `--limit-pids` | | int | resolved (see profiles) | Override the max process count (`0` = no override, keep the resolved value). |
| `--dry-run` | | bool | false | Print the resolved config and `bwrap` command without executing |
| `--json` | | bool | false | With `--dry-run`, print the resolved sandbox as JSON (see [JSON output](#json-output)); refused without `--dry-run` |
| `--yes` | `-y` | bool | false | Skip the routine confirmation prompts (keyring unlock, implicit workdir). Deliberately does **not** consent to a profile downloaded from a URL |
| `--allow-remote` | | bool | false | Consent to running a profile downloaded from a URL, without the interactive prompt. Required when there is no terminal on stdin |
| `--trust-remote` | | bool | false | Run a downloaded profile **unhardened** (implies `--allow-remote`) |
| `--sha256` | | string | — | Pin the sha256 of a profile downloaded from a URL; abort on mismatch. Rejected for a local profile |

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
inner run -p shell-oneshot -- "ls -la ~/projects"

# Reach a dev server running in the sandbox from the host browser
inner run -p shell -w ~/projects/myapp --publish 5173

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

# Use a profile from a URL (downloaded for this run only, not saved).
# A downloaded profile is hardened and needs explicit consent — see below.
inner run -p https://raw.githubusercontent.com/acme/profiles/main/claude-restricted.toml
```

### Profiles downloaded from a URL

A profile fetched over HTTPS configures the whole sandbox, so it is treated as untrusted
input: `inner` strips the settings that would hand it host privileges (`inherit_all`,
secret-looking `[env] inherit` names, credential and socket `allow` keys,
`pid_namespace = false`, `git_dir = "rw"`, `git_scan = false`, mounts that move a hidden path elsewhere,
`clipboard`, a published port on a non-loopback address), ignores the host-side paths it could choose (`[output] log`,
`workspaces_path`, `[entrypoint] workdir` — pass `-w`), resolves `$VAR` in its values
without your environment (only `$HOME`, `$USER`, `$UID`), prints what it still asks
for, and requires an explicit yes. See
[the trust boundary](internals.md#the-trust-boundary-profiles-downloaded-from-a-url).

| Flag | Type | Description |
|------|------|-------------|
| `--allow-remote` | bool | Consent to running the downloaded profile without the interactive prompt. Required for non-interactive runs — `--yes` deliberately does not cover this |
| `--trust-remote` | bool | Run it unhardened (implies `--allow-remote`); only for a profile you control |
| `--sha256` | string | Pin the sha256 of the downloaded bytes; abort on mismatch. The digest of each download is printed so it can be pinned |

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
network:     full
...

resource limits:
  memory: 4G
  cpu:    200% (systemd CPUQuota)
  pids:   512

bwrap command:
  bwrap --ro-bind / / ...
```

Under `network_mode = "allowlist"` the `network` line is followed by the
effective allow list, with the layer that contributed each entry — a profile
inherits its capabilities' destinations and extends them, so this is how you see
what the run can actually reach and why:

```
network:     allowlist
  allow: api.anthropic.com [capability:claude]
  allow: claude.ai [capability:claude]
  …                                   (the rest of the claude capability's list)
  allow: github.com [group:github]
  allow: api.github.com [group:github]
  …
  allow: internal.example.com [profile]
  deny:  http-intake.logs.us5.datadoghq.com
  publish: 127.0.0.1:5173 -> sandbox 127.0.0.1:5173
```

A `"@group"` entry in `network_allow` is shown expanded, one line per
destination, with the group named as its origin — the dry-run answers "what can
this run reach", so a name standing in for four hosts would defeat it. Published
ports ([`publish`](profiles.md#publish-host-ports)) follow the network lines,
in every mode.

See [network — allowlist mode](profiles.md#network-allowlist-mode).

The `resource limits` block shows the **effective** caps for this exact run
(after the full resolution chain and any `--limit-*` flags). See
[`[sandbox.limits]`](profiles.md#sandbox-limits-resource-limits) for how the
values are resolved and auto-detected.

### When the sandbox does not start

If the sandbox exits with a code that a start failure can produce (1, 126 or
127), `inner` rebuilds the same sandbox with a tiny probe as the entrypoint and
reads bwrap's error from it (the real run hands the terminal to the sandbox
unfiltered, so its stderr cannot be captured). When the probe fails too, one
actionable line follows bwrap's own message:

```
bwrap: execvp claude: No such file or directory
error: sandbox did not start: entrypoint claude was not found inside the sandbox
  hint: check that it is installed and on the sandbox PATH ([env] path_prepend); with home = "isolated", list its directory in [sandbox] home_allow
```

| bwrap error | Diagnosis |
|---|---|
| `No permissions to create new namespace`, `setting up uid map`, `loopback: Failed RTM_NEWADDR`, … | user namespaces disabled or restricted; the hint names the sysctl at fault (`kernel.unprivileged_userns_clone`, `user.max_user_namespaces`, `kernel.apparmor_restrict_unprivileged_userns`) |
| `Can't find source path P` | mount source missing on the host |
| `Can't mkdir P`, `Can't create file at P` | mount destination cannot be created under the read-only host root |
| messages about `ptmx` / `devpts` | pseudo-terminal setup |
| `execvp CMD` (or the probe cannot find `CMD`) | entrypoint not on the sandbox PATH |

When the probe succeeds the sandbox did start, the exit code is the
entrypoint's own, and nothing is added.

---

## `inner profile`

Manage sandbox profiles stored in `~/.config/inner/profiles/`.

### `inner profile list`

List all available profiles. On first use it installs the default profiles
into `~/.config/inner/profiles/`, as `inner run` and `inner doctor` do:

```bash
inner profile list
```

Output:

```
   NAME                DESCRIPTION
   claude-interactive  Interactive Claude Code agent (network enabled)
   claude-one-shot     Non-interactive Claude Code agent (dangerously-skip-permissions)
   cursor-interactive  Interactive Cursor Agent (network enabled)
   gemini-interactive  Interactive Gemini CLI agent (network enabled)
   gemini-one-shot     Non-interactive Gemini CLI agent (--yolo)
   shell-oneshot       Run a single shell command in sandbox (no network)
*  shell               Interactive bash shell in sandbox (no network)
   my-project          Project-specific profile  [local]
```

- `*` marks the profile that will be used when `-p` is not specified (the effective default, accounting for any local `default_profile` override).
- `[local]` marks profiles found in `.config/inner/profiles/` of the current directory rather than `~/.config/inner/profiles/`.
- When a local profile has the same name as a global one, the global profile is **hidden** (the local takes precedence). Use `--wide` to reveal it.

| Flag | Short | Description |
|------|-------|-------------|
| `--wide` | `-w` | Show SCOPE and PATH columns; also display shadowed global profiles (marked `[shadowed]`) |
| `--json` | | Print the profiles as a JSON array, shadowed ones included |

Wide output example (when a local `shell` profile shadows the global one):

```
   NAME                SCOPE   DESCRIPTION                                                   PATH
   claude-interactive  global  Interactive Claude Code agent (network enabled)               ~/.config/inner/profiles/claude-interactive.toml
   shell               global  Interactive bash shell in sandbox (no network)  [shadowed]    ~/.config/inner/profiles/shell.toml
*  shell               local   Project shell variant  [local]                                ~/projects/myapp/.config/inner/profiles/shell.toml
```

### `inner profile show`

Display the TOML content of a profile:

```bash
inner profile show claude-interactive
```

With no argument it shows the **current profile** — the one a bare `inner run`
would use: `default_profile` from the project config in the conventional path
(`.config/inner.toml`, `.config/inner/config.toml`, `inner.toml`, …), then the
user config, falling back to `default`. A header line names the profile and the
file it was read from:

```bash
inner profile show
# current profile: project-shell (/home/me/projects/myapp/.config/inner/profiles/project-shell.toml)
```

The raw TOML is followed by the **resolved network settings** as TOML comments.
Many profiles (especially ones written before `network_mode` existed) say nothing
about network at all; silence resolves to `off` — a private, empty network
namespace — and this line is the only place that is visible:

```toml
schema_version = "1"
name = "legacy"

[entrypoint]
cmd = "bash"

# resolved network: off — private empty netns, no outbound connection possible
```

In `allowlist` mode the comments also list every effective destination with the
layer that contributed it. The block is comments, so the output stays valid TOML.

| Flag | Description |
|------|-------------|
| `--explain` | Append the effective network settings and an explanation of each declared capability (injected mounts, pre-run actions, notes) |
| `--resolved` | Print the effective profile after resolving `extends`, capabilities and the network allow list |

```bash
# Show profile content with network and capability details
inner profile show claude-interactive --explain
```

The network section of `--explain` shows the model actually applied and, in
`allowlist` mode, every destination with the layer that contributed it — the
capability defaults, a `@group` reference, or the profile itself:

```
── network ───────────────────────────────────────────────────────
  mode: allowlist
  allow:
    api.anthropic.com                        [capability:claude]
    registry.npmjs.org                       [group:npm]
    example.com                              [profile]
  deny:
    telemetry.example.com
```

### `inner profile explain`

Show the effective policy of a profile: the `extends` chain, then every value of
the merged profile with the layer that set it. With no argument it explains the
current profile.

```bash
inner profile explain leaf
```

```
profile: leaf
chain (base first, later layers win):
  1.  base  ~/.config/inner/profiles/base.toml
  2.  mid   ~/.config/inner/profiles/mid.toml
  3.  leaf  ~/.config/inner/profiles/leaf.toml

── sandbox ───────────────────────────────────────────────────────
  network_mode   "allowlist"  [mid]
  allow          ssh-agent    [base]
                 gpg-agent    [leaf]
  home           "isolated"   [mid]

── mounts ────────────────────────────────────────────────────────
    ~/src → /work (rw)  [mid]
```

The attribution follows the merge rules of `extends`: a scalar belongs to the
last layer that sets it, a list element to the first layer that adds it, a map
entry (`[mounts]`, `[env] set`, …) to the last layer that sets its key.
`[default]` marks a built-in value that no layer sets. When two layers share a
name (a local profile extending the global one of the same name) they are
labelled by path. The network and capability sections of `profile show
--explain` follow.

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
inner profile install URL [--name NAME] [--force] [--sha256 DIGEST]
```

| Flag | Type | Description |
|------|------|-------------|
| `--name` | string | Override the profile name (default: last URL path segment, `.toml` stripped) |
| `--force` | bool | Overwrite an existing profile with the same name |
| `--sha256` | string | Refuse to install if the downloaded content has a different sha256 |

```bash
# Install under the name derived from the URL
inner profile install https://example.com/my-profile.toml

# Install under a custom name
inner profile install https://example.com/my-profile.toml --name restricted

# Overwrite an existing local profile
inner profile install https://example.com/my-profile.toml --force
```

The TOML is validated before being written to disk; the sha256 of the download and every
validation warning are printed. An installed profile is a local profile: `inner run -p
<name>` skips the consent gate and the hardening applied to `inner run -p <url>`, so review
what the install reported before running it. Use `inner run -p URL` to try a remote
profile without installing it first.

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

The inside invocation never re-reads the profile file: everything the checks need
(home mode, `network`, `allow` keys, whether shims are expected, `[verify.custom]`
checks) is passed to it through `INNER_VERIFY_*` environment variables. Under
`home = "isolated"` the profile lives in the hidden home and would simply be
unreadable from inside, and the values passed this way describe the sandbox that
was actually built rather than what the file says.

### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--profile` | `-p` | string | configured default (as for `inner run`) | Profile to verify |
| `--suggest` | | bool | false | Print TOML snippets for failed checks |
| `--json` | | bool | false | Print the report as JSON (see [JSON output](#json-output)) |

### What it checks

| Check | Severity | Description |
|-------|----------|-------------|
| user is not root | CRITICAL | Process must not run as uid 0 |
| /usr is read-only | CRITICAL | Base filesystem must not be writable |
| git credentials not exposed | CRITICAL | `~/.git-credentials` must be absent or empty |
| `~/.ssh` not accessible | HIGH | No private key files visible in `~/.ssh/` |
| `~/.gnupg` not accessible | HIGH | `~/.gnupg/` must be empty |
| no obviously-named secrets in env vars | MEDIUM | Heuristic, not a guarantee: flags env var names containing `PASSWORD`, `PASSWD`, `SECRET`, `TOKEN`, `CREDENTIAL`, `PRIVATE_KEY`, `API_KEY`, `ACCESS_KEY`, `_KEY` or `AUTH`. A name match doesn't prove a secret is present, and a differently-named variable can still hold one |
| docker socket not accessible | MEDIUM | no Docker socket (system, rootless, Docker Desktop) may accept connections |
| `~/.netrc` not accessible | MEDIUM | `~/.netrc` must be absent or empty |
| home directory isolated (tmpfs) | HIGH | With `home = "isolated"`, `$HOME` must be the empty tmpfs the profile asked for; passes under `host-ro` |
| shims active in PATH | MEDIUM | Shim directory must be first in `PATH` (only when the profile declares `[noop]` entries) |
| network restricted | MEDIUM | TCP connection to 8.8.8.8:53 must fail; skipped under `network_mode = "full"`, where an open network is the declared intent |
| `<key>` not accessible | HIGH or MEDIUM | One check per other key of the [hidden resource list](profiles.md#allow--sensitive-resource-opt-in) (`aws-credentials`, `session-bus`, …): HIGH for credentials and host-privilege keys, MEDIUM otherwise |
| host abstract unix sockets not reachable | HIGH | Under `network_mode = "full"`, no abstract socket listed in `/proc/net/unix` may accept a connection (needs Linux 6.12+, see `inner doctor`); passes in the other modes, which have a private network namespace |

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

`inner verify` builds the same sandbox as `inner run` with two exceptions: the
capability handlers do not run (they can prompt for a keyring unlock), and the
host ports of [`publish`](profiles.md#publish-host-ports) are not opened.

---

## `inner log`

Every `inner run` gets a run ID, printed on stderr when it starts and when it
ends:

```
inner: run 20261014-182411-6a37
...
inner: run 20261014-182411-6a37 exited with code 1 (inner log 20261014-182411-6a37)
```

For every run, interactive ones included, inner writes a record
(`<run-id>.json`: profile, command, workdir, home and network mode, start, end,
exit code, path of the output log) to the logs directory: `log_dir` from the
config, or `~/.config/inner/logs/`. The output itself is logged only for
non-interactive runs, and only when a log directory is set explicitly —
`[output] log` in the profile, or `log_dir` in the config. Records are `0600`
and store the entrypoint arguments as given, prompts included.

```bash
inner log <run-id>              # same as: inner log show <run-id>
```

### `inner log list`

All runs, newest first, with date, profile, exit code and log size:

```bash
inner log list
inner log list --json
```

### `inner log show`

The run record, then the output log (through `$PAGER`, default `less`, when
writing to a terminal):

```bash
inner log show <run-id>
inner log show <run-id> --json   # the record alone, as JSON
```

### `inner log clean`

Delete old runs (record, log and legacy summary):

```bash
inner log clean
```

| Flag | Default | Description |
|------|---------|-------------|
| `--older-than` | `30` | Delete runs older than this many days |
| `--dry-run` | false | Preview deletions without removing files |

```bash
# Preview what would be deleted (older than 7 days)
inner log clean --dry-run --older-than 7

# Actually delete runs older than 14 days
inner log clean --older-than 14
```

---

## JSON output

`--json` prints machine-readable output with stable `snake_case` keys, on
stdout only (warnings go to stderr):

| Command | Output |
|---|---|
| `inner run --dry-run --json` | the resolved sandbox: profile, entrypoint (as requested, before inner's own wrappers), home, network with the origin of each allow entry, published ports, mounts, env, noop, allow, capabilities, limits, full command line |
| `inner verify --json` | `{profile, conformant, checks: [{id, name, passed, severity, detail, suggest, allow_override}]}`; exit code as without `--json` |
| `inner doctor --json` | `{checks: [{status, check, message}], errors, warnings}` |
| `inner profile list --json` | `[{name, description, path, scope, default, shadowed, experimental}]`, shadowed profiles included |
| `inner log list --json` | `[{run_id, started_at, profile, exit_code, log_path, log_size}]` |
| `inner log show RUN_ID --json` | the run record |

`inner run --json` without `--dry-run` is refused: a run's output belongs to
its entrypoint; its record is `inner log RUN_ID --json`.

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
profile claude-interactive: installed
profile claude-one-shot: installed
profile cursor-interactive: installed
profile gemini-interactive: installed
profile gemini-one-shot: installed
profile shell-oneshot: installed
profile shell: installed
```

Example output when everything already exists:

```
dir: /home/alice/.config/inner
config: already exists (skipped)
profile claude-interactive: already exists (skipped)
profile claude-one-shot: already exists (skipped)
profile cursor-interactive: already exists (skipped)
profile gemini-interactive: already exists (skipped)
profile gemini-one-shot: already exists (skipped)
profile shell-oneshot: already exists (skipped)
profile shell: already exists (skipped)
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
inner reset             # asks for confirmation
inner reset --force     # skips confirmation (useful in scripts); short form -f
```

The command overwrites every file in `~/.config/inner/profiles/` whose name matches a
built-in profile. Files that do not match any built-in name — user-created
profiles, cloned profiles under a custom name — are left completely untouched.

Use this after upgrading `inner` to pick up changes to the built-in profiles
without losing your own customizations.

Example output:

```
profile claude-interactive: reset
profile claude-one-shot: reset
profile cursor-interactive: reset
profile gemini-interactive: reset
profile gemini-one-shot: reset
profile shell-oneshot: reset
profile shell: reset

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
| `default_profile` | `"default"` (the starter `config.toml` sets `"shell"`) | Profile used when `-p` is not specified |
| `log_dir` | `~/.config/inner/logs/` | Directory for run records. When set explicitly it is also where the output of non-interactive runs is logged, unless the profile sets `[output] log` |
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

Each line starts with `[ok]`, `[??]` (warning) or `[!!]` (error), and a summary
line counts errors and warnings. The exit code is `0` either way: in a script,
read `errors` from `inner doctor --json`. Checks performed:

- `bwrap` binary found and version reported
- Unprivileged user namespaces supported, naming the setting that blocks them: `kernel.unprivileged_userns_clone = 0` (Debian) or `user.max_user_namespaces = 0` are errors; `kernel.apparmor_restrict_unprivileged_userns = 1` (Ubuntu 23.10+) is a warning, since it only blocks a bwrap without an AppArmor profile that allows user namespaces
- Terminal injection (`TIOCSTI`) blocked by the kernel. The sandbox shares the terminal `inner` was started from, so on a kernel that allows `ioctl(TIOCSTI)` a sandboxed process can type commands your shell runs after the sandbox exits. Linux 6.2+ can refuse it (`dev.tty.legacy_tiocsti = 0`); older kernels cannot. A warning names the fix:

  ```bash
  sudo sysctl -w dev.tty.legacy_tiocsti=0
  echo 'dev.tty.legacy_tiocsti = 0' | sudo tee /etc/sysctl.d/90-tiocsti.conf
  ```
- Host abstract unix sockets blocked. A `network_mode = "full"` sandbox shares the host network namespace, where the X server listens on an abstract socket (`@/tmp/.X11-unix/X0`) that no mount can hide: through it a sandboxed process can type into any window of your session. On Linux 6.12+ (Landlock ABI 6) `inner` blocks the host's abstract sockets; on older kernels this line is a warning, and `network_mode = "allowlist"` or `"off"` is the way to keep an untrusted agent away from them.
- `~/.config/inner/profiles/` directory exists and each profile is validated (unknown `allow` keys, missing mounts, etc.)
- `~/.config/inner/logs/` directory exists
- Claude Code credentials: OAuth credentials in `~/.claude/.credentials.json`, or else `ANTHROPIC_API_KEY` set
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

Print version, git commit and build time (`inner --version` prints the same line):

```bash
inner version
```

Example output:

```
inner 0.13.0 (git: a5d820c1f0…, built: 2026-10-10T12:00:00Z)
```
