package netproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── Test harness ──────────────────────────────────────────────────────────────

// fakeResolver maps names to addresses and counts how often it was consulted.
// The counter is the point: a denied hostname must be refused BEFORE anything
// is resolved, and the only way to observe that is to see that nobody asked.
type fakeResolver struct {
	hosts map[string][]net.IP
	calls atomic.Int32
}

func (r *fakeResolver) LookupIP(_ context.Context, host string) ([]net.IP, error) {
	r.calls.Add(1)
	if ips, ok := r.hosts[host]; ok {
		return ips, nil
	}
	return nil, fmt.Errorf("no such host: %s", host)
}

// startProxy serves p on a loopback listener and returns its address.
func startProxy(t *testing.T, p *Proxy) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.Serve(l) }()
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// newTestProxy builds a proxy whose upstream is reachable. Every address a test
// machine can bind is loopback, i.e. exactly what the always-deny list refuses,
// so the seam is what makes any of these tests possible at all — see
// Policy.AllowPrivateDestinations.
func newTestProxy(t *testing.T, allow []string, res Resolver) (*Proxy, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	return &Proxy{
		Policy:   Policy{Allow: allow, AllowPrivateDestinations: true},
		Resolver: res,
		Log:      &logs,
	}, &logs
}

// proxyClient returns an http.Client that reaches origin servers only through
// the proxy at addr.
func proxyClient(t *testing.T, addr string) *http.Client {
	t.Helper()
	u, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// originPort returns the port an httptest server is listening on.
func originPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := net.LookupPort("tcp", u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// ── The ordering regression test ──────────────────────────────────────────────

// The reason AllowsHost and AllowsAddr take different argument types. Resolving
// before checking the allowlist turns the proxy into a DNS exfiltration channel:
// the query for <secret>.attacker.com leaves the machine even though the TCP
// connection is then refused. This test fails the moment the chain is reordered.
func TestConnect_deniedHostIsRejectedWithoutResolving(t *testing.T) {
	res := &fakeResolver{hosts: map[string][]net.IP{
		"exfil.attacker.com": {net.ParseIP("127.0.0.1")},
	}}
	p, logs := newTestProxy(t, []string{"api.anthropic.com"}, res)
	addr := startProxy(t, p)

	resp, err := proxyClient(t, addr).Get("https://exfil.attacker.com/")
	if err == nil {
		resp.Body.Close()
	}

	if got := res.calls.Load(); got != 0 {
		t.Errorf("resolver consulted %d time(s) for a host that is not in the allow list — "+
			"the allowlist gate must run before resolution", got)
	}
	if !strings.Contains(logs.String(), "not in the allow list") {
		t.Errorf("expected a denial log naming the reason, got: %q", logs.String())
	}
}

// ── CONNECT ───────────────────────────────────────────────────────────────────

func TestConnect_allowedTargetTunnels(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "hello from upstream")
	}))
	defer origin.Close()
	port := originPort(t, origin)

	res := &fakeResolver{hosts: map[string][]net.IP{
		"allowed.example.com": {net.ParseIP("127.0.0.1")},
	}}
	p, _ := newTestProxy(t, []string{fmt.Sprintf("allowed.example.com:%d", port)}, res)
	proxyAddr := startProxy(t, p)

	client := proxyClient(t, proxyAddr)
	tr := client.Transport.(*http.Transport)
	tr.TLSClientConfig = origin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.TLSClientConfig.InsecureSkipVerify = true

	resp, err := client.Get(fmt.Sprintf("https://allowed.example.com:%d/", port))
	if err != nil {
		t.Fatalf("request through the proxy failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q, want the upstream response", body)
	}
}

func TestConnect_deniedTargetGets403(t *testing.T) {
	res := &fakeResolver{hosts: map[string][]net.IP{"denied.example.com": {net.ParseIP("127.0.0.1")}}}
	p, _ := newTestProxy(t, []string{"allowed.example.com"}, res)
	addr := startProxy(t, p)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "CONNECT denied.example.com:443 HTTP/1.1\r\nHost: denied.example.com:443\r\n\r\n")

	status, _ := bufioReadLine(t, conn)
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403", status)
	}
}

// DNS rebinding: the name is allowed, but it resolves somewhere it must not be
// allowed to reach. The address gate runs on the resolved IP, and the dial then
// uses that validated literal rather than re-resolving.
func TestConnect_rebindingToADeniedAddressIsRefused(t *testing.T) {
	res := &fakeResolver{hosts: map[string][]net.IP{
		"allowed.example.com": {net.ParseIP("169.254.169.254")},
	}}
	p := &Proxy{
		// NOT AllowPrivateDestinations: this is the check under test.
		Policy:   Policy{Allow: []string{"allowed.example.com"}},
		Resolver: res,
		Log:      &bytes.Buffer{},
	}
	addr := startProxy(t, p)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "CONNECT allowed.example.com:443 HTTP/1.1\r\nHost: allowed.example.com:443\r\n\r\n")

	status, _ := bufioReadLine(t, conn)
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403 for a name resolving to the metadata endpoint", status)
	}
}

// A mixed answer is refused wholesale: picking the first allowed address out of
// it would let a name that also resolves to 127.0.0.1 be used as a probe.
func TestConnect_mixedResolutionIsRefusedWholesale(t *testing.T) {
	res := &fakeResolver{hosts: map[string][]net.IP{
		"mixed.example.com": {net.ParseIP("1.1.1.1"), net.ParseIP("127.0.0.1")},
	}}
	p := &Proxy{
		Policy:   Policy{Allow: []string{"mixed.example.com"}},
		Resolver: res,
		Log:      &bytes.Buffer{},
	}
	addr := startProxy(t, p)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "CONNECT mixed.example.com:443 HTTP/1.1\r\nHost: mixed.example.com:443\r\n\r\n")

	status, _ := bufioReadLine(t, conn)
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403 when any resolved address is denied", status)
	}
}

// ── Plain HTTP forwarding ─────────────────────────────────────────────────────

func TestHTTP_allowedTargetIsForwarded(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("Proxy-Authorization must not reach the upstream: it is addressed to the proxy")
		}
		fmt.Fprint(w, "plain hello")
	}))
	defer origin.Close()
	port := originPort(t, origin)

	res := &fakeResolver{hosts: map[string][]net.IP{"allowed.example.com": {net.ParseIP("127.0.0.1")}}}
	p, _ := newTestProxy(t, []string{fmt.Sprintf("allowed.example.com:%d", port)}, res)
	proxyAddr := startProxy(t, p)

	req, err := http.NewRequest("GET", fmt.Sprintf("http://allowed.example.com:%d/", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")

	resp, err := proxyClient(t, proxyAddr).Do(req)
	if err != nil {
		t.Fatalf("plain HTTP through the proxy failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "plain hello" {
		t.Errorf("body = %q, want the upstream response", body)
	}
}

// A Transport, not a Client: a Client would follow the redirect itself and
// fetch a denied host without the policy ever seeing it. The redirect must come
// back to the sandboxed client, which then re-requests through the proxy.
func TestHTTP_redirectIsNotFollowedByTheProxy(t *testing.T) {
	var deniedHit atomic.Int32
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deniedHit.Add(1)
		fmt.Fprint(w, "should never be reached")
	}))
	defer denied.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, denied.URL+"/secret", http.StatusFound)
	}))
	defer origin.Close()
	port := originPort(t, origin)

	res := &fakeResolver{hosts: map[string][]net.IP{"allowed.example.com": {net.ParseIP("127.0.0.1")}}}
	p, _ := newTestProxy(t, []string{fmt.Sprintf("allowed.example.com:%d", port)}, res)
	proxyAddr := startProxy(t, p)

	resp, err := proxyClient(t, proxyAddr).Get(fmt.Sprintf("http://allowed.example.com:%d/", port))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the 302 handed back to the client unfollowed", resp.StatusCode)
	}
	if got := deniedHit.Load(); got != 0 {
		t.Errorf("the proxy followed a redirect to a host outside the allow list (%d hits)", got)
	}
}

func TestHTTP_relativeRequestIsRejected(t *testing.T) {
	p, _ := newTestProxy(t, []string{"allowed.example.com"}, &fakeResolver{})
	addr := startProxy(t, p)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /secret HTTP/1.1\r\nHost: allowed.example.com\r\n\r\n")

	status, _ := bufioReadLine(t, conn)
	if !strings.Contains(status, "400") {
		t.Errorf("status = %q, want 400 for a non-proxy request", status)
	}
}

// ── Resource limits ───────────────────────────────────────────────────────────

// The client is an untrusted agent: an unbounded goroutine-per-connection loop
// would let it exhaust the host process's file descriptors.
func TestServe_concurrencyCapRefusesRatherThanBlocking(t *testing.T) {
	res := &fakeResolver{hosts: map[string][]net.IP{"allowed.example.com": {net.ParseIP("127.0.0.1")}}}
	p, _ := newTestProxy(t, []string{"allowed.example.com"}, res)
	p.MaxConns = 1
	p.RequestTimeout = 2 * time.Second
	addr := startProxy(t, p)

	// Hold the single slot with a connection that never sends a request.
	held, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	// Give the accept loop a moment to take the slot.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		over, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = over.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		line, err := bufioReadLineErr(over)
		over.Close()
		if err == nil && strings.Contains(line, "403") {
			return // refused promptly, which is the point
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("a connection over the cap was neither served nor promptly refused")
}

func TestConnect_idleTunnelIsReclaimed(t *testing.T) {
	// An upstream that accepts and then says nothing, like a hung service.
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		time.Sleep(5 * time.Second)
	}()
	port := upstream.Addr().(*net.TCPAddr).Port

	res := &fakeResolver{hosts: map[string][]net.IP{"allowed.example.com": {net.ParseIP("127.0.0.1")}}}
	p, _ := newTestProxy(t, []string{fmt.Sprintf("allowed.example.com:%d", port)}, res)
	p.IdleTimeout = 200 * time.Millisecond
	proxyAddr := startProxy(t, p)

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT allowed.example.com:%d HTTP/1.1\r\nHost: allowed.example.com:%d\r\n\r\n", port, port)

	status := readResponseHead(t, conn)
	if !strings.Contains(status, "200") {
		t.Fatalf("tunnel was not established: %q", status)
	}

	// Nothing flows in either direction; the idle timeout must tear it down
	// well before the upstream's own 5s sleep ends.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Error("expected the idle tunnel to be closed, got data")
	} else if isTimeout(err) {
		t.Error("the idle tunnel was never reclaimed")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// readResponseHead consumes the status line AND the header block, so a
// subsequent Read observes the tunnel itself rather than the leftover CRLF that
// terminates the headers.
func readResponseHead(t *testing.T, conn net.Conn) string {
	t.Helper()
	status, _ := bufioReadLine(t, conn)
	for {
		line, err := bufioReadLineErr(conn)
		if err != nil || line == "" {
			return status
		}
	}
}

func bufioReadLine(t *testing.T, conn net.Conn) (string, error) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufioReadLineErr(conn)
	if err != nil {
		t.Fatalf("reading status line: %v", err)
	}
	return line, nil
}

func bufioReadLineErr(conn net.Conn) (string, error) {
	buf := make([]byte, 0, 128)
	one := make([]byte, 1)
	for len(buf) < cap(buf) {
		n, err := conn.Read(one)
		if err != nil {
			return string(buf), err
		}
		if n == 0 {
			continue
		}
		if one[0] == '\n' {
			return strings.TrimSpace(string(buf)), nil
		}
		buf = append(buf, one[0])
	}
	return string(buf), nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
