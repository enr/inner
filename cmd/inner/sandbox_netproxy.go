package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/netproxy"
)

// applyNetworkProxy starts the host-side allowlist proxy for this run and
// rewrites the entrypoint to go through the in-sandbox relay.
//
// A no-op for every mode but "allowlist".
//
// It runs for `inner verify` as well as `inner run`, unlike the capability
// handlers. The reason those are excluded does not apply here: the proxy has no
// side effects outside the run — it is a listener on a unix socket in a
// private temp directory, removed when the run ends, touching nothing of the
// user's. And verify exists to certify the sandbox a run gets, so building it a
// materially different one (no socket, no relay, a different entrypoint shape)
// would mean certifying something the user never runs. A conformance check that
// passes on a sandbox nobody uses is worse than no check: it is false assurance.
//
// This requires the entrypoint to be FINAL before prepareSandbox runs, since
// the rewrite wraps whatever is there. Both callers now satisfy that.
func applyNetworkProxy(rc *config.RunConfig) (func(), error) {
	noop := func() {}
	if rc.EffectiveNetworkMode() != config.NetworkAllowlist {
		return noop, nil
	}

	// The relay is inner itself, re-invoked inside the sandbox.
	innerBin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("network proxy: cannot determine the inner binary path: %w", err)
	}
	// Under an isolated home the empty tmpfs would erase the binary we are
	// about to make the entrypoint. Idempotent, so it does not matter whether
	// verify already added it.
	appendHomeAllowIfHidden(rc, innerBin)

	// The socket lives in its own 0700 directory rather than a shared one: the
	// set of processes that can reach the proxy is then exactly the set that
	// could run inner anyway. See network.md on why there is no session token.
	dir, err := os.MkdirTemp("", "inner-net-*")
	if err != nil {
		return nil, fmt.Errorf("network proxy: creating socket directory: %w", err)
	}
	sockPath := filepath.Join(dir, "proxy.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		os.RemoveAll(dir)
		// Fatal, not best-effort. Starting the sandbox anyway would point
		// HTTP_PROXY at a socket nobody is listening on, and every request
		// inside would fail with a message that says nothing about the cause.
		return nil, fmt.Errorf("network proxy: listening on %s: %w", sockPath, err)
	}

	denyLog := &netproxy.DenyLog{W: os.Stderr, Deferred: deferDenyLog(rc)}
	proxy := &netproxy.Proxy{
		Policy: netproxy.Policy{
			Allow: rc.NetworkAllow,
			Deny:  rc.NetworkDeny,
		},
		Log: denyLog,
	}
	go func() { _ = proxy.Serve(listener) }()

	rc.NetProxySocketPath = sockPath
	rewriteEntrypointThroughRelay(rc, innerBin)

	return func() {
		// Close first, then flush: the listener is what produces new lines, so
		// closing it is what makes the summary below the whole story.
		_ = listener.Close()
		denyLog.Flush()
		_ = os.RemoveAll(dir)
	}, nil
}

// deferDenyLog reports whether refusals must be held until the run is over
// instead of printed as they happen.
//
// Both conditions are needed, and each one alone is the wrong answer:
//
//   - The entrypoint is a full-screen TUI. It positions the cursor and redraws
//     regions on its own terms; a line we write mid-frame is painted into its
//     output and stays there. A line-oriented entrypoint has no such problem —
//     the refusal simply scrolls past, where it is most useful, immediately.
//   - Our stderr is that same terminal. With stderr redirected to a file or a
//     pipe there is nothing to corrupt, and deferring would only delay the
//     diagnosis the user redirected the stream to collect.
//
// An interactive shell that later launches a TUI child is deliberately NOT
// covered: the entrypoint we can see is the shell, the user is at a prompt for
// most of the session, and holding every refusal until the shell exits would
// make the proxy silent exactly when someone is poking at it by hand.
func deferDenyLog(rc *config.RunConfig) bool {
	return rc.Entrypoint.TUI && term.IsTerminal(int(os.Stderr.Fd()))
}

// rewriteEntrypointThroughRelay wraps the real entrypoint in `inner __net-relay`.
//
// It must run after every other step that inspects or mutates the entrypoint
// (the $SHELL fallback, the interactive-bash --init-file injection, the TUI
// column setup), or those would examine the relay instead of the program the
// profile actually names.
func rewriteEntrypointThroughRelay(rc *config.RunConfig, innerBin string) {
	cmd := rc.Entrypoint.Cmd
	if cmd == "" {
		// Mirrors the isolator's own fallback. Resolving it here rather than
		// leaving it empty matters: an empty Cmd would become an empty argument
		// to the relay instead of a shell.
		cmd = os.Getenv("SHELL")
		if cmd == "" {
			cmd = "/bin/sh"
		}
	}

	args := []string{
		"__net-relay",
		"--listen", netproxy.RelayListenAddr,
		"--unix", netproxy.SandboxSocketPath,
		"--", cmd,
	}
	rc.Entrypoint.Args = append(args, rc.Entrypoint.Args...)
	rc.Entrypoint.Cmd = innerBin
}
