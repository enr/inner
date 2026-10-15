package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/netproxy"
)

// publishFixture parses one publish entry on a free host port, forwarding to
// sandboxPort.
func publishFixture(t *testing.T, sandboxPort int) config.PortPublish {
	t.Helper()
	_, port, _ := net.SplitHostPort(freeLoopbackAddr(t))
	p, err := config.ParsePublish(port + ":" + strconv.Itoa(sandboxPort))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// echoServer stands in for the server running inside the sandbox.
func echoServer(t *testing.T) *net.TCPAddr {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l.Addr().(*net.TCPAddr)
}

// The whole path without bwrap: a host client on the published port reaches
// the relay's socket (here in the host directory itself, which is what the
// bind mount makes it), and the relay hands it to the server "inside".
func TestPublish_hostReachesTheServerThroughTheRelay(t *testing.T) {
	server := echoServer(t)
	pub := publishFixture(t, server.Port)
	rc := &config.RunConfig{
		NetworkMode: config.NetworkOff,
		Publish:     []config.PortPublish{pub},
		Entrypoint:  config.Entrypoint{Cmd: "sleep", Args: []string{"5"}},
	}

	cleanup, err := applyPublish(rc)
	if err != nil {
		t.Fatalf("applyPublish: %v", err)
	}
	defer cleanup()
	if rc.PublishSocketDir == "" {
		t.Fatal("no socket directory recorded")
	}
	if info, err := os.Stat(rc.PublishSocketDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory: %v, mode %v; want 0700", err, info.Mode().Perm())
	}

	// The relay side, as the entrypoint rewrite would start it inside.
	sock := filepath.Join(rc.PublishSocketDir, netproxy.PublishSocketName(0))
	done := make(chan int, 1)
	go func() {
		code, err := runNetRelay(io.Discard, netRelayOptions{
			Publish: []string{sock + "=" + pub.SandboxAddr()},
			Cmd:     "sleep", Args: []string{"2"},
		})
		if err != nil {
			t.Errorf("runNetRelay: %v", err)
		}
		done <- code
	}()
	waitForFile(t, sock)

	conn, err := net.DialTimeout("tcp", pub.HostAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dialing the published port: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("echo = %q, %v; want \"ping\\n\"", line, err)
	}

	// The relay exits with its child even with this connection still open.
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("relay exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the relay waited on an open published connection instead of exiting")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// A taken host port stops the run before the sandbox starts, naming the entry.
func TestApplyPublish_busyPortIsAnError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	p, _ := config.ParsePublish(strconv.Itoa(port))

	rc := &config.RunConfig{NetworkMode: config.NetworkAllowlist, Publish: []config.PortPublish{p}}
	if _, err := applyPublish(rc); err == nil || !strings.Contains(err.Error(), p.HostAddr()) {
		t.Fatalf("err = %v, want an error naming %s", err, p.HostAddr())
	}
	if rc.PublishSocketDir != "" {
		t.Error("a failed publish must not record a socket directory")
	}
}

// Under "full" the sandbox already shares the host network: listening on the
// host would only take the port the server inside wants.
func TestApplyPublish_noOpUnderFullNetwork(t *testing.T) {
	rc := &config.RunConfig{NetworkMode: config.NetworkFull, Publish: []config.PortPublish{publishFixture(t, 80)}}
	cleanup, err := applyPublish(rc)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if rc.PublishSocketDir != "" {
		t.Error("full network: nothing must be published")
	}
}

// Off mode has no proxy, so the relay runs for the published ports alone.
func TestApplyNetworkProxy_wrapsForPublishWithoutAProxy(t *testing.T) {
	pub := publishFixture(t, 3000)
	rc := &config.RunConfig{
		NetworkMode: config.NetworkOff,
		Publish:     []config.PortPublish{pub},
		Entrypoint:  config.Entrypoint{Cmd: "npm", Args: []string{"run", "dev"}},
	}
	cleanupPub, err := applyPublish(rc)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupPub()
	cleanup, err := applyNetworkProxy(rc)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if rc.NetProxySocketPath != "" {
		t.Error("off mode must not start the proxy")
	}
	want := []string{
		"__net-relay", "--listen", "",
		"--publish", netproxy.SandboxPublishDir + "/p0.sock=127.0.0.1:3000",
		"--", "npm", "run", "dev",
	}
	if !slices.Equal(rc.Entrypoint.Args, want) {
		t.Errorf("entrypoint args =\n %q\nwant\n %q", rc.Entrypoint.Args, want)
	}
}

// verify certifies the sandbox, it does not serve it: no host listener, so a
// run of the same profile holding the port does not make verify fail.
func TestPrepareSandbox_verifyDoesNotPublish(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	p, _ := config.ParsePublish(strconv.Itoa(busy.Addr().(*net.TCPAddr).Port))
	rc := &config.RunConfig{
		NetworkMode: config.NetworkOff,
		Publish:     []config.PortPublish{p},
		Entrypoint:  config.Entrypoint{Cmd: "true"},
	}
	cleanup, err := prepareSandbox(rc, verifySandboxOptions())
	if err != nil {
		t.Fatalf("verify preparation failed on a taken publish port: %v", err)
	}
	defer cleanup()
	if rc.PublishSocketDir != "" {
		t.Error("verify must not publish")
	}
}

func TestRunNetRelay_invalidPublishSpecIsFatal(t *testing.T) {
	_, err := runNetRelay(io.Discard, netRelayOptions{Publish: []string{"no-equals"}, Cmd: "true"})
	if err == nil {
		t.Fatal("a malformed --publish must stop the relay before the child starts")
	}
}

func TestGuardPublish(t *testing.T) {
	var buf bytes.Buffer
	rc := &config.RunConfig{NetworkMode: config.NetworkOff}
	flags := runCLIFlags{publish: []string{"0.0.0.0:8080"}}
	if err := applyOverrides(rc, flags, nil); err != nil {
		t.Fatal(err)
	}
	guardPublish(&buf, rc, flags)
	if !strings.Contains(buf.String(), "other machines") {
		t.Errorf("non-loopback --publish: want a warning, got %q", buf.String())
	}

	buf.Reset()
	rc = &config.RunConfig{NetworkMode: config.NetworkOff}
	flags = runCLIFlags{publish: []string{"8080"}, networkOn: true}
	if err := applyOverrides(rc, flags, nil); err != nil {
		t.Fatal(err)
	}
	guardPublish(&buf, rc, flags)
	if !strings.Contains(buf.String(), "publish ignored") {
		t.Errorf("--publish with --network: want the ignored warning, got %q", buf.String())
	}
}

func TestApplyOverrides_publishConflictIsAnError(t *testing.T) {
	rc := &config.RunConfig{Publish: []config.PortPublish{{HostIP: "127.0.0.1", HostPort: 8080, SandboxPort: 8080}}}
	if err := applyOverrides(rc, runCLIFlags{publish: []string{"8080:3000"}}, nil); err == nil {
		t.Error("--publish binding the same host address as the profile with another target must be refused")
	}
}

func TestHardenRemoteProfile_dropsNonLoopbackPublish(t *testing.T) {
	lo, _ := config.ParsePublish("8080")
	all, _ := config.ParsePublish("0.0.0.0:9000")
	rc := &config.RunConfig{PidNamespace: true, NetworkMode: config.NetworkOff, Publish: []config.PortPublish{lo, all}}
	applied := hardenRemoteProfile(rc)
	if len(rc.Publish) != 1 || rc.Publish[0] != lo {
		t.Errorf("Publish = %v, want only the loopback entry", rc.Publish)
	}
	if len(applied) != 1 || !strings.Contains(applied[0], "0.0.0.0:9000") {
		t.Errorf("applied = %v, want one line naming the dropped entry", applied)
	}
	if out := strings.Join(remoteProfileRequests(rc), "\n"); !strings.Contains(out, "publish: 127.0.0.1:8080") {
		t.Errorf("consent prompt must list the published port, got:\n%s", out)
	}
}

func TestPrintDryRun_listsPublishedPorts(t *testing.T) {
	p, _ := config.ParsePublish("8080:3000")
	rc := &config.RunConfig{NetworkMode: config.NetworkOff, Publish: []config.PortPublish{p}}
	var buf bytes.Buffer
	printDryRun(&buf, "", "", "", rc, nil)
	if !strings.Contains(buf.String(), "publish: 127.0.0.1:8080 -> sandbox 127.0.0.1:3000") {
		t.Errorf("dry-run output lacks the published port:\n%s", buf.String())
	}
	out := newDryRunJSON("", "", "", rc, "sh", nil, nil)
	if len(out.Publish) != 1 || out.Publish[0].Host != "127.0.0.1:8080" || out.Publish[0].Ignored {
		t.Errorf("dry-run JSON publish = %+v", out.Publish)
	}
}
