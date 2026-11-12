package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrust_flagsChangedFiles(t *testing.T) {
	a, _, workDir := newTestAppWithWorkDir(t)
	cfg := filepath.Join(workDir, "inner.toml")
	prof := filepath.Join(workDir, ".config", "inner", "profiles", "dev.toml")
	writeTestFile(t, cfg, "default_profile = \"dev\"\n")
	writeTestFile(t, prof, "name = \"dev\"\n")

	var out bytes.Buffer
	if err := a.trust_(&out, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "— new"); n != 2 {
		t.Fatalf("first trust: want 2 new, got:\n%s", out.String())
	}

	writeTestFile(t, prof, "name = \"dev\"\n[env]\ninherit_all = true\n")
	out.Reset()
	if err := a.trust_(&out, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("second trust: want 2 lines, got:\n%s", out.String())
	}
	// The changed file comes first, flagged; the unchanged one is not re-announced as trusted.
	if !strings.Contains(lines[0], prof) || !strings.Contains(lines[0], "CHANGED") {
		t.Errorf("line 0 = %q, want %s flagged CHANGED", lines[0], prof)
	}
	if !strings.HasPrefix(lines[1], "already trusted ") || !strings.Contains(lines[1], cfg) {
		t.Errorf("line 1 = %q, want %s already trusted", lines[1], cfg)
	}
}
