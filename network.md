S2 — network allowlist proxy

 Status

 Groundwork LANDED (behavior-preserving, no proxy yet): [sandbox] network_mode
 exists alongside the legacy network bool, with config.ResolveNetworkMode /
 RunConfig.EffectiveNetworkMode as the single fallback, the merge precedence
 rule below, --network/--no-network moving the mode, the isolator switching on
 the mode fail-closed, `inner verify` carrying INNER_VERIFY_NETWORK_MODE, and
 the shared appendHomeAllowIfHidden helper. "allowlist" is a reserved name the
 validator refuses until the proxy ships.

 Also LANDED: the wiring — applyNetworkProxy in prepareSandbox, the socket bind
 and proxy environment in bwrap.go, the dry-run output — so
 network_mode = "allowlist" is accepted and enforced. Verified against a real
 bwrap on this machine: the socket is bind-mounted and HTTP(S)_PROXY point at
 the relay; a direct connection bypassing the proxy fails (no route, no DNS);
 an allowed domain returns 200 through the proxy; a domain outside the list is
 refused with its reason; the cloud metadata endpoint and host loopback are
 refused even when listed in network_allow; network_deny overrides an allow
 entry; and `inner verify` reports "network restricted" as a pass. That sequence is now
 an automated section in .sdlc/e2e (ten assertions; only the
 allowed-destination one needs connectivity, and it skips without it).

 Docs are written: profiles.md has the user-facing section, internals.md the
 relay/proxy design, commands.md the dry-run output. NONO_COMPARISON.md §S2 and
 ISSUES.md ISS-05 are marked done, both recording what shipped beyond the
 original proposal and what was deliberately deferred.

 What remains: the manual TUI checklist (SECURITY_REVIEW.md §9) re-run with a
 relay in the chain, which needs a real terminal.

 Also LANDED: the relay (cmd/inner/cmd_net_relay.go) and internal/netproxy in full — the decision layer (ParseTarget,
 Policy.AllowsHost, Policy.AllowsAddr, the AllowPrivateDestinations test seam)
 and the CONNECT/plain-HTTP server on top of it, with its timeouts, concurrency
 cap and hop-by-hop header stripping. The package is self-contained and needs no
 bwrap: it serves any net.Listener. The relay and the cmd_run/bwrap wiring are
 not written yet, and neither are the capability allowlist layers.

 Everything below that is not marked LANDED is still to build.

 Context

 NONO_COMPARISON.md §S2 flags that inner's network policy is binary
 (network = true/false), so every agent profile that needs API access gets
 the whole internet — the classic exfiltration surface. nono's answer is a
 kernel-blocked sandbox plus a supervised proxy that only allows a domain
 allowlist. This plan adapts that to bwrap without adopting nono's
 architecture (no daemon, no seccomp-notify, no external dependency like
 slirp4netns).

 Key finding from spiking against a real bwrap on this machine: the
 kernel-enforced version is achievable with primitives inner already uses,
 no new external binary required:

 - bwrap --unshare-net gives the sandbox a private network namespace with
   loopback already up (confirmed: ip addr show lo reports UP immediately,
   no ip link set lo up needed — bwrap does this itself) and no other
   interface — a raw socket to any real address fails at the kernel level,
   exactly like today's network = false already guarantees on the
   network-policy verify check.
 - A host-side UNIX domain socket bind-mounts into that isolated netns and
   is fully usable — confirmed end-to-end: a Python client inside
   --unshare-net --tmpfs /tmp --dir /tmp/inner --ro-bind <host.sock> /tmp/inner/net-proxy.sock connected to a host-side unix listener and
   exchanged bytes. This is the exact --dir /tmp/inner + --ro-bind pattern
   GitConfigPath/ContainersConfPath already use (internal/isolator/bwrap.go).
   A read-only bind does not block connect(2): the EROFS check in
   inode_permission only covers regular files, directories and symlinks —
   sockets are excluded.

 So the design is: sandbox has zero real network access; the only way out is
 one bind-mounted unix socket to a host-side CONNECT-capable proxy that
 enforces the domain allowlist (with real DNS-rebinding protection, since the
 proxy — not the sandboxed process — resolves DNS) and always denies cloud
 metadata, loopback, link-local and private addresses. A tiny relay, run
 inside the sandbox by re-invoking inner itself, bridges a loopback TCP port
 (what HTTP_PROXY needs) to that unix socket, and wraps the real entrypoint so
 the relay's exit code/signal handling stay transparent.

 Threat-model note, stated up front because it shapes several decisions
 below: the proxy runs on the HOST, in the host network namespace. Turning it
 on does not only narrow what the sandbox can reach — it also re-attaches a
 previously fully-isolated netns to the host's stack. Everything the host can
 reach on loopback and on the LAN becomes reachable unless it is explicitly
 denied. That is why the always-deny list below is much larger than "the
 metadata IP".

 Config surface

 Mirror the existing home / home_allow pattern (flat fields, not a nested
 TOML table) in internal/config/types.go SandboxConfig.

 This diverges from NONO_COMPARISON.md §S2 and ISS-05, which both sketch a
 nested [sandbox.network] table with mode/allow keys. Flat wins because every
 other two-part policy in this file is already flat (home/home_allow,
 noop.block, sandbox.allow), because a nested table needs its own merge branch
 and its own IsDefined paths for no gain, and because [sandbox.network] would
 sit awkwardly next to the legacy [sandbox] network key it has to coexist with.
 Noted here so nobody re-opens the question mid-implementation; the issue text
 should be updated to match.

 [sandbox]
 network_mode  = "allowlist"     # "off" | "full" | "allowlist"; empty = legacy fallback
 network_allow = ["*.githubusercontent.com", "github.com"]
 network_deny  = ["sentry.io"]   # subtracted from the union, see "Allowlist layers"

 - Legacy network = true/false keeps working: network_mode empty resolves
   to full/off from the bool (mirrors Home's HomeHostRO default).
   New config.ResolveNetworkMode(sb SandboxConfig) string is the one place
   this fallback lives, used by the loader, the validator and verify.
 - New consts: config.NetworkOff/NetworkFull/NetworkAllowlist,
   config.ValidNetworkModes.
 - RunConfig gains NetworkMode string, NetworkAllow []string,
   NetworkAllowOrigin map[string]string, NetProxySocketPath string (host
   path, set by cmd_run.go, consumed by the isolator). Network bool stays as
   NetworkMode != NetworkOff — every existing consumer (dry-run, warnings)
   keeps working unchanged.
 - internal/config/merge.go: network_allow and network_deny union via
   mergeUnique like home_allow; network_mode follows the precedence rule below
   rather than a plain scalar override.

 network vs network_mode across extends. The two fields live on different
 axes, so "scalar override" alone is not enough and gets the precedence
 wrong in the unsafe direction. Concretely: base sets
 network_mode = "allowlist", child sets network = false — a naive
 ResolveNetworkMode only looks at the non-empty mode and the child silently
 KEEPS network access despite having written network = false. The rule is:

   1. network_mode declared in the overlay          → wins outright.
   2. otherwise, network declared in the overlay    → the bool wins over an
      inherited network_mode (mode := full/off).
   3. otherwise                                     → inherit the base's mode.

 LANDED. This needs the toml.MetaData at merge time, so the resolution of (2)
 happens in mergeProfiles (recording the effective mode into
 Sandbox.NetworkMode), not later in ResolveNetworkMode, which only sees the
 merged struct. loadRaw normalises the pair on decode as well, so a profile
 declaring only network_mode never carries a stale legacy bool — without that,
 a base declaring only the mode merges into a child whose two fields disagree.

 An earlier draft of this plan also wanted the validator to warn when one
 extends chain declares both fields. It cannot: Validate receives the MERGED
 profile and has no metadata about which level declared what. It is also
 unnecessary — the rule above plus the decode-time normalisation make a
 contradictory pair unrepresentable, so there is nothing left to warn about.

 CLI flags (LANDED). cmd_run.go assigned rc.Network directly from
 --network / --no-network. With Network derived from NetworkMode that becomes
 a silent no-op — or worse, an incoherent state (Network=true while
 NetworkMode is still "allowlist" and --unshare-net is emitted). Both flags
 must map onto rc.NetworkMode (full / off) instead, and rc.Network is
 recomputed from it.

 Allowlist layers

 A profile that declares capabilities = ["claude"] must not have to know
 which endpoints Claude Code talks to. Capabilities carry their own egress
 defaults, a profile inherits them and extends them:

   L0  always-deny, hard-coded        never overridable by any config
   L1  capability defaults            config.CapabilityNetworkAllow[name]
   L2  base profile (extends)         network_allow   ─┐ mergeUnique,
   L3  child profile                  network_allow   ─┘ like home_allow
   L4  CLI --network-allow            (deferred, see scope cuts)
   ────────────────────────────────────────────────────────────────────
   effective = (L1 ∪ L2 ∪ L3 ∪ L4) − network_deny − L0

 Union going up (a profile inherits and EXTENDS, it never implicitly
 narrows), with network_deny as the single explicit valve for dropping
 something a capability brought in — e.g. opting out of telemetry without
 losing the API endpoint. network_deny is subtracted after the union and
 before L0, so it can never re-open anything L0 denies.

 The table lives in internal/config/types.go next to CapabilityHostDirs, not
 in the cmd/inner capability registry: the loader, the validator, printDryRun
 and verify all need to read it, and none of them can import package main.

   // CapabilityNetworkAllow maps a capability to the egress domains its tool
   // needs to function at all. Unioned into RunConfig.NetworkAllow by
   // toRunConfig, the same way CapabilityHostDirs is the source of truth for
   // the host directories a capability sandboxes.
   var CapabilityNetworkAllow = map[string][]string{
       "claude": {"api.anthropic.com", "console.anthropic.com",
                  "statsig.anthropic.com", "sentry.io"},
       ...
   }

 The per-capability lists MUST be checked against each vendor's current
 documented egress domains before being committed, and re-checked when a
 capability is touched — they are the kind of data that rots silently and
 fails as an opaque connection error inside the sandbox. Split "needed to
 work" from "telemetry" in the comment so a profile author knows what is safe
 to put in network_deny.

 The union happens in toRunConfig (internal/config/loader.go), NOT inside
 Capability.Apply: dry-run, the validator and profile show --explain must see
 the effective list without executing anything. Apply may still append
 dynamic entries — the ordering in cmd_run.go already works (capabilities are
 step 10, applyNetworkProxy is inserted after 11c).

 LANDED: config.ResolveNetworkAllow returns the union plus the origins map, and
 the loader populates RunConfig.NetworkAllow/NetworkDeny/NetworkAllowOrigin.

 One correction to the formula above. network_deny is NOT subtracted when the
 config is resolved — the resolved allow list still contains the entry — it is
 evaluated per-request by netproxy.Policy, which checks the deny patterns
 before the allow patterns. That is what lets "*.internal.example.com" carve a
 hole out of "*.example.com": a list subtraction could only remove entries that
 match a deny string exactly, and would silently drop wildcard denies on the
 floor. So the formula holds at request time, not at load time.

 CapabilityNetworkAllow currently carries "claude" only. gemini, cursor and
 opencode are deliberately absent rather than guessed: an invented domain is
 worse than an absent one, because it looks verified. The validator reports a
 capability that contributes nothing, so a profile using one of those under
 allowlist mode is told to list the domains itself instead of failing as an
 opaque connection error inside the sandbox.

 Provenance. With four contributing sources, "why is this domain open?"
 becomes the common question, so record it:
 RunConfig.NetworkAllowOrigin maps host → "capability:claude" | "profile" |
 "cli". printDryRun and CapabilityExplain (which gains a Network []string
 field, alongside Mounts/PreRun/Notes) render it.

 Pattern matching (LANDED). Fixed semantics, encoded as a table test — every one
 of these is either a bypass or a false negative if left to the implementation:

 - Matching is case-insensitive; the CONNECT host is lower-cased and a single
   trailing dot is stripped before matching (api.anthropic.com. resolves to
   the same name and must not slip through).
 - Hostnames must be ASCII LDH (letters, digits, hyphen, dots). A non-ASCII
   host in a CONNECT request or in a config entry is REJECTED, not converted.
   Correct IDN handling means punycode (A-label) normalisation on both sides,
   which means a golang.org/x/net/idna dependency; go.mod today carries only
   toml, cobra, x/sys and x/term, and adding a dependency to a
   security-critical normaliser costs more than the use case is worth. "IDN
   hostnames are not supported" is honest, testable, and fail-closed —
   revisit only if someone actually needs one.
 - "example.com" matches exactly that name and nothing else.
 - "*.example.com" matches any subdomain at any depth (a.example.com,
   a.b.example.com) but NOT the apex — list the apex separately. Explicit is
   safer than convenient here.
 - An IP-literal entry matches an IP-literal CONNECT target as a string, with
   the parsed-IP form compared rather than the text (so 127.1 and
   ::ffff:127.0.0.1 cannot be smuggled past a textual "127.0.0.1" entry).
   Note that L0 denies loopback and private ranges anyway.
 - Ports: a bare host authorises ports 443 and 80 ONLY. "host:port"
   authorises exactly that port. Without this, network_allow = ["github.com"]
   also authorises CONNECT github.com:22 — a full push channel that bypasses
   HTTPS entirely on a profile that also allows ssh-keys.

 The proxy (internal/netproxy/proxy.go, new package)

 LANDED. A Proxy struct carrying a Policy and an injectable Resolver, served
 over any net.Listener (a unix listener on the host side in production, a
 loopback one in tests). Handles two request shapes:

 - CONNECT host:port (HTTPS, the common case): hijack the connection, run the
   decision chain below, then write 200 Connection Established or 403 with a
   reason, then splice bytes both directions.
 - Plain absolute-URI HTTP (GET http://host/...): same decision chain, then
   relay via http.Transport (NOT http.Client — a Client would follow
   redirects to an unvalidated host) with a DialContext that connects to the
   pre-validated IP.

 Decision chain — the ORDER is load-bearing (LANDED as internal/netproxy.Policy;
 the two halves take different argument types — a Target for the name gate, a
 net.IP for the address gate — so a server that resolves before consulting the
 allowlist has to write code that visibly does that, rather than getting it
 wrong by accident):

   1. Match the hostname (and port) against the effective allowlist.
      REJECT HERE, BEFORE RESOLVING.
   2. Resolve the name once, via the injectable resolver.
   3. Reject if any resolved IP hits the always-deny list.
   4. Dial the validated IP literal — never re-resolve at connect time; this
      is what closes the DNS-rebinding window.

 Resolving before checking the allowlist (the obvious ordering) opens a DNS
 exfiltration channel: the sandbox issues
 CONNECT <secret-in-base32>.attacker.com:443, the proxy resolves it — a real
 DNS query leaving the machine towards the attacker's nameserver — and only
 then denies the TCP connection. The data is already out. The hostname gate
 must run first.

 Always-deny (LANDED as Policy.AllowsAddr; checked after resolution, independent
 of Allow, cannot be overridden by profile or capability config):

 - 127.0.0.0/8 and ::1 — the proxy runs in the HOST netns, so without this
   the sandbox gains access to every host-local service: the Docker API on
   127.0.0.1:2375, Ollama on :11434, a local Postgres, a dev server. The
   sandbox is otherwise completely cut off from these, so the proxy would be
   handing out a NEW capability, not narrowing an existing one.
 - 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10 — LAN and
   CGNAT/Tailscale: routers, NAS, internal CI.
 - 169.254.0.0/16 (covers the AWS/GCP/Azure/DO metadata IP 169.254.169.254),
   fd00:ec2::254 (AWS IMDSv6), fe80::/10.
 - 0.0.0.0/8, :: (unspecified), and anything not global unicast.

 Implementation note: normalise with net.IP.To4() before any range test, or
 ::ffff:169.254.169.254 walks straight through an IPv4-only check. In
 practice the cleanest form is a positive test — reject unless the address is
 global unicast and not private/loopback/link-local/unspecified — rather than
 an enumeration of CIDRs.

 Profiles that genuinely need the LAN get an explicit, separately-named
 escape hatch (network_allow_private = true) rather than being able to smuggle
 10.x into network_allow. Deferred to a fast-follow; noted here so the
 always-deny list is not quietly weakened later to accommodate that use case.

 Robustness (LANDED — all missing from the first draft, all cheap):

 - Timeouts on: the CONNECT request read, DNS resolution, the upstream dial,
   and an idle timeout on the established tunnel. Without the last one a
   hung tunnel is never reclaimed.
 - A cap on concurrent connections. The client is an untrusted agent; an
   unbounded goroutine-per-connection loop lets it exhaust the file
   descriptors of the host inner process.
 - Strip hop-by-hop headers (Proxy-Authorization, Proxy-Connection,
   Connection-listed headers) before forwarding.
 - The proxy's own listen address is in the always-deny set by construction
   (it is on loopback), so CONNECT to the proxy itself cannot loop.

 Denied/blocked attempts are logged to stderr with the reason
 (inner: network-allowlist: blocked CONNECT to %s (not in allow list)) —
 audit-log integration is out of scope (that's NONO_COMPARISON.md §S4).

 Testability vs. the always-deny list — decide this BEFORE writing the proxy,
 because it shapes the struct. Every test target reachable from a developer
 machine or from CI is loopback or RFC1918, i.e. exactly what L0 denies. There
 is no "non-private address the always-deny list permits" to bind a test server
 to. So the Proxy struct needs one seam:

   // AllowPrivateDestinations disables the L0 address checks. It exists so the
   // test suite can point the proxy at a loopback listener, and is NOT
   // reachable from profile config — no TOML key sets it. Production code
   // constructs Proxy without it.
   AllowPrivateDestinations bool

 A struct field, never a TOML key and never an env var: an env var would let a
 compromised profile or a wrapper script turn the always-deny list off, which
 is the one thing L0 exists to prevent. The e2e harness builds the proxy
 through the same code path as a run but flips this field; a unit test asserts
 that no config surface can set it.

 With that seam in place the proxy is fully unit-testable with no bwrap
 dependency: httptest-style loopback listeners as CONNECT targets and a fake
 resolver for the rebinding-protection tests. The always-deny tests themselves
 leave the field false, so the production default stays honest.

 The relay (cmd/inner/cmd_net_relay.go, hidden subcommand) — LANDED

 inner __net-relay --listen 127.0.0.1:10108 --unix /tmp/inner/net-proxy.sock -- <real cmd> <real args...> — a cobra.Command{Hidden: true} registered in
 root.go like the other subcommands, with SetInterspersed(false) so an
 entrypoint argument that looks like one of the relay's own flags, or a second
 "--" inside the entrypoint's arguments, reaches the child untouched (both
 shapes occur in real profiles and both have a test).

 The addresses are three constants in internal/netproxy — RelayListenAddr,
 SandboxSocketPath, ProxyURL() — read by both the relay and the isolator,
 because a mismatch between any two of them is a sandbox with no working
 network and no error message explaining it.
 Confirmed empirically: the sandboxed entrypoint runs as PID 2 (bwrap itself
 is PID 1 / reaper — it does not pass --as-pid-1, per the invariant comment in
 bwrap.go), so the relay is a perfectly ordinary child process, not an init —
 no special PID-1 signal semantics to worry about, and double-forked
 grandchildren are still reaped by bwrap.

 Logic:
 1. net.Listen("tcp", listenAddr); accept loop in a goroutine, each
    connection dials the unix socket and pipes bytes both ways (best-effort,
    one bad connection never blocks others).
 2. Starts the real entrypoint as a child (exec.Command, inherited
    stdin/stdout/stderr — this is what keeps raw-mode/TUI passthrough working,
    the same way env/nice/time wrapping a process doesn't touch termios).
 3. Signal handling — NOT a blanket forward. The relay and the child share
    the foreground process group, so the tty driver already delivers
    SIGINT/SIGQUIT/SIGTSTP/SIGWINCH to BOTH. Forwarding those would deliver a
    second copy to the child, which breaks a TUI with "press Ctrl-C twice to
    exit" semantics (claude) — exactly the regression the manual checklist
    below is supposed to catch, so do not build it in on purpose. Forward
    only the signals the tty does not broadcast: SIGTERM and SIGHUP. SIGURG
    is used by the Go runtime for async preemption and must never be
    forwarded.
 4. Waits for the child and exits transparently: its exit code when it exited
    normally, 128+signum when it died from a signal, and 127 when the entrypoint
    could not be started at all (which is almost always "not in the sandbox's
    PATH", and deserves to be recognisable as that).

    Resolved: no reset-and-re-raise. The only observable difference is to a
    parent that inspects WaitStatus.Signaled(), and bwrap's reaper already
    collapses that into an exit status before inner's launcher sees it.

 This is the one part of the design that needs a human, same as issue #9: a
 relay hop sits between bwrap and the real TUI now. The mechanics
 (stdio inheritance, signal forwarding) are the standard, well-established way
 to wrap a process without breaking its terminal — but Ctrl-C/resize on a
 real claude/gemini TUI under network_mode = "allowlist" should be
 manually re-checked the same way issue #9 was, since that's exactly the kind
 of thing that only a real terminal proves.

 Wiring (cmd/inner/cmd_run.go)

 New step applyNetworkProxy(rc) (cleanup func(), err error), inserted right
 after applyGenericSafeMounts (existing step 11c) and before
 iso.Build (step 12), defer-cleaned up exactly like applyCapabilities/
 applyGenericSafeMounts already are (so it also runs, harmlessly, during
 --dry-run — consistent with how capability copying already behaves today;
 no dry-run special-casing needed).

 When rc.NetworkMode == config.NetworkAllowlist:
 1. Resolve innerBin, _ := os.Executable() — same call cmd_verify.go
    already makes for its --inside re-invocation.
 2. If rc.HomeIsolated(), append innerBin to rc.HomeAllow when it's
    under $HOME — the exact same six-line check cmd_verify.go already has
    for its own innerBin; factor it into a small shared helper
    (appendHomeAllowIfHidden) both call, instead of duplicating it. LANDED —
    cmd/inner/sandbox_home.go; it is idempotent, so the proxy wiring can append
    without caring what verify already added.
 3. os.MkdirTemp("", "inner-net-*"), socket at <dir>/proxy.sock; start
    netproxy.Proxy{Allow: rc.NetworkAllow, ...} on a unix listener there; set
    rc.NetProxySocketPath. A failure here is FATAL, not best-effort: starting
    the sandbox with HTTP_PROXY pointing at a socket nobody is listening on
    produces an opaque, hard-to-diagnose failure inside the agent.
 4. Rewrite rc.Entrypoint: Cmd = innerBin,
    Args = ["__net-relay", "--listen", relayAddr, "--unix", sandboxSocketPath, "--", <original Cmd>, <original Args...>]. Must run
    after prepareInteractiveShell/PS1/cursor-fix/TUI-COLUMNS steps (steps 5–5f,
    already true given the insertion point) so those inspect the real
    entrypoint binary and not the relay.
 5. Cleanup closes the listener and removes the temp dir.

 Note on the socket's host-side permissions — and a deliberate divergence from
 the tracked issue. ISS-05 (and nono, which it comes from) require a per-run
 session token "so other localhost processes can't ride the proxy". That
 requirement was written for a design where the proxy listens on a TCP port on
 the host: there, any local process can reach it, and a token is the only
 thing standing in the way.

 This design does not listen on a host TCP port. The proxy is a unix socket
 inside an 0700 MkdirTemp directory owned by the user, so the set of processes
 that can reach it is exactly "processes running as this UID" — the same set
 that can already read the profile, the shim dir and the sanitized gitconfig,
 and that could simply run `inner` itself. A token would authenticate that set
 to itself and protect nothing.

 So: no session token, deliberately. This must be written down in the issue and
 in internals.md, not left as a silently dropped requirement — and it must be
 revisited immediately if the proxy ever grows a TCP listener on the host side.

 internal/isolator/bwrap.go: the existing if !cfg.Network { --unshare-net } becomes a switch on cfg.NetworkMode (off/allowlist → --unshare-net;
 full → unchanged passthrough). A new section placed alongside the
 existing Git-config/containers.conf injection blocks (same
 --dir /tmp/inner idiom, right before ## Entrypoint) does, only for
 allowlist mode with NetProxySocketPath set: --ro-bind <NetProxySocketPath> /tmp/inner/net-proxy.sock, then --setenv for
 HTTP_PROXY/HTTPS_PROXY/http_proxy/https_proxy (all four, matching how
 http.ProxyFromEnvironment and most non-Go tools check them) to
 http://127.0.0.1:10108, NO_PROXY/no_proxy forced empty, and
 NODE_USE_ENV_PROXY=1 (Node 26+ requires this for fetch()/undici to honor the
 proxy env vars — flagged in NONO_COMPARISON.md).

 On the forced-empty NO_PROXY: with --clearenv (the default) there is nothing
 to inherit, so the only real cases are [env] inherit_all = true and a
 NO_PROXY set in [env] set. The latter is emitted earlier by the sorted
 setKeys loop, so this block wins by position — the same mechanism the
 containers.conf block already documents. Say it that way; "an inherited
 value can't punch a bypass" describes a case that clearenv already prevents.

 Interaction with remote (untrusted) profiles

 A downloaded profile configures the whole sandbox, and issue #2 built a
 hardening pass plus a blocking consent prompt around exactly that. The
 allowlist is a new policy surface a remote profile gets to set, so it has to
 pass through both.

 remoteProfileRequests (cmd/inner/remote_profile.go) prints one line per thing
 the user is being asked to accept. It said "network: enabled — the sandboxed
 process can reach the internet" for anything that was not off, which under
 allowlist would be both wrong and — worse — LESS alarming than the truth,
 while hiding the destination list entirely. That line is now named by mode
 (LANDED); the allowlist branch must additionally render the effective allow
 list with its provenance, so a remote profile cannot smuggle a domain past
 the prompt by inheriting it from a capability the user did not read about.

 hardenRemoteProfile is the open decision, and it must be made before the
 config surface ships. Three options, in increasing strictness:

   a) accept the remote allow list as-is (the prompt shows it, the user
      decides) — consistent with how the prompt treats the entrypoint;
   b) accept it but strip entries a capability did not contribute, so a remote
      profile can only narrow;
   c) drop it entirely and force network_mode = "off" for remote profiles.

 (a) is the most consistent with the existing gate and the least surprising;
 (c) is the most defensible if we think users click through prompts. Whatever
 is chosen, hardenRemoteProfile must say something about the network today —
 right now it says nothing at all, which means option (a) is in force by
 accident rather than by decision.

 Interaction with the claude capability (D-Bus / keyring)

 This is a functional regression the design must handle explicitly, not a
 detail. cmd/inner/sandbox_claude.go binds the session bus socket back into
 the sandbox when DBUS_SESSION_BUS_ADDRESS is the unix:path=... form, and its
 own comment says of the other form:

   "Abstract-socket addresses (unix:abstract=...) need no filesystem bind:
    the claude profiles keep the host network namespace (network = true), so
    those stay reachable via the env var."

 Abstract unix sockets are namespaced BY THE NETWORK NAMESPACE. The moment
 network_mode = "allowlist" adds --unshare-net, an abstract-socket session bus
 becomes unreachable: libsecret can no longer find the keyring daemon, the
 mid-session OAuth refresh fails, and the session 401s — on exactly the
 profile this feature exists to migrate first.

 So: applyClaude detects the abstract form and, when the resolved network mode
 is allowlist or off, prints a clear warning naming the consequence (no
 mid-session token refresh; relaunch after renewal) instead of failing
 mysteriously later. The comment in sandbox_claude.go must be corrected at the
 same time — it currently states a guarantee that stops being true.

 Validator / verify / docs

 - internal/profile/validator.go (partly LANDED — validateNetwork exists and
   the two warnings below are already gated):
   - reject an unknown network_mode (mirrors the unknown-home-mode error), and
     reject "allowlist" while this build cannot enforce it: a reserved-but-
     unimplemented mode must never degrade into "off" (a broken agent) or
     "full" (a false sense of protection). Removing that second rejection is
     the switch that turns the feature on;
   - warn when network_mode = "allowlist" resolves to an EMPTY effective
     allowlist — i.e. empty network_allow AND no contributing capability. With
     a contributing capability an empty network_allow is the normal, desirable
     case and must not warn;
   - warn when a capability is declared together with network_mode = "off"
     (the tool cannot work);
   - warn when a network_deny entry matches nothing in the effective union
     (typo);
   - the existing "network = true + credential allow keys = exfiltration
     setup" and "nested-user-ns + network" warnings only make sense for
     full — gate them on ResolveNetworkMode(p.Sandbox) == config.NetworkFull
     instead of the bare bool, since allowlist mode is specifically what
     removes that risk.
 - internal/sandbox/checker.go (LANDED): checkNetworkPolicy already does exactly
   the right probe for this — dial 8.8.8.8:53 directly and expect it to
   fail. Today it's skipped whenever NetworkEnabled; change the skip
   condition to NetworkMode == full only, so off and allowlist both
   get the existing check (their kernel guarantee — no direct socket
   escape — is identical; the check already generalizes without touching its
   body). Checker gains NetworkMode string/NetworkAllow []string for
   reporting context; cmd_verify.go passes them through the existing
   INNER_VERIFY_* env-var channel (INNER_VERIFY_NETWORK_MODE,
   INNER_VERIFY_NETWORK_ALLOW), same pattern as INNER_VERIFY_HOME_MODE.
   INNER_VERIFY_NETWORK_MODE takes precedence over the legacy boolean
   INNER_VERIFY_NETWORK whenever it is present; the profile fallback in
   runVerifyInside goes through ResolveNetworkMode, not p.Sandbox.Network,
   or an allowlist profile would report an open network and SKIP the probe.
   The Checker field is now NetworkMode (NetworkEnabled is gone); an empty
   value means "unknown" and the probe runs, which is the safe default.
 - Verify and run share prepareSandbox (cmd/inner/sandbox_prepare.go, LANDED),
   so the sandbox the checks are judged against is the one a run actually gets,
   and the places the two commands differ are named fields on sandboxOptions
   with the reason recorded on each.

   RESOLVED: verify DOES get the socket and the relay. The objection that keeps
   capability handlers out of it does not apply — the proxy has no side effects
   outside the run (a listener on a unix socket in a private temp directory,
   removed when the run ends), and a conformance check that certifies a
   materially different sandbox than the user runs is not a weaker check, it is
   false assurance. This required making the entrypoint final BEFORE
   prepareSandbox in runVerifyOutside: the proxy step wraps the entrypoint, and
   verify used to replace it afterwards, which would have thrown the relay away
   and left a socket nothing routed to.

   The one difference that is a real decision rather than an omission:
   capability handlers do NOT run under verify. They are not pure mount
   injection — the claude handler can launch `claude -p /try-login` to trigger
   the OS keyring's graphical unlock dialog and then wait for Enter, and a
   read-only conformance check must not have side effects on the user's
   credential store. The cost is that verify cannot judge the capability
   mounts; closing that means splitting each handler's mount injection from its
   pre-flight actions, which is its own change.

   The network-policy check still proves only the floor (no direct socket
   escape), which is the right scope for it: asserting that a specific allowed
   domain is actually reachable is environment dependent and not what a static
   conformance check should assert. The difference is that it now proves that
   floor about the sandbox the user really gets.
 - cmd_run.go printDryRun: extend the network: %v line to show the mode and,
   for allowlist, the effective allow list WITH its provenance
   (api.anthropic.com [capability:claude], github.com [profile]) — without
   starting anything.
 - Docs: docs/content/profiles.md gets a new ### network — allowlist mode {#network-allowlist-mode} section right after the existing home
   section, same structure/tone: what it does, the layering table, the
   always-deny list, the DNS-rebinding note, a migration example, and the
   two things that predictably break — anything that is not HTTP(S)
   (git+ssh, database drivers, custom registries) fails closed, and tools
   that do a DNS preflight fail before ever trying the proxy, because
   /etc/resolv.conf exists inside the sandbox but resolves nothing.
   docs/content/internals.md extends the existing ### Network: --unshare-net
   section with the relay design and the host-netns threat-model note
   (mirrors how the ### trust boundary section was added for issue #2).
   docs/content/commands.md gets the new dry-run output line.
   NONO_COMPARISON.md §S2 marked IMPLEMENTED once shipped, same
   convention as S1/S5, with the plain-HTTP-forwarding/IP-literal support
   noted as going slightly beyond the original proposal and CLI
   one-off-flag/audit-log-integration noted as the deliberately-deferred rest.

 Explicit v1 scope cuts (documented, not silently dropped)

 - No --network-allow CLI flag (L4 above is profile-only for v1; cheap
   fast-follow).
 - No network_allow_private escape hatch for LAN access (fast-follow; the
   always-deny list must not be weakened to substitute for it).
 - No TLS interception / method+path endpoint filtering (that's §S3,
   credential injection — a separate, larger feature). Consequence to state
   plainly in the docs: an allowed domain is allowed for BOTH directions. A
   profile that allows github.com can still push a secret to a gist. The
   allowlist shrinks the set of destinations, it does not stop exfiltration
   to an allowed one.
 - No structured audit log of proxy decisions (that's §S4); stderr only.
 - No Windows/macOS concern (inner is Linux-only already).

 Testing

 - internal/netproxy/proxy_test.go: allow/deny matching as a table over the
   full pattern semantics above (exact, apex-vs-wildcard, depth, case,
   trailing dot, punycode, bare-host port defaulting, host:port); the
   hostname gate rejecting BEFORE the resolver is consulted (assert the fake
   resolver was never called — this is the DNS-exfiltration regression test);
   always-deny for metadata/loopback/private/link-local IPs even when the
   hostname itself matches Allow, including the ::ffff: IPv4-mapped form;
   network_deny subtracting a capability-contributed domain; CONNECT tunnel
   success and 403-on-deny; plain-HTTP forwarding not following a redirect to
   a denied host; DNS-rebinding protection via an injected fake resolver
   returning a denied IP for an otherwise-allowed name; the concurrency cap
   and the idle timeout.
 - internal/config/network_test.go (LANDED for the mode half): ResolveNetworkMode
   and EffectiveNetworkMode fallbacks, the network/network_mode precedence rules
   across extends including the decode-time normalisation, and the loader
   refusing an unknown mode. Still to add: network_allow/network_deny merge
   behaviour and the capability-defaults union with its provenance map.
 - internal/profile/validator_network_test.go (LANDED for the mode half):
   unknown mode, allowlist-is-reserved, and the two softened warnings firing
   only for full. Still to add: empty-effective-allow warning AND its absence
   when a capability contributes, capability + mode "off", unmatched
   network_deny.
 - internal/isolator/bwrap_test.go: the fail-closed table (full → no
   --unshare-net; off, allowlist and an unrecognised value → --unshare-net) is
   LANDED. Still to add: that allowlist mode emits the socket bind and all the
   --setenvs, in the right order relative to --clearenv.
 - cmd/inner: a wiring test using the existing capturingIsolator pattern
   (already used in remote_profile_test.go) to assert applyNetworkProxy
   correctly rewrites rc.Entrypoint/sets rc.NetProxySocketPath/appends to
   rc.HomeAllow under an isolated home — without needing real bwrap. The
   --network / --no-network flags moving NetworkMode, and appendHomeAllowIfHidden
   itself (scoping to $HOME, no-op under host-ro, idempotence), are LANDED.
 - One real end-to-end test, in .sdlc/e2e (new section, same style as
   the existing "Planted secrets: read-side policy"): run a minimal
   network_mode = "allowlist" profile against two host-side loopback test
   servers (an "allowed" one, a "denied" one), with the proxy built in the
   AllowPrivateDestinations test mode described above — every address a dev
   box or CI runner can bind is otherwise denied by L0, so without that seam
   this test cannot exist. Assert: the allowed server is reachable through the
   proxy, the denied one is rejected, and a raw direct-connect attempt
   bypassing the proxy fails outright. This is the only thing that proves the
   whole namespace+socket+relay+proxy chain actually composes under a real
   bwrap, matching the rigor issue #1's e2e section already applies.
   The L0 address checks themselves are NOT exercised here — they are unit
   tests, because the mode that makes this e2e possible is the mode that turns
   them off.
 - Manual: re-run the interactive-TUI checklist from issue #9
   (SECURITY_REVIEW.md §9) once more, this time with a profile in
   network_mode = "allowlist", specifically for Ctrl-C (single vs double
   press, given the deliberate non-forwarding of tty signals) and resize
   through the added relay hop. The baseline exists: §9 was signed off on
   2026-08-25 without a relay in the chain, so a difference observed here is
   attributable to the relay rather than to --unshare-pid.
