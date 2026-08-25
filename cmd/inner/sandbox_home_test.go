package main

import (
	"path/filepath"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestAppendHomeAllowIfHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	inHome := filepath.Join(home, ".local", "bin", "inner")
	otherInHome := filepath.Join(home, ".config", "inner")
	outsideHome := "/usr/local/bin/inner"

	t.Run("adds only paths under an isolated home", func(t *testing.T) {
		rc := &config.RunConfig{HomeMode: config.HomeIsolated}
		appendHomeAllowIfHidden(rc, inHome, outsideHome, otherInHome, "")

		want := []string{inHome, otherInHome}
		if len(rc.HomeAllow) != len(want) {
			t.Fatalf("HomeAllow = %v, want %v", rc.HomeAllow, want)
		}
		for i, p := range want {
			if rc.HomeAllow[i] != p {
				t.Errorf("HomeAllow[%d] = %q, want %q", i, rc.HomeAllow[i], p)
			}
		}
	})

	// Under host-ro nothing was hidden, so re-exposing anything would only grow
	// the allowlist with entries that grant nothing.
	t.Run("no-op under host-ro", func(t *testing.T) {
		rc := &config.RunConfig{HomeMode: config.HomeHostRO}
		appendHomeAllowIfHidden(rc, inHome)
		if len(rc.HomeAllow) != 0 {
			t.Errorf("HomeAllow = %v, want empty under host-ro", rc.HomeAllow)
		}
	})

	// Several callers append to the same run (verify adds two paths, the future
	// network-proxy wiring adds one more), so repeated calls must not duplicate.
	t.Run("idempotent", func(t *testing.T) {
		rc := &config.RunConfig{HomeMode: config.HomeIsolated}
		appendHomeAllowIfHidden(rc, inHome)
		appendHomeAllowIfHidden(rc, inHome)
		if len(rc.HomeAllow) != 1 {
			t.Errorf("HomeAllow = %v, want a single entry", rc.HomeAllow)
		}
	})

	// A path that merely shares a textual prefix with $HOME is not under it.
	t.Run("prefix is a path boundary, not a string prefix", func(t *testing.T) {
		rc := &config.RunConfig{HomeMode: config.HomeIsolated}
		appendHomeAllowIfHidden(rc, home+"-backup/secret")
		if len(rc.HomeAllow) != 0 {
			t.Errorf("HomeAllow = %v, want empty (sibling directory is not under $HOME)", rc.HomeAllow)
		}
	})
}
