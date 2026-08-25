package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/netproxy"
)

func TestApplyNetworkProxy_isANoOpOutsideAllowlistMode(t *testing.T) {
	for _, mode := range []string{config.NetworkOff, config.NetworkFull} {
		rc := &config.RunConfig{
			NetworkMode: mode,
			Entrypoint:  config.Entrypoint{Cmd: "claude", Args: []string{"--verbose"}},
		}
		cleanup, err := applyNetworkProxy(rc)
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		cleanup()

		if rc.NetProxySocketPath != "" {
			t.Errorf("mode %q started a proxy", mode)
		}
		if rc.Entrypoint.Cmd != "claude" {
			t.Errorf("mode %q rewrote the entrypoint to %q", mode, rc.Entrypoint.Cmd)
		}
	}
}

func TestApplyNetworkProxy_startsAListeningSocketAndWrapsTheEntrypoint(t *testing.T) {
	rc := &config.RunConfig{
		NetworkMode:  config.NetworkAllowlist,
		NetworkAllow: []string{"api.anthropic.com"},
		Entrypoint:   config.Entrypoint{Cmd: "claude", Args: []string{"--verbose", "--"}},
	}

	cleanup, err := applyNetworkProxy(rc)
	if err != nil {
		t.Fatalf("applyNetworkProxy: %v", err)
	}
	defer cleanup()

	if rc.NetProxySocketPath == "" {
		t.Fatal("no socket path was recorded")
	}
	// The socket must be live before the sandbox starts: a child that made its
	// first request against a dead socket would fail in a way that reads as
	// "the allow list is wrong".
	conn, err := net.Dial("unix", rc.NetProxySocketPath)
	if err != nil {
		t.Fatalf("nothing is listening on the proxy socket: %v", err)
	}
	conn.Close()

	// The socket lives in its own 0700 directory: the processes that can reach
	// the proxy are then exactly the ones that could run inner anyway, which is
	// the argument for having no session token.
	info, err := os.Stat(filepath.Dir(rc.NetProxySocketPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode = %o, want 0700", perm)
	}

	// The entrypoint is now the relay, with the real command behind "--" and
	// its own arguments untouched — including one that is itself "--".
	self, _ := os.Executable()
	if rc.Entrypoint.Cmd != self {
		t.Errorf("entrypoint Cmd = %q, want the inner binary %q", rc.Entrypoint.Cmd, self)
	}
	want := []string{
		"__net-relay",
		"--listen", netproxy.RelayListenAddr,
		"--unix", netproxy.SandboxSocketPath,
		"--", "claude", "--verbose", "--",
	}
	if strings.Join(rc.Entrypoint.Args, " ") != strings.Join(want, " ") {
		t.Errorf("entrypoint args =\n %v\nwant\n %v", rc.Entrypoint.Args, want)
	}
}

func TestApplyNetworkProxy_cleanupRemovesTheSocket(t *testing.T) {
	rc := &config.RunConfig{
		NetworkMode: config.NetworkAllowlist,
		Entrypoint:  config.Entrypoint{Cmd: "sh"},
	}
	cleanup, err := applyNetworkProxy(rc)
	if err != nil {
		t.Fatalf("applyNetworkProxy: %v", err)
	}
	dir := filepath.Dir(rc.NetProxySocketPath)
	cleanup()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the socket directory survived cleanup: %v", err)
	}
}

// An empty Cmd means "$SHELL" to the isolator. Left empty it would become an
// empty argument to the relay instead of a shell.
func TestApplyNetworkProxy_resolvesAnEmptyEntrypointLikeTheIsolatorDoes(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	rc := &config.RunConfig{NetworkMode: config.NetworkAllowlist}

	cleanup, err := applyNetworkProxy(rc)
	if err != nil {
		t.Fatalf("applyNetworkProxy: %v", err)
	}
	defer cleanup()

	if got := rc.Entrypoint.Args[len(rc.Entrypoint.Args)-1]; got != "/bin/zsh" {
		t.Errorf("relay was handed %q as the command, want the resolved $SHELL", got)
	}
}

// verify certifies the sandbox a run gets, so it must build the same one. The
// objection that keeps capability handlers out of verify does not apply: the
// proxy touches nothing outside the run.
func TestPrepareSandbox_verifyAlsoGetsTheNetworkProxy(t *testing.T) {
	rc := &config.RunConfig{
		NetworkMode:  config.NetworkAllowlist,
		NetworkAllow: []string{"api.anthropic.com"},
		Entrypoint:   config.Entrypoint{Cmd: "inner", Args: []string{"verify", "--inside"}},
	}

	cleanup, err := prepareSandbox(rc, verifySandboxOptions())
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	defer cleanup()

	if rc.NetProxySocketPath == "" {
		t.Error("verify built a sandbox with no proxy: its checks would certify something the user never runs")
	}
	if len(rc.Entrypoint.Args) == 0 || rc.Entrypoint.Args[0] != "__net-relay" {
		t.Errorf("verify's entrypoint was not wrapped in the relay: %v", rc.Entrypoint.Args)
	}
}
