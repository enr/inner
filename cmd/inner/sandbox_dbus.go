package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enr/inner/internal/config"
)

// The claude capability used to hand every sandbox a session bus filtered to
// org.freedesktop.secrets, on the belief that Claude refreshes its OAuth token
// through libsecret. On Linux it does not: Claude Code keeps its credentials in
// ~/.claude/.credentials.json and talks to no keyring (only macOS has a
// Keychain backend). And the filter is per bus name, not per secret:
// org.freedesktop.secrets is the whole Secret Service API, so the sandbox
// could read and enumerate every item of an unlocked keyring — the gh token,
// browser "Safe Storage" keys, saved passwords — and, with network access,
// send them anywhere.
//
// So by default the claude capability gives the sandbox no session bus at all.
// Two explicit opt-ins remain:
//   - allow = ["keyrings"]: the keyring, and nothing else on the bus, through
//     xdg-dbus-proxy (the filter flatpak uses) restricted to
//     org.freedesktop.secrets. The proxy socket is bound at a fixed path on the
//     sandbox /tmp tmpfs and DBUS_SESSION_BUS_ADDRESS is pointed at it; the
//     real bus socket stays hidden (the session-bus hide rule, or the
//     profile's tmpfs over /run/user/<uid>). If xdg-dbus-proxy is missing or
//     fails to start: no bus at all, with a warning.
//   - allow = ["session-bus"] (or a profile that sets DBUS_SESSION_BUS_ADDRESS
//     itself): the unfiltered bus, the real socket bound as is.
//
// The whole bus must never go in by default: org.freedesktop.systemd1 on it
// starts arbitrary commands in the user's systemd instance — outside the
// sandbox — and any other service on the bus acts with the user's full
// authority.

// keyringBusAllowKey is the [sandbox] allow key that opts into the filtered
// keyring bus. It is the key that already un-hides the keyring files and the
// gnome-keyring control socket: asking for the Secret Service is asking for
// the same secrets.
const keyringBusAllowKey = "keyrings"

// sandboxDBusSocket is where the filtered bus appears inside the sandbox. /tmp
// is a fresh tmpfs there, so bwrap can create the mount point.
const sandboxDBusSocket = "/tmp/inner-dbus/bus"

// dbusProxyTalkNames are the only bus names the sandbox may call.
var dbusProxyTalkNames = []string{"org.freedesktop.secrets"}

// dbusProxyReadyTimeout bounds the wait for the proxy's readiness byte.
var dbusProxyReadyTimeout = 5 * time.Second

// dbusProxyLookPath finds xdg-dbus-proxy. Overridable in tests.
var dbusProxyLookPath = exec.LookPath

// dbusProxyArgs builds the xdg-dbus-proxy command line: the readiness fd is a
// global option, the filter applies to the one ADDRESS PATH pair that follows.
func dbusProxyArgs(addr, sock string) []string {
	args := []string{"--fd=3", addr, sock, "--filter"}
	for _, name := range dbusProxyTalkNames {
		args = append(args, "--talk="+name)
	}
	return args
}

// dbusProxy is a running xdg-dbus-proxy.
type dbusProxy struct {
	socket string // host path of the filtered socket
	stop   func()
}

// startDBusProxy starts xdg-dbus-proxy on addr and waits until it listens.
//
// Readiness and lifetime share one pipe, the way flatpak drives the proxy: the
// proxy writes a byte to fd 3 once its socket is listening, and exits when the
// other end of that pipe is closed — by stop, or by the kernel if inner dies.
func startDBusProxy(addr string) (*dbusProxy, error) {
	bin, err := dbusProxyLookPath("xdg-dbus-proxy")
	if err != nil {
		return nil, fmt.Errorf("xdg-dbus-proxy not found: %w", err)
	}
	dir, err := os.MkdirTemp("", "inner-dbus-*")
	if err != nil {
		return nil, fmt.Errorf("creating proxy dir: %w", err)
	}
	sock := filepath.Join(dir, "bus")

	ready, readyW, err := os.Pipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("creating readiness pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(bin, dbusProxyArgs(addr, sock)...)
	cmd.ExtraFiles = []*os.File{readyW}
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		ready.Close()
		readyW.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("starting xdg-dbus-proxy: %w", err)
	}
	readyW.Close() // the proxy holds the only write end now

	var once sync.Once
	stop := func() {
		once.Do(func() {
			ready.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			os.RemoveAll(dir)
		})
	}

	got := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := io.ReadFull(ready, buf)
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			stop()
			return nil, fmt.Errorf("xdg-dbus-proxy exited before it was ready: %s", strings.TrimSpace(stderr.String()))
		}
	case <-time.After(dbusProxyReadyTimeout):
		stop()
		return nil, fmt.Errorf("xdg-dbus-proxy not ready after %s", dbusProxyReadyTimeout)
	}
	if _, err := os.Stat(sock); err != nil {
		stop()
		return nil, fmt.Errorf("xdg-dbus-proxy reported ready but %s is missing", sock)
	}
	return &dbusProxy{socket: sock, stop: stop}, nil
}

// limitedWriter keeps at most n bytes: enough to quote the proxy's error.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k]) //nolint:errcheck
		l.n -= k
	}
	return len(p), nil
}

// applyClaudeSessionBus gives the sandbox the session bus only when the
// profile asks for it: filtered to the keyring with allow = ["keyrings"],
// unfiltered with allow = ["session-bus"] or a profile-set address. Otherwise
// the sandbox gets no bus. Returns the cleanup for the proxy, if any.
func applyClaudeSessionBus(w io.Writer, rc *config.RunConfig) func() {
	addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" {
		return func() {}
	}

	// A profile that sets the address itself, or that allows the full bus,
	// keeps the pre-filter behaviour it was written against.
	_, profileSetsAddr := rc.Env.Set["DBUS_SESSION_BUS_ADDRESS"]
	if profileSetsAddr || slices.Contains(rc.Allow, "session-bus") {
		passSessionBusUnfiltered(rc, addr)
		return func() {}
	}

	// No opt-in: no bus. Claude does not need one (see the comment at the top
	// of this file), and the keyring behind it holds every other secret too.
	if !config.AllowKeyEnabled(rc.Allow, keyringBusAllowKey) {
		return func() {}
	}

	proxy, err := startDBusProxy(addr)
	if err != nil {
		// Fail closed: the whole bus would hand the sandbox
		// org.freedesktop.systemd1, which starts processes outside it.
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "inner: warning")+
			": claude: cannot filter the session bus (%v) — allow = [\"keyrings\"] asked for the keyring, "+
			"but the sandbox gets no session bus. Install xdg-dbus-proxy to give it the keyring only, or add "+
			"allow = [\"session-bus\"] to the profile to pass the whole bus (org.freedesktop.systemd1 on it "+
			"can start processes outside the sandbox)\n", err)
		return func() {}
	}

	rc.Mounts = append(rc.Mounts, config.Mount{Src: proxy.socket, Dest: sandboxDBusSocket, Mode: "rw"})
	if rc.Env.Set == nil {
		rc.Env.Set = make(map[string]string)
	}
	rc.Env.Set["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + sandboxDBusSocket
	return proxy.stop
}

// passSessionBusUnfiltered is the pre-filter passthrough: inherit the address
// and, for the unix:path= form, bind the real socket back at its own path.
//
// Inheriting the variable is not enough on its own: the default claude
// profiles mount a tmpfs over /run/user/$UID (to hide the other host runtime
// sockets that hang Node.js at startup), which also erases the socket the
// variable points at; and on a profile without that tmpfs the session-bus hide
// rule covers it. The bind is emitted after the tmpfs and is exempt from the
// hide rule, so it survives both. Abstract-socket addresses need no bind: the
// claude profiles keep the host network namespace, where abstract sockets stay
// reachable.
func passSessionBusUnfiltered(rc *config.RunConfig, addr string) {
	rc.Env.Inherit = append(rc.Env.Inherit, "DBUS_SESSION_BUS_ADDRESS")
	sock := dbusSocketPath(addr)
	if sock == "" {
		// An abstract bus lives in the network namespace: the scope that keeps
		// the sandbox off the host's abstract sockets would cut it off.
		if strings.Contains(addr, "abstract=") {
			rc.KeepHostAbstractSockets = true
		}
		return
	}
	if _, err := os.Stat(sock); err != nil {
		return
	}
	// Mode rw because connect(2) on a Unix socket requires write permission on
	// the socket inode; the bind exposes only the bus socket, nothing else.
	rc.Mounts = append(rc.Mounts, config.Mount{Src: sock, Dest: sock, Mode: "rw"})
	rc.HideExempt = append(rc.HideExempt, sock)
}
