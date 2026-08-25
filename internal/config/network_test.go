package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ── ResolveNetworkMode: the legacy fallback ───────────────────────────────────

func TestResolveNetworkMode(t *testing.T) {
	tests := []struct {
		name string
		sb   SandboxConfig
		want string
	}{
		{"unset falls back to off", SandboxConfig{}, NetworkOff},
		{"legacy true falls back to full", SandboxConfig{Network: true}, NetworkFull},
		{"legacy false falls back to off", SandboxConfig{Network: false}, NetworkOff},
		{"explicit mode wins over legacy false", SandboxConfig{NetworkMode: NetworkFull}, NetworkFull},
		{"explicit mode wins over legacy true", SandboxConfig{NetworkMode: NetworkOff, Network: true}, NetworkOff},
		{"unknown mode is returned as-is for the caller to reject", SandboxConfig{NetworkMode: "bogus"}, "bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveNetworkMode(tt.sb); got != tt.want {
				t.Errorf("ResolveNetworkMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// EffectiveNetworkMode is the RunConfig-side twin of the same fallback: a
// RunConfig built by hand (tests, older callers) must still report a mode.
func TestRunConfig_EffectiveNetworkMode(t *testing.T) {
	if got := (RunConfig{Network: true}).EffectiveNetworkMode(); got != NetworkFull {
		t.Errorf("legacy Network=true → %q, want %q", got, NetworkFull)
	}
	if got := (RunConfig{}).EffectiveNetworkMode(); got != NetworkOff {
		t.Errorf("zero value → %q, want %q", got, NetworkOff)
	}
	if got := (RunConfig{NetworkMode: NetworkOff, Network: true}).EffectiveNetworkMode(); got != NetworkOff {
		t.Errorf("explicit mode must win over the bool, got %q", got)
	}
}

// ── merge: network and network_mode across extends ────────────────────────────

// writeProfile writes a profile file and returns its path.
func writeProfile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// The interesting case, and the reason the two fields are resolved together
// rather than overridden one by one: a base declaring network_mode and a child
// declaring the legacy bool. A naive per-field override leaves the base's mode
// in place, so a child that wrote network = false silently keeps network access.
func TestMerge_networkModeAndBoolAcrossExtends(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		child    string
		wantMode string
	}{
		{
			name:     "child network_mode wins over base bool",
			base:     "[sandbox]\nnetwork = true\n",
			child:    "[sandbox]\nnetwork_mode = \"off\"\n",
			wantMode: NetworkOff,
		},
		{
			name:     "child legacy bool wins over inherited network_mode",
			base:     "[sandbox]\nnetwork_mode = \"full\"\n",
			child:    "[sandbox]\nnetwork = false\n",
			wantMode: NetworkOff,
		},
		{
			name:     "child legacy bool can also open an inherited off",
			base:     "[sandbox]\nnetwork_mode = \"off\"\n",
			child:    "[sandbox]\nnetwork = true\n",
			wantMode: NetworkFull,
		},
		{
			name:     "child declaring neither inherits the base mode",
			base:     "[sandbox]\nnetwork_mode = \"full\"\n",
			child:    "[sandbox]\nclipboard = true\n",
			wantMode: NetworkFull,
		},
		{
			name:     "child declaring neither inherits the base bool",
			base:     "[sandbox]\nnetwork = true\n",
			child:    "[sandbox]\nclipboard = true\n",
			wantMode: NetworkFull,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			profiles := filepath.Join(dir, "profiles")
			if err := os.MkdirAll(profiles, 0o755); err != nil {
				t.Fatal(err)
			}
			writeProfile(t, profiles, "base", "schema_version = \"1\"\nname = \"base\"\n"+tt.base)
			writeProfile(t, profiles, "child",
				"schema_version = \"1\"\nname = \"child\"\nextends = \"base\"\n"+tt.child+
					"\n[entrypoint]\ncmd = \"sh\"\n")

			l := NewLoader(dir)
			p, err := l.LoadProfileAuto("child")
			if err != nil {
				t.Fatalf("LoadProfileAuto: %v", err)
			}
			if got := ResolveNetworkMode(p.Sandbox); got != tt.wantMode {
				t.Errorf("resolved mode = %q, want %q", got, tt.wantMode)
			}
			// The merge must also leave the two fields agreeing, so no consumer
			// still reading the bool can disagree with one reading the mode.
			if wantBool := tt.wantMode != NetworkOff; p.Sandbox.Network != wantBool {
				t.Errorf("legacy Network = %v, want %v (mode %q)", p.Sandbox.Network, wantBool, tt.wantMode)
			}
		})
	}
}

// ── loader: an unrecognised mode is refused, never defaulted ──────────────────

func TestBuild_rejectsUnknownNetworkMode(t *testing.T) {
	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, profiles, "typo",
		"schema_version = \"1\"\nname = \"typo\"\n[sandbox]\nnetwork_mode = \"allowist\"\n[entrypoint]\ncmd = \"sh\"\n")

	if _, err := NewLoader(dir).Build("typo"); err == nil {
		t.Fatal("expected Build to reject an unknown network_mode, got nil error")
	}
}

func TestBuild_setsNetworkModeAndBoolCoherently(t *testing.T) {
	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, profiles, "legacy",
		"schema_version = \"1\"\nname = \"legacy\"\n[sandbox]\nnetwork = true\n[entrypoint]\ncmd = \"sh\"\n")

	rc, err := NewLoader(dir).Build("legacy")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.NetworkMode != NetworkFull {
		t.Errorf("NetworkMode = %q, want %q", rc.NetworkMode, NetworkFull)
	}
	if !rc.Network {
		t.Error("Network should stay true for a legacy network = true profile")
	}
}
