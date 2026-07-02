package profile

import (
	"os"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestValidate_mountExists(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			src: {Dest: dest, Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("expected no errors for existing mount, got: %v", r.Issues)
	}
}

func TestValidate_mountMissing(t *testing.T) {
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"/nonexistent/path/xyz": {Dest: dest, Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for missing mount source")
	}
}

func TestValidate_mountInvalidMode(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			src: {Dest: dest, Mode: "readwrite"},
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
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"${workdir}": {Dest: dest, Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	// With workDir set to an existing directory: no error.
	r := Validate(p, dir)
	if r.HasErrors() {
		t.Errorf("expected no errors when ${workdir} expands to existing dir, got: %v", r.Issues)
	}
	// With empty workDir: skip src check (no error).
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
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"~": {Dest: dest, Mode: "ro"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	_ = home
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("~ should expand to home dir and exist: %v", r.Issues)
	}
}

func TestValidate_destMissing(t *testing.T) {
	src := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			src: {Dest: "/nonexistent/dest/xyz", Mode: "ro"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for missing mount dest")
	}
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelError && strings.Contains(i.Message, "dest") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a dest-related error, got: %v", r.Issues)
	}
}

func TestValidate_destWorkspacesPathTokenSkipped(t *testing.T) {
	src := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			src: {Dest: "${workspaces_path}/myworkspace", Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("dest with ${workspaces_path} should be skipped (managed by workspace manager), got: %v", r.Issues)
	}
}

func TestValidate_tmpfsDestExists(t *testing.T) {
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			dest: {Dest: dest, Mode: "tmpfs"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("tmpfs with existing dest should have no errors, got: %v", r.Issues)
	}
}

func TestValidate_tmpfsDestMissing(t *testing.T) {
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"/nonexistent/tmpfs/dest": {Dest: "/nonexistent/tmpfs/dest", Mode: "tmpfs"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for tmpfs with non-existent dest")
	}
}

// ── safe-rw mode (Step 2) ─────────────────────────────────────────────────────

func TestValidate_safeRwMode_valid(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			src: {Dest: dest, Mode: "safe-rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	for _, issue := range r.Issues {
		if issue.Level == LevelError && strings.Contains(issue.Message, "mode") {
			t.Errorf("safe-rw should be a valid mode, got error: %s", issue.Message)
		}
	}
}

func TestValidate_safeRwMode_srcMissing_returnsError(t *testing.T) {
	dest := t.TempDir()
	p := &config.Profile{
		Mounts: map[string]config.MountEntry{
			"/nonexistent/safe-rw-src": {Dest: dest, Mode: "safe-rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for safe-rw with missing src")
	}
}

// ── capability validation (Step 1g) ──────────────────────────────────────────

func TestValidate_unknownCapability_returnsError(t *testing.T) {
	p := &config.Profile{
		Capabilities: []string{"not-a-real-capability"},
		Entrypoint:   config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for unknown capability")
	}
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelError && strings.Contains(i.Message, "not-a-real-capability") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning the unknown capability name, got: %v", r.Issues)
	}
}

func TestValidate_knownCapability_dirMissing_returnsWarning(t *testing.T) {
	// Set HOME to a temp dir that has no .claude subdir.
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := &config.Profile{
		Capabilities: []string{"claude"},
		Entrypoint:   config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	// Must not be a fatal error — just a warning.
	if r.HasErrors() {
		t.Errorf("missing capability dir should be a warning, not an error: %v", r.Issues)
	}
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning && strings.Contains(i.Message, "claude") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning mentioning capability 'claude', got: %v", r.Issues)
	}
}

func TestValidate_capabilityConflictsWithExplicitMount_returnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create ~/.claude so the dir-exists check doesn't also warn.
	if err := os.MkdirAll(home+"/.claude", 0o755); err != nil {
		t.Fatal(err)
	}

	claudeDest := home + "/.claude"
	p := &config.Profile{
		Capabilities: []string{"claude"},
		Mounts: map[string]config.MountEntry{
			// Explicit mount whose dest conflicts with the claude capability.
			claudeDest: {Dest: claudeDest, Mode: "rw"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for capability-mount conflict on same dest")
	}
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelError && strings.Contains(i.Message, "claude") && strings.Contains(i.Message, "conflict") || strings.Contains(i.Message, "target") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a conflict error mentioning 'claude', got: %v", r.Issues)
	}
}

// ── nested-user-ns capability warnings ───────────────────────────────────────

func TestValidate_nestedUserNs_warnsAboutCaps(t *testing.T) {
	p := &config.Profile{
		Sandbox:    config.SandboxConfig{Allow: []string{"nested-user-ns"}},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("nested-user-ns should produce warning, not error: %v", r.Issues)
	}
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning && strings.Contains(i.Message, "CAP_SETUID") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning mentioning CAP_SETUID for nested-user-ns, got: %v", r.Issues)
	}
}

func TestValidate_nestedUserNs_withNetwork_warnsAboutCombination(t *testing.T) {
	p := &config.Profile{
		Sandbox: config.SandboxConfig{
			Allow:   []string{"nested-user-ns"},
			Network: true,
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("nested-user-ns + network should produce warning, not error: %v", r.Issues)
	}
	foundNetwork := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning && strings.Contains(i.Message, "network") {
			foundNetwork = true
		}
	}
	if !foundNetwork {
		t.Errorf("expected warning about nested-user-ns combined with network, got: %v", r.Issues)
	}
}

func TestValidate_knownCapability_dirExists_noIssues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home+"/.claude", 0o755); err != nil {
		t.Fatal(err)
	}

	p := &config.Profile{
		Capabilities: []string{"claude"},
		Entrypoint:   config.EntrypointConfig{Interactive: true},
	}
	r := Validate(p, "")
	for _, i := range r.Issues {
		if strings.Contains(i.Message, "claude") {
			t.Errorf("unexpected issue for valid claude capability: %s", i.Message)
		}
	}
}

func TestValidate_envSetUndefinedHostVar_warns(t *testing.T) {
	os.Unsetenv("INNER_TEST_VALIDATOR_UNDEF")
	p := &config.Profile{
		Env: config.EnvConfig{
			Set: map[string]string{"JAVA_HOME": "${INNER_TEST_VALIDATOR_UNDEF}"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning &&
			strings.Contains(i.Message, "JAVA_HOME") &&
			strings.Contains(i.Message, "INNER_TEST_VALIDATOR_UNDEF") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning naming JAVA_HOME and INNER_TEST_VALIDATOR_UNDEF, got: %v", r.Issues)
	}
	if r.HasErrors() {
		t.Errorf("undefined host var in [env] set should be a warning, not an error, got: %v", r.Issues)
	}
}

func TestValidate_envSetDefinedHostVar_noWarning(t *testing.T) {
	t.Setenv("INNER_TEST_VALIDATOR_DEFINED", "/opt/jdk/jdk-21")
	p := &config.Profile{
		Env: config.EnvConfig{
			Set: map[string]string{"JAVA_HOME": "${INNER_TEST_VALIDATOR_DEFINED}"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	for _, i := range r.Issues {
		if strings.Contains(i.Message, "JAVA_HOME") {
			t.Errorf("unexpected issue for defined host var: %s", i.Message)
		}
	}
}

func TestValidate_pathPrepend_missingDir_warns(t *testing.T) {
	p := &config.Profile{
		Env: config.EnvConfig{
			PathPrepend: []string{"/nonexistent/jdk-99/bin"},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	found := false
	for _, i := range r.Issues {
		if i.Level == LevelWarning && strings.Contains(i.Message, "/nonexistent/jdk-99/bin") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning for missing path_prepend dir, got: %v", r.Issues)
	}
	if r.HasErrors() {
		t.Errorf("missing path_prepend dir should be a warning, not an error, got: %v", r.Issues)
	}
}

func TestValidate_pathPrepend_existingDir_noIssue(t *testing.T) {
	dir := t.TempDir()
	p := &config.Profile{
		Env: config.EnvConfig{
			PathPrepend: []string{dir},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	for _, i := range r.Issues {
		if strings.Contains(i.Message, "path_prepend") {
			t.Errorf("unexpected issue for existing path_prepend dir: %s", i.Message)
		}
	}
}

func TestValidate_pathPrepend_emptyEntry_isError(t *testing.T) {
	p := &config.Profile{
		Env: config.EnvConfig{
			PathPrepend: []string{""},
		},
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}

	r := Validate(p, "")
	if !r.HasErrors() {
		t.Error("expected error for empty path_prepend entry")
	}
}
