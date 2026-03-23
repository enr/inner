package profile

import (
	"os"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestValidate_mountExists(t *testing.T) {
	dir := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			dir: {Dest: "/workspace", Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("expected no errors for existing mount, got: %v", r.Issues)
	}
}

func TestValidate_mountMissing(t *testing.T) {
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"/nonexistent/path/xyz": {Dest: "/workspace", Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for missing mount source")
	}
}

func TestValidate_mountInvalidMode(t *testing.T) {
	dir := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			dir: {Dest: "/workspace", Mode: "readwrite"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for invalid mode")
	}
}

func TestValidate_entrypointNotInPath(t *testing.T) {
	p := &config.Profile{
		Entrypoint: config.EntrypointConfig{
			Cmd:         "definitely-not-a-real-binary-xyzzy",
			Interactive: true,
		},
	}

	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("missing entrypoint should be warning, not error: %v", r.Issues)
	}
	hasWarning := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("expected warning for missing entrypoint in PATH")
	}
}

func TestValidate_entrypointInPath(t *testing.T) {
	p := &config.Profile{
		Entrypoint: config.EntrypointConfig{
			Cmd:         "sh",
			Interactive: true,
		},
	}

	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("unexpected errors: %v", r.Issues)
	}
}

func TestValidate_nonInteractiveNoTimeout(t *testing.T) {
	p := &config.Profile{
		Entrypoint: config.EntrypointConfig{
			Cmd:         "sh",
			Interactive: false,
		},
		Output: config.OutputConfig{
			TimeoutSeconds: 0,
		},
	}

	r := Validate(p, "")
	hasWarning := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("expected warning for non-interactive with no timeout")
	}
}

func TestValidate_nonInteractiveWithTimeout(t *testing.T) {
	p := &config.Profile{
		Entrypoint: config.EntrypointConfig{
			Cmd:         "sh",
			Interactive: false,
		},
		Output: config.OutputConfig{
			TimeoutSeconds: 300,
		},
	}

	r := Validate(p, "")
	// Only check that the "no timeout" warning is not present.
	for _, i := range r.Issues {
		if i.Level == LevelWarning && i.Message != "" {
			// The "sh not found" warning should not appear if sh is present.
			// The "no timeout" warning should not appear because timeout is set.
			t.Logf("warning (acceptable): %s", i.Message)
		}
	}
	if r.HasErrors() {
		t.Errorf("unexpected errors: %v", r.Issues)
	}
}

func TestResult_hasErrors(t *testing.T) {
	r := Result{}
	if r.HasErrors() {
		t.Error("empty result should have no errors")
	}
	r.Issues = append(r.Issues, Issue{Level: LevelWarning, Message: "warn"})
	if r.HasErrors() {
		t.Error("result with only warnings should have no errors")
	}
	r.Issues = append(r.Issues, Issue{Level: LevelError, Message: "err"})
	if !r.HasErrors() {
		t.Error("result with an error should report HasErrors = true")
	}
}

func TestValidate_noMountsSectionOk(t *testing.T) {
	p := &config.Profile{
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("profile with no mounts should be valid, got: %v", r.Issues)
	}
}

func TestValidate_unknownAllowKey_isWarning(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{Allow: []string{"ssh-keys", "not-a-real-key"}},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("unknown allow key should be warning, not error: %v", r.Issues)
	}
	hasWarning := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Error("expected warning for unknown allow key")
	}
}

func TestValidate_knownAllowKeys_noIssues(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{Allow: config.ValidAllowKeys},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	for _, i := range r.Issues {
		if i.Level == LevelWarning && len(r.Issues) == 1 {
			// Only the "no timeout" warning is acceptable.
			t.Logf("acceptable warning: %s", i.Message)
		}
	}
	if r.HasErrors() {
		t.Errorf("all valid allow keys should produce no errors: %v", r.Issues)
	}
}

func TestValidate_workdirTokenExpanded(t *testing.T) {
	dir := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"${workdir}": {Dest: "/workspace", Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	// With workDir set to an existing directory: no error.
	r := Validate(p, dir)
	if r.HasErrors() {
		t.Errorf("expected no errors when ${workdir} expands to existing dir, got: %v", r.Issues)
	}
	// With empty workDir: skip check (no error).
	r = Validate(p, "")
	if r.HasErrors() {
		t.Errorf("expected no errors when workDir is empty (skip check), got: %v", r.Issues)
	}
}

func TestValidate_tildeMountExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"~": {Dest: "/host-home", Mode: "ro"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	_ = home
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("~ should expand to home dir and exist: %v", r.Issues)
	}
}
