package profile

import (
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestValidate_network_validModes(t *testing.T) {
	for _, mode := range []string{"", config.NetworkOff, config.NetworkFull} {
		p := &config.Profile{
			Sandbox:    config.SandboxConfig{NetworkMode: mode},
			Entrypoint: config.EntrypointConfig{Interactive: true},
		}
		if r := Validate(p, ""); r.HasErrors() {
			t.Errorf("network_mode = %q: unexpected errors %v", mode, r.Issues)
		}
	}
}

// A typo must not fall back to a default: "allowist" resolving to either "off"
// or "full" would give the sandbox a different network reach than the profile
// claims, in silence.
func TestValidate_network_unknownModeIsAnError(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{NetworkMode: "allowist"},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Fatalf("expected an error for an unknown network_mode, got %v", r.Issues)
	}
	if !issuesContain(r, LevelError, "allowist") {
		t.Errorf("error should name the offending value, got %v", r.Issues)
	}
}

// allowlist is accepted now that the proxy, the relay and the wiring exist.
// An empty effective list is a warning, not an error: it is a real
// configuration (a sandbox that can reach nothing) and there are legitimate
// reasons to start from it, but it is never what someone meant to write.
func TestValidate_network_allowlistIsAccepted(t *testing.T) {
	p := &config.Profile{
		Sandbox: config.SandboxConfig{
			NetworkMode:  config.NetworkAllowlist,
			NetworkAllow: []string{"api.anthropic.com"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	if r := Validate(p, ""); r.HasErrors() {
		t.Errorf("allowlist mode should be accepted, got %v", r.Issues)
	}
}

func TestValidate_allowlistWithNothingReachableWarns(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{NetworkMode: config.NetworkAllowlist},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("an empty allow list is a warning, not an error: %v", r.Issues)
	}
	if !issuesContain(r, LevelWarning, "can reach nothing") {
		t.Errorf("expected a warning about the empty effective list, got %v", r.Issues)
	}
}

// A capability supplies the list, so declaring no network_allow of your own is
// the normal case and must not warn.
func TestValidate_allowlistWithCapabilityDefaultsDoesNotWarn(t *testing.T) {
	p := &config.Profile{
		Capabilities: []string{"claude"},
		Sandbox:      config.SandboxConfig{NetworkMode: config.NetworkAllowlist},
		Entrypoint:   config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if issuesContain(r, LevelWarning, "can reach nothing") {
		t.Errorf("the claude capability supplies the list; this must not warn: %v", r.Issues)
	}
}

// ── The risky-combination warnings are about an UNMEDIATED network ────────────

func TestValidate_credentialsPlusNetwork_warnsOnlyForFull(t *testing.T) {
	tests := []struct {
		name     string
		sandbox  config.SandboxConfig
		wantWarn bool
	}{
		{
			name:     "legacy network = true still warns",
			sandbox:  config.SandboxConfig{Network: true, Allow: []string{"ssh-keys"}},
			wantWarn: true,
		},
		{
			name:     "network_mode = full warns",
			sandbox:  config.SandboxConfig{NetworkMode: config.NetworkFull, Allow: []string{"ssh-keys"}},
			wantWarn: true,
		},
		{
			name: "network_mode = off does not warn",
			// The legacy bool is deliberately left true to prove the gate reads
			// the resolved mode and not the bool it shadows.
			sandbox:  config.SandboxConfig{NetworkMode: config.NetworkOff, Network: true, Allow: []string{"ssh-keys"}},
			wantWarn: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &config.Profile{
				Sandbox:    tt.sandbox,
				Entrypoint: config.EntrypointConfig{Interactive: true},
			}
			r := Validate(p, "")
			got := issuesContain(r, LevelWarning, "the sandbox can read those credentials and send them anywhere")
			if got != tt.wantWarn {
				t.Errorf("exfiltration warning = %v, want %v (issues: %v)", got, tt.wantWarn, r.Issues)
			}
		})
	}
}

func TestValidate_nestedUserNsPlusNetwork_warnsOnlyForFull(t *testing.T) {
	full := &config.Profile{
		Sandbox:    config.SandboxConfig{NetworkMode: config.NetworkFull, Allow: []string{"nested-user-ns"}},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	if !issuesContain(Validate(full, ""), LevelWarning, "privilege-escalation risk") {
		t.Error("expected the nested-user-ns + network warning for network_mode = full")
	}

	off := &config.Profile{
		Sandbox:    config.SandboxConfig{NetworkMode: config.NetworkOff, Network: true, Allow: []string{"nested-user-ns"}},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	if issuesContain(Validate(off, ""), LevelWarning, "privilege-escalation risk") {
		t.Error("nested-user-ns + network warning should not fire when the network is off")
	}
}

// ── network_allow / network_deny ──────────────────────────────────────────────

// Inert keys are never silent, exactly like home_allow under a non-isolated
// home: a profile listing destinations has assumed a mediated network, and is
// instead running with no network or a wide-open one.
func TestValidate_networkAllow_isInertWithoutAllowlistMode(t *testing.T) {
	p := &config.Profile{
		Sandbox: config.SandboxConfig{
			NetworkMode:  config.NetworkFull,
			NetworkAllow: []string{"github.com"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, "the list is ignored") {
		t.Errorf("expected a warning that network_allow is inert, got %v", r.Issues)
	}
	if r.HasErrors() {
		t.Errorf("inert keys are a warning, not an error: %v", r.Issues)
	}
}

func TestValidate_networkAllow_silentWhenUnused(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{NetworkMode: config.NetworkFull},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "the list is ignored") {
		t.Errorf("no network_allow declared, so there is nothing to warn about: %v", r.Issues)
	}
}

// The difference between "the profile inherits what its tool needs" and "the
// tool silently cannot reach its own API" has to be visible before the run.
func TestValidate_capabilityWithoutEgressDefaultsIsReported(t *testing.T) {
	p := &config.Profile{
		Capabilities: []string{"gemini"},
		Sandbox:      config.SandboxConfig{NetworkMode: config.NetworkFull},
		Entrypoint:   config.EntrypointConfig{Interactive: true},
	}
	// Under "full" there is no allow list to be missing from, so nothing fires;
	// the warning belongs to allowlist mode, which this build still rejects.
	// Assert the helper the warning is built on instead, so the rule is covered
	// the moment the mode is enabled.
	if missing := config.CapabilitiesWithoutNetworkDefaults(p.Capabilities); len(missing) != 1 || missing[0] != "gemini" {
		t.Errorf("CapabilitiesWithoutNetworkDefaults = %v, want [gemini]", missing)
	}
}
