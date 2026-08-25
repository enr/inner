package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/enr/inner/internal/netproxy"
)

// freeLoopbackAddr returns a loopback address that is free right now.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// ── Signal policy ─────────────────────────────────────────────────────────────

// The relay and the child share the foreground process group, so the tty
// already delivers these to both. Forwarding them would give the child a second
// copy of every Ctrl-C, and a TUI whose quit gesture is "press Ctrl-C twice"
// would see one keypress as two.
func TestForwardedSignals_excludeEverythingTheTtyBroadcasts(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP, syscall.SIGWINCH} {
		if slices.Contains(forwardedSignals, sig) {
			t.Errorf("%v is delivered to the whole foreground process group; forwarding it double-delivers to the child", sig)
		}
	}
	// SIGURG is the Go runtime's asynchronous preemption signal: forwarding it
	// would be constant meaningless noise.
	if slices.Contains(forwardedSignals, os.Signal(syscall.SIGURG)) {
		t.Error("SIGURG is a Go runtime internal and must never be forwarded")
	}
}

// What the tty does NOT broadcast still has to reach the child, or it would
// outlive the wrapper it was told to stop.
func TestForwardedSignals_includeTheOnesNobodyElseDelivers(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(forwardedSignals, sig) {
			t.Errorf("%v must be forwarded: nothing else delivers it to the child", sig)
		}
	}
}

// ── Exit status ───────────────────────────────────────────────────────────────

func TestRunNetRelay_propagatesTheChildExitCode(t *testing.T) {
	var stderr bytes.Buffer
	code, err := runNetRelay(&stderr, netRelayOptions{
		ListenAddr: freeLoopbackAddr(t),
		UnixPath:   filepath.Join(t.TempDir(), "absent.sock"),
		Cmd:        "sh",
		Args:       []string{"-c", "exit 42"},
	})
	if err != nil {
		t.Fatalf("runNetRelay: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want the child's 42", code)
	}
}

func TestRunNetRelay_successIsZero(t *testing.T) {
	var stderr bytes.Buffer
	code, err := runNetRelay(&stderr, netRelayOptions{
		ListenAddr: freeLoopbackAddr(t),
		UnixPath:   filepath.Join(t.TempDir(), "absent.sock"),
		Cmd:        "true",
	})
	if err != nil {
		t.Fatalf("runNetRelay: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// A child killed by a signal reports 128+signum, the shell convention, rather
// than a bare 1 that loses which signal it was.
func TestRunNetRelay_signalledChildReports128PlusSignum(t *testing.T) {
	var stderr bytes.Buffer
	code, err := runNetRelay(&stderr, netRelayOptions{
		ListenAddr: freeLoopbackAddr(t),
		UnixPath:   filepath.Join(t.TempDir(), "absent.sock"),
		Cmd:        "sh",
		Args:       []string{"-c", "kill -TERM $$"},
	})
	if err != nil {
		t.Fatalf("runNetRelay: %v", err)
	}
	if want := 128 + int(syscall.SIGTERM); code != want {
		t.Errorf("exit code = %d, want %d for a SIGTERM death", code, want)
	}
}

func TestRunNetRelay_missingEntrypointIs127(t *testing.T) {
	var stderr bytes.Buffer
	code, err := runNetRelay(&stderr, netRelayOptions{
		ListenAddr: freeLoopbackAddr(t),
		UnixPath:   filepath.Join(t.TempDir(), "absent.sock"),
		Cmd:        "definitely-not-a-real-binary-xyz",
	})
	if err != nil {
		t.Fatalf("runNetRelay: %v", err)
	}
	if code != 127 {
		t.Errorf("exit code = %d, want 127 (command not found)", code)
	}
	if !strings.Contains(stderr.String(), "cannot start") {
		t.Errorf("expected a message naming the failure, got %q", stderr.String())
	}
}

// Fatal, not best-effort: starting the entrypoint anyway would leave HTTP_PROXY
// pointing at nothing, and every request would fail with a message that says
// nothing about the real cause.
func TestRunNetRelay_unusableListenAddressIsFatalBeforeTheChildStarts(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-ran")
	var stderr bytes.Buffer

	_, err := runNetRelay(&stderr, netRelayOptions{
		ListenAddr: "127.0.0.1:1", // privileged port: bind must fail as a normal user
		UnixPath:   filepath.Join(t.TempDir(), "absent.sock"),
		Cmd:        "touch",
		Args:       []string{marker},
	})
	if err == nil {
		t.Fatal("expected a fatal error when the relay cannot listen")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the child was started despite the relay having no listener")
	}
}

// ── Forwarding ────────────────────────────────────────────────────────────────

// The relay is a dumb pipe: bytes in on TCP, out on the unix socket, and back.
func TestRelayConn_pipesBothDirections(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "proxy.sock")
	ul, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()

	go func() {
		c, err := ul.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		fmt.Fprintf(c, "got:%s", buf)
	}()

	client, server := net.Pipe()
	defer client.Close()
	go relayConn(io.Discard, server, sockPath)

	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("got:hello"))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("reading the reply back through the relay: %v", err)
	}
	if string(buf) != "got:hello" {
		t.Errorf("got %q, want the echoed reply", buf)
	}
}

// An unreachable socket must drop that one connection, not the relay.
func TestRelayConn_unreachableSocketClosesOnlyThatConnection(t *testing.T) {
	var stderr bytes.Buffer
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		relayConn(&stderr, server, filepath.Join(t.TempDir(), "absent.sock"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relayConn hung on an unreachable socket")
	}
	if !strings.Contains(stderr.String(), "cannot reach the proxy socket") {
		t.Errorf("expected a message naming the unreachable socket, got %q", stderr.String())
	}
}

// ── The chain the sandbox actually uses ───────────────────────────────────────

// relay → unix socket → proxy → upstream, with the real netproxy on the other
// end. This is everything except bwrap: the namespace and the bind mount are
// what the e2e test covers, and they cannot be exercised from a unit test.
func TestRelay_throughTheRealProxyToAnAllowedUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "reached the upstream")
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Host side: the proxy on a unix socket, as inner would start it.
	sockPath := filepath.Join(t.TempDir(), "proxy.sock")
	ul, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()

	proxy := &netproxy.Proxy{
		Policy: netproxy.Policy{
			Allow: []string{"127.0.0.1:" + u.Port()},
			// Every address a test machine can bind is loopback, which the
			// always-deny list rejects; see Policy.AllowPrivateDestinations.
			AllowPrivateDestinations: true,
		},
		Log: io.Discard,
	}
	go func() { _ = proxy.Serve(ul) }()

	// Sandbox side: the relay's forwarder on a loopback port.
	relayAddr := freeLoopbackAddr(t)
	rl, err := net.Listen("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()
	go func() {
		for {
			conn, err := rl.Accept()
			if err != nil {
				return
			}
			go relayConn(io.Discard, conn, sockPath)
		}
	}()

	// A client configured exactly as the sandbox would be, via HTTP_PROXY.
	proxyURL, err := url.Parse("http://" + relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("request through relay+proxy failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "reached the upstream" {
		t.Errorf("body = %q, want the upstream response", body)
	}

	// And the same chain refuses a destination outside the allow list.
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a denied upstream was reached through the proxy")
	}))
	defer denied.Close()

	resp2, err := client.Get(denied.URL)
	if err != nil {
		t.Fatalf("expected a 403 response, got a transport error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a destination outside the allow list", resp2.StatusCode)
	}
}

// The relay and the isolator live in different packages and must agree on where
// the socket and the port are, or the sandbox gets no network and no
// explanation. One source, asserted here.
func TestSandboxAddressesComeFromOneSource(t *testing.T) {
	if netproxy.ProxyURL() != "http://"+netproxy.RelayListenAddr {
		t.Errorf("ProxyURL() = %q, does not match RelayListenAddr %q", netproxy.ProxyURL(), netproxy.RelayListenAddr)
	}
	if !strings.HasPrefix(netproxy.SandboxSocketPath, "/tmp/inner/") {
		t.Errorf("SandboxSocketPath = %q, want it under /tmp/inner (already a tmpfs in the sandbox)", netproxy.SandboxSocketPath)
	}
}

// The relay sits between bwrap and the real entrypoint, so it must hand the
// entrypoint its arguments byte for byte. Two shapes are easy to break and both
// occur in real profiles: an argument that looks like one of the relay's own
// flags, and a second "--" inside the entrypoint's arguments.
func TestNetRelayCmd_passesChildArgumentsThroughUntouched(t *testing.T) {
	app, _ := newTestApp(t)
	marker := filepath.Join(t.TempDir(), "args")

	root := buildRootCmd(app)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"__net-relay",
		"--listen", freeLoopbackAddr(t),
		"--unix", filepath.Join(t.TempDir(), "absent.sock"),
		"--",
		"sh", "-c", `printf "[%s]" "$@" > ` + marker, "x",
		"--listen", "bogus", "--", "-v",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("__net-relay: %v", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the child did not run: %v", err)
	}
	const want = "[--listen][bogus][--][-v]"
	if string(got) != want {
		t.Errorf("child received %s, want %s — the relay must not consume the entrypoint's arguments", got, want)
	}
}
