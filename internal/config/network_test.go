package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// ── Allowlist layers ──────────────────────────────────────────────────────────

// The point of the whole layering: a profile declaring capabilities = ["claude"]
// inherits the destinations Claude Code needs without knowing what they are, and
// adds its own on top.
func TestResolveNetworkAllow_capabilityDefaultsAreInherited(t *testing.T) {
	sb := SandboxConfig{NetworkAllow: []string{"github.com"}}
	allow, origins := ResolveNetworkAllow(sb, []string{"claude"})

	if !slices.Contains(allow, "api.anthropic.com") {
		t.Errorf("the claude capability did not contribute its API endpoint: %v", allow)
	}
	if !slices.Contains(allow, "github.com") {
		t.Errorf("the profile's own entry was dropped: %v", allow)
	}
	if got := origins["api.anthropic.com"]; got != "capability:claude" {
		t.Errorf("origin of the capability entry = %q, want %q", got, "capability:claude")
	}
	if got := origins["github.com"]; got != "profile" {
		t.Errorf("origin of the profile entry = %q, want %q", got, "profile")
	}
}

// The endpoints a real Claude Code session was observed to need but the first
// list did not carry: an update check, the changelog fetch after it, and the
// telemetry intake. Each one cost a user a blocked request and a corrupted TUI
// before it was found, so pin them — dropping one must be a decision, not a
// side effect of tidying the list.
func TestResolveNetworkAllow_claudeCarriesTheEndpointsASessionActuallyUses(t *testing.T) {
	allow, _ := ResolveNetworkAllow(SandboxConfig{}, []string{"claude"})
	for _, host := range []string{
		"api.anthropic.com",
		"platform.claude.com",                // OAuth exchange/refresh: without it the session 401s mid-run
		"downloads.claude.ai",                // update checks and the native updater
		"raw.githubusercontent.com",          // the changelog shown after updating
		"http-intake.logs.us5.datadoghq.com", // telemetry intake
	} {
		if !slices.Contains(allow, host) {
			t.Errorf("the claude capability no longer contributes %q: %v", host, allow)
		}
	}
}

// A capability default is an egress destination granted to every profile that
// names the capability, so the broad ones stay opt-in. Both of these are real
// rows of the vendor's table, deliberately left out; see the comment there.
func TestResolveNetworkAllow_claudeDoesNotOpenTheBroadThirdPartyHosts(t *testing.T) {
	allow, _ := ResolveNetworkAllow(SandboxConfig{}, []string{"claude"})
	for _, host := range []string{"storage.googleapis.com", "registry.npmjs.org"} {
		if slices.Contains(allow, host) {
			t.Errorf("%q is granted by default; a profile that wants it must list it: %v", host, allow)
		}
	}
}

// "Why is this domain open?" must have an answer even when two layers agree,
// because "the profile also lists it" and "only the capability brings it" call
// for different edits.
func TestResolveNetworkAllow_recordsEveryContributingLayer(t *testing.T) {
	sb := SandboxConfig{NetworkAllow: []string{"api.anthropic.com"}}
	allow, origins := ResolveNetworkAllow(sb, []string{"claude"})

	if n := countOccurrences(allow, "api.anthropic.com"); n != 1 {
		t.Errorf("entry appears %d times, want deduplicated to 1: %v", n, allow)
	}
	got := origins["api.anthropic.com"]
	if !strings.Contains(got, "capability:claude") || !strings.Contains(got, "profile") {
		t.Errorf("origin = %q, want both contributing layers named", got)
	}
}

// The effective list is rendered in --dry-run and compared in tests, so it must
// not depend on the order the profile happened to list its capabilities in.
func TestResolveNetworkAllow_isDeterministic(t *testing.T) {
	sb := SandboxConfig{NetworkAllow: []string{"b.example.com", "a.example.com"}}
	first, _ := ResolveNetworkAllow(sb, []string{"claude", "gemini"})
	second, _ := ResolveNetworkAllow(sb, []string{"gemini", "claude"})
	if !slices.Equal(first, second) {
		t.Errorf("capability order changed the result:\n %v\n %v", first, second)
	}
}

func TestResolveNetworkAllow_skipsEmptyEntries(t *testing.T) {
	sb := SandboxConfig{NetworkAllow: []string{"", "  ", "github.com"}}
	allow, _ := ResolveNetworkAllow(sb, nil)
	if !slices.Equal(allow, []string{"github.com"}) {
		t.Errorf("allow = %v, want only the non-empty entry", allow)
	}
}

// network_deny is NOT subtracted here: it is evaluated per-request by the proxy,
// which is what lets a deny pattern carve a hole out of a broader allow pattern
// (netproxy.Policy covers that). Pinned so nobody "simplifies" it into a list
// subtraction and silently loses wildcard denies.
func TestResolveNetworkAllow_denyIsNotSubtractedFromTheList(t *testing.T) {
	const telemetry = "http-intake.logs.us5.datadoghq.com" // contributed by the claude capability
	sb := SandboxConfig{
		NetworkAllow: []string{"github.com"},
		NetworkDeny:  []string{telemetry},
	}
	allow, _ := ResolveNetworkAllow(sb, []string{"claude"})
	if !slices.Contains(allow, telemetry) {
		t.Error("the resolved allow list should still carry the entry; the deny is applied by the proxy at request time")
	}
}

// A capability with no confirmed egress data must contribute nothing rather than
// something invented — an invented domain is worse than an absent one, because
// it looks verified.
func TestCapabilitiesWithoutNetworkDefaults(t *testing.T) {
	missing := CapabilitiesWithoutNetworkDefaults([]string{"claude", "gemini"})
	if slices.Contains(missing, "claude") {
		t.Error("claude has defaults and must not be reported as missing")
	}
	if !slices.Contains(missing, "gemini") {
		t.Error("gemini has no confirmed defaults and must be reported so the profile lists them by hand")
	}
}

// ── merge across extends ──────────────────────────────────────────────────────

// The lists only ever add: a child extends its base's destinations rather than
// replacing them. Narrowing is a separate, explicit act (network_deny).
func TestMerge_networkAllowAndDenyUnion(t *testing.T) {
	dir := t.TempDir()
	profiles := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, profiles, "base", `schema_version = "1"
name = "base"
[sandbox]
network_allow = ["github.com"]
network_deny  = ["sentry.io"]
`)
	writeProfile(t, profiles, "child", `schema_version = "1"
name = "child"
extends = "base"
[sandbox]
network_allow = ["*.githubusercontent.com"]
network_deny  = ["telemetry.example.com"]
[entrypoint]
cmd = "sh"
`)

	p, err := NewLoader(dir).LoadProfileAuto("child")
	if err != nil {
		t.Fatalf("LoadProfileAuto: %v", err)
	}
	for _, want := range []string{"github.com", "*.githubusercontent.com"} {
		if !slices.Contains(p.Sandbox.NetworkAllow, want) {
			t.Errorf("network_allow = %v, missing %q", p.Sandbox.NetworkAllow, want)
		}
	}
	for _, want := range []string{"sentry.io", "telemetry.example.com"} {
		if !slices.Contains(p.Sandbox.NetworkDeny, want) {
			t.Errorf("network_deny = %v, missing %q", p.Sandbox.NetworkDeny, want)
		}
	}
}

func countOccurrences(xs []string, want string) int {
	n := 0
	for _, x := range xs {
		if x == want {
			n++
		}
	}
	return n
}
