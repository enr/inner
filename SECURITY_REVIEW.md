# Security & code review — open issues

This file tracks issues found during a senior-level review of `inner`. It is
written to be actionable by a developer new to the codebase: each item explains
**what is wrong**, **why it matters**, **where to look**, and **how to fix it**.

Severity legend:

- **Critical** — fix before the next release; exploitable or data-losing.
- **High** — serious; schedule soon.
- **Medium** — real bug or risk, lower blast radius.
- **Low** — polish / correctness nit.

Status of items already handled:

- ✅ **[FIXED] Nested-user-ns `--` insertion bug** — `prepareNestedUserNs` used
  to splice bwrap fds before *every* `--`. Now inserts before the first
  separator only. (`cmd/inner/cmd_run.go`)
- ✅ **[FIXED] Interactive PID-namespace leak** — the sandbox now gets
  `--unshare-pid` for all runs by default (opt out with
  `[sandbox] pid_namespace = false`). See issue **#9** below for the manual
  TUI verification that still has to be performed on a real terminal.

---

## #1 — [IMPLEMENTED / partially open] The read side of the sandbox is a denylist, not an allowlist

**Where:** `internal/isolator/bwrap.go` — `--ro-bind / /` (base mount) plus the
hard-coded `sensitive` list (~`bwrap.go:248`).

**What is wrong:** the entire host filesystem is bind-mounted read-only into the
sandbox, and then a *fixed list* of ~17 known credential paths is hidden on top.
Anything **not** on that list is fully readable by the sandboxed agent.

**Why it matters:** a coding agent (claude, etc.) running in the sandbox can read
the user's whole home directory and system: browser cookie databases
(`~/.mozilla`, `~/.config/google-chrome`), `.env` files anywhere in the tree, the
user's source code and documents, and tokens for tools not on the deny-list
(`~/.config/gh`, `~/.terraform.d`, `~/.m2/settings.xml`, `~/.config/helm`,
JetBrains/VS Code config, …). The sandbox protects against **writes** and a fixed
set of credential files; it does **not** protect the confidentiality of data at
rest. The list will also silently rot as new tools invent new token paths.

**How to fix (options, in increasing order of strength):**

1. *Short term:* extend the `sensitive` list (add `~/.config/gh`,
   `~/.terraform.d`, `~/.m2`, `~/.config/helm`, `~/.local/share/keyrings`, browser
   profile dirs) and add a test that fails when a new well-known secret path is
   not covered.
2. *Proper fix:* invert the model for workspace-style profiles — start from an
   empty home (`--tmpfs $HOME`) and **allowlist** only the directories the agent
   needs (the workdir, the capability dirs). This is a larger change and should
   be a new profile mode (e.g. `[sandbox] home = "isolated"`).

**How to verify:** write an e2e test that, with a default profile, tries to
`cat` a planted secret outside the deny-list (e.g. `~/.config/gh/hosts.yml`) and
asserts the chosen policy (readable under denylist mode, hidden under allowlist
mode).

### Status — fix 2 implemented (`[sandbox] home = "isolated"`)

The inverted model shipped as a profile mode:

- `[sandbox] home` selects `"host-ro"` (previous behaviour, still the default
  for any profile that does not ask) or `"isolated"` (`--tmpfs $HOME`, emitted
  before every other mount so profile mounts, capability dirs and the workdir
  land inside it).
- `[sandbox] home_allow` is the allowlist: read-only re-binds of the paths the
  run needs (agent CLI, runtimes). Paths missing on the host are skipped, so one
  profile works across machines.
- The built-in **agent** profiles (`claude-*`, `gemini-*`, `cursor-*`) ship with
  `home = "isolated"`; the two `shell` profiles stay on `host-ro` on purpose and
  document the opt-in. Existing user profiles are untouched (`inner init` never
  overwrites), so this is not a silent behaviour change for current installs.
- `inner verify` gained the `home-isolated` check (HIGH): it reads
  `/proc/self/mountinfo` from inside the sandbox and fails unless `$HOME` really
  is a tmpfs.
- Guardrails: `inner run` refuses a workdir that covers `$HOME` under
  `home = "isolated"` (the read-write bind would restore the host home on top of
  the tmpfs), warns when the entrypoint binary itself lives in the hidden home,
  and the profile validator rejects an unknown mode, an allowlist entry equal to
  `$HOME`, and warns when an entry re-exposes a path from the hide list.

Still open:

- **Fix 1 (extend the deny list)** was *not* done: under `home = "host-ro"`
  `~/.config/gh`, `~/.terraform.d`, `~/.m2`, `~/.config/helm`,
  `~/.local/share/keyrings` and browser profile directories are still readable.
  Isolated mode makes this marginal for agent profiles, but the default mode
  keeps the original weakness.
- **The e2e verification above is not automated.** Unit coverage asserts the
  emitted bwrap argv and the checker logic; nobody has yet run a planted-secret
  test against a real bubblewrap (none is available in the CI container used for
  this change). Sign this off on a real machine before relying on the mode —
  same treatment as issue #9.

---

## #2 — [High] Remote profiles are fully trusted to configure the sandbox

**Where:** `cmd/inner/cmd_run.go` (URL download path, ~`cmd_run.go:72`) and
`internal/config/remote.go`.

**What is wrong:** `inner run https://…/profile.toml` downloads a TOML profile and
runs it. The profile controls **everything** about the sandbox: `network = true`,
`inherit_all = true` (forwards the whole host environment), `[sandbox] allow =
[...]` (un-hides ssh keys, cloud creds), the entrypoint command and its
arguments, and `capabilities`. Validation only **warns**; it never blocks. There
is no integrity check (no checksum/pin), and `FetchURL` follows redirects, so the
served content can change between runs.

**Why it matters:** one malicious URL can produce a sandbox with
`network = true` + `inherit_all = true` + an arbitrary entrypoint = trivial
exfiltration of every host secret in the environment (`AWS_*`, `GITHUB_TOKEN`,
…). This is a supply-chain footgun: the sandbox is supposed to be the safety net,
but a remote profile can disable the net.

**How to fix:**

1. Treat a remote profile as untrusted input. Before running it, print a
   **blocking** confirmation summarizing the dangerous settings it requests
   (network, env inheritance, `allow` keys, entrypoint) and require explicit
   consent (a `--allow-remote` flag or an interactive y/N that `--yes` does *not*
   auto-accept for remote sources).
2. Refuse privilege-escalating settings from a remote source unless separately
   opted in: reject `inherit_all`, force-clear `allow`, default `network=false`,
   or apply a hardened overlay on top of the downloaded profile.
3. Support a checksum pin (`inner run https://… --sha256 <hash>`) and reject on
   mismatch, so the fetched content cannot silently change.

**How to verify:** add a test that a remote profile with
`inherit_all = true` / `network = true` is rejected (or requires explicit
consent) by `runSandbox`, and that a checksum mismatch aborts.

---

## #3 — [Medium] `safe-rw` and capability copies follow symlinks

**Where:** `cmd/inner/sandbox_claude.go` — `copyFile` / `copyDir`
(~`sandbox_claude.go:463`), used by `applyGenericSafeMounts`
(`cmd/inner/sandbox_safe.go`) and every capability `prepare*` function
(`prepareClaude`, `prepareCursor`, …).

**What is wrong:** `copyFile` uses `os.ReadFile`, which **follows symlinks**.
`copyDir` walks with `filepath.WalkDir` (which does not descend into symlinked
*directories*) but still hands file symlinks to `copyFile`. So a symlink planted
inside a copied source tree (e.g. `~/.claude/skills/evil -> /home/user/.ssh/id_rsa`)
has its **contents** copied into the sandbox temp dir and then mounted into the
sandbox — bypassing the sensitive-path hiding from issue #1.

**Why it matters:** the whole point of "copy the dir so the agent can't corrupt
the original" is integrity isolation. Symlink-following turns a copied directory
into a read primitive for arbitrary host files.

**How to fix:** in `copyDir`'s walk and in `copyFile`, `Lstat` each entry; if it
is a symlink, either skip it or recreate it as a symlink **without**
dereferencing (`os.Readlink` + `os.Symlink`). Skipping is simplest and safest for
the capability use case.

**How to verify:** unit test — create a source dir containing a symlink to a file
outside it, run `copyDir`, and assert the destination does **not** contain the
target's contents.

---

## #4 — [Medium] CLI `-m` mount parser rejects `safe-rw` / `tmpfs`

**Where:** `cmd/inner/cmd_run.go` — `parseMount` (~`cmd_run.go:479`), vs the modes
accepted in profiles (`internal/config/types.go` `MountEntry`, and the validator
`internal/profile/validator.go`).

**What is wrong:** profile mounts accept four modes (`ro`, `rw`, `safe-rw`,
`tmpfs`), but the CLI `-m SRC:DEST[:MODE]` parser only allows `ro` and `rw`. A
user cannot express `-m ~/data:/data:safe-rw` on the command line even though it
is a first-class concept elsewhere.

**Why it matters:** inconsistent surface; users hit a confusing "invalid mode"
error for a mode the tool clearly supports.

**How to fix:** extend the allowed set in `parseMount` to include `safe-rw` and
`tmpfs` (note `tmpfs` has no host source, so decide whether to support it via
`-m` at all, or document the omission explicitly). Keep the error message listing
the full set of valid modes.

**How to verify:** table test for `parseMount` covering all four modes plus an
invalid one.

---

## #5 — [Medium] Workspace dir creation can leak partially-created directories

**Where:** `internal/workspace/manager.go` — `Prepare`, the per-dest loop
(~`manager.go:93`).

**What is wrong:** for each dest, the list of directories that `MkdirAll` will
create is computed by `dirsToCreate` **before** the `MkdirAll` call, but it is
appended to the rollback list `created` only **after** `MkdirAll` succeeds. If
`MkdirAll` fails partway (e.g. permission denied at a deep level after creating
the shallower parents), `removeDeepestFirst(created)` runs without this dest's
just-created directories, so they are left behind on disk.

**Why it matters:** failed runs can litter the workspaces directory with empty
orphan directories. Not catastrophic, but it is a correctness/cleanliness bug in
the error path that someone will eventually have to debug.

**How to fix:** capture `newDirs := dirsToCreate(dest)` and add it to `created`
**before** calling `MkdirAll`, so the rollback covers a partial failure.
Alternatively, on `MkdirAll` error, recompute what now exists and clean it up.

**How to verify:** test that injects a mkdir failure mid-chain (e.g. a dest under
a read-only parent) and asserts no new directories survive.

---

## #6 — [Medium] `extractExpiresAt` iterates a map → nondeterministic token decision

**Where:** `cmd/inner/sandbox_claude.go` — `extractExpiresAt`, the nested-object
fallback loop (~`sandbox_claude.go:155`).

**What is wrong:** when the top-level `expiresAt` / `expires_at` keys are absent,
the function scans nested objects by ranging over a Go `map`, whose iteration
order is **randomized**. If more than one nested object carries an expiry field,
which value is returned varies run to run.

**Why it matters:** token expiry drives whether `inner` runs the credential
unlock flow or skips it. Nondeterminism here means a flaky "sometimes prompts,
sometimes doesn't" behaviour that is painful to reproduce.

**How to fix:** check the known keys explicitly in a defined priority order, or
collect candidate timestamps and pick deterministically (e.g. the smallest, i.e.
earliest expiry — the safe choice). Avoid relying on map order.

**How to verify:** unit test with a credentials blob containing two nested objects
each with an `expiresAt`; assert the same value is returned across many runs.

---

## #7 — [Low] `quoteGitConfigValue` rewrites `\r` to `\n`

**Where:** `internal/git/sanitizer.go` — `quoteGitConfigValue`
(~`sanitizer.go:243`).

**What is wrong:** the escape `switch` has `case '\r':` writing the literal `\n`
escape (looks copy-pasted from the `\n` case). A carriage return in a value is
silently turned into a newline escape in the emitted gitconfig.

**Why it matters:** low impact (CR in a git value is rare), but it is a silent
data-mangling bug.

**How to fix:** emit `\r` for the `'\r'` case.

**How to verify:** unit test that a value containing `\r` round-trips to `\r` in
the quoted output.

---

## #8 — [Low] `checkUsrReadonly` infers read-only from a failed write

**Where:** `internal/sandbox/checker.go` — `checkUsrReadonly`
(~`checker.go:252`).

**What is wrong:** the check decides `/usr` is read-only by *attempting to create
a temp file there and treating failure as success*. Run outside the sandbox as a
normal user, `/usr` is not writable anyway, so the check reports "conformant"
while proving nothing. The failure mode is "silently passes," which is the wrong
direction for a security check. (This is also why the unit test
`TestCheck_usrReadonly_pass_when_readonly` fails when the suite runs as **root** —
root *can* write the temp file, so the check correctly reports writable, but the
test expected a pass. That test failure is pre-existing and unrelated to recent
changes.)

**Why it matters:** a check that can only fail-open gives false assurance.

**How to fix:** make intent explicit — this check is only meaningful **inside**
the sandbox (where `inner verify` runs it). Either gate it on an
"in sandbox" signal, or detect the mount's read-only flag directly (e.g. parse
`/proc/mounts` for the `ro` option on the mount backing `/usr`) instead of
probing by writing. For the failing unit test, make it skip when `os.Getuid()==0`
or use an injected `UsrDir` that is genuinely read-only.

---

## #9 — [High → needs manual sign-off] Verify the PID-namespace / TUI change on real terminals

**Status:** code change is **done and unit-/dry-run-tested**; what remains is a
manual smoke test that **cannot be automated in CI** because it needs a real
controlling terminal and the real TUI binaries.

**Context:** `inner` now passes `--unshare-pid` for **all** runs (interactive
included) to close the `/proc/<pid>/environ` host-process leak. Previously it was
skipped for interactive runs out of a belief that it broke TUI apps. Empirical
testing (bubblewrap 0.9.0 under a PTY) showed the controlling terminal survives
`--unshare-pid`; the flag that actually breaks the TTY is `--new-session`, which
`inner` never emits. See `docs/content/internals.md` →
"Process isolation: `--unshare-pid`" for the evidence table and invariants.

**Why a human still has to check:** the automated test replicates only the *core*
of libuv's terminal probe (`open("/dev/tty", O_RDWR)` + `tcgetattr`). The real
claude / gemini / cursor TUIs do more during init (alternate screen, kitty
keyboard protocol, resize handling). Confidence requires running them.

**Exact steps (run on a machine with a real terminal):**

1. **Low-level TTY probe under PID isolation** — should print `OK`:
   ```bash
   script -qec "bwrap --ro-bind / / --proc /proc --dev /dev \
     --bind /dev/pts /dev/pts --die-with-parent --unshare-pid -- \
     python3 -c 'import os,termios; fd=os.open(\"/dev/tty\",os.O_RDWR); \
     termios.tcgetattr(fd); print(\"OK\")'" /dev/null
   ```
2. **Real TUIs, full interactive session** — for each, type a prompt, confirm the
   UI renders, press **Ctrl-C** (must interrupt the operation, not kill the whole
   session), and **resize the window** (must redraw, i.e. SIGWINCH works):
   ```bash
   inner run -p claude-interactive
   inner run -p gemini-interactive
   inner run -p cursor-interactive   # if ~/.cursor is set up
   ```
3. **Plain interactive bash** — confirm multiline **paste** is visible and
   **up-arrow history** works (these depend on the launcher's raw-mode handling,
   not the PID namespace, but re-check them since this is the same code path):
   ```bash
   inner run -p shell
   # inside: paste a 3-line snippet; press up-arrow; run: sleep 100 & ; jobs ; fg ; Ctrl-C
   ```
4. **Confirm isolation is actually in effect** — should print a small number, not
   hundreds:
   ```bash
   inner run -p shell -- -c 'ls /proc | grep -c "^[0-9]"'
   ```
5. **Confirm the leak is closed** — reading PID 1's environ inside the sandbox
   must not reveal host-process secrets, and the host process list must be hidden:
   ```bash
   inner run -p shell -- -c 'ps -e | wc -l'   # expect a tiny number
   ```

**Acceptance:** all three TUIs usable (render, Ctrl-C, resize), bash paste +
history intact, `/proc` shows only sandbox processes.

**Rollback if any TUI misbehaves on a given host:** set the escape hatch in that
profile and re-test — this immediately restores the old behaviour without a code
change or redeploy:
```toml
[sandbox]
pid_namespace = false
```

---

## #10 — [Low] `expandAliases` splits on whitespace

**Where:** `cmd/inner/root.go` — `expandAliases` (~`root.go:115`,
`strings.Fields(expansion)`).

**What is wrong:** alias expansions are split on raw whitespace, so an alias whose
value contains a quoted argument with spaces (e.g. `review = "run -p x --arg
'two words'"`) is split into the wrong tokens.

**Why it matters:** minor usability surprise; aliases silently misbehave for
quoted args.

**How to fix:** parse the alias value with a shell-style tokenizer that respects
quotes (e.g. a small `shlex`-like split) instead of `strings.Fields`.

**How to verify:** test that an alias containing a quoted multi-word argument
expands to the expected argv.

---

## #11 — [Low] `checkEnvSecrets` is a name-only heuristic

**Where:** `internal/sandbox/checker.go` — `checkEnvSecrets`
(~`checker.go:336`).

**What is wrong:** it flags env var **names** containing `PASSWORD`, `SECRET`,
`TOKEN`, `CREDENTIAL`, `PRIVATE_KEY`. It misses common real secrets whose names
don't match (`AWS_ACCESS_KEY_ID`, `OPENAI_API_KEY`, `GH_TOKEN` is caught but
`OPENAI_API_KEY` is not, etc.).

**Why it matters:** it is an informational check; the risk is that it is
*oversold* as "no secrets in env" when it only catches a few naming patterns.

**How to fix:** broaden the pattern list (add `API_KEY`, `ACCESS_KEY`, `_KEY`,
`PASSWD`, `AUTH`) and soften the check's wording/severity so it is clearly a
heuristic, not a guarantee.

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
  the secret readable.
- **Profile name / extends traversal**: `validateProfileName` blocks `/`, `\`,
  `..`; the extends cycle detector canonicalizes via `EvalSymlinks`.
