package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// issuesContain reports whether any issue of the given level mentions substr.
func issuesContain(r Result, level Level, substr string) bool {
	for _, i := range r.Issues {
		if i.Level == level && strings.Contains(i.Message, substr) {
			return true
		}
	}
	return false
}

func TestValidate_home_validModes(t *testing.T) {
	for _, mode := range []string{"", config.HomeHostRO, config.HomeIsolated} {
		p := &config.Profile{
			Sandbox:    config.SandboxConfig{Home: mode},
			Entrypoint: config.EntrypointConfig{Interactive: true},
		}
		if r := Validate(p, ""); r.HasErrors() {
			t.Errorf("home = %q: unexpected errors %v", mode, r.Issues)
		}
	}
}

func TestValidate_home_unknownModeIsAnError(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{Home: "isolate"},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Fatalf("expected an error for an unknown home mode, got %v", r.Issues)
	}
	if !issuesContain(r, LevelError, "invalid [sandbox] home") {
		t.Errorf("error message does not name the offending key: %v", r.Issues)
	}
}

func TestValidate_homeAllow_withoutIsolatedWarns(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{HomeAllow: []string{"~/.local/bin"}},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Fatalf("unexpected errors: %v", r.Issues)
	}
	if !issuesContain(r, LevelWarning, "home_allow has no effect") {
		t.Errorf("expected a no-effect warning, got %v", r.Issues)
	}
}

func TestValidate_homeAllow_wholeHomeIsAnError(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	p := &config.Profile{
		Sandbox: config.SandboxConfig{
			Home:      config.HomeIsolated,
			HomeAllow: []string{home},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !issuesContain(r, LevelError, "re-exposes the whole home directory") {
		t.Errorf("expected an error for allowlisting the whole home, got %v", r.Issues)
	}
}

func TestValidate_homeAllow_sensitivePathWarns(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	p := &config.Profile{
		Sandbox: config.SandboxConfig{
			Home:      config.HomeIsolated,
			HomeAllow: []string{filepath.Join(home, ".ssh")},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, "ssh-keys") {
		t.Errorf("expected a warning about re-exposing ssh keys, got %v", r.Issues)
	}

	// Declaring the matching allow key means the exposure is deliberate and
	// already visible in `inner verify` — no second warning about it.
	p.Sandbox.Allow = []string{"ssh-keys"}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "normally hidden by") {
		t.Errorf("unexpected warning when the allow key is declared, got %v", r.Issues)
	}
}

func TestValidate_homeAllow_outsideHomeWarns(t *testing.T) {
	p := &config.Profile{
		Sandbox: config.SandboxConfig{
			Home:      config.HomeIsolated,
			HomeAllow: []string{"/opt/toolchain"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, "outside the home directory") {
		t.Errorf("expected a warning for a path outside the home, got %v", r.Issues)
	}
}

func TestValidate_isolatedHome_mountDestNeedNotExist(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	src := t.TempDir()
	// bwrap creates this mount point inside the home tmpfs, so requiring it on
	// the host would reject a perfectly valid profile.
	dest := filepath.Join(home, ".config", "inner-does-not-exist-"+filepath.Base(src))

	p := &config.Profile{
		Sandbox:    config.SandboxConfig{Home: config.HomeIsolated},
		Mounts:     map[string]config.MountEntry{src: {Dest: dest, Mode: "ro"}},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	if r := Validate(p, ""); r.HasErrors() {
		t.Errorf("unexpected errors for a dest inside the isolated home: %v", r.Issues)
	}

	// Without home isolation the dest must exist: bwrap cannot create it on the
	// read-only root bind.
	p.Sandbox.Home = ""
	if r := Validate(p, ""); !r.HasErrors() {
		t.Error("expected a missing-dest error under the host-ro model")
	}

	// Nor can it create one inside a read-only home_allow bind: there the dest
	// has to exist on the host, exactly as under host-ro.
	p.Sandbox.Home = config.HomeIsolated
	p.Sandbox.HomeAllow = []string{filepath.Join(home, ".config")}
	if r := Validate(p, ""); !r.HasErrors() {
		t.Error("expected a missing-dest error for a mount inside an allowlisted read-only subtree")
	}
}
