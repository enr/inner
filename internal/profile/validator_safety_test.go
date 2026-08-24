package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enr/inner/internal/config"
)

// baseProfile returns a profile that produces no issues on its own, so a test
// asserts only on what it adds.
func baseProfile() *config.Profile {
	return &config.Profile{
		Entrypoint: config.EntrypointConfig{Interactive: true},
	}
}

// ── allow keys neutralised by an isolated home ────────────────────────────────

func TestValidate_allowKeyNeutralisedByIsolatedHome(t *testing.T) {
	if _, err := os.UserHomeDir(); err != nil {
		t.Skip("no home directory on this machine")
	}
	p := baseProfile()
	p.Sandbox.Home = config.HomeIsolated
	p.Sandbox.Allow = []string{"ssh-keys"}

	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, `allow "ssh-keys" has no effect`) {
		t.Fatalf("expected a warning that the allow key is inert, got %v", r.Issues)
	}
	if !issuesContain(r, LevelWarning, "home_allow") {
		t.Errorf("the warning must name the fix, got %v", r.Issues)
	}
}

func TestValidate_allowKeyReexposedByHomeAllow_noWarning(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	p := baseProfile()
	p.Sandbox.Home = config.HomeIsolated
	p.Sandbox.Allow = []string{"ssh-keys"}
	p.Sandbox.HomeAllow = []string{filepath.Join(home, ".ssh")}

	if r := Validate(p, ""); issuesContain(r, LevelWarning, "has no effect under home") {
		t.Errorf("no warning expected when home_allow puts the path back, got %v", r.Issues)
	}
}

func TestValidate_allowKeyOutsideHome_notAffectedByHomeMode(t *testing.T) {
	p := baseProfile()
	p.Sandbox.Home = config.HomeIsolated
	// The docker socket lives outside the home: the mode does not touch it.
	p.Sandbox.Allow = []string{"docker-socket"}

	if r := Validate(p, ""); issuesContain(r, LevelWarning, "has no effect under home") {
		t.Errorf("a resource outside the home must not be reported as neutralised, got %v", r.Issues)
	}
}

func TestValidate_allowKeyUnderHostRO_noWarning(t *testing.T) {
	p := baseProfile()
	p.Sandbox.Allow = []string{"ssh-keys"}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "has no effect under home") {
		t.Errorf("host-ro must keep allow keys effective, got %v", r.Issues)
	}
}

// ── entrypoint hidden by the isolated home ────────────────────────────────────

// fakeHomeWithBin creates a temp home containing .local/bin/<name>, points HOME
// and PATH at it, and returns (home, binPath).
func fakeHomeWithBin(t *testing.T, name string) (string, string) {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binPath := filepath.Join(binDir, name)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)
	return home, binPath
}

func TestValidate_entrypointHiddenByIsolatedHome(t *testing.T) {
	_, binPath := fakeHomeWithBin(t, "fakeagent")
	p := baseProfile()
	p.Sandbox.Home = config.HomeIsolated
	p.Entrypoint.Cmd = "fakeagent"

	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, binPath) {
		t.Fatalf("expected a warning naming %s, got %v", binPath, r.Issues)
	}
	if !issuesContain(r, LevelWarning, "home_allow") {
		t.Errorf("the warning must name the fix, got %v", r.Issues)
	}

	// Covered by the allowlist → silent.
	p.Sandbox.HomeAllow = []string{filepath.Dir(binPath)}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "command not found") {
		t.Errorf("no warning expected when home_allow covers the binary, got %v", r.Issues)
	}

	// Covered by a mount → silent.
	p.Sandbox.HomeAllow = nil
	p.Mounts = map[string]config.MountEntry{
		filepath.Dir(binPath): {Dest: filepath.Dir(binPath), Mode: "ro"},
	}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "command not found") {
		t.Errorf("no warning expected when a mount covers the binary, got %v", r.Issues)
	}

	// host-ro → the binary is visible through the root bind.
	p.Mounts = nil
	p.Sandbox.Home = config.HomeHostRO
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "command not found") {
		t.Errorf("no warning expected under host-ro, got %v", r.Issues)
	}
}

func TestValidate_entrypointSymlinkTargetHiddenByIsolatedHome(t *testing.T) {
	home, binPath := fakeHomeWithBin(t, "payload")
	// Native installers link ~/.local/bin/<cli> to a versioned payload elsewhere
	// in the home: allowlisting only the link leaves a dangling symlink.
	payloadDir := filepath.Join(home, ".local", "share", "agent")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := filepath.Join(payloadDir, "payload")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := os.Remove(binPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(payload, binPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	p := baseProfile()
	p.Sandbox.Home = config.HomeIsolated
	p.Sandbox.HomeAllow = []string{filepath.Dir(binPath)}
	p.Entrypoint.Cmd = "payload"

	if r := Validate(p, ""); !issuesContain(r, LevelWarning, payloadDir) {
		t.Errorf("expected a warning about the uncovered symlink target, got %v", r.Issues)
	}
}

// ── dangerous mounts ──────────────────────────────────────────────────────────

func TestValidate_mountRootReadWriteIsAnError(t *testing.T) {
	p := baseProfile()
	p.Mounts = map[string]config.MountEntry{"/": {Dest: "/", Mode: "rw"}}

	r := Validate(p, "")
	if !issuesContain(r, LevelError, "entire host filesystem writable") {
		t.Fatalf("expected an error for a read-write root mount, got %v", r.Issues)
	}
	if !r.HasErrors() {
		t.Error("a read-write root mount must block the run")
	}
}

func TestValidate_mountSystemDirReadWriteWarns(t *testing.T) {
	p := baseProfile()
	p.Mounts = map[string]config.MountEntry{"/usr": {Dest: "/usr", Mode: "rw"}}

	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, "rewrite host system files") {
		t.Errorf("expected a warning for a read-write system directory, got %v", r.Issues)
	}
	// Read-only is the normal case and must stay silent.
	p.Mounts = map[string]config.MountEntry{"/usr": {Dest: "/usr", Mode: "ro"}}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "rewrite host system files") {
		t.Errorf("a read-only system mount must not warn, got %v", r.Issues)
	}
}

func TestValidate_mountSystemSubdirIsFine(t *testing.T) {
	p := baseProfile()
	// Mounting a specific toolchain directory read-write is ordinary.
	p.Mounts = map[string]config.MountEntry{"/opt/toolchain": {Dest: "/opt/toolchain", Mode: "rw"}}
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "rewrite host system files") {
		t.Errorf("a subdirectory of a system dir must not warn, got %v", r.Issues)
	}
}

func TestValidate_mountTmpfsOverSystemDirIsFine(t *testing.T) {
	p := baseProfile()
	// A tmpfs exposes nothing from the host and disappears with the run; the
	// built-in profiles use exactly this to hide /run/user/$UID.
	p.Mounts = map[string]config.MountEntry{"/var": {Dest: "/var", Mode: "tmpfs"}}
	r := Validate(p, "")
	if issuesContain(r, LevelWarning, "rewrite host system files") || r.HasErrors() {
		t.Errorf("a tmpfs mount must not be reported as dangerous, got %v", r.Issues)
	}
}

func TestValidate_mountCoveringHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	p := baseProfile()
	p.Mounts = map[string]config.MountEntry{home: {Dest: home, Mode: "rw"}}

	// host-ro: a warning — it is a persistence vector, but a legitimate choice.
	r := Validate(p, "")
	if r.HasErrors() {
		t.Fatalf("a read-write home mount must not block a host-ro profile: %v", r.Issues)
	}
	if !issuesContain(r, LevelWarning, "persistence vector") {
		t.Errorf("expected a warning for a read-write home mount, got %v", r.Issues)
	}

	// isolated: an error — the mount would cancel the isolation the profile asks for.
	p.Sandbox.Home = config.HomeIsolated
	r = Validate(p, "")
	if !issuesContain(r, LevelError, "covers the home directory") {
		t.Errorf("expected an error for a home mount under an isolated home, got %v", r.Issues)
	}
}

// ── risky combinations ────────────────────────────────────────────────────────

func TestValidate_inheritAllWarns(t *testing.T) {
	p := baseProfile()
	p.Env.InheritAll = true

	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, "forwards the entire host environment") {
		t.Fatalf("expected a warning for inherit_all, got %v", r.Issues)
	}
	if issuesContain(r, LevelWarning, "those secrets can also leave the machine") {
		t.Errorf("the network clause must appear only with network = true, got %v", r.Issues)
	}

	p.Sandbox.Network = true
	if r := Validate(p, ""); !issuesContain(r, LevelWarning, "those secrets can also leave the machine") {
		t.Errorf("expected the network clause with network = true, got %v", r.Issues)
	}

	p.Sandbox.Network = false
	p.Sandbox.Home = config.HomeIsolated
	if r := Validate(p, ""); !issuesContain(r, LevelWarning, "contradicts home") {
		t.Errorf("expected the contradiction warning with an isolated home, got %v", r.Issues)
	}
}

func TestValidate_networkPlusCredentialsWarns(t *testing.T) {
	p := baseProfile()
	p.Sandbox.Network = true
	p.Sandbox.Allow = []string{"aws-credentials", "docker-socket"}

	r := Validate(p, "")
	if !issuesContain(r, LevelWarning, "aws-credentials") {
		t.Fatalf("expected an exfiltration warning naming the credential key, got %v", r.Issues)
	}
	// docker-socket is an escalation vector, not a readable secret: it has its
	// own warnings and must not be listed here.
	if issuesContain(r, LevelWarning, "docker-socket]") {
		t.Errorf("only credential keys belong in this warning, got %v", r.Issues)
	}

	// Without network there is nowhere to send them.
	p.Sandbox.Network = false
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "send them anywhere") {
		t.Errorf("no exfiltration warning expected without network, got %v", r.Issues)
	}
}

func TestValidate_pidNamespaceOptOutWarns(t *testing.T) {
	off := false
	on := true

	p := baseProfile()
	p.Sandbox.PidNamespace = &off
	if r := Validate(p, ""); !issuesContain(r, LevelWarning, "pid_namespace = false") {
		t.Errorf("expected a warning for the PID namespace opt-out, got %v", r.Issues)
	}

	p.Sandbox.PidNamespace = &on
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "pid_namespace = false") {
		t.Errorf("no warning expected with pid_namespace = true, got %v", r.Issues)
	}

	p.Sandbox.PidNamespace = nil
	if r := Validate(p, ""); issuesContain(r, LevelWarning, "pid_namespace = false") {
		t.Errorf("no warning expected when the key is unset, got %v", r.Issues)
	}
}
