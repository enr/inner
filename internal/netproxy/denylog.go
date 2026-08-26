package netproxy

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// DenyLog is the io.Writer a run gives Proxy.Log to make the refusal stream
// survivable on a terminal something else is drawing on.
//
// The proxy runs on the HOST, and writes to the same terminal the sandboxed
// process is using. For a line-oriented program that is fine — the refusal
// scrolls past with everything else. For a full-screen TUI (claude, gemini) it
// is not: the TUI owns the screen, positions the cursor itself, and has no idea
// a second writer exists, so a line landing mid-frame is painted into whatever
// the TUI had drawn there. The result is the corrupted display in the report
// this type comes from, where a refusal overwrote the prompt box.
//
// Two failure modes, one type:
//
//   - Volume. A tool that retries a blocked endpoint (telemetry, an update
//     check) emits the same refusal every few seconds. Only the FIRST occurrence
//     of a given line carries information; the rest are noise no matter where
//     they are printed. Repeats are counted and suppressed.
//
//   - Placement. With Deferred set nothing is written while the run is in
//     progress; Flush prints everything afterwards, once the child has exited
//     and given the terminal back.
//
// The deferred output is the same lines the proxy would have written, replayed
// verbatim: what the user sees under a TUI is what they would have seen without
// one — same words, same reasons — only later, and without the duplicates.
//
// A DenyLog is safe for concurrent use; the proxy serves every connection on
// its own goroutine.
type DenyLog struct {
	// W receives the lines. Defaults to os.Stderr.
	W io.Writer
	// Deferred holds every line until Flush instead of writing it as it
	// happens. Set for a TUI entrypoint sharing our terminal.
	Deferred bool

	mu    sync.Mutex
	order []string       // distinct lines, in the order they first occurred
	count map[string]int // line → how many times it occurred
}

// Write records one refusal.
//
// It takes exactly one message per call, which is what Proxy.deny does and what
// TestDenyIsOneWritePerRefusal pins: deduplication is per line, so a writer
// that split a message across calls would defeat it.
func (d *DenyLog) Write(p []byte) (int, error) {
	line := string(p)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.count == nil {
		d.count = make(map[string]int)
	}
	first := d.count[line] == 0
	d.count[line]++
	if first {
		d.order = append(d.order, line)
	}

	if d.Deferred || !first {
		// Report success regardless: the caller is a refusal path with nowhere
		// useful to send a write error, and "the line was accepted" is true —
		// it is held, or it is already on screen.
		return len(p), nil
	}
	return d.w().Write(p)
}

// Flush writes the summary of everything that was refused during the run.
//
// Callers invoke it once, after the sandboxed process has exited. It is a no-op
// when nothing was refused, and when the log is not Deferred — there the lines
// went out as they happened and reprinting them would say nothing new.
func (d *DenyLog) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.Deferred || len(d.order) == 0 {
		return
	}

	w := d.w()
	fmt.Fprintf(w, "inner: network-allowlist: %s refused while the session was running:\n", plural(len(d.order), "destination was", "destinations were"))
	for _, line := range d.order {
		if n := d.count[line]; n > 1 {
			// Trim the newline the proxy wrote so the count lands on the line
			// it belongs to rather than under it.
			fmt.Fprintf(w, "%s (x%d)\n", trimNewline(line), n)
			continue
		}
		fmt.Fprint(w, line)
	}
	fmt.Fprint(w, "inner: network-allowlist: add what the tool needs to network_allow in the profile.\n")

	// Reset so a second Flush (a cleanup that runs twice, a long-lived proxy)
	// does not reprint a session that has already been reported.
	d.order = nil
	d.count = nil
}

func (d *DenyLog) w() io.Writer {
	if d.W != nil {
		return d.W
	}
	return os.Stderr
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
