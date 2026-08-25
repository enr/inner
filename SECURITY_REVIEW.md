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

- ✅ **[SIGNED OFF] Issue #9 — PID-namespace / TUI manual verification** — the
  checklist was run by the maintainer on a real terminal with a locally built
  `inner` binary (2026-08-25); reported outcome: all steps OK. The
  `pid_namespace = false` escape hatch stays documented as the per-host
  rollback. See **#9** below.
- ✅ **[FIXED] Issue #11 — `checkEnvSecrets` was an oversold name-only
  heuristic** — broader pattern list, renamed and reworded to say plainly it is
  a heuristic, and downgraded from High to Medium severity so a false positive
  doesn't fail `inner verify` on its own. See **#11** below.
- ✅ **[FIXED] Issue #10 — `expandAliases` split on raw whitespace** — already
  fixed in `31697a9`, before this review pass; the review text below just
  hadn't been updated to say so. See **#10** below.
- ✅ **[FIXED] Issue #8 — `checkUsrReadonly` failed open** — it now reads the
  `ro` option of the mount actually covering `/usr` from mountinfo, the same
  approach `checkHomeIsolated` already used for `$HOME`, instead of inferring
  read-only from a failed write attempt. See **#8** below.
- ✅ **[FIXED] Issue #7 — `quoteGitConfigValue` mangled `\r`** — it now writes a
  raw CR byte inside the quotes, matching real `git config`'s own output,
  instead of the copy-pasted `\n` escape (or the `\r` escape the review
  suggested, which turns out to not exist in git's own parser). See **#7**
  below.
- ✅ **[FIXED] Issue #6 — `extractExpiresAt` map iteration was nondeterministic**
  — it now collects every candidate expiry and returns the earliest, instead of
  the first one a randomized map iteration happens to visit. See **#6** below.
- ✅ **[FIXED] Issue #5 — workspace prep could leak partial directories** —
  `dirsToCreate` output is now recorded for rollback *before* `MkdirAll` runs,
  not after it succeeds. See **#5** below.
- ✅ **[FIXED] Issue #4 — CLI `-m` rejected `safe-rw`** — `parseMount` now
  accepts `safe-rw`; `tmpfs` is explicitly rejected with a pointer to `[mounts]`
  since it has no host source. See **#4** below.
- ✅ **[FIXED] Issue #3 — `safe-rw` and capability copies followed symlinks** —
  `copyFile` refuses a symlink source (`os.Lstat`), `copyDir` skips a symlinked
  file instead of dereferencing it. See **#3** below.
- ✅ **[FIXED] Issue #2 — remote profiles were fully trusted** — a downloaded
  profile is now hardened, summarized and blocked on explicit consent, and can be
  pinned with `--sha256`. See **#2** below.
- ✅ **[FIXED] Issue #1 — denylist read side** — both fixes shipped: the
  allowlist home mode (`[sandbox] home = "isolated"`) and the extended,
  test-guarded hide list, with planted-secret e2e coverage. See **#1** below.
- ✅ **[FIXED] Nested-user-ns `--` insertion bug** — `prepareNestedUserNs` used
  to splice bwrap fds before *every* `--`. Now inserts before the first
  separator only. (`cmd/inner/cmd_run.go`)
- ✅ **[FIXED] Interactive PID-namespace leak** — the sandbox now gets
  `--unshare-pid` for all runs by default (opt out with
  `[sandbox] pid_namespace = false`). See issue **#9** below for the manual
  TUI verification that still has to be performed on a real terminal.

---

## #1 — [CLOSED] The read side of the sandbox is a denylist, not an allowlist

**Where:** `internal/isolator/bwrap.go` — `--ro-bind / /` (base mount) plus the
hide list in `config.SensitiveResources` (`internal/config/types.go`).

**What was wrong:** the entire host filesystem is bind-mounted read-only into the
sandbox, and then a *fixed list* of ~17 known credential paths is hidden on top.
Anything **not** on that list is fully readable by the sandboxed agent: browser
cookie databases, `.env` files anywhere in the tree, the user's source code, and
tokens for tools not on the deny-list (`~/.config/gh`, `~/.terraform.d`,
`~/.m2/settings.xml`, `~/.config/helm`, …). The sandbox protected against
**writes** and a fixed set of credential files; it did **not** protect the
confidentiality of data at rest, and the list would silently rot as new tools
invent new token paths.

Both fixes proposed in the review are now in.

### Fix 2 — the allowlist model (`[sandbox] home = "isolated"`)

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
  `home = "isolated"`, warns when the entrypoint binary itself lives in the
  hidden home, and the profile validator rejects an unknown mode, an allowlist
  entry equal to `$HOME`, and warns when an entry re-exposes a path from the
  hide list.

### Fix 1 — the deny list was extended and is now guarded

`config.SensitiveResources` gained the paths named in the review plus the
neighbouring ones that carry the same class of secret. New allow keys:
`gh-config`, `terraform-credentials`, `maven-settings`, `gradle-properties`,
`helm-config`, `pgpass`, `mysql-config`, `keyrings`, `onepassword-config`,
`browser-profiles` (`~/.mozilla` and the Chrome/Chromium/Brave/Edge/Vivaldi/Opera
profile directories), plus `~/.cargo/credentials.toml` under the existing
`cargo-credentials` key. A key may now cover several paths; allowing the key
un-hides all of them. Only the *credential files* of `~/.m2` and `~/.gradle` are
hidden — the rest of those trees is the artifact cache, and hiding it would break
offline builds for no security gain.

Three mechanisms keep the list from rotting again:

- `TestSensitiveResources_coverWellKnownSecrets` (`internal/config/sensitive_test.go`)
  holds a canary list of well-known secret paths; adding a tool's token path
  there fails the build until the hide list covers it. Two companion tests assert
  every hide key is declassifiable via `[sandbox] allow` (except the two
  shell-history keys, deliberately) and is listed in `CredentialAllowKeys`, so a
  new entry cannot silently escape the "network + credentials" warning.
- `inner verify` now derives **one check per hide key** from the same table
  (`Checker.checkSensitiveResources`), instead of covering only the five
  resources that had a hand-written check. Growing the hide list grows `verify`
  automatically. A check fails when any of its paths is readable and non-empty
  inside the sandbox; `[sandbox] allow` declassifies it like any other check.
- The docs tables (`docs/content/internals.md`, `docs/content/profiles.md`) list
  the full table and state plainly that a denylist only protects what someone
  thought of, pointing at `home = "isolated"` as the real answer.

### Verification — automated, and signed off on a real bubblewrap

`.sdlc/e2e` gained a **"Planted secrets: read-side policy"** section. It plants
real files on the host (`~/.config/gh/inner-e2e-hosts-$$.yml`, an *unlisted*
`~/.inner-e2e-unlisted-secret-$$.env`, and an allowlisted directory), then
asserts, through a real `bwrap`:

1. host-ro: the planted `~/.config/gh` secret is **not** readable;
2. host-ro: the unlisted secret **is** readable — the denylist limitation,
   asserted so it cannot change silently;
3. host-ro: `allow = ["gh-config"]` re-exposes it (escape hatch works);
4. isolated: **both** secrets are unreadable;
5. isolated: `$HOME` is really a tmpfs;
6. isolated: `inner verify` passes;
7. isolated: a `home_allow` entry is readable and nothing else is.

Everything planted is removed on exit, and the whole section skips itself when
`$HOME` is not writable (e.g. when the suite runs inside an inner sandbox).

Manual sign-off on bubblewrap 0.11.2 (2026-08-25), using the maintainer's real
home instead of planted files, since the check session itself ran with a
read-only home:

| Check | Result |
|-------|--------|
| `~/.config/gh/hosts.yml` under host-ro | gone (parent is an empty tmpfs) |
| `~/.mozilla`, `~/.config/google-chrome`, `~/.config/chromium` | 0 entries |
| `~/.m2/settings.xml`, `~/.gradle/gradle.properties` | `character special file` (/dev/null bind) |
| `~/.m2/settings-ifis.xml` (non-standard name, unlisted) | readable — denylist limitation, as designed |
| `allow = ["gh-config"]` | `hosts.yml` readable again |
| `home = "isolated"` + `home_allow = ["~/.m2"]` | `$HOME` fstype `tmpfs`, entries `.m2 Projects`, gh secret gone, `~/.m2/settings.xml` still `/dev/null` |
| `inner verify -p <isolated profile>` | 33/33 checks passed |

### Fallout fixed along the way — `inner verify` under an isolated home

Running the e2e suite outside a sandbox surfaced a real bug in the isolated-home
work: `inner verify` re-read the profile file **from inside** the sandbox, but
`verify` sets no workdir, so under `home = "isolated"` the profile (living in the
hidden home) was unreadable. The load failed silently and the checks ran with a
default context: `network = false`, no shims expected, **no `allow` keys and no
`[verify.custom]` checks**. Visible symptom: every agent profile failed the
`network restricted` check. Worse, unseen: `[sandbox] allow` declassification and
custom checks were silently dropped for exactly the profiles that isolate the home.

Fix in `cmd/inner/cmd_verify.go`: the host side now passes the whole verify
context through `INNER_VERIFY_*` env vars (home mode, network, shims-expected,
allow list, JSON-encoded custom checks), following the precedent already used for
the home mode; the profile is only re-read to fill in what the environment does
not carry. The values now describe the sandbox that was actually built. Covered
by `cmd/inner/cmd_verify_test.go`, and `.sdlc/e2e`'s `run_verify` now prints the
failing check lines instead of a bare red line.

Residual, accepted: under `home = "host-ro"` any path nobody listed is still
readable (item 2 above). That is inherent to the denylist model — the fix for a
profile that cannot accept it is `home = "isolated"`.

---

## #2 — [CLOSED] Remote profiles are fully trusted to configure the sandbox

**Where:** `cmd/inner/cmd_run.go` (URL download path, ~`cmd_run.go:72`) and
`internal/config/remote.go`.

**What was wrong:** `inner run https://…/profile.toml` downloads a TOML profile and
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

### Earlier partial mitigation (kept)

Profile validation *reports* the dangerous settings a remote profile could
request, and refuses the outright destructive ones: a `rw` mount of `/` (or of
a path covering `$HOME` under `home = "isolated"`) is an error that blocks the
run, while `inherit_all = true`, `network = true` combined with credential
`allow` keys, `pid_namespace = false` and `rw` mounts of system directories
produce warnings naming the consequence and the fix. Same checks in
`inner profile validate` and `inner doctor`, so a downloaded profile can be
inspected before it is run. Warnings do not block, which is why the three fixes
below were needed on top.

### Fix — a downloaded profile is now untrusted input (`cmd/inner/remote_profile.go`)

All three measures asked for in the review are in. The gate runs in `runSandbox`
right after the profile is built and **before** any CLI override, so the user's
own flags are applied on top of the hardened profile.

1. **Hardening (`hardenRemoteProfile`)** — applied by default to any profile
   fetched from a URL, it removes what would turn the sandbox into a host-secret
   pipe:
   - `[env] inherit_all = true` → ignored, the environment stays cleared;
   - `[env] inherit` entries whose name looks like a secret (`*TOKEN*`,
     `*SECRET*`, `*PASSWORD*`, `*PASSWD*`, `*CREDENTIAL*`, `*KEY*`, `*AUTH*`,
     `*SESSION*`) → dropped, so the profile cannot ask for a host credential by
     name either;
   - `[sandbox] allow` keys that un-hide a readable credential
     (`config.CredentialAllowKeys`) or grant a host privilege (`docker-socket`,
     `podman-socket`, `nested-user-ns`) → dropped; the verify-only keys
     (`env-secrets`, `shims-active`, `network-policy`) are harmless and kept;
   - `[sandbox] pid_namespace = false` → ignored.

   `--trust-remote` opts out of the hardening (and counts as consent) for a
   profile the user controls.

   Deliberately **not** stripped: `network`, the entrypoint, mounts and
   capabilities. Those are what a profile legitimately exists to describe;
   forcing `network = false` would break every remote agent profile while the
   hardening above already removes the secrets that make an open network an
   exfiltration channel. They are reported instead, and consent is what gates
   them.

2. **Blocking consent (`gateRemoteProfile`)** — the summary lists the source URL,
   the sha256, every hardening that was applied, and what the profile still asks
   for (command + args, network, home mode, capabilities, remaining `allow`
   keys, inherited env, writable mounts, noop rewrites). Then it blocks on
   `run this downloaded profile? [y/N]`. `--yes` deliberately does **not** answer
   it — that flag skips routine confirmations, it is not blanket trust for
   downloaded code. `--allow-remote` (or `--trust-remote`) accepts without the
   prompt; a run with no terminal on stdin and no flag is **refused**, never
   silently accepted. `--dry-run` prints the summary and proceeds, since nothing
   executes.

3. **Checksum pin** — `inner run <url> --sha256 <digest>` hashes the exact bytes
   fetched and aborts on mismatch (accepts a bare or `sha256:`-prefixed digest,
   any case). The digest of every download is printed with the command to pin it,
   so serving different content tomorrow fails instead of running. Passing
   `--sha256` with a local profile is an error, not a silent no-op.

`inner profile install <url>` gained the same `--sha256` pin, and now prints the
digest and the profile's validation warnings. It stays *without* a consent gate
on purpose: installing is the explicit act of trust, and the installed file is a
local profile from then on. Both the command docs and the profiles docs say so.

**Verification:** `cmd/inner/remote_profile_test.go` — digest match / mismatch /
malformed pin; hardening strips exactly the privileged settings and leaves a
benign profile untouched; `--trust-remote` skips it; the interactive answers
(`y`/`Y`/`n`/empty/EOF); and four `runSandbox` integration tests over a TLS test
server: `--yes` alone is refused ("without consent"), `--allow-remote` runs and
the *isolator actually receives* a config with no `inherit_all`, no `allow` keys
and the PID namespace back on, a wrong `--sha256` aborts, a matching one runs.

Residual, accepted: a profile installed with `inner profile install` is a local
profile and is not re-gated at run time; and `--trust-remote` is, by design, a
full opt-out. Both are explicit user acts, printed and documented.

---

## #3 — [CLOSED] `safe-rw` and capability copies follow symlinks

**Where:** `cmd/inner/sandbox_claude.go` — `copyFile` / `copyDir`
(~`sandbox_claude.go:463`), used by `applyGenericSafeMounts`
(`cmd/inner/sandbox_safe.go`) and every capability `prepare*` function
(`prepareClaude`, `prepareCursor`, …).

**What was wrong:** `copyFile` used `os.ReadFile`, which **follows symlinks**.
`copyDir` walks with `filepath.WalkDir` (which does not descend into symlinked
*directories*) but still handed file symlinks to `copyFile`. So a symlink planted
inside a copied source tree (e.g. `~/.claude/skills/evil -> /home/user/.ssh/id_rsa`)
had its **contents** copied into the sandbox temp dir and then mounted into the
sandbox — bypassing the sensitive-path hiding from issue #1.

**Why it matters:** the whole point of "copy the dir so the agent can't corrupt
the original" is integrity isolation. Symlink-following turns a copied directory
into a read primitive for arbitrary host files.

### Fix

`copyFile` now `os.Lstat`s the source before reading it and refuses (returns an
error) when it is a symlink — fail closed, matching the existing broken-symlink
handling in `internal/isolator/bwrap.go` (see the Appendix). `copyDir`'s walk
checks `d.Type()&fs.ModeSymlink` and skips a symlinked file entry instead of
handing it to `copyFile` (which would otherwise abort the whole walk on the
first symlink found, since `filepath.WalkDir` stops on a non-nil callback
error). Every existing call site already ignores `copyFile`/`copyDir` errors for
missing files (`_ = copyFile(...)`), so a rejected symlink degrades the same way
a missing file already did: that one entry is absent from the copy, not a hard
failure of the capability.

**Verification:** `TestCopyFile_refusesSymlink` and `TestCopyDir_skipsSymlinkedFile`
in `cmd/inner/sandbox_claude_test.go` — a symlink to a file outside the source
tree is neither read into the destination directly nor via a directory walk.

---

## #4 — [CLOSED] CLI `-m` mount parser rejects `safe-rw` / `tmpfs`

**Where:** `cmd/inner/cmd_run.go` — `parseMount` (~`cmd_run.go:479`), vs the modes
accepted in profiles (`internal/config/types.go` `MountEntry`, and the validator
`internal/profile/validator.go`).

**What was wrong:** profile mounts accept four modes (`ro`, `rw`, `safe-rw`,
`tmpfs`), but the CLI `-m SRC:DEST[:MODE]` parser only allowed `ro` and `rw`. A
user could not express `-m ~/data:/data:safe-rw` on the command line even though
it is a first-class concept elsewhere.

### Fix

`parseMount` now accepts `safe-rw` (it needs a real `Src`, exactly like `ro`/`rw`,
so it fits the `src:dest:mode` shape and `applyGenericSafeMounts` handles it with
no other change). `tmpfs` is explicitly rejected with an error naming why —
it has no host source, so it does not fit `src:dest:mode` — and pointing at the
profile's `[mounts]` table instead of silently doing nothing or misparsing. The
`--mount` flag help text and `docs/content/commands.md` were updated to match.

**Verification:** `TestParseMount_modes` (`cmd/inner/cmd_run_test.go`), a table
test covering `ro` (implicit and explicit), `rw`, `safe-rw`, the rejected
`tmpfs`, and an unrelated invalid mode.

---

## #5 — [CLOSED] Workspace dir creation can leak partially-created directories

**Where:** `internal/workspace/manager.go` — `Prepare`, the per-dest loop
(~`manager.go:93`).

**What was wrong:** for each dest, the list of directories that `MkdirAll` would
create was computed by `dirsToCreate` **before** the `MkdirAll` call, but it was
appended to the rollback list `created` only **after** `MkdirAll` succeeded. If
`MkdirAll` failed partway (e.g. permission denied at a deep level after creating
the shallower parents), `removeDeepestFirst(created)` ran without this dest's
just-created directories, so they were left behind on disk.

**Why it matters:** failed runs could litter the workspaces directory with empty
orphan directories. Not catastrophic, but a correctness/cleanliness bug in the
error path that someone would eventually have to debug.

### Fix

`newDirs := dirsToCreate(dest)` is now appended to `created` immediately, before
`os.MkdirAll(dest, ...)` runs — so a partial failure inside a single dest's own
`MkdirAll` call is rolled back exactly like a failure on a different dest
already was.

**Verification:** `TestPrepare_rollsBackDirsCreatedBeforeMidChainFailure`
(`internal/workspace/manager_test.go`). Reproducing "MkdirAll creates some
directories then fails deeper in the same call" needed more than a read-only
parent (that only fails the *first* level, creating nothing — indistinguishable
from the old code): the test uses a final path component longer than `NAME_MAX`
(300 bytes; the OS limit is 255 on every common Linux filesystem), so `MkdirAll`
successfully creates the two shallower directories above it and only then fails
with `ENAMETOOLONG` on the last one. Confirmed the test fails on the pre-fix code
(both intermediate directories survive) and passes after the fix.

---

## #6 — [CLOSED] `extractExpiresAt` iterates a map → nondeterministic token decision

**Where:** `cmd/inner/sandbox_claude.go` — `extractExpiresAt`, the nested-object
fallback loop (~`sandbox_claude.go:155`).

**What was wrong:** when the top-level `expiresAt` / `expires_at` keys are
absent, the function scanned nested objects by ranging over a Go `map`, whose
iteration order is **randomized**, and returned on the first match. If more than
one nested object carried an expiry field, which value came back varied run to
run.

**Why it matters:** token expiry drives whether `inner` runs the credential
unlock flow or skips it. Nondeterminism here means a flaky "sometimes prompts,
sometimes doesn't" behaviour that is painful to reproduce.

### Fix

The loop now collects every candidate timestamp across all nested objects
instead of returning on the first match, and picks the **earliest** — the safe
choice, since it never delays the unlock prompt past when a token actually
expires. Map iteration order no longer affects the result.

**Verification:** `TestExtractExpiresAt_multipleNestedObjects_deterministic`
(`cmd/inner/sandbox_claude_test.go`) — two nested objects with different
expiries, asserted to return the earlier one across 50 runs (map iteration order
reshuffles every run, which is what made the old code flaky).

---

## #7 — [CLOSED] `quoteGitConfigValue` rewrites `\r` to `\n`

**Where:** `internal/git/sanitizer.go` — `quoteGitConfigValue`
(~`sanitizer.go:243`).

**What was wrong:** the escape `switch` had `case '\r':` writing the literal
`\n` escape (looks copy-pasted from the `\n` case). A carriage return in a
value was silently turned into a newline escape in the emitted gitconfig.

**Why it matters:** low impact (CR in a git value is rare), but it is a silent
data-mangling bug.

### Fix — not the one originally suggested

The review's suggested fix ("emit `\r` for the `'\r'` case") turned out to be
wrong: git's own config parser has **no `\r` escape**. Verified against the
real `git` binary — a config file containing the literal two-char sequence
`\` `r` inside a quoted value is a hard parse failure (`fatal: bad config line`),
not a mangled-but-parseable value. And `git config <key> <value>` itself, given
a value containing a real carriage return, writes a **raw CR byte** inside the
quotes, not any escape sequence. `quoteGitConfigValue` now does the same:
`case '\r': sb.WriteByte('\r')`.

**Verification:** `TestProcess_overrideQuotesCarriageReturn`
(`internal/git/sanitizer_test.go`) asserts the emitted config contains a raw CR
in the value and no `\r` two-char sequence, then — when `git` is on `PATH` —
writes the sanitized output to a temp file and round-trips it through the real
`git config --get`, confirming git itself accepts the file and returns the
original value byte-for-byte. Confirmed the test fails against the pre-fix code
(which emits `\n` for a `\r` input, as documented in the original report).

---

## #8 — [CLOSED] `checkUsrReadonly` infers read-only from a failed write

**Where:** `internal/sandbox/checker.go` — `checkUsrReadonly`
(~`checker.go:252`).

**What was wrong:** the check decided `/usr` was read-only by *attempting to
create a temp file there and treating failure as success*. Run outside the
sandbox as a normal user, `/usr` is not writable anyway, so the check reported
"conformant" while proving nothing. The failure mode was "silently passes,"
which is the wrong direction for a security check.

**Why it matters:** a check that can only fail-open gives false assurance.

### Fix

`checkUsrReadonly` now reads `/proc/self/mountinfo` and checks the `ro` option
on the mount actually covering `UsrDir` — the same signal the kernel itself
enforces, and the same approach `checkHomeIsolated` already used for `$HOME`
(issue #1's `home-isolated` check). The new `mountReadOnly` helper resolves the
*covering* mount the way the kernel would for a path with no dedicated mount
entry — `/usr` has none; it is covered by inner's own `--ro-bind / /` at `/` —
by picking the longest matching mount-point prefix from mountinfo, mirroring
the existing `mountFsType` helper's shadowing rule (last matching line wins).
An unreadable mountinfo or no covering mount now **fails** the check, instead of
the old code's "can't write → must be read-only."

The unit test that used to fail under root (`TestCheck_usrReadonly_pass_when_readonly`,
noted in the original report) is gone along with the write-probe it exercised;
the new tests need no root/non-root branching since they drive the check
through injected mountinfo fixtures, not real filesystem permissions.

**Verification:** four tests in `internal/sandbox/checker_test.go` — fails on
an `rw` covering mount, passes on an `ro` one, correctly resolves the covering
`/` mount when `/usr` has no dedicated entry (inner's own sandbox layout), and
fails closed when mountinfo cannot be read. Cross-checked against a real
`bwrap --ro-bind / /`: `/proc/self/mountinfo` inside it lists the root mount
with `ro` in the options field, confirming the check passes correctly in the
actual sandbox `inner verify` runs in.

---

## #9 — [CLOSED] Verify the PID-namespace / TUI change on real terminals

**Status:** **signed off on 2026-08-25.** The maintainer ran the checklist below
by hand, on a real terminal, against a locally built `inner` binary, and reported
all steps OK. Nothing further is required for this item.

Scope of the attestation, stated plainly so a future reader knows what this
sign-off does and does not carry: it is a single-machine, single-session pass
reported as a whole, not a captured per-step transcript. That is exactly what
this item asked for — the point was to put the real TUIs in front of a human
once, because the automated test only replicates the core of libuv's terminal
probe. It is not evidence that every kernel/bubblewrap/terminal-emulator
combination behaves identically, which is why the rollback below stays
documented rather than being removed.

The steps are kept verbatim so they can be re-run: `network.md` calls for
exactly this checklist to be repeated once the network relay adds a process hop
between bwrap and the TUI, and that re-run only means something against this
baseline.

Since this sign-off, steps 1, 4 and 5 have been automated — they never needed a
human, only a script nobody had written — and live in `.sdlc/e2e` under
"Process isolation: runtime". What is left needs a real terminal: see
`manual-tests/`, which has the profiles, an A/B signal probe that covers the
mechanism without needing a third-party TUI, and the reasons the residue cannot
honestly be scripted.

**Original status:** code change was **done and unit-/dry-run-tested**; what
remained was a manual smoke test that **cannot be automated in CI** because it
needs a real controlling terminal and the real TUI binaries.

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

## #10 — [CLOSED] `expandAliases` splits on whitespace

**Where:** `cmd/inner/root.go` — `expandAliases` (~`root.go:115`).

**What was wrong:** alias expansions were split on raw whitespace, so an alias
whose value contained a quoted argument with spaces (e.g. `review = "run -p x
--arg 'two words'"`) was split into the wrong tokens.

**Status:** already fixed, in commit `31697a9` ("fix: correct log path, verify
checks, alias quoting, and remove legacy config handling"), which predates this
review pass — the review text just hadn't been updated to reflect it.
`expandAliases` calls `splitArgs`, a small shell-like tokenizer (honours single
and double quotes, `\`-escaping, unterminated quotes run to end of string) in
place of `strings.Fields`.

**Verification:** `TestSplitArgs` (`cmd/inner/root_test.go`) — quoted
multi-word arguments (both quote styles), escaped spaces, adjacent
quoted/unquoted concatenation (`'single '"double"`), and empty/whitespace-only
input.

---

## #11 — [CLOSED] `checkEnvSecrets` is a name-only heuristic

**Where:** `internal/sandbox/checker.go` — `checkEnvSecrets`
(~`checker.go:336`).

**What was wrong:** it flagged env var **names** containing `PASSWORD`,
`SECRET`, `TOKEN`, `CREDENTIAL`, `PRIVATE_KEY`. It missed common real secrets
whose names don't match (`AWS_ACCESS_KEY_ID`, `OPENAI_API_KEY`, `GH_TOKEN` was
caught but `OPENAI_API_KEY` was not, etc.).

**Why it matters:** it is an informational check; the risk is that it is
*oversold* as "no secrets in env" when it only catches a few naming patterns.

### Fix

Both parts of the suggested fix, applied exactly as scoped:

- **Broader patterns** — added `PASSWD`, `API_KEY`, `ACCESS_KEY`, `_KEY`,
  `AUTH` to the existing `PASSWORD`, `SECRET`, `TOKEN`, `CREDENTIAL`,
  `PRIVATE_KEY` list. `_KEY` alone now catches `OPENAI_API_KEY` and
  `AWS_ACCESS_KEY_ID` too, but the specific patterns stay for clearer detail
  text on the common cases.
- **Softer wording and severity** — renamed to "no obviously-named secrets in
  env vars", the failure detail now says "(heuristic, may include false
  positives)" instead of stating a match as fact, and severity dropped from
  High to Medium: a name-only match is suggestive, not conclusive, in *both*
  directions (it can flag `BASIC_AUTH_REALM` with nothing secret in it, and
  miss a real secret in a variable it has never heard of), so it should nudge
  a profile author to double-check `[env] inherit`, not fail `inner verify`
  outright the way a High-severity finding does.

**Verification:** `internal/sandbox/checker_test.go` — a table test over the
newly-covered names (`OPENAI_API_KEY`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `DB_PASSWD`, `BASIC_AUTH`), plus an explicit assertion
that the check's severity is `SeverityMedium`.

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
