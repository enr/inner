package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadProfile_basic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "myprofile.toml"), `
schema_version = "1"
name = "myprofile"
description = "Test profile"

[sandbox]
network = true
clipboard = false

[env]
clearenv = true
inherit  = ["HOME", "TERM"]

[entrypoint]
cmd         = "bash"
args        = ["-l"]
interactive = true

[output]
summary         = false
log             = "/tmp/logs/"
timeout_seconds = 30
`)

	l := NewLoader(dir)
	p, err := l.LoadProfile("myprofile")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}

	if p.Name != "myprofile" {
		t.Errorf("Name = %q, want %q", p.Name, "myprofile")
	}
	if !p.Sandbox.Network {
		t.Error("expected Network = true")
	}
	if !p.Env.Clear {
		t.Error("expected Env.Clear = true")
	}
	if len(p.Env.Inherit) != 2 {
		t.Errorf("Inherit len = %d, want 2", len(p.Env.Inherit))
	}
	if p.Entrypoint.Cmd != "bash" {
		t.Errorf("Entrypoint.Cmd = %q, want %q", p.Entrypoint.Cmd, "bash")
	}
	if p.Output.TimeoutSeconds != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", p.Output.TimeoutSeconds)
	}
}

func TestLoadProfile_notFound(t *testing.T) {
	l := NewLoader(t.TempDir())
	_, err := l.LoadProfile("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestLoadProfile_backfillName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "unnamed.toml"), `
schema_version = "1"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("unnamed")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Name != "unnamed" {
		t.Errorf("Name = %q, want %q", p.Name, "unnamed")
	}
}

func TestLoadGlobal_missing(t *testing.T) {
	l := NewLoader(t.TempDir())
	cfg, err := l.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal with missing file should not error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil GlobalConfig")
	}
}

func TestLoadGlobal_withFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `
log_dir = "/custom/logs"
`)
	l := NewLoader(dir)
	cfg, err := l.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if cfg.LogDir != "/custom/logs" {
		t.Errorf("LogDir = %q, want %q", cfg.LogDir, "/custom/logs")
	}
}

func TestBuild_mounts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "mtest.toml"), `
[mounts]
"/tmp/src" = { dest = "/workspace", mode = "rw" }
"/etc/hosts" = { dest = "/etc/hosts" }
`)
	l := NewLoader(dir)
	rc, err := l.Build("mtest")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(rc.Mounts) != 2 {
		t.Fatalf("Mounts len = %d, want 2", len(rc.Mounts))
	}

	byDest := make(map[string]Mount)
	for _, m := range rc.Mounts {
		byDest[m.Dest] = m
	}

	ws, ok := byDest["/workspace"]
	if !ok {
		t.Fatal("mount /workspace not found")
	}
	if ws.Src != "/tmp/src" {
		t.Errorf("Src = %q, want %q", ws.Src, "/tmp/src")
	}
	if ws.Mode != "rw" {
		t.Errorf("Mode = %q, want %q", ws.Mode, "rw")
	}

	hosts, ok := byDest["/etc/hosts"]
	if !ok {
		t.Fatal("mount /etc/hosts not found")
	}
	if hosts.Mode != "ro" {
		t.Errorf("Mode default = %q, want %q", hosts.Mode, "ro")
	}
}

func TestBuild_logDirFallbackToGlobal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `log_dir = "/global/logs"`)
	writeFile(t, filepath.Join(dir, "profiles", "p.toml"), ``)

	l := NewLoader(dir)
	rc, err := l.Build("p")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.LogDir != "/global/logs" {
		t.Errorf("LogDir = %q, want %q", rc.LogDir, "/global/logs")
	}
}

func TestBuild_defaultProfileName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "default.toml"), `name = "default"`)

	l := NewLoader(dir)
	rc, err := l.Build("")
	if err != nil {
		t.Fatalf("Build with empty name: %v", err)
	}
	if rc == nil {
		t.Fatal("expected non-nil RunConfig")
	}
}

func TestBuild_noopBlock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "p.toml"), `
[noop]
block = ["apt-get", "apt", "brew"]
`)
	l := NewLoader(dir)
	rc, err := l.Build("p")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Noop.Block) != 3 {
		t.Fatalf("Noop.Block len = %d, want 3", len(rc.Noop.Block))
	}
	if rc.Noop.Block[0] != "apt-get" {
		t.Errorf("Noop.Block[0] = %q, want %q", rc.Noop.Block[0], "apt-get")
	}
}

func TestBuild_noopRewrite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "p.toml"), `
[noop]
rewrite = { "docker" = "podman", "rm" = "rm -i" }
`)
	l := NewLoader(dir)
	rc, err := l.Build("p")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Noop.Rewrite) != 2 {
		t.Fatalf("Noop.Rewrite len = %d, want 2", len(rc.Noop.Rewrite))
	}
	if rc.Noop.Rewrite["docker"] != "podman" {
		t.Errorf("Noop.Rewrite[docker] = %q, want %q", rc.Noop.Rewrite["docker"], "podman")
	}
}

func TestBuild_sandboxAllow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "p.toml"), `
[sandbox]
allow = ["ssh-keys", "netrc"]
`)
	l := NewLoader(dir)
	rc, err := l.Build("p")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Allow) != 2 {
		t.Fatalf("Allow len = %d, want 2", len(rc.Allow))
	}
	if rc.Allow[0] != "ssh-keys" {
		t.Errorf("Allow[0] = %q, want %q", rc.Allow[0], "ssh-keys")
	}
}

func TestBuild_verifyCustomChecks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "p.toml"), `
[verify.custom]
checks = [
  { name = "Output dir scrivibile", cmd = "touch /workspace/.test", severity = "critical" },
  { name = "Nessun .env",           cmd = "! find /workspace -name '.env'", severity = "high" },
]
`)
	l := NewLoader(dir)
	rc, err := l.Build("p")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.VerifyCustomChecks) != 2 {
		t.Fatalf("VerifyCustomChecks len = %d, want 2", len(rc.VerifyCustomChecks))
	}
	check := rc.VerifyCustomChecks[0]
	if check.Name != "Output dir scrivibile" {
		t.Errorf("Name = %q, want %q", check.Name, "Output dir scrivibile")
	}
	if check.Severity != "critical" {
		t.Errorf("Severity = %q, want %q", check.Severity, "critical")
	}
}

func TestLoadProfileFromPath_basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "myprofile.toml")
	writeFile(t, path, `
schema_version = "1"
name = "myprofile"
[entrypoint]
cmd = "bash"
`)
	l := NewLoader(t.TempDir())
	p, err := l.LoadProfileFromPath(path)
	if err != nil {
		t.Fatalf("LoadProfileFromPath: %v", err)
	}
	if p.Name != "myprofile" {
		t.Errorf("Name = %q, want %q", p.Name, "myprofile")
	}
	if p.Entrypoint.Cmd != "bash" {
		t.Errorf("Cmd = %q, want bash", p.Entrypoint.Cmd)
	}
}

func TestLoadProfileFromPath_backfillName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-profile.toml")
	writeFile(t, path, `schema_version = "1"`)
	l := NewLoader(t.TempDir())
	p, err := l.LoadProfileFromPath(path)
	if err != nil {
		t.Fatalf("LoadProfileFromPath: %v", err)
	}
	if p.Name != "test-profile" {
		t.Errorf("Name = %q, want %q", p.Name, "test-profile")
	}
}

func TestLoadProfileFromPath_notFound(t *testing.T) {
	l := NewLoader(t.TempDir())
	_, err := l.LoadProfileFromPath("/nonexistent/path/profile.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadProfileAuto_prefersFileOverName(t *testing.T) {
	// Create both a file in cwd-equivalent temp dir and a named profile.
	dir := t.TempDir()
	// Named profile in profiles dir.
	writeFile(t, filepath.Join(dir, "profiles", "myprofile.toml"), `name = "from-profiles-dir"`)
	// File with the same name in another dir (simulates a local file).
	fileDir := t.TempDir()
	localPath := filepath.Join(fileDir, "myprofile.toml")
	writeFile(t, localPath, `name = "from-local-file"`)

	l := NewLoader(dir)
	p, err := l.LoadProfileAuto(localPath)
	if err != nil {
		t.Fatalf("LoadProfileAuto with path: %v", err)
	}
	if p.Name != "from-local-file" {
		t.Errorf("Name = %q, want %q", p.Name, "from-local-file")
	}
}

func TestLoadProfileAuto_fallsBackToName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "named.toml"), `name = "named"`)
	l := NewLoader(dir)
	p, err := l.LoadProfileAuto("named")
	if err != nil {
		t.Fatalf("LoadProfileAuto with name: %v", err)
	}
	if p.Name != "named" {
		t.Errorf("Name = %q, want named", p.Name)
	}
}

func TestBuild_fromPath(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "custom.toml")
	writeFile(t, profilePath, `
[entrypoint]
cmd = "zsh"
`)
	// Loader points at a different dir (no profiles dir needed).
	l := NewLoader(t.TempDir())
	rc, err := l.Build(profilePath)
	if err != nil {
		t.Fatalf("Build with path: %v", err)
	}
	if rc.Entrypoint.Cmd != "zsh" {
		t.Errorf("Entrypoint.Cmd = %q, want zsh", rc.Entrypoint.Cmd)
	}
}

func TestBuild_emptyNoop(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "p.toml"), `name = "p"`)
	l := NewLoader(dir)
	rc, err := l.Build("p")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Noop.Block) != 0 {
		t.Errorf("expected empty Noop.Block, got %v", rc.Noop.Block)
	}
	if len(rc.Allow) != 0 {
		t.Errorf("expected empty Allow, got %v", rc.Allow)
	}
	if len(rc.VerifyCustomChecks) != 0 {
		t.Errorf("expected empty VerifyCustomChecks, got %v", rc.VerifyCustomChecks)
	}
}
