package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enr/inner/internal/config"
)

// The test binary doubles as a fake xdg-dbus-proxy: TestMain hands control to
// fakeDBusProxyMain when fakeDBusProxyEnv is set. The value selects the
// behaviour; fakeDBusProxyArgsEnv, when set, receives the argv it was given.
const (
	fakeDBusProxyEnv     = "INNER_TEST_FAKE_DBUS_PROXY"
	fakeDBusProxyArgsEnv = "INNER_TEST_FAKE_DBUS_PROXY_ARGS"
)

func fakeDBusProxyMain() int {
	args := os.Args[1:]
	if p := os.Getenv(fakeDBusProxyArgsEnv); p != "" {
		os.WriteFile(p, []byte(strings.Join(args, "\n")), 0o600) //nolint:errcheck
	}
	switch os.Getenv(fakeDBusProxyEnv) {
	case "fail":
		fmt.Fprintln(os.Stderr, "fake proxy: cannot connect upstream")
		return 1
	case "hang":
		time.Sleep(time.Minute)
		return 0
	}
	// "ok": listen on the PATH argument (the one after the address), signal
	// readiness on fd 3, then serve until killed.
	var sock string
	for i, a := range args {
		if !strings.HasPrefix(a, "--") && i+1 < len(args) {
			sock = args[i+1]
			break
		}
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer ln.Close()
	os.NewFile(3, "ready").Write([]byte("x")) //nolint:errcheck
	time.Sleep(time.Minute)
	return 0
}

// useFakeDBusProxy makes the test binary the xdg-dbus-proxy for this test.
func useFakeDBusProxy(t *testing.T, mode string) (argsFile string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prev := dbusProxyLookPath
	dbusProxyLookPath = func(string) (string, error) { return exe, nil }
	t.Cleanup(func() { dbusProxyLookPath = prev })
	argsFile = filepath.Join(t.TempDir(), "args")
	t.Setenv(fakeDBusProxyEnv, mode)
	t.Setenv(fakeDBusProxyArgsEnv, argsFile)
	return argsFile
}

func TestDBusProxyArgs(t *testing.T) {
	got := dbusProxyArgs("unix:path=/run/user/1000/bus", "/tmp/x/bus")
	want := []string{"--fd=3", "unix:path=/run/user/1000/bus", "/tmp/x/bus", "--filter", "--talk=org.freedesktop.secrets"}
	if !slices.Equal(got, want) {
		t.Errorf("dbusProxyArgs = %q, want %q", got, want)
	}
}

// Filtered path: the sandbox gets the proxy socket at a fixed /tmp path and an
// address pointing at it, never the real bus.
func TestApplyClaudeSessionBus_filtered(t *testing.T) {
	argsFile := useFakeDBusProxy(t, "ok")
	realBus := filepath.Join(t.TempDir(), "bus")
	os.WriteFile(realBus, nil, 0o600) //nolint:errcheck
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+realBus)

	var warn bytes.Buffer
	rc := &config.RunConfig{}
	cleanup := applyClaudeSessionBus(&warn, rc)

	if warn.Len() > 0 {
		t.Errorf("unexpected warning: %s", warn.String())
	}
	if got := rc.Env.Set["DBUS_SESSION_BUS_ADDRESS"]; got != "unix:path="+sandboxDBusSocket {
		t.Errorf("DBUS_SESSION_BUS_ADDRESS = %q, want the proxy socket", got)
	}
	if slices.Contains(rc.Env.Inherit, "DBUS_SESSION_BUS_ADDRESS") {
		t.Error("the host address must not be inherited in filtered mode")
	}
	if len(rc.HideExempt) > 0 {
		t.Errorf("filtered mode must not exempt anything from hiding: %v", rc.HideExempt)
	}
	var proxySock string
	for _, m := range rc.Mounts {
		if m.Src == realBus || m.Dest == realBus {
			t.Errorf("real bus bound in filtered mode: %+v", m)
		}
		if m.Dest == sandboxDBusSocket {
			proxySock = m.Src
		}
	}
	if proxySock == "" {
		t.Fatalf("no mount of the proxy socket; mounts: %v", rc.Mounts)
	}
	if _, err := os.Stat(proxySock); err != nil {
		t.Errorf("proxy socket missing while the proxy runs: %v", err)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--talk=org.freedesktop.secrets") || !strings.Contains(string(args), "unix:path="+realBus) {
		t.Errorf("proxy argv = %q", args)
	}

	cleanup()
	if _, err := os.Stat(filepath.Dir(proxySock)); !os.IsNotExist(err) {
		t.Errorf("proxy dir left behind after cleanup: %v", err)
	}
	cleanup() // idempotent
}

// No proxy available: the pre-filter behaviour, plus a warning that names the
// risk and the two ways out.
func TestApplyClaudeSessionBus_fallbackWhenProxyMissing(t *testing.T) {
	realBus := filepath.Join(t.TempDir(), "bus")
	os.WriteFile(realBus, nil, 0o600) //nolint:errcheck
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+realBus)

	var warn bytes.Buffer
	rc := &config.RunConfig{}
	applyClaudeSessionBus(&warn, rc)()

	assertUnfilteredBus(t, rc, realBus)
	for _, want := range []string{"xdg-dbus-proxy", "systemd1", `allow = ["session-bus"]`} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("warning %q does not mention %q", warn.String(), want)
		}
	}
}

func TestApplyClaudeSessionBus_fallbackWhenProxyFails(t *testing.T) {
	useFakeDBusProxy(t, "fail")
	realBus := filepath.Join(t.TempDir(), "bus")
	os.WriteFile(realBus, nil, 0o600) //nolint:errcheck
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+realBus)

	var warn bytes.Buffer
	rc := &config.RunConfig{}
	applyClaudeSessionBus(&warn, rc)()

	assertUnfilteredBus(t, rc, realBus)
	if !strings.Contains(warn.String(), "cannot connect upstream") {
		t.Errorf("warning should quote the proxy's error: %q", warn.String())
	}
}

func TestApplyClaudeSessionBus_fallbackWhenProxyHangs(t *testing.T) {
	useFakeDBusProxy(t, "hang")
	prev := dbusProxyReadyTimeout
	dbusProxyReadyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { dbusProxyReadyTimeout = prev })
	realBus := filepath.Join(t.TempDir(), "bus")
	os.WriteFile(realBus, nil, 0o600) //nolint:errcheck
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+realBus)

	var warn bytes.Buffer
	rc := &config.RunConfig{}
	applyClaudeSessionBus(&warn, rc)()

	assertUnfilteredBus(t, rc, realBus)
	if !strings.Contains(warn.String(), "not ready") {
		t.Errorf("warning should say the proxy was not ready: %q", warn.String())
	}
}

// allow = ["session-bus"] is the explicit opt-in to the whole bus: no proxy,
// no warning.
func TestApplyClaudeSessionBus_allowSessionBus(t *testing.T) {
	useFakeDBusProxy(t, "ok")
	realBus := filepath.Join(t.TempDir(), "bus")
	os.WriteFile(realBus, nil, 0o600) //nolint:errcheck
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+realBus)

	var warn bytes.Buffer
	rc := &config.RunConfig{Allow: []string{"session-bus"}}
	applyClaudeSessionBus(&warn, rc)()

	assertUnfilteredBus(t, rc, realBus)
	if warn.Len() > 0 {
		t.Errorf("unexpected warning: %s", warn.String())
	}
}

func assertUnfilteredBus(t *testing.T, rc *config.RunConfig, realBus string) {
	t.Helper()
	if !slices.Contains(rc.Env.Inherit, "DBUS_SESSION_BUS_ADDRESS") {
		t.Error("expected DBUS_SESSION_BUS_ADDRESS in Env.Inherit")
	}
	if _, ok := rc.Env.Set["DBUS_SESSION_BUS_ADDRESS"]; ok {
		t.Error("the address must not be rewritten in unfiltered mode")
	}
	if !slices.ContainsFunc(rc.Mounts, func(m config.Mount) bool {
		return m.Src == realBus && m.Dest == realBus && m.Mode == "rw"
	}) {
		t.Errorf("expected rw bind of the real bus; mounts: %v", rc.Mounts)
	}
	if !slices.Contains(rc.HideExempt, realBus) {
		t.Errorf("the real bus must be exempt from the session-bus hide rule: %v", rc.HideExempt)
	}
}

// Against the real xdg-dbus-proxy and a private dbus-daemon, when both are
// installed: the readiness handshake works, the filtered socket answers bus
// driver calls, and stop() really ends the proxy. Whether the filter blocks
// org.freedesktop.systemd1 is asserted end to end in .sdlc/e2e.
func TestStartDBusProxy_real(t *testing.T) {
	proxyBin, err := exec.LookPath("xdg-dbus-proxy")
	if err != nil {
		t.Skip("xdg-dbus-proxy not installed")
	}
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	dbusSend, err := exec.LookPath("dbus-send")
	if err != nil {
		t.Skip("dbus-send not installed")
	}
	prev := dbusProxyLookPath
	dbusProxyLookPath = func(string) (string, error) { return proxyBin, nil }
	t.Cleanup(func() { dbusProxyLookPath = prev })

	sockDir, err := os.MkdirTemp("", "inner-dbus-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	addr := "unix:path=" + filepath.Join(sockDir, "bus")
	d := exec.Command(daemon, "--session", "--nofork", "--address="+addr)
	if err := d.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { d.Process.Kill(); d.Wait() }) //nolint:errcheck
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(filepath.Join(sockDir, "bus")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	proxy, err := startDBusProxy(addr)
	if err != nil {
		t.Fatalf("startDBusProxy: %v", err)
	}
	out, err := exec.Command(dbusSend, "--bus=unix:path="+proxy.socket, "--print-reply",
		"--dest=org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.ListNames").CombinedOutput()
	if err != nil {
		t.Errorf("bus driver call through the proxy failed: %v\n%s", err, out)
	}

	proxy.stop()
	if _, err := os.Stat(filepath.Dir(proxy.socket)); !os.IsNotExist(err) {
		t.Errorf("proxy dir left behind: %v", err)
	}
	if err := exec.Command(dbusSend, "--bus=unix:path="+proxy.socket, "--print-reply",
		"--dest=org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.ListNames").Run(); err == nil {
		t.Error("the proxy socket still answers after stop()")
	}
}
