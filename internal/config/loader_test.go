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
