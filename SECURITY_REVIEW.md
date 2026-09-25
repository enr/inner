# Security & code review — open issues

This file tracks what is still open from the senior-level review of `inner`.
Items #1–#11 of the original review are closed and have been removed from this
file; their full write-up (what was wrong, the fix, how it was verified) is in
the git history of this file, up to commit `5633c67`.

Severity legend:

- **Critical** — fix before the next release; exploitable or data-losing.
- **High** — serious; schedule soon.
- **Medium** — real bug or risk, lower blast radius.
- **Low** — polish / correctness nit.

Each open item was re-checked against the code at `5633c67`.

---

## A — Sign-off pending: second pass on #1–#3

**Status:** code done and unit-tested; what is missing is running
`.sdlc/e2e-security` on a real desktop session (real bwrap, a real session bus,
real ssh/gpg agents). Not a code change.

A re-review of the three closed fixes found gaps and regressions, all fixed:

- **#1 — host runtime sockets were not hidden.** `/run/user/<uid>` came in
  through the root bind; neither the read-only bind nor `--unshare-net` stops
  `connect(2)` on a filesystem socket. Hidden now under the allow keys
  `session-bus`, `systemd-user`, `ssh-agent`, `gpg-agent`; `inner verify`
  judges a socket by `connect(2)`. The claude capability passes an
  `xdg-dbus-proxy` filtered to `org.freedesktop.secrets` (falls back to the
  whole bus with a warning when the proxy is missing).
- **#1 — Maven regression.** `~/.m2/settings.xml` is now an empty
  `<settings/>` placeholder instead of `/dev/null`.
- **#2 — remote-profile hardening was a list of known-bad fields.** Fixed at
  load time (`Loader.BuildUntrusted`) and in `hardenRemoteProfile`; the prompt
  lists every mount and env value, escaped; a reflection test forces a policy
  decision for every new profile field.
- **#3 — copies.** `O_NOFOLLOW`, regular files only (a FIFO used to hang
  inner), symlinks recreated instead of dropped or dereferenced.

**To close:** run `.sdlc/e2e-security` (sections A–D) on a real desktop and
record the outcome here.

---

## B — Residuals found in the second pass (tracked as ISS-34…37)

### ISS-35 — [Medium] `docker-socket` verify check always fails on a Docker host

**Where:** `internal/sandbox/checker.go` — `checkDockerSocket`.

**Still valid.** The check fails whenever `os.Stat("/var/run/docker.sock")`
succeeds. When the socket is hidden, `internal/isolator/bwrap.go` binds
`/dev/null` over it, so the path still exists: on any host running Docker,
`inner verify` reports the socket as accessible even though it is hidden. False
positive, not a hole — but it trains users to ignore or `allow` a check that
guards a root-equivalent resource.

**Fix:** fail only if the path is a socket (or, like the generic sensitive
check, only if `connect(2)` succeeds).

### ISS-36 — [Medium] A broken symlink on a hidden path aborts every run

**Where:** `internal/isolator/bwrap.go`, sensitive-resource hiding loop.

**Still valid.** `pathExists` uses `os.Lstat` (a dangling link "exists"), then
`EvalSymlinks` fails and `Build` returns an error. One dangling link among ~30
hidden paths (e.g. `~/.mozilla` after a move to flatpak) breaks `inner run` for
every profile. Fail-closed is right for other errors; for `ENOENT` on the
target there is nothing to hide.

**Fix:** on `ENOENT` of the resolved target, skip the hide with a warning;
keep every other error fatal.

### ISS-34 — [Low, accepted] Denylist does not cover relocated paths and other tools

**Still valid** — none of the listed paths is in `config.SensitiveResources`:
paths moved by env vars (`XDG_CONFIG_HOME`, `GNUPGHOME`, `KUBECONFIG`,
`DOCKER_CONFIG`, `CARGO_HOME`, `GH_CONFIG_DIR`, …), flatpak/snap browsers,
`~/.vault-token`, `~/.config/containers/auth.json`, the agents' own credential
files, and more (full list in `ISSUES.md`).

This is inherent to `home = "host-ro"`. The built-in agent profiles already use
`home = "isolated"`, which makes this list irrelevant for them. Extending the
list is worth doing opportunistically, but it is not a vulnerability fix.

### ISS-37 — [Low] Residuals of the remote-profile gate

**Still valid**, all minor:

- `--dry-run` on a remote profile skips consent but `prepareSandbox` still runs
  host-side preparation (temp copies of capability dirs) before the dry-run
  print (`cmd/inner/cmd_run.go`). It runs on the already-hardened profile and
  executes nothing.
- With an abstract D-Bus address (`unix:abstract=…`) and a shared host network
  namespace, the host bus stays reachable despite the proxy (documented in
  `cmd/inner/sandbox_dbus.go`).
- The "secret-looking env name" filter is still a name heuristic.

---

## Appendix — what was already solid (do not "fix")

For a newcomer: these areas were reviewed and found correct; changing them is
likely to introduce bugs.

- **Workspace locking** (`internal/workspace/manager.go`): a sentinel `flock`
  makes scan+mkdir+write atomic across processes; lock files use
  `O_EXCL`+`0600`; liveness uses `flock` (not `kill(pid,0)`), correctly avoiding
  PID-reuse false positives.
- **Shim name/replacement validation** (`internal/shim/builder.go`):
  `isSafeShimName` / `isSafeShimReplacement` reject path separators and shell
  metacharacters.
- **Git override ambiguity** (`internal/git/sanitizer.go` `applyOverride`):
  refuses to edit a section name that appears more than once (e.g. two `[remote]`
  blocks) instead of guessing.
- **Broken-symlink handling in sensitive hiding** (`internal/isolator/bwrap.go`):
  fails **closed** (returns an error) rather than skipping the mount and leaving
  the secret readable. (ISS-36 above narrows this for `ENOENT` only.)
- **Profile name / extends traversal**: `validateProfileName` blocks `/`, `\`,
  `..`; the extends cycle detector canonicalizes via `EvalSymlinks`.
