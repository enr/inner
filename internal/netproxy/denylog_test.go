package netproxy

import (
	"bytes"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// countingWriter records every Write separately, so a test can tell one line
// written once from the same line written twice.
type countingWriter struct {
	mu     sync.Mutex
	writes []string
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *countingWriter) all() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.writes, "")
}

func (w *countingWriter) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.writes...)
}

// The whole point of the type: a tool retrying a blocked endpoint must not
// produce a line per attempt.
func TestDenyLog_repeatsAreSuppressed(t *testing.T) {
	var out bytes.Buffer
	d := &DenyLog{W: &out}

	line := "inner: network-allowlist: blocked example.com:443 (not in the allow list)\n"
	for range 5 {
		if _, err := d.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	other := "inner: network-allowlist: blocked other.example.com:443 (not in the allow list)\n"
	_, _ = d.Write([]byte(other))

	if got := strings.Count(out.String(), "blocked example.com:443"); got != 1 {
		t.Errorf("the same refusal was printed %d times, want 1:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "blocked other.example.com:443") {
		t.Errorf("a distinct refusal was suppressed:\n%s", out.String())
	}
}

// Not deferred means "print it now" — a line-oriented session must not have to
// wait until it exits to learn what was refused.
func TestDenyLog_liveModeWritesImmediatelyAndFlushAddsNothing(t *testing.T) {
	var out bytes.Buffer
	d := &DenyLog{W: &out}

	line := "inner: network-allowlist: blocked example.com:443 (not in the allow list)\n"
	_, _ = d.Write([]byte(line))
	if out.String() != line {
		t.Fatalf("live mode wrote %q, want %q", out.String(), line)
	}

	d.Flush()
	if out.String() != line {
		t.Errorf("Flush reprinted a session that was already reported live: %q", out.String())
	}
}

// Deferred mode is the TUI case: nothing may reach the terminal while the
// child owns it.
func TestDenyLog_deferredHoldsUntilFlush(t *testing.T) {
	var out bytes.Buffer
	d := &DenyLog{W: &out, Deferred: true}

	blocked := "inner: network-allowlist: blocked downloads.claude.ai:443 (not in the allow list)\n"
	for range 3 {
		_, _ = d.Write([]byte(blocked))
	}
	_, _ = d.Write([]byte("inner: network-allowlist: blocked raw.githubusercontent.com:443 (not in the allow list)\n"))

	if out.Len() != 0 {
		t.Fatalf("deferred log wrote while the session was running:\n%s", out.String())
	}

	d.Flush()
	got := out.String()
	if !strings.Contains(got, "2 destinations were refused") {
		t.Errorf("summary does not count the distinct destinations:\n%s", got)
	}
	// Same words the live path would have used, so a user who has seen one
	// form recognises the other.
	if !strings.Contains(got, "blocked downloads.claude.ai:443 (not in the allow list) (x3)") {
		t.Errorf("summary does not replay the line with its repeat count:\n%s", got)
	}
	if !strings.Contains(got, "blocked raw.githubusercontent.com:443 (not in the allow list)\n") {
		t.Errorf("summary lost a refusal that occurred once:\n%s", got)
	}
	if !strings.Contains(got, "network_allow") {
		t.Errorf("summary does not say what to do about it:\n%s", got)
	}
}

// The cleanup that calls Flush is deferred, and inner has more than one path
// that can run it; reprinting the whole session on the second call would be
// worse than the noise this type exists to remove.
func TestDenyLog_flushIsNotRepeated(t *testing.T) {
	var out bytes.Buffer
	d := &DenyLog{W: &out, Deferred: true}
	_, _ = d.Write([]byte("inner: network-allowlist: blocked example.com:443 (not in the allow list)\n"))

	d.Flush()
	first := out.String()
	d.Flush()
	if out.String() != first {
		t.Errorf("a second Flush reprinted the summary:\n%s", out.String())
	}
}

func TestDenyLog_flushWithNothingRefusedIsSilent(t *testing.T) {
	var out bytes.Buffer
	(&DenyLog{W: &out, Deferred: true}).Flush()
	if out.Len() != 0 {
		t.Errorf("Flush spoke up about a session with no refusals: %q", out.String())
	}
}

// DenyLog identifies a refusal by its line, so the proxy must emit one message
// per Write. A deny path that built its line with two Fprintf calls would be
// counted as two refusals and would never deduplicate.
func TestDenyIsOneWritePerRefusal(t *testing.T) {
	w := &countingWriter{}
	p := &Proxy{
		Policy:   Policy{Allow: []string{"allowed.example.com"}, AllowPrivateDestinations: true},
		Resolver: &fakeResolver{hosts: map[string][]net.IP{}},
		Log:      w,
	}
	addr := startProxy(t, p)

	for range 3 {
		resp, err := proxyClient(t, addr).Get("https://denied.example.com/")
		if err == nil {
			resp.Body.Close()
		}
	}

	lines := w.lines()
	if got := len(lines); got != 3 {
		t.Errorf("3 refusals produced %d writes, want 3 (one per refusal):\n%s", got, w.all())
	}
	for _, line := range lines {
		if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
			t.Errorf("a refusal was not written as exactly one complete line: %q", line)
		}
	}
}

// End to end through the proxy: the retry storm the report shows becomes one
// block after the run.
func TestProxyWithDeferredDenyLog(t *testing.T) {
	var out bytes.Buffer
	d := &DenyLog{W: &out, Deferred: true}
	p := &Proxy{
		Policy:   Policy{Allow: []string{"allowed.example.com"}, AllowPrivateDestinations: true},
		Resolver: &fakeResolver{hosts: map[string][]net.IP{}},
		Log:      d,
	}
	addr := startProxy(t, p)

	for range 4 {
		resp, err := proxyClient(t, addr).Get("https://telemetry.example.com/")
		if err == nil {
			resp.Body.Close()
		}
	}
	if out.Len() != 0 {
		t.Fatalf("the terminal was written to while the session was running:\n%s", out.String())
	}

	d.Flush()
	if got := strings.Count(out.String(), "telemetry.example.com"); got != 1 {
		t.Errorf("4 refusals of one destination produced %d lines, want 1:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "(x4)") {
		t.Errorf("summary lost the number of attempts:\n%s", out.String())
	}
}

// Deferring changes only what reaches the terminal, never what the sandbox is
// told: the client still gets its 403 immediately.
func TestDeferredDenyLogStillRefusesTheRequest(t *testing.T) {
	d := &DenyLog{W: &bytes.Buffer{}, Deferred: true}
	p := &Proxy{
		Policy:   Policy{Allow: []string{"allowed.example.com"}, AllowPrivateDestinations: true},
		Resolver: &fakeResolver{hosts: map[string][]net.IP{}},
		Log:      d,
	}
	addr := startProxy(t, p)

	resp, err := proxyClient(t, addr).Get("http://denied.example.com/")
	if err != nil {
		t.Fatalf("expected a 403 response, got a transport error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}
