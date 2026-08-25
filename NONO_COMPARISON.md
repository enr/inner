# inner vs nono — comparison and improvement roadmap

Comparison between `inner` and [nono](https://github.com/nolabs-ai/nono)
(nono.sh), a Rust-based AI-agent sandboxing platform, with concrete
improvement proposals for `inner` in four areas: security, features,
performance, UI/UX.

Sources: nono README, docs (docs.nono.sh content in the repo under
`docs/cli/`), and the `inner` codebase as of this writing. nono is pre-1.0
(v0.67.x, ~1,500 commits) and moves fast; details below may age.

---

## 1. At a glance

| | **inner** | **nono** |
|---|---|---|
| Language | Go | Rust (FFI bindings for Rust/Python/TS/Go) |
| Platforms | Linux only | macOS, Linux, Windows (WSL2) |
| Isolation primitive | bubblewrap: mount/PID/net/user namespaces | Landlock LSM + seccomp-notify supervisor (Linux); Seatbelt (macOS) |
| Filesystem model | host root ro-bound + denylist hiding of ~17 sensitive paths; explicit rw mounts | deny-by-default: only granted paths visible; dynamic per-file approval via seccomp-notify |
| Network | binary on/off (`--unshare-net`) | kernel-blocked except a supervised localhost proxy: domain allowlist, L7 endpoint filtering, DNS-rebinding protection, metadata-endpoint denial |
| Credentials | hidden by default; `[sandbox] allow` re-exposes raw files | never enter the sandbox: proxy injects them from supervisor memory / OS keyring, scoped to method+path globs |
| Delegated tools | shims: block or rewrite commands | child sandboxes: each tool (`git`, `gh`, `ssh`) runs under its own chained policy |
| Profiles | TOML, `extends`, local + URL | JSON, `extends`, signed registry (registry.nono.sh) with trust model |
| Audit | per-run log file (`inner log`) | append-only NDJSON events, hash chain + Merkle root, optional DSSE signing, secret redaction, `audit list/show/verify` |
| Rollback | none | content-addressable pre/post snapshots, `rollback list/show/restore/verify/cleanup` |
| Sessions | foreground run only | supervisor with PTY: detached sessions, `ps/attach/detach/stop/inspect/prune` |
| Resource limits | systemd-run `--user --scope` (memory/CPU/pids, auto-detected defaults) | resource-limits feature (comparable) |
| Self-verification | `inner verify` runs checks *inside* the sandbox; `doctor` on host | no equivalent in-sandbox conformance check |

### Where inner is already ahead (keep, don't copy over)

- **`inner verify`** — running conformance checks from inside the sandbox
  (plus profile-defined custom checks) has no nono equivalent and is a
  genuinely distinctive feature.
- **TOML profiles** are more readable/writable by hand than nono's JSON.
- **TUI correctness work** (raw-mode handling, cursor fix, PID-ns +
  controlling-terminal invariants documented in `bwrap.go`) is deeper than
  what nono documents.
- **Git config sanitization** (`internal/git/sanitizer.go`) is a targeted
  feature nono does not advertise.
- **Simplicity**: no daemon, no supervisor process, one binary + bwrap.
  Several proposals below add a supervisor-like component; each should stay
  opt-in so the zero-moving-parts default survives.

### Architectural difference to keep in mind

nono's supervisor is *in the loop* (seccomp-notify traps `openat`, the
proxy terminates TLS or tunnels bytes, credentials live in supervisor
memory). inner is *fire-and-forget*: it builds a bwrap argv and execs.
Most nono-inspired features below therefore need a long-lived host-side
process for the duration of the run. That is a real architectural step —
worth it for the network proxy and sessions, avoidable for others.

---

## 2. Security improvements

### S1. Allowlist filesystem mode — **IMPLEMENTED** (confirms SECURITY_REVIEW #1)

nono's core promise is "read/write access to the current directory and
nothing else". inner's `--ro-bind / /` + denylist means everything not on
the 17-entry sensitive list is readable (browser cookies, `~/.config/gh`,
`.env` files, all of `$HOME`). SECURITY_REVIEW #1 already flags this; nono
validates that the inverted model is shippable and usable.

Proposal: a profile mode

```toml
[sandbox]
home = "isolated"   # --tmpfs $HOME, then allowlist mounts only
```

keeping the current behaviour as `home = "host-ro"` (default for one
release, then flip the default for agent profiles). System paths
(`/usr`, `/etc`, `/lib*`) stay ro-bound so toolchains keep working; only
`$HOME` inverts. This is the single highest-leverage security change.

**Shipped as proposed**, with the flip done immediately for the built-in agent
profiles rather than after a release: `home = "host-ro"` stays the default for
every profile that does not ask (including the two `shell` profiles and every
profile already installed on a user's machine), while `claude-*`, `gemini-*` and
`cursor-*` ship isolated. `[sandbox] home_allow` carries the allowlist of paths
put back read-only — needed in practice because agent CLIs live under `~`
(`~/.local/bin`, `~/.nvm`, …). `inner verify` asserts the result from inside the
sandbox (`home-isolated`, HIGH). See `docs/content/profiles.md`
("home — filesystem model" and the migration section).

### S2. Network allowlist via a supervised proxy (HIGH)

inner's network policy is all-or-nothing, and agent profiles practically
require `network = true` (API access), which then allows exfiltration to
anywhere. nono's design:

- sandbox kernel-blocked from all outbound TCP except `localhost:<port>`;
- a proxy in the (unsandboxed) parent enforces a **domain allowlist**
  (CONNECT tunnel — no TLS interception needed for plain domain filtering);
- always-deny cloud metadata endpoints (`169.254.169.254`,
  `metadata.google.internal`, link-local ranges);
- DNS resolved by the proxy, resolved IPs checked **before** connecting
  (kills DNS rebinding);
- a per-run session token so other localhost processes can't ride the proxy.

Adaptation to bwrap: keep `--unshare-net` and bind a unix socket (or use
`slirp4netns` restricted to the proxy port) into the sandbox; run the
proxy in the `inner` parent process for the life of the run; set
`HTTP_PROXY`/`HTTPS_PROXY` (+ `NODE_USE_ENV_PROXY=1` for Node 26+ agents).

```toml
[sandbox.network]
mode = "allowlist"                  # "off" | "full" | "allowlist"
allow = ["api.anthropic.com", "github.com", "*.githubusercontent.com"]
```

This turns the weakest point of every agent profile (open network) into a
policy surface. It is the feature most worth stealing wholesale.

### S3. Credential injection instead of credential exposure (HIGH)

Today the only way to give an agent `gh` access is `allow =
["git-credentials"]`-style raw exposure — once readable, a token is
exfiltratable. nono never lets credentials into the sandbox: the proxy
injects the real token (from OS keyring, e.g. `keyring://gh:github.com`)
into upstream requests, optionally restricted to **method+path globs**
(`POST:/repos/*/issues`, deny the rest → 403 + audit event).

For inner this builds directly on the S2 proxy (reverse-proxy mode):

```toml
[credentials.github]
upstream = "https://api.github.com"
source = "keyring://inner/github"   # or env://, file://
inject = { header = "Authorization", format = "Bearer {secret}" }
endpoints = ["GET:/repos/**", "POST:/repos/*/issues"]
```

Even without endpoint filtering in v1, "token never on the sandbox
filesystem or in its env" is a categorical improvement over `allow` keys.

### S4. Audit trail with tamper evidence (MEDIUM)

inner already has run IDs (`internal/executor/runid.go`) and per-run logs.
nono records every session as append-only NDJSON (command, timing, exit
code, capability decisions, network/proxy events) with a hash chain +
Merkle root, best-effort secret redaction of argv/headers/URLs, and
optional DSSE signing at session end.

Incremental path for inner:

1. structured NDJSON run record: resolved profile (post-merge), mounts,
   allow keys, network mode, entrypoint, verify report, exit code,
   duration — this also doubles as reproducibility metadata;
2. argv/env secret redaction on write;
3. hash chain over events (cheap, one SHA-256 running state);
4. signing only if/when someone asks for compliance use.

Plus proxy request logs once S2 exists (`inner log show <run-id>
--network`).

### S5. Remote profile trust — **IMPLEMENTED** (closes SECURITY_REVIEW #2)

nono profiles come from a registry with signing and a trust/attestation
story (OpenSSF-aligned). inner used to fetch TOML from any URL and trust it to
configure the sandbox (network, `inherit_all`, `allow`, entrypoint).
Minimum viable fix as already sketched here: blocking consent showing the
dangerous settings + `--sha256` pinning.

**Shipped, and going a step further than "show and confirm":** a downloaded
profile is now also *hardened* by default before the consent prompt is even
shown — `inherit_all`, secret-looking `[env] inherit` names, and the
credential/socket/`nested-user-ns` `allow` keys are stripped outright, not just
flagged (`--trust-remote` opts back out for a profile the user controls). The
consent prompt (`--allow-remote`, never satisfied by `--yes`) then covers what
survives hardening — network, entrypoint, mounts, capabilities. `--sha256`
pinning shipped for both `inner run <url>` and `inner profile install`. See
`cmd/inner/remote_profile.go` and SECURITY_REVIEW.md #2.

**Not done — still the natural follow-up:** a signed profile index
(cosign/minisign over `contrib/profiles`), which remains much cheaper than a
full registry and is unrelated to the consent/hardening/pinning work above.

### S6. Canonical-path denial for blocked commands (MEDIUM)

inner's `[noop] block` shims only win via PATH; `/bin/rm` still works.
nono explicitly denies canonical-path invocation of policy-controlled
commands. bwrap equivalent: for each blocked command, also bind the shim
over every resolved real path (`--ro-bind <shim> /usr/bin/rm`, following
symlink chains like `/bin -> /usr/bin`). Cheap to implement in
`internal/shim` + the isolator, and closes an obvious bypass. Document the
residual gap (agent can copy the binary elsewhere) — full parity needs
Landlock-style exec control, out of scope.

### S7. Optional seccomp filter (LOW)

bwrap accepts `--seccomp <fd>`. A conservative default filter (deny
`ptrace`, `process_vm_readv`, `keyctl`, `add_key`, `bpf`, `mount`,
`kexec_*`, `open_by_handle_at`) adds a defense-in-depth layer analogous to
nono's kernel-first posture without adopting seccomp-notify complexity.
Landlock itself is also worth tracking: Go bindings exist
(`landlock-lsm/go-landlock`) and it composes with bwrap.

---

## 3. Feature improvements

### F1. Workspace snapshots / rollback (HIGH value for agent use)

nono's most user-visible safety feature: content-addressable (SHA-256
dedup) snapshots of tracked files before and after the run, then
`rollback list/show/restore/verify/cleanup` with unified/side-by-side
diffs, gitignore-aware exclusions (`node_modules`, `__pycache__`), storage
caps (5 GB / 10 sessions default).

inner adaptation — snapshot the **workspace/workdir mounts only** (they
are the only writable host paths, so the surface is small and known):

- `inner snapshot` integrated into `inner run` (`[output] snapshot = true`
  or `--snapshot`);
- store under `~/.local/state/inner/snapshots/<run-id>/`;
- use reflinks (`FICLONE`) on btrfs/XFS for cheap copies (the Linux analog
  of nono's APFS `clonefile()`), fall back to hardlink-by-hash dedup;
- `inner rollback <run-id> [--diff|--restore]`.

For git repos a cheaper v0: record `git stash create` / dirty-state hash
pre-run and show `git diff` post-run; snapshots then cover non-git files.

### F2. Detached sessions (MEDIUM)

nono: `nono run --detached`, `nono ps`, `attach`, `detach` (Ctrl-] d),
`stop`, `inspect`, `prune` — a tmux-like PTY supervisor that redraws
terminal state on attach. For inner this is the "run three agents in
parallel and check on them" workflow. Given inner's existing careful TUI
layer, a pragmatic v0 is documented tmux/abduco integration or a thin
`inner ps`/`inner stop` over run-ID pidfiles; a real PTY supervisor is a
large project — do it only if detached agents become a core use case.

### F3. Chained tool policies via re-entrant shims (MEDIUM)

nono's flagship idea: the agent's `git` invocation runs in a *narrower
child sandbox* (repo + object store only), `gh` gets only the credential
proxy, `git`→`ssh` is allowed while direct `ssh` is denied. inner already
has the two ingredients: shims and profiles. A third rewrite mode makes it
composable:

```toml
[noop.sandbox]
git = "git-only"     # shim re-invokes: inner run -p git-only -- git "$@"
```

Caveats to solve: nesting bwrap requires nested user namespaces (the
`nested-user-ns` machinery already exists), and caller-identity ("deny
`sh` when spawned by `npm`") is not reliably enforceable with shims alone
— scope v1 to per-command narrowing, not caller policies.

### F4. Profile discovery (`inner profile search/add`) (LOW)

nono has a registry with `nono search` and `nono profile init --extends
vendor/profile`. inner's `contrib/profiles` is the seed of the same thing:
add `inner profile add <name-or-url>` that copies from a curated, signed
index (see S5) into `~/.config/inner/profiles/`, and `inner profile
search` over that index. Community profile sharing is how nono grew its
agent coverage (Claude Code, Codex, CoPilot, OpenCode, …) without the core
team writing every policy.

### F5. Sandboxed OAuth login flows (LOW, watch)

nono documents "sandboxed OAuth logins" (capturing agent OAuth flows so
tokens land in the keyring, not in agent-readable files). Relevant to
inner because `claude` login state lives under `~/.claude` which the
capability currently copies wholesale. Worth a look once S3 exists.

---

## 4. Performance improvements

inner's architecture is already the fast one (exec bwrap, no daemon, no
seccomp-notify round-trips — nono pays a supervisor round-trip on trapped
`openat` calls; inner pays zero). Remaining opportunities are small:

- **P1. Runtime detection cache** — `runtime.Detect()` shells out /
  stats on every invocation; cache results (bwrap path/version, display
  server) keyed by mtime in `~/.cache/inner/` to shave startup.
- **P2. Remote profile HTTP cache** — cache URL profiles with
  ETag/If-None-Match instead of re-downloading per run (also reduces the
  S5 TOCTOU surface once pinning exists).
- **P3. Reflink-first snapshot store** (if F1 lands) — `FICLONE` ioctl on
  btrfs/XFS makes pre-run snapshots near-free; hash-dedup is the fallback,
  as nono does with APFS clonefile.
- **P4. Parallel verify checks** — `Checker.Run` is sequential; the dial
  timeout check alone can take 2 s. Run checks in a `sync.WaitGroup` and
  keep output ordering.

Non-goals: seccomp-notify dynamic FD injection (nono needs it because of
its allowlist-with-approvals model; inner's static mounts don't), and a
resident daemon.

---

## 5. UI/UX improvements

- **U1. `--json` output** for `verify`, `doctor`, `profile list`,
  `log`, and `run --dry-run`. nono ships `--json` on `ps`/`audit` for
  automation; inner's structured data (Report, RunConfig) is one encoder
  away. Prerequisite for CI integration.
- **U2. Print the run ID at start/end of every run** and accept it
  everywhere (`inner log <run-id>`, future `inner rollback <run-id>`).
  nono's session-ID-first UX makes everything else (audit, rollback,
  attach) addressable.
- **U3. Policy flags for one-off narrowing** — nono expresses common
  cases as flags (`--allow-domain`, `--credential`, `--allow-cwd`)
  without editing a profile. inner has `-m/-e/--network`; add
  `--allow-domain` (with S2), `--allow <key>`, `--block <cmd>` so users
  can experiment before persisting to TOML.
- **U4. `inner profile explain <name>`** — nono has "profile
  introspection" showing the effective policy. inner's `--dry-run` is
  close but run-oriented; a profile-oriented view of the *merged* extends
  chain (final mounts/env/allow/noop, with which layer each value came
  from) would make `extends` debugging trivial. The capability `Explain`
  plumbing already exists.
- **U5. Friendlier failure triage** — nono's docs lean on
  troubleshooting flows; inner's equivalent is scattered. When bwrap exits
  non-zero, map the common failures (missing mount dest, userns disabled,
  ptmx quirks) to actionable one-liners referencing `inner doctor`.
- **U6. First-run onboarding** — nono's `curl | sh` + `nono run` in
  seconds is a marketing point. inner: publish to package repos (AUR,
  deb/rpm via the existing release pipeline), and make `inner init`
  interactive (pick agent → generate profile → run `doctor`).

---

## 6. Suggested priority

| Priority | Item | Rationale |
|---|---|---|
| 1 | S2 network allowlist proxy | biggest real-world risk reduction; unlocks S3 |
| 2 | ~~S1 isolated-home allowlist mode~~ | **done** — closes the fix-2 half of review #1 |
| 3 | S3 credential injection | removes the worst `allow` escape hatches |
| 4 | F1 workspace snapshots/rollback | highest user-visible safety win |
| 5 | ~~S5 remote profile pinning/consent~~ | **done** — closes review #2 (signed profile index still open, see S5) |
| 6 | S6 canonical-path shim denial | cheap bypass fix |
| 7 | U1/U2 JSON output + run-ID UX | enables automation, low effort |
| 8 | S4 structured audit log | builds on U2 |
| 9 | F3 chained tool policies | differentiating, medium effort |
| 10 | F2 detached sessions | large; validate demand first |
