package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// fakeHomeWithBin creates a temp home containing bin/<name> as an executable,
// points HOME and PATH at it, and returns (home, binPath).
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

func TestRunSandbox_isolatedHome_refusesWorkdirCoveringHome(t *testing.T) {
	app, dir := newRunTestApp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	minimalProfile(t, dir, "agent", `
[sandbox]
home = "isolated"

[entrypoint]
cmd = "sh"
interactive = false
`)

	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{profile: "agent", workdir: home, dryRun: true}, nil)
	if err == nil {
		t.Fatalf("expected runSandbox to refuse a workdir covering the home, output: %s", buf.String())
	}
	// The read-write workdir bind is emitted after the home tmpfs, so it would
	// silently restore the whole host home: the run must not start.
	if !strings.Contains(err.Error(), `home = "isolated"`) {
		t.Errorf("error should explain the conflict, got: %v", err)
	}
}

func TestRunSandbox_hostROHome_workdirCoveringHomeStillWarnsOnly(t *testing.T) {
	app, dir := newRunTestApp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	minimalProfile(t, dir, "shellish", `
[entrypoint]
cmd = "sh"
interactive = false
`)

	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{profile: "shellish", workdir: home, dryRun: true}, nil)
	if err != nil {
		t.Fatalf("host-ro behaviour changed: %v", err)
	}
	if !strings.Contains(buf.String(), "covers your home directory") {
		t.Errorf("expected the pre-existing warning, got: %s", buf.String())
	}
}

func TestRunSandbox_dryRun_showsHomeModeAndAllowlist(t *testing.T) {
	app, dir := newRunTestApp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	minimalProfile(t, dir, "agent", `
[sandbox]
home       = "isolated"
home_allow = ["~/.local/bin", "~/.nvm"]

[entrypoint]
cmd = "sh"
interactive = false
`)

	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{profile: "agent", dryRun: true}, nil); err != nil {
		t.Fatalf("runSandbox --dry-run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "home:        isolated") {
		t.Errorf("dry-run should report the home mode, got:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(home, ".local", "bin")) {
		t.Errorf("dry-run should list the allowlisted path, got:\n%s", out)
	}
	if !strings.Contains(out, "missing on host — skipped") {
		t.Errorf("dry-run should flag allowlist entries missing on this host, got:\n%s", out)
	}
}

func TestRunSandbox_dryRun_showsHostROByDefault(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "plain", `
[entrypoint]
cmd = "sh"
interactive = false
`)

	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{profile: "plain", dryRun: true}, nil); err != nil {
		t.Fatalf("runSandbox --dry-run: %v", err)
	}
	if !strings.Contains(buf.String(), "home:        host-ro") {
		t.Errorf("dry-run should report the default home mode, got:\n%s", buf.String())
	}
}

func TestWarnIsolatedHomeEntrypoint(t *testing.T) {
	_, binPath := fakeHomeWithBin(t, "fakeagent")

	rc := &config.RunConfig{
		HomeMode:   config.HomeIsolated,
		Entrypoint: config.Entrypoint{Cmd: "fakeagent"},
	}
	var buf bytes.Buffer
	warnIsolatedHomeEntrypoint(&buf, rc)
	out := buf.String()
	if !strings.Contains(out, binPath) {
		t.Fatalf("expected a warning naming %s, got: %q", binPath, out)
	}
	if !strings.Contains(out, "home_allow") {
		t.Errorf("warning should name the fix, got: %q", out)
	}

	// Covered by the allowlist → silent.
	rc.HomeAllow = []string{filepath.Dir(binPath)}
	buf.Reset()
	warnIsolatedHomeEntrypoint(&buf, rc)
	if buf.Len() != 0 {
		t.Errorf("no warning expected when home_allow covers the binary, got: %q", buf.String())
	}

	// Covered by a mount → silent.
	rc.HomeAllow = nil
	rc.Mounts = []config.Mount{{Src: filepath.Dir(binPath), Dest: filepath.Dir(binPath), Mode: "ro"}}
	buf.Reset()
	warnIsolatedHomeEntrypoint(&buf, rc)
	if buf.Len() != 0 {
		t.Errorf("no warning expected when a mount covers the binary, got: %q", buf.String())
	}

	// host-ro → the binary is visible through the root bind, nothing to say.
	rc.Mounts = nil
	rc.HomeMode = config.HomeHostRO
	buf.Reset()
	warnIsolatedHomeEntrypoint(&buf, rc)
	if buf.Len() != 0 {
		t.Errorf("no warning expected under host-ro, got: %q", buf.String())
	}
}

func TestWarnIsolatedHomeEntrypoint_symlinkTarget(t *testing.T) {
	home, binPath := fakeHomeWithBin(t, "payload")
	// Native installers put a link in ~/.local/bin pointing at a versioned
	// payload elsewhere in the home: allowlisting only the link would leave a
	// dangling symlink inside the sandbox.
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

	rc := &config.RunConfig{
		HomeMode:   config.HomeIsolated,
		HomeAllow:  []string{filepath.Dir(binPath)},
		Entrypoint: config.Entrypoint{Cmd: "payload"},
	}
	var buf bytes.Buffer
	warnIsolatedHomeEntrypoint(&buf, rc)
	if !strings.Contains(buf.String(), payloadDir) {
		t.Errorf("expected a warning about the uncovered symlink target, got: %q", buf.String())
	}
}
