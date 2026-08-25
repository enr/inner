package netproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Default timeouts and limits. All of them exist because the client is an
// untrusted agent, not because a well-behaved client would ever hit them.
const (
	// defaultRequestTimeout bounds reading the proxy request itself, so a
	// client that opens a connection and sends nothing cannot hold a slot.
	defaultRequestTimeout = 30 * time.Second
	// defaultResolveTimeout bounds DNS, which is the one step that talks to
	// something outside this machine before any policy decision has succeeded.
	defaultResolveTimeout = 10 * time.Second
	// defaultDialTimeout bounds the upstream connection attempt.
	defaultDialTimeout = 30 * time.Second
	// defaultIdleTimeout reclaims an established tunnel that has gone quiet.
	// Without it a hung tunnel is never released and slowly eats the cap below.
	defaultIdleTimeout = 5 * time.Minute
	// defaultMaxConns caps concurrent connections. An unbounded
	// goroutine-per-connection loop would let the sandbox exhaust the file
	// descriptors of the host inner process — a denial of service against the
	// user's own machine, from inside the sandbox.
	defaultMaxConns = 128
)

// hopByHopHeaders are stripped before forwarding. Proxy-Authorization in
// particular must never reach the upstream: it is addressed to us.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
	"Keep-Alive",
}

// Resolver resolves a hostname to addresses. Injectable so the tests can prove
// two things that are otherwise invisible: that a denied hostname is rejected
// WITHOUT the resolver ever being consulted, and that a name resolving to a
// denied address is refused (DNS rebinding).
type Resolver interface {
	LookupIP(ctx context.Context, host string) ([]net.IP, error)
}

type systemResolver struct{}

func (systemResolver) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// Proxy is the host-side CONNECT proxy that mediates network access for a
// sandbox running with network_mode = "allowlist".
//
// It serves a unix listener whose socket is bind-mounted into the sandbox; the
// in-sandbox relay bridges a loopback TCP port to it. The sandbox itself has no
// route to anything (bwrap --unshare-net), so this is the only way out, and
// every byte crossing it has passed Policy.
type Proxy struct {
	Policy Policy

	// Resolver defaults to the system resolver.
	Resolver Resolver
	// Log receives one line per denied request. Defaults to os.Stderr.
	// Structured audit logging is out of scope (NONO_COMPARISON.md §S4).
	Log io.Writer

	MaxConns       int
	RequestTimeout time.Duration
	ResolveTimeout time.Duration
	DialTimeout    time.Duration
	IdleTimeout    time.Duration

	// dialContext is a test seam for the upstream dial. Production code leaves
	// it nil and gets a plain net.Dialer.
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (p *Proxy) resolver() Resolver {
	if p.Resolver != nil {
		return p.Resolver
	}
	return systemResolver{}
}

func (p *Proxy) logw() io.Writer {
	if p.Log != nil {
		return p.Log
	}
	return os.Stderr
}

func orDuration(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

func (p *Proxy) maxConns() int {
	if p.MaxConns > 0 {
		return p.MaxConns
	}
	return defaultMaxConns
}

// Serve accepts connections until the listener is closed. It always returns a
// non-nil error; ErrClosed after a normal Close.
func (p *Proxy) Serve(l net.Listener) error {
	sem := make(chan struct{}, p.maxConns())
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		select {
		case sem <- struct{}{}:
		default:
			// At the cap. Refuse this connection immediately rather than
			// blocking the accept loop: a client that opened too many
			// connections must get an answer, and the ones already established
			// must keep working.
			p.deny(conn, "", "too many concurrent connections")
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer conn.Close()
			p.handle(conn)
		}()
	}
}

// handle serves proxy requests on one client connection.
func (p *Proxy) handle(conn net.Conn) {
	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(orDuration(p.RequestTimeout, defaultRequestTimeout)))
		req, err := http.ReadRequest(br)
		if err != nil {
			return // client went away, or sent something that is not HTTP
		}
		_ = conn.SetReadDeadline(time.Time{})

		if req.Method == http.MethodConnect {
			// A CONNECT consumes the connection for the rest of its life.
			p.handleConnect(conn, br, req)
			return
		}
		if !req.URL.IsAbs() {
			p.writeStatus(conn, http.StatusBadRequest, "this is a proxy: use an absolute URI or CONNECT")
			return
		}
		if keepAlive := p.handleHTTP(conn, req); !keepAlive {
			return
		}
	}
}

// authorize runs the decision chain for a target and returns the validated
// upstream address. The order here is the whole point — see the package
// comment: the allowlist gate runs on the NAME, before anything is resolved.
func (p *Proxy) authorize(ctx context.Context, hostport string) (*net.TCPAddr, error) {
	target, err := ParseTarget(hostport)
	if err != nil {
		return nil, err
	}
	if err := p.Policy.AllowsHost(target); err != nil {
		return nil, err
	}

	ips := []net.IP{target.IP}
	if target.IP == nil {
		rctx, cancel := context.WithTimeout(ctx, orDuration(p.ResolveTimeout, defaultResolveTimeout))
		defer cancel()
		ips, err = p.resolver().LookupIP(rctx, target.Host)
		if err != nil {
			return nil, &DenyError{Target: target.String(), Reason: fmt.Sprintf("cannot resolve: %v", err)}
		}
		if len(ips) == 0 {
			return nil, &DenyError{Target: target.String(), Reason: "resolved to no addresses"}
		}
	}

	// Every resolved address must pass. Picking the first ALLOWED one out of a
	// mixed answer would let a name that also resolves to 127.0.0.1 be used as
	// a probe; refusing the whole name is the honest reading of "this name
	// points somewhere it must not".
	for _, ip := range ips {
		if err := p.Policy.AllowsAddr(ip); err != nil {
			var de *DenyError
			if errors.As(err, &de) {
				return nil, &DenyError{
					Target: target.String(),
					Reason: fmt.Sprintf("resolves to %s: %s", de.Target, de.Reason),
				}
			}
			return nil, err
		}
	}
	// Dial the validated literal, never the name: re-resolving at connect time
	// is exactly the DNS-rebinding window this closes.
	return &net.TCPAddr{IP: ips[0], Port: target.Port}, nil
}

func (p *Proxy) dial(ctx context.Context, addr *net.TCPAddr) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, orDuration(p.DialTimeout, defaultDialTimeout))
	defer cancel()
	if p.dialContext != nil {
		return p.dialContext(dctx, "tcp", addr.String())
	}
	var d net.Dialer
	return d.DialContext(dctx, "tcp", addr.String())
}

// handleConnect establishes a byte tunnel to a validated upstream.
func (p *Proxy) handleConnect(conn net.Conn, br *bufio.Reader, req *http.Request) {
	ctx := context.Background()
	addr, err := p.authorize(ctx, req.Host)
	if err != nil {
		p.denyErr(conn, err)
		return
	}
	upstream, err := p.dial(ctx, addr)
	if err != nil {
		p.deny(conn, req.Host, fmt.Sprintf("upstream dial failed: %v", err))
		return
	}
	defer upstream.Close()

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	idle := orDuration(p.IdleTimeout, defaultIdleTimeout)
	client := &idleConn{Conn: conn, idle: idle}
	up := &idleConn{Conn: upstream, idle: idle}

	// Splice both directions. When either side finishes, close the other so the
	// second copy cannot hang: a half-closed tunnel is the classic way to leak
	// a goroutine and a file descriptor per connection.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, br) // br, not conn: it may hold buffered client bytes
		_ = upstream.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, up)
		_ = conn.Close()
	}()
	wg.Wait()
}

// handleHTTP forwards one plain (non-TLS) absolute-URI request. It reports
// whether the client connection can be reused for another request.
func (p *Proxy) handleHTTP(conn net.Conn, req *http.Request) bool {
	ctx := context.Background()

	host := req.URL.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "80") // absolute URIs usually omit :80
	}
	addr, err := p.authorize(ctx, host)
	if err != nil {
		p.denyErr(conn, err)
		return false
	}

	outReq := req.Clone(ctx)
	outReq.RequestURI = ""
	for _, h := range hopByHopHeaders {
		outReq.Header.Del(h)
	}

	// A Transport, deliberately NOT a Client: a Client follows redirects, and a
	// 302 to a denied host would be fetched without ever passing the policy
	// again. The sandboxed client sees the redirect and must re-request it
	// through the proxy, where it goes through the full chain like any other.
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.dial(ctx, addr)
		},
		DisableCompression:    true,
		ResponseHeaderTimeout: orDuration(p.DialTimeout, defaultDialTimeout),
	}
	defer tr.CloseIdleConnections()

	resp, err := tr.RoundTrip(outReq)
	if err != nil {
		p.deny(conn, host, fmt.Sprintf("upstream request failed: %v", err))
		return false
	}
	defer resp.Body.Close()

	for _, h := range hopByHopHeaders {
		resp.Header.Del(h)
	}
	if err := resp.Write(conn); err != nil {
		return false
	}
	return !resp.Close && !req.Close
}

// denyErr renders a policy rejection to the client and to the log.
func (p *Proxy) denyErr(conn net.Conn, err error) {
	var de *DenyError
	if errors.As(err, &de) {
		p.deny(conn, de.Target, de.Reason)
		return
	}
	p.deny(conn, "", err.Error())
}

func (p *Proxy) deny(conn net.Conn, target, reason string) {
	if target == "" {
		fmt.Fprintf(p.logw(), "inner: network-allowlist: refused a request (%s)\n", reason)
	} else {
		fmt.Fprintf(p.logw(), "inner: network-allowlist: blocked %s (%s)\n", target, reason)
	}
	p.writeStatus(conn, http.StatusForbidden, reason)
}

func (p *Proxy) writeStatus(conn net.Conn, code int, reason string) {
	body := "inner: network-allowlist: " + reason + "\n"
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
	fmt.Fprintf(conn, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(conn, "Content-Length: %s\r\n", strconv.Itoa(len(body)))
	fmt.Fprintf(conn, "Connection: close\r\n\r\n%s", body)
}

// idleConn applies an idle timeout by refreshing the deadline before every
// operation, so a tunnel is reclaimed when it goes quiet rather than when the
// whole transfer takes too long — a long download must not be killed for being
// long, only for being stalled.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(b []byte) (int, error) {
	if c.idle > 0 {
		_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Read(b)
}

func (c *idleConn) Write(b []byte) (int, error) {
	if c.idle > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	}
	return c.Conn.Write(b)
}
