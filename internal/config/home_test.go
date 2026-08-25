package config

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestBuild_homeMode_andAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "agent.toml"), `
schema_version = "1"
name = "agent"

[sandbox]
home       = "isolated"
home_allow = ["~/.local/bin", "$HOME/.nvm", ""]

[entrypoint]
cmd = "sh"
`)
	l := NewLoader(dir)
	rc, err := l.Build("agent")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.HomeMode != HomeIsolated {
		t.Errorf("HomeMode = %q, want %q", rc.HomeMode, HomeIsolated)
	}
	if !rc.HomeIsolated() {
		t.Error("HomeIsolated() = false, want true")
	}
	// ~ and $HOME are expanded; empty entries are dropped so they never reach
	// bwrap as a bind of "".
	if len(rc.HomeAllow) != 2 {
		t.Fatalf("HomeAllow = %v, want 2 expanded entries", rc.HomeAllow)
	}
	for _, p := range rc.HomeAllow {
		if !filepath.IsAbs(p) {
			t.Errorf("HomeAllow entry %q is not absolute", p)
		}
	}
}

func TestBuild_homeMode_defaultsToHostRO(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "plain.toml"), `
schema_version = "1"
name = "plain"

[entrypoint]
cmd = "sh"
`)
	rc, err := NewLoader(dir).Build("plain")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.HomeMode != "" {
		t.Errorf("HomeMode = %q, want empty (host-ro)", rc.HomeMode)
	}
	if rc.HomeIsolated() {
		t.Error("a profile without [sandbox] home must not isolate the home directory")
	}
}

func TestLoadProfile_extends_homeMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name = "base"

[sandbox]
home       = "isolated"
home_allow = ["~/.local/bin"]

[entrypoint]
cmd = "sh"
`)
	// A child adding toolchain paths: home_allow is unioned, home is inherited.
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"

[sandbox]
home_allow = ["~/.asdf"]
`)
	// A child opting back out: the scalar wins.
	writeFile(t, filepath.Join(dir, "profiles", "optout.toml"), `
extends = "base"

[sandbox]
home = "host-ro"
`)
	l := NewLoader(dir)

	child, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile(child): %v", err)
	}
	if child.Sandbox.Home != HomeIsolated {
		t.Errorf("child home = %q, want inherited %q", child.Sandbox.Home, HomeIsolated)
	}
	want := []string{"~/.local/bin", "~/.asdf"}
	if !slices.Equal(child.Sandbox.HomeAllow, want) {
		t.Errorf("child home_allow = %v, want %v", child.Sandbox.HomeAllow, want)
	}

	optout, err := l.LoadProfile("optout")
	if err != nil {
		t.Fatalf("LoadProfile(optout): %v", err)
	}
	if optout.Sandbox.Home != HomeHostRO {
		t.Errorf("optout home = %q, want %q", optout.Sandbox.Home, HomeHostRO)
	}
	if !slices.Equal(optout.Sandbox.HomeAllow, []string{"~/.local/bin"}) {
		t.Errorf("optout home_allow = %v, want the base entries", optout.Sandbox.HomeAllow)
	}
}

func TestSensitiveResources_coverKnownKeys(t *testing.T) {
	resources := SensitiveResources("/home/tester", "1000")
	seen := make(map[string]bool, len(resources))
	for _, r := range resources {
		if !filepath.IsAbs(r.Path) {
			t.Errorf("resource %q has a non-absolute path %q", r.Key, r.Path)
		}
		seen[r.Key] = true
	}
	// Every filesystem hide key must be declared exactly once: a key in
	// ValidAllowKeys with no resource would be silently unenforceable.
	verifyOnly := []string{"nested-user-ns", "env-secrets", "shims-active", "network-policy"}
	for _, key := range ValidAllowKeys {
		if slices.Contains(verifyOnly, key) {
			continue
		}
		if !seen[key] {
			t.Errorf("allow key %q has no entry in SensitiveResources", key)
		}
	}
}

func TestReexposedInHome(t *testing.T) {
	rc := RunConfig{
		HomeAllow: []string{"/home/tester/.local/bin"},
		Mounts: []Mount{
			{Src: "/tmp/x", Dest: "/home/tester/.claude", Mode: "rw"},
			{Src: "", Dest: "/home/tester/.cache", Mode: "tmpfs"},
		},
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/tester/.local/bin", true},
		{"/home/tester/.local/bin/claude", true},
		{"/home/tester/.local/bindir", false},
		{"/home/tester/.claude/settings.json", true},
		{"/home/tester/.cache/anything", false}, // a tmpfs re-empties, it does not expose
		{"/home/tester/.ssh/id_rsa", false},
	}
	for _, c := range cases {
		if got := rc.ReexposedInHome(c.path); got != c.want {
			t.Errorf("ReexposedInHome(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
