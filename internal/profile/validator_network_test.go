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

// The name is reserved so the merge and resolution rules could be written once,
// but this build cannot enforce it — so it must be refused loudly rather than
// degrade into "off" (a broken agent) or "full" (a false sense of protection).
func TestValidate_network_allowlistIsReservedNotSilent(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{NetworkMode: config.NetworkAllowlist},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Fatalf("expected an error for the not-yet-implemented allowlist mode, got %v", r.Issues)
	}
	if !issuesContain(r, LevelError, "not implemented yet") {
		t.Errorf("error should say the mode is not implemented yet, got %v", r.Issues)
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
