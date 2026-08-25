package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/enr/inner/internal/config"
)

// appendHomeAllowIfHidden re-exposes host paths that an isolated home would
// otherwise erase, and only those.
//
// With home = "isolated" $HOME is replaced by an empty tmpfs, so anything
// inner itself needs to reach from inside — the inner binary when it is
// installed under ~/.local/bin, the profiles directory `inner verify --inside`
// re-reads, the unix socket a future network proxy hands to the sandbox —
// disappears unless it is named in home_allow.
//
// Paths outside $HOME are left alone: they are already reachable through the
// read-only root bind, and adding them would grow the allowlist with entries
// that grant nothing. Under home = "host-ro" the whole call is a no-op, since
// nothing was hidden in the first place. Duplicates are skipped so repeated
// calls (verify adds two paths, the proxy wiring adds one more) stay idempotent.
//
// Two callers share this: cmd_verify.go for its --inside re-invocation, and
// cmd_run.go for anything the sandbox needs from inner's own installation.
func appendHomeAllowIfHidden(rc *config.RunConfig, paths ...string) {
	if !rc.HomeIsolated() {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return // cannot reason about what is under $HOME on this machine
	}
	prefix := filepath.Clean(home) + string(os.PathSeparator)
	for _, p := range paths {
		if p == "" || !strings.HasPrefix(p, prefix) {
			continue
		}
		if slices.Contains(rc.HomeAllow, p) {
			continue
		}
		rc.HomeAllow = append(rc.HomeAllow, p)
	}
}
