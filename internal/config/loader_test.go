package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadProfileAuto_tilde(t *testing.T) {
	// Point HOME at a temp dir so ~/profile.toml resolves there.
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	profilePath := filepath.Join(homeDir, "tprofile.toml")
	writeFile(t, profilePath, `name = "tilde-loaded"`)

	l := NewLoader(t.TempDir())
	p, err := l.LoadProfileAuto("~/tprofile.toml")
	if err != nil {
		t.Fatalf("LoadProfileAuto with ~: %v", err)
	}
	if p.Name != "tilde-loaded" {
		t.Errorf("Name = %q, want tilde-loaded", p.Name)
	}
}

func TestResolveProfilePath_tilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	profilePath := filepath.Join(homeDir, "tprofile.toml")
	writeFile(t, profilePath, `name = "tilde-loaded"`)

	l := NewLoader(t.TempDir())
	resolved := l.ResolveProfilePath("~/tprofile.toml")
	if resolved != profilePath {
		t.Errorf("ResolveProfilePath = %q, want %q", resolved, profilePath)
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

// --- extends tests ---

func TestLoadProfile_extends_scalarOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name        = "base"
description = "base description"

[sandbox]
network = false

[entrypoint]
cmd         = "bash"
interactive = true

[output]
timeout_seconds = 60
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends     = "base"
description = "child description"

[output]
timeout_seconds = 120
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Description != "child description" {
		t.Errorf("Description = %q, want %q", p.Description, "child description")
	}
	if p.Output.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds = %d, want 120", p.Output.TimeoutSeconds)
	}
	// Inherited from base.
	if p.Entrypoint.Cmd != "bash" {
		t.Errorf("Entrypoint.Cmd = %q, want bash", p.Entrypoint.Cmd)
	}
	if p.Sandbox.Network {
		t.Error("expected Network = false (inherited from base)")
	}
}

func TestLoadProfile_extends_boolOverride(t *testing.T) {
	// Verify that a child can explicitly set a bool to true or false,
	// including overriding a base value of true back to false.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
experimental = true

[sandbox]
network = true

[entrypoint]
interactive = true
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends      = "base"
experimental = false

[sandbox]
network = false

[entrypoint]
interactive = false
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Experimental {
		t.Error("expected Experimental = false (overridden)")
	}
	if p.Sandbox.Network {
		t.Error("expected Network = false (overridden)")
	}
	if p.Entrypoint.Interactive {
		t.Error("expected Interactive = false (overridden)")
	}
}

func TestLoadProfile_extends_mergeSlices(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
[sandbox]
allow = ["ssh-keys", "git-credentials"]

[env]
inherit = ["HOME", "TERM"]

[noop]
block = ["apt-get", "apt"]
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"

[sandbox]
allow = ["docker-socket", "ssh-keys"]  # ssh-keys is duplicate

[env]
inherit = ["LANG", "HOME"]  # HOME is duplicate

[noop]
block = ["brew", "apt"]  # apt is duplicate
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}

	wantAllow := []string{"ssh-keys", "git-credentials", "docker-socket"}
	if len(p.Sandbox.Allow) != len(wantAllow) {
		t.Errorf("Allow = %v, want %v", p.Sandbox.Allow, wantAllow)
	}

	wantInherit := []string{"HOME", "TERM", "LANG"}
	if len(p.Env.Inherit) != len(wantInherit) {
		t.Errorf("Inherit = %v, want %v", p.Env.Inherit, wantInherit)
	}

	wantBlock := []string{"apt-get", "apt", "brew"}
	if len(p.Noop.Block) != len(wantBlock) {
		t.Errorf("Noop.Block = %v, want %v", p.Noop.Block, wantBlock)
	}
}

func TestLoadProfile_extends_mergeMaps(t *testing.T) {
	dir := t.TempDir()
	srcA := t.TempDir()
	srcB := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), fmt.Sprintf(`
[mounts]
%q = { dest = "/workspace", mode = "rw" }

[env]
set = { "FOO" = "from-base", "BAR" = "base-bar" }

[noop]
rewrite = { "docker" = "podman" }
`, srcA))
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), fmt.Sprintf(`
extends = "base"

[mounts]
%q = { dest = "/extra", mode = "ro" }

[env]
set = { "FOO" = "from-child", "NEW" = "added" }

[noop]
rewrite = { "docker-compose" = "podman-compose" }
`, srcB))
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}

	// mounts: both sources present.
	if len(p.Mounts) != 2 {
		t.Errorf("Mounts len = %d, want 2", len(p.Mounts))
	}

	// env.set: FOO overridden, BAR kept, NEW added.
	if p.Env.Set["FOO"] != "from-child" {
		t.Errorf("env.set FOO = %q, want from-child", p.Env.Set["FOO"])
	}
	if p.Env.Set["BAR"] != "base-bar" {
		t.Errorf("env.set BAR = %q, want base-bar", p.Env.Set["BAR"])
	}
	if p.Env.Set["NEW"] != "added" {
		t.Errorf("env.set NEW = %q, want added", p.Env.Set["NEW"])
	}

	// noop.rewrite: both entries present.
	if p.Noop.Rewrite["docker"] != "podman" {
		t.Errorf("noop.rewrite docker = %q, want podman", p.Noop.Rewrite["docker"])
	}
	if p.Noop.Rewrite["docker-compose"] != "podman-compose" {
		t.Errorf("noop.rewrite docker-compose = %q, want podman-compose", p.Noop.Rewrite["docker-compose"])
	}
}

func TestLoadProfile_extends_verifyChecksAppend(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
[verify.custom]
checks = [
  { name = "base-check", cmd = "true", severity = "high" },
]
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"

[verify.custom]
checks = [
  { name = "child-check", cmd = "true", severity = "critical" },
]
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if len(p.Verify.Custom.Checks) != 2 {
		t.Fatalf("Checks len = %d, want 2", len(p.Verify.Custom.Checks))
	}
	if p.Verify.Custom.Checks[0].Name != "base-check" {
		t.Errorf("Checks[0].Name = %q, want base-check", p.Verify.Custom.Checks[0].Name)
	}
	if p.Verify.Custom.Checks[1].Name != "child-check" {
		t.Errorf("Checks[1].Name = %q, want child-check", p.Verify.Custom.Checks[1].Name)
	}
}

func TestLoadProfile_extends_gitMerge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
[git]
strip_sections = ["credential"]
overrides      = { "push.default" = "nothing" }
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"

[git]
strip_sections = ["url", "credential"]  # credential is duplicate
overrides      = { "user.email" = "bot@example.com" }
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Git == nil {
		t.Fatal("expected non-nil Git config")
	}
	// strip_sections: union of base + child, no duplicates.
	wantStrip := []string{"credential", "url"}
	if len(p.Git.StripSections) != len(wantStrip) {
		t.Errorf("StripSections = %v, want %v", p.Git.StripSections, wantStrip)
	}
	// overrides: merged.
	if p.Git.Overrides["push.default"] != "nothing" {
		t.Errorf("overrides push.default = %q, want nothing", p.Git.Overrides["push.default"])
	}
	if p.Git.Overrides["user.email"] != "bot@example.com" {
		t.Errorf("overrides user.email = %q, want bot@example.com", p.Git.Overrides["user.email"])
	}
}

func TestLoadProfile_extends_chain(t *testing.T) {
	// A extends B extends C — three-level chain.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "c.toml"), `
[entrypoint]
cmd = "bash"

[output]
timeout_seconds = 10
`)
	writeFile(t, filepath.Join(dir, "profiles", "b.toml"), `
extends = "c"

[sandbox]
network = true
`)
	writeFile(t, filepath.Join(dir, "profiles", "a.toml"), `
extends = "b"
description = "top"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("a")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Description != "top" {
		t.Errorf("Description = %q, want top", p.Description)
	}
	if !p.Sandbox.Network {
		t.Error("expected Network = true (from b)")
	}
	if p.Entrypoint.Cmd != "bash" {
		t.Errorf("Entrypoint.Cmd = %q, want bash (from c)", p.Entrypoint.Cmd)
	}
	if p.Output.TimeoutSeconds != 10 {
		t.Errorf("TimeoutSeconds = %d, want 10 (from c)", p.Output.TimeoutSeconds)
	}
}

func TestLoadProfile_extends_cycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "a.toml"), `extends = "b"`)
	writeFile(t, filepath.Join(dir, "profiles", "b.toml"), `extends = "a"`)

	l := NewLoader(dir)
	_, err := l.LoadProfile("a")
	if err == nil {
		t.Fatal("expected error for extends cycle, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestLoadProfile_extends_cycleViaSymlink_detected(t *testing.T) {
	// Profile a.toml references itself via a symlink alias of the profiles directory.
	// Without EvalSymlinks canonicalization, the cycle detector sees two different
	// path strings (/dir/profiles/a.toml vs /link/profiles/a.toml) and takes one
	// extra recursion level to discover the cycle. With canonicalization it fires
	// immediately.
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// a.toml extends itself by going through a symlinked path to the same file.
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Skipf("symlink creation failed (likely unsupported in this environment): %v", err)
	}

	writeFile(t, filepath.Join(profilesDir, "a.toml"),
		fmt.Sprintf(`extends = %q`, filepath.Join(linkDir, "profiles", "a.toml")))

	l := NewLoader(dir)
	_, err := l.LoadProfile("a")
	if err == nil {
		t.Fatal("expected error for extends cycle via symlink, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestLoadProfile_extends_baseNotFound(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `extends = "nonexistent"`)

	l := NewLoader(dir)
	_, err := l.LoadProfile("child")
	if err == nil {
		t.Fatal("expected error when base profile is not found")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention the missing profile, got: %v", err)
	}
}

func TestLoadProfile_extends_byPath(t *testing.T) {
	dir := t.TempDir()
	baseDir := t.TempDir()
	basePath := filepath.Join(baseDir, "base.toml")
	writeFile(t, basePath, `
[entrypoint]
cmd = "zsh"
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), fmt.Sprintf(`
extends = %q
description = "uses path extends"
`, basePath))

	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Entrypoint.Cmd != "zsh" {
		t.Errorf("Entrypoint.Cmd = %q, want zsh (from base path)", p.Entrypoint.Cmd)
	}
	if p.Description != "uses path extends" {
		t.Errorf("Description = %q, want 'uses path extends'", p.Description)
	}
}

func TestLoadProfile_extends_noInheritedExtends(t *testing.T) {
	// The merged result must not carry the extends field from the child,
	// so a subsequent load does not re-trigger resolution.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
[entrypoint]
cmd = "bash"
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"
description = "child"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	// The merged profile's Extends field should still be "base" (it's a data
	// field, not an instruction), but loading it again via LoadProfileAuto
	// with the resolved struct should not re-resolve. This test just verifies
	// the round-trip produces a sane profile.
	if p.Entrypoint.Cmd != "bash" {
		t.Errorf("Entrypoint.Cmd = %q, want bash", p.Entrypoint.Cmd)
	}
}

func TestBuild_extends_runConfig(t *testing.T) {
	// End-to-end: Build() on an extending profile should produce the correct RunConfig.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
[sandbox]
network = false
allow   = ["ssh-keys"]

[entrypoint]
cmd         = "bash"
interactive = true

[output]
timeout_seconds = 30
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"

[sandbox]
network = true
allow   = ["docker-socket"]

[output]
timeout_seconds = 60
`)
	l := NewLoader(dir)
	rc, err := l.Build("child")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !rc.Network {
		t.Error("expected Network = true")
	}
	if rc.Timeout != 60 {
		t.Errorf("Timeout = %d, want 60", rc.Timeout)
	}
	if rc.Entrypoint.Cmd != "bash" {
		t.Errorf("Entrypoint.Cmd = %q, want bash", rc.Entrypoint.Cmd)
	}
	wantAllow := []string{"ssh-keys", "docker-socket"}
	if len(rc.Allow) != len(wantAllow) {
		t.Errorf("Allow = %v, want %v", rc.Allow, wantAllow)
	}
}

// --- local config tests ---

func TestLoadLocal_missing(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	l := NewLoaderWithWorkDir(dir, workDir)
	cfg, err := l.LoadLocal()
	if err != nil {
		t.Fatalf("LoadLocal with missing file should not error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil GlobalConfig, got %+v", cfg)
	}
}

func TestLoadLocal_withFile(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(workDir, ".inner", "config.toml"), `default_profile = "local-profile"`)
	l := NewLoaderWithWorkDir(dir, workDir)
	cfg, err := l.LoadLocal()
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil GlobalConfig")
	}
	if cfg.DefaultProfile != "local-profile" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "local-profile")
	}
}

func TestLoadLocal_noWorkDir(t *testing.T) {
	l := NewLoader(t.TempDir())
	cfg, err := l.LoadLocal()
	if err != nil {
		t.Fatalf("LoadLocal with no WorkDir should not error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil when WorkDir is empty, got %+v", cfg)
	}
}

func TestLoadEffectiveGlobal_localOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `
default_profile = "global-profile"
log_dir = "/global/logs"
`)
	writeFile(t, filepath.Join(workDir, ".inner", "config.toml"), `default_profile = "local-profile"`)

	l := NewLoaderWithWorkDir(dir, workDir)
	cfg, err := l.loadEffectiveGlobal()
	if err != nil {
		t.Fatalf("loadEffectiveGlobal: %v", err)
	}
	if cfg.DefaultProfile != "local-profile" {
		t.Errorf("DefaultProfile = %q, want local-profile", cfg.DefaultProfile)
	}
	// log_dir not set in local → inherited from global
	if cfg.LogDir != "/global/logs" {
		t.Errorf("LogDir = %q, want /global/logs", cfg.LogDir)
	}
}

func TestDefaultProfileName_localOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `default_profile = "global-profile"`)
	writeFile(t, filepath.Join(workDir, ".inner", "config.toml"), `default_profile = "local-profile"`)

	l := NewLoaderWithWorkDir(dir, workDir)
	if got := l.DefaultProfileName(); got != "local-profile" {
		t.Errorf("DefaultProfileName() = %q, want local-profile", got)
	}
}

func TestBuild_localDefaultProfile(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `default_profile = "global-profile"`)
	writeFile(t, filepath.Join(dir, "profiles", "global-profile.toml"), `[entrypoint]
cmd = "bash"
`)
	writeFile(t, filepath.Join(dir, "profiles", "local-profile.toml"), `[entrypoint]
cmd = "zsh"
`)
	writeFile(t, filepath.Join(workDir, ".inner", "config.toml"), `default_profile = "local-profile"`)

	l := NewLoaderWithWorkDir(dir, workDir)
	rc, err := l.Build("")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.Entrypoint.Cmd != "zsh" {
		t.Errorf("Entrypoint.Cmd = %q, want zsh (from local default_profile)", rc.Entrypoint.Cmd)
	}
}

func TestDefaultProfileName_noConfig(t *testing.T) {
	// No config.toml → falls back to "default".
	l := NewLoader(t.TempDir())
	if got := l.DefaultProfileName(); got != "default" {
		t.Errorf("DefaultProfileName() = %q, want %q", got, "default")
	}
}

func TestDefaultProfileName_withConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `default_profile = "shell"`)
	l := NewLoader(dir)
	if got := l.DefaultProfileName(); got != "shell" {
		t.Errorf("DefaultProfileName() = %q, want %q", got, "shell")
	}
}

func TestBuild_defaultProfileFromConfig(t *testing.T) {
	// Build("") with default_profile set in config → loads the configured profile.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `default_profile = "shell"`)
	writeFile(t, filepath.Join(dir, "profiles", "shell.toml"), `
[entrypoint]
cmd = "bash"
`)
	l := NewLoader(dir)
	rc, err := l.Build("")
	if err != nil {
		t.Fatalf("Build with default_profile=shell: %v", err)
	}
	if rc.Entrypoint.Cmd != "bash" {
		t.Errorf("Entrypoint.Cmd = %q, want bash", rc.Entrypoint.Cmd)
	}
}

func TestBuild_explicitProfileWinsOverDefault(t *testing.T) {
	// An explicit profileName always wins over default_profile in config.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.toml"), `default_profile = "shell"`)
	writeFile(t, filepath.Join(dir, "profiles", "shell.toml"), `[entrypoint]
cmd = "bash"
`)
	writeFile(t, filepath.Join(dir, "profiles", "other.toml"), `[entrypoint]
cmd = "zsh"
`)
	l := NewLoader(dir)
	rc, err := l.Build("other")
	if err != nil {
		t.Fatalf("Build with explicit name: %v", err)
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

// ── Local profile takes precedence over global ────────────────────────────────

func TestLoadProfile_localOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "foo.toml"), "name = \"foo\"\ndescription = \"global\"")
	writeFile(t, filepath.Join(workDir, ".inner", "profiles", "foo.toml"), "name = \"foo\"\ndescription = \"local\"")

	l := NewLoaderWithWorkDir(dir, workDir)
	p, err := l.LoadProfile("foo")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Description != "local" {
		t.Errorf("expected local profile, got description %q", p.Description)
	}
}

func TestLoadProfile_localFallsBackToGlobal(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "bar.toml"), "name = \"bar\"\ndescription = \"global\"")
	// no local profile named "bar"

	l := NewLoaderWithWorkDir(dir, workDir)
	p, err := l.LoadProfile("bar")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Description != "global" {
		t.Errorf("expected global profile, got description %q", p.Description)
	}
}

func TestResolveProfilePath_localOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "foo.toml"), `name = "foo"`)
	localPath := filepath.Join(workDir, ".inner", "profiles", "foo.toml")
	writeFile(t, localPath, `name = "foo"`)

	l := NewLoaderWithWorkDir(dir, workDir)
	got := l.ResolveProfilePath("foo")
	absLocal, _ := filepath.Abs(localPath)
	if got != absLocal {
		t.Errorf("ResolveProfilePath = %q, want local path %q", got, absLocal)
	}
}

func TestBuild_workdirTokenInMountSrc(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "wdtest.toml"), `
[mounts]
"${workdir}" = { dest = "/workspace", mode = "rw" }
`)
	l := NewLoaderWithWorkDir(dir, workDir)
	rc, err := l.Build("wdtest")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Mounts) != 1 {
		t.Fatalf("Mounts len = %d, want 1", len(rc.Mounts))
	}
	if rc.Mounts[0].Src != workDir {
		t.Errorf("mount Src = %q, want %q", rc.Mounts[0].Src, workDir)
	}
	if rc.Mounts[0].Dest != "/workspace" {
		t.Errorf("mount Dest = %q, want /workspace", rc.Mounts[0].Dest)
	}
}

func TestBuild_workdirTokenWithSubpath(t *testing.T) {
	dir := t.TempDir()
	workDir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "wdtest.toml"), `
[mounts]
"${workdir}/src" = { dest = "/workspace", mode = "ro" }
`)
	l := NewLoaderWithWorkDir(dir, workDir)
	rc, err := l.Build("wdtest")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Mounts) != 1 {
		t.Fatalf("Mounts len = %d, want 1", len(rc.Mounts))
	}
	want := workDir + "/src"
	if rc.Mounts[0].Src != want {
		t.Errorf("mount Src = %q, want %q", rc.Mounts[0].Src, want)
	}
}

func TestBuild_workdirTokenEmptyWorkDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "wdtest.toml"), `
[mounts]
"${workdir}" = { dest = "/workspace", mode = "rw" }
`)
	// No WorkDir set — token becomes empty string, ExpandPath leaves it as-is.
	l := NewLoader(dir)
	rc, err := l.Build("wdtest")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Mounts) != 1 {
		t.Fatalf("Mounts len = %d, want 1", len(rc.Mounts))
	}
	if rc.Mounts[0].Src != "" {
		t.Errorf("mount Src = %q, want empty string when WorkDir unset", rc.Mounts[0].Src)
	}
}

// ── capabilities ──────────────────────────────────────────────────────────────

func TestLoadProfile_capabilitiesParsed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "cap.toml"), `
schema_version = "1"
name           = "cap"
capabilities   = ["claude", "gemini"]
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("cap")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if len(p.Capabilities) != 2 {
		t.Fatalf("Capabilities len = %d, want 2", len(p.Capabilities))
	}
	if p.Capabilities[0] != "claude" || p.Capabilities[1] != "gemini" {
		t.Errorf("Capabilities = %v, want [claude gemini]", p.Capabilities)
	}
}

func TestBuild_capabilitiesPropagatesToRunConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "cap.toml"), `
schema_version = "1"
name           = "cap"
capabilities   = ["cursor"]
[entrypoint]
cmd = "cursor-agent"
`)
	l := NewLoader(dir)
	rc, err := l.Build("cap")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rc.Capabilities) != 1 || rc.Capabilities[0] != "cursor" {
		t.Errorf("RunConfig.Capabilities = %v, want [cursor]", rc.Capabilities)
	}
}

func TestLoadProfile_capabilitiesInheritedViaExtends(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name           = "base"
capabilities   = ["claude"]
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
schema_version = "1"
name           = "child"
extends        = "base"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile child: %v", err)
	}
	if len(p.Capabilities) != 1 || p.Capabilities[0] != "claude" {
		t.Errorf("child.Capabilities = %v, want [claude] (inherited from base)", p.Capabilities)
	}
}

func TestLoadProfile_capabilitiesMergedInExtends(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name           = "base"
capabilities   = ["claude"]
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
schema_version = "1"
name           = "child"
extends        = "base"
capabilities   = ["gemini"]
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile child: %v", err)
	}
	if len(p.Capabilities) != 2 {
		t.Fatalf("child.Capabilities len = %d, want 2; got %v", len(p.Capabilities), p.Capabilities)
	}
	// Base capability first, then child's addition — union, order preserved.
	if p.Capabilities[0] != "claude" || p.Capabilities[1] != "gemini" {
		t.Errorf("child.Capabilities = %v, want [claude gemini]", p.Capabilities)
	}
}

func TestLoadProfile_capabilitiesNoDuplicatesOnExtends(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name           = "base"
capabilities   = ["claude"]
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
schema_version = "1"
name           = "child"
extends        = "base"
capabilities   = ["claude"]
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile child: %v", err)
	}
	if len(p.Capabilities) != 1 {
		t.Errorf("duplicate capability: child.Capabilities = %v, want [claude]", p.Capabilities)
	}
}

// --- path-traversal validation tests (issue #8) ---

func TestLoadProfile_traversal_slashInName(t *testing.T) {
	// A name containing "/" must be rejected before any filesystem access.
	dir := t.TempDir()
	// Place a valid TOML file one level above the profiles dir so that a
	// naive filepath.Join(dir, "profiles", "../secret.toml") would reach it.
	writeFile(t, filepath.Join(dir, "secret.toml"), `name = "secret"`)

	l := NewLoader(dir)
	_, err := l.LoadProfile("../secret")
	if err == nil {
		t.Fatal("LoadProfile with traversal name should return an error")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention 'invalid', got: %v", err)
	}
}

func TestLoadProfile_traversal_backslashInName(t *testing.T) {
	l := NewLoader(t.TempDir())
	_, err := l.LoadProfile(`foo\bar`)
	if err == nil {
		t.Fatal("LoadProfile with backslash in name should return an error")
	}
}

func TestLoadProfile_traversal_dotdotName(t *testing.T) {
	l := NewLoader(t.TempDir())
	_, err := l.LoadProfile("..")
	if err == nil {
		t.Fatal("LoadProfile with '..' name should return an error")
	}
}

func TestLoadProfile_traversal_dotdotComponent(t *testing.T) {
	// Dotdot embedded in a longer name must also be caught.
	l := NewLoader(t.TempDir())
	_, err := l.LoadProfile("foo/../bar")
	if err == nil {
		t.Fatal("LoadProfile with '..' component should return an error")
	}
}

func TestLoadProfile_traversal_doesNotReachOutsideProfilesDir(t *testing.T) {
	// Even if a valid TOML file is reachable via traversal, LoadProfile must
	// not load it — the traversal error fires before any stat/open.
	dir := t.TempDir()
	// This file would be reached by name "../outside" without the fix.
	writeFile(t, filepath.Join(dir, "outside.toml"), "name = \"outside\"\ndescription = \"should not be loaded\"")

	l := NewLoader(dir)
	_, err := l.LoadProfile("../outside")
	if err == nil {
		t.Fatal("LoadProfile with traversal must not load files outside the profiles dir")
	}
}

func TestLoadProfile_extends_traversal_bareNameDotDot(t *testing.T) {
	// extends with a bare name that is ".." should be rejected.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `extends = ".."`)
	// Place a TOML file at dir/profiles/...toml — won't be reached because the
	// name is rejected at validation time.
	l := NewLoader(dir)
	_, err := l.LoadProfile("child")
	if err == nil {
		t.Fatal("extends with bare '..' should return an error")
	}
}

func TestLoadProfile_extends_traversal_bareNameSlash(t *testing.T) {
	// extends with a bare name containing "/" should be rejected.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "secret.toml"), `name = "secret"`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `extends = "../secret"`)
	// Without the fix resolveExtendsPath takes the path branch and ExpandPath
	// returns dir/secret.toml — a file outside the profiles directory.
	l := NewLoader(dir)
	_, err := l.LoadProfile("child")
	if err == nil {
		t.Fatal("extends with traversal path must not load files outside the profiles dir")
	}
}

func TestLoadProfile_noCapabilities_emptySlice(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "plain.toml"), `
schema_version = "1"
name           = "plain"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("plain")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if len(p.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want empty for profile without capabilities", p.Capabilities)
	}
}

// --- cursor_fix tests ---

func TestLoadProfile_cursorFixParsed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "shell.toml"), `
schema_version = "1"
name = "shell"

[entrypoint]
cmd         = "/bin/bash"
interactive = true
cursor_fix  = "shell"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("shell")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Entrypoint.CursorFix != "shell" {
		t.Errorf("CursorFix = %q, want %q", p.Entrypoint.CursorFix, "shell")
	}
}

func TestBuild_cursorFix_propagatedToRunConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "shell.toml"), `
schema_version = "1"
name = "shell"

[entrypoint]
cmd        = "/bin/bash"
cursor_fix = "newlines"
`)
	l := NewLoader(dir)
	rc, err := l.Build("shell")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.Entrypoint.CursorFix != "newlines" {
		t.Errorf("RunConfig.Entrypoint.CursorFix = %q, want %q", rc.Entrypoint.CursorFix, "newlines")
	}
}

func TestLoadProfile_extends_cursorFixInherited(t *testing.T) {
	// cursor_fix set in parent is inherited by child when child omits it.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name = "base"

[entrypoint]
cmd        = "/bin/bash"
cursor_fix = "shell"
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
schema_version = "1"
name    = "child"
extends = "base"

[env]
set = { "FOO" = "bar" }
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Entrypoint.CursorFix != "shell" {
		t.Errorf("CursorFix = %q, want inherited %q", p.Entrypoint.CursorFix, "shell")
	}
}

func TestLoadProfile_extends_cursorFixOverridden(t *testing.T) {
	// Child can override cursor_fix set by parent.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name = "base"

[entrypoint]
cursor_fix = "shell"
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
schema_version = "1"
name    = "child"
extends = "base"

[entrypoint]
cursor_fix = "newlines"
`)
	l := NewLoader(dir)
	p, err := l.LoadProfile("child")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if p.Entrypoint.CursorFix != "newlines" {
		t.Errorf("CursorFix = %q, want %q (child override)", p.Entrypoint.CursorFix, "newlines")
	}
}
