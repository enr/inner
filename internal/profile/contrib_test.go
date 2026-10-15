package profile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// copyFile copies src to dst, creating dst's parent directory if needed.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

// TestContribProfile_node22_validatesCleanly is a regression guard for
// contrib/profiles/node-22.toml (added in T8, docs/tasks/java-pinning.md):
// with its base profile ("shell", a built-in profile) available and with
// its mount directories (~/.npm, ~/.npm-global) actually present on the
// host — the two things profile.Validate treats as errors rather than
// warnings — the profile must load, merge, and validate with zero errors.
//
// The pinned Node installation itself (/opt/node/node-22) is intentionally
// left absent: a missing pinned toolchain is a warning (see T1/T5), not an
// error, and asserting on it here would make this test depend on machine
// state outside the repo.
func TestContribProfile_node22_validatesCleanly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".npm-global"), 0o755); err != nil {
		t.Fatal(err)
	}

	configDir := t.TempDir()
	copyFile(t, filepath.Join("..", "setup", "profiles", "shell.toml"), filepath.Join(configDir, "profiles", "shell.toml"))
	copyFile(t, filepath.Join("..", "..", "contrib", "profiles", "node-22.toml"), filepath.Join(configDir, "profiles", "node-22.toml"))

	l := config.NewLoader(configDir)
	p, err := l.LoadProfile("node-22")
	if err != nil {
		t.Fatalf("LoadProfile(node-22): %v", err)
	}

	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("contrib/profiles/node-22.toml has validation errors with its mounts present: %v", r.Issues)
	}
}

// TestContribProfile_java21_validatesCleanly is the same regression guard as
// TestContribProfile_node22_validatesCleanly, for contrib/profiles/java-21.toml
// (T6). It was previously verified only with an ad hoc script; this makes the
// check permanent.
func TestContribProfile_java21_validatesCleanly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".m2"), 0o755); err != nil {
		t.Fatal(err)
	}

	configDir := t.TempDir()
	copyFile(t, filepath.Join("..", "setup", "profiles", "shell.toml"), filepath.Join(configDir, "profiles", "shell.toml"))
	copyFile(t, filepath.Join("..", "..", "contrib", "profiles", "java-21.toml"), filepath.Join(configDir, "profiles", "java-21.toml"))

	l := config.NewLoader(configDir)
	p, err := l.LoadProfile("java-21")
	if err != nil {
		t.Fatalf("LoadProfile(java-21): %v", err)
	}

	r := Validate(p, "")
	if r.HasErrors() {
		t.Errorf("contrib/profiles/java-21.toml has validation errors with its mounts present: %v", r.Issues)
	}
}

// TestContribProfile_openclaw_validatesCleanly guards contrib/profiles/openclaw.toml:
// with its state directory present it must validate without errors, keep the
// network on the allowlist, publish the gateway port on loopback only and not
// fall back to the caller's cwd as a read-write workdir.
func TestContribProfile_openclaw_validatesCleanly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".local", "share", "inner", "openclaw")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}

	configDir := t.TempDir()
	copyFile(t, filepath.Join("..", "..", "contrib", "profiles", "openclaw.toml"), filepath.Join(configDir, "profiles", "openclaw.toml"))

	l := config.NewLoader(configDir)
	p, err := l.LoadProfile("openclaw")
	if err != nil {
		t.Fatalf("LoadProfile(openclaw): %v", err)
	}
	// /run/user/$UID belongs to the login session, not to this profile: a CI
	// container may have none, and the validator then reports the tmpfs
	// destination as missing. That one is machine state, not a profile error.
	runtimeDir := fmt.Sprintf("/run/user/%d", os.Getuid())
	_, runtimeErr := os.Stat(runtimeDir)
	for _, issue := range Validate(p, "").Issues {
		if issue.Level != LevelError {
			continue
		}
		if runtimeErr != nil && strings.Contains(issue.Message, runtimeDir) {
			continue
		}
		t.Errorf("contrib/profiles/openclaw.toml has a validation error with its state dir present: %v", issue)
	}

	rc, err := l.Build("openclaw")
	if err != nil {
		t.Fatalf("Build(openclaw): %v", err)
	}
	if rc.EffectiveNetworkMode() != config.NetworkAllowlist {
		t.Errorf("network mode = %q, want allowlist", rc.EffectiveNetworkMode())
	}
	if len(rc.Publish) != 1 || rc.Publish[0].HostAddr() != "127.0.0.1:18789" || !rc.Publish[0].Loopback() {
		t.Errorf("publish = %+v, want only 127.0.0.1:18789", rc.Publish)
	}
	if !rc.HomeIsolated() {
		t.Error("home must be isolated")
	}
	if rc.Workdir != state {
		t.Errorf("workdir = %q, want the state directory %q", rc.Workdir, state)
	}
	if rc.Timeout != 0 || rc.Entrypoint.Interactive {
		t.Errorf("gateway must be non-interactive with no timeout, got timeout=%d interactive=%v", rc.Timeout, rc.Entrypoint.Interactive)
	}
}
