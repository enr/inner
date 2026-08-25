# Manual tests

Checks that need a **real terminal** and, in one case, a real third-party TUI.
Everything that can be asserted by a script already is — see `.sdlc/e2e`. What
is left here is what a script cannot honestly assert.

Run these from **inside this directory**: the probe profiles invoke
`./probe.sh`, and the sandbox inherits the caller's working directory.

```bash
cd manual-tests
```

## Why these are not in `.sdlc/e2e`

Not because a PTY is hard to automate — `script`, `expect` or a Go pty library
all do it, and the e2e already allocates one for the controlling-terminal
probe. The obstacles are elsewhere:

- **The assertion is on someone else's rendered UI.** "The UI renders
  correctly" means parsing an ANSI stream into a terminal grid and comparing it
  against a golden snapshot. That snapshot depends on the upstream version of
  claude/gemini/cursor, so every release upstream breaks the test in a way that
  is indistinguishable from a real regression. A test that cries wolf gets
  muted, and a muted test is worse than no test.
- **The subject needs credentials, network and money.** Driving the real claude
  means an authenticated session and real API calls. That cannot run in CI, and
  on a dev machine it spends money to assert something about `termios`.
- **Some of it is a judgement.** Cursor position after a TUI exits, bracketed
  paste echo, whether a redraw looks torn — these are "does it feel right in
  MY terminal emulator", which is a person's call.

The probe below exists to shrink that residue: it covers the *mechanism*
(signals, terminal state, process shape) deterministically, so the human check
is only about the parts that genuinely need eyes.

## 1. Signal and terminal probe — the A/B pair

**What it is for.** `network_mode = "allowlist"` inserts `inner __net-relay`
between bwrap and the entrypoint. The relay deliberately forwards only
`SIGTERM` and `SIGHUP`: it shares the foreground process group with the child,
so the tty already delivers `SIGINT`, `SIGQUIT`, `SIGTSTP` and `SIGWINCH` to
both. Forwarding those would deliver each keypress **twice**, and a TUI whose
quit gesture is "press Ctrl-C twice" would read one press as two.

This pair is what proves that, without needing a TUI at all.

Run the baseline first, so you know what this machine and this terminal do
before the feature under test is in the chain:

```bash
inner run -p profiles/signal-probe.toml          # no relay
inner run -p profiles/signal-probe-relay.toml    # relay in the chain
```

In each run:

| Action | Expected |
|---|---|
| Press **Ctrl-C** once | exactly **one** `SIGINT received` line, and the probe keeps running |
| Press **Ctrl-C** three times | total reaches exactly **3**, never 6 |
| **Resize** the window | `SIGWINCH received` with the new size, once per resize |
| Type a line + **Enter** | the line is echoed back |
| `q` + **Enter** | clean exit, summary printed |

Also compare the header the probe prints at startup:

- `tty:` must say the controlling terminal is intact in **both** runs;
- `termios:` should be the same in both — the relay must not touch it;
- `parent:` shows the relay in the relay run and bwrap in the baseline — that
  is the extra hop, made visible;
- `proxy:` shows `HTTPS_PROXY` set only in the relay run.

**A failure looks like:** SIGINT counting up by two per keypress, SIGWINCH not
arriving, or `/dev/tty` unopenable in the relay run when the baseline could
open it.

Note that SIGINT arriving **at all** is itself the check that the child still
shares the relay's foreground process group — if the relay had put it in its
own group, the tty would stop delivering to it and the counter would never
move. The probe does not print the process group id: under `--unshare-pid`
procps resolves it to 0 because the group leader is not visible inside the
namespace, so comparing that number between runs would look like evidence
without being any.

## 2. Real TUI — the part that needs eyes

The A/B pair above covers the mechanism. This covers everything a rendered UI
does that the probe does not.

```bash
inner run -p profiles/tui-full.toml       # baseline: open network, no relay
inner run -p profiles/tui-allowlist.toml  # relay in the chain
```

Requires `~/.claude` to be set up on the host. Check what the allow list
resolves to before running:

```bash
inner run -p profiles/tui-allowlist.toml --dry-run
```

In each run: type a prompt and confirm the UI renders and responds; press
Ctrl-C mid-operation (it must interrupt the operation, not kill the session);
resize the window (it must redraw); exit and confirm the cursor and prompt are
left in a sane state.

If the allowlist run cannot reach something the tool needs, the proxy says so
on stderr:

```
inner: network-allowlist: blocked <host>:443 (not in the allow list)
```

Add that host to `network_allow` in the profile and note it — a destination
that turns out to be necessary belongs in `config.CapabilityNetworkAllow`, not
in every user's profile.

## 3. Interactive bash under the allowlist

For poking at the proxy by hand, and for the paste/history/job-control half of
the checklist:

```bash
inner run -p profiles/shell-allowlist.toml
```

Inside:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://example.com          # allowed
curl -sS -o /dev/null https://www.google.com                            # refused: not in the list
curl -sS -o /dev/null https://raw.githubusercontent.com/x               # refused: network_deny wins
curl -sS --noproxy '*' -o /dev/null https://example.com                 # fails: no route at all
env | grep -i proxy
```

Terminal behaviour to check while you are in there: paste a three-line snippet
(it must appear in full), press up-arrow (history must work), and run
`sleep 100 & ; jobs ; fg ; Ctrl-C` (job control must behave).

## Known environmental limits

Two of these need things a plain shell session has but a container or a nested
sandbox often does not:

- **A pseudo-terminal.** Without one, `/dev/tty` cannot be opened and the probe
  says so in its header. That is the probe reporting the environment
  correctly, not a failure of `inner` — but it also means the run proves
  nothing about signals, so do not record it as a pass.
- **A writable `~/.config/inner`.** `shell-allowlist.toml` uses a `bash`
  entrypoint, which makes `inner` write a `shell-init.sh` there to set the
  sandbox prompt. Where that directory is read-only the run stops before
  starting. The probe profiles avoid this by making the script itself the
  entrypoint.

## Recording the result

These runs are the evidence for `SECURITY_REVIEW.md` §9. When re-running after
a change to the relay, the entrypoint wrapping or the signal policy, note in
that file: the date, the machine, the terminal emulator, and any difference
between the baseline and the relay run. "No difference from baseline" is the
result worth recording — it is the whole claim being made.
