package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

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

// ── guardWorkdir ──────────────────────────────────────────────────────────────

func TestGuardWorkdir_refusesFilesystemRoot(t *testing.T) {
	rc := &config.RunConfig{Workdir: "/"}
	var buf bytes.Buffer
	proceed, err := guardWorkdir(&buf, rc, false, false)
	if err == nil {
		t.Fatalf("expected an error for workdir /, output: %s", buf.String())
	}
	if proceed {
		t.Error("proceed must be false when the workdir is refused")
	}
	// The message has to say why, not just refuse.
	if !strings.Contains(err.Error(), "modify any file on this machine") {
		t.Errorf("error should explain the consequence, got: %v", err)
	}
}

func TestGuardWorkdir_warnsOnSystemDir(t *testing.T) {
	rc := &config.RunConfig{Workdir: "/etc"}
	var buf bytes.Buffer
	proceed, err := guardWorkdir(&buf, rc, false, false)
	if err != nil || !proceed {
		t.Fatalf("an explicit system workdir is allowed with a warning, got proceed=%v err=%v", proceed, err)
	}
	if !strings.Contains(buf.String(), "system directory") {
		t.Errorf("expected a system-directory warning, got: %q", buf.String())
	}
}

func TestGuardWorkdir_ordinaryWorkdirIsSilent(t *testing.T) {
	rc := &config.RunConfig{Workdir: t.TempDir()}
	var buf bytes.Buffer
	proceed, err := guardWorkdir(&buf, rc, true, false)
	if err != nil || !proceed {
		t.Fatalf("ordinary workdir rejected: proceed=%v err=%v", proceed, err)
	}
	if buf.Len() != 0 {
		t.Errorf("no output expected for an ordinary workdir, got: %q", buf.String())
	}
}

func TestGuardWorkdir_isolatedHomeRefusesHomeWorkdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := &config.RunConfig{Workdir: home, HomeMode: config.HomeIsolated}
	var buf bytes.Buffer
	proceed, err := guardWorkdir(&buf, rc, false, false)
	if err == nil || proceed {
		t.Fatalf("expected a refusal, got proceed=%v err=%v", proceed, err)
	}
	if !strings.Contains(err.Error(), "cancel the isolation") {
		t.Errorf("error should explain what would be undone, got: %v", err)
	}
}

func TestGuardWorkdir_hostROHomeWorkdirWarnsAndProceeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := &config.RunConfig{Workdir: home}
	var buf bytes.Buffer
	// Explicit (-w) → warning only, no prompt.
	proceed, err := guardWorkdir(&buf, rc, false, false)
	if err != nil || !proceed {
		t.Fatalf("host-ro behaviour changed: proceed=%v err=%v", proceed, err)
	}
	if !strings.Contains(buf.String(), "covers your home directory") {
		t.Errorf("expected the home warning, got: %q", buf.String())
	}
	if strings.Contains(buf.String(), "continue?") {
		t.Errorf("an explicitly chosen workdir must not prompt, got: %q", buf.String())
	}
}

func TestGuardWorkdir_dryRunNeverPrompts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := &config.RunConfig{Workdir: home}
	var buf bytes.Buffer
	proceed, err := guardWorkdir(&buf, rc, true, true)
	if err != nil || !proceed {
		t.Fatalf("dry-run must proceed: proceed=%v err=%v", proceed, err)
	}
	if strings.Contains(buf.String(), "continue?") {
		t.Errorf("--dry-run must not prompt, got: %q", buf.String())
	}
}

func TestGuardWorkdir_autoConfirmNeverPrompts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := &config.RunConfig{Workdir: home, AutoConfirm: true}
	var buf bytes.Buffer
	proceed, err := guardWorkdir(&buf, rc, true, false)
	if err != nil || !proceed {
		t.Fatalf("--yes must proceed: proceed=%v err=%v", proceed, err)
	}
	if strings.Contains(buf.String(), "continue?") {
		t.Errorf("--yes must not prompt, got: %q", buf.String())
	}
}

// ── guardCLIMounts ────────────────────────────────────────────────────────────

func TestGuardCLIMounts_refusesRootReadWrite(t *testing.T) {
	var buf bytes.Buffer
	err := guardCLIMounts(&buf, &config.RunConfig{}, []string{"/:/:rw"})
	if err == nil {
		t.Fatalf("expected -m /:/:rw to be refused, output: %s", buf.String())
	}
	if !strings.Contains(err.Error(), "every file on this machine") {
		t.Errorf("error should explain the consequence, got: %v", err)
	}
}

func TestGuardCLIMounts_refusesHomeUnderIsolatedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := &config.RunConfig{HomeMode: config.HomeIsolated}

	var buf bytes.Buffer
	// Even read-only: re-binding the host home makes the whole home readable
	// again, which is exactly what the mode exists to prevent.
	err := guardCLIMounts(&buf, rc, []string{home + ":" + home})
	if err == nil {
		t.Fatalf("expected a home mount to be refused under an isolated home, output: %s", buf.String())
	}
	if !strings.Contains(err.Error(), `home = "isolated"`) {
		t.Errorf("error should name the conflicting setting, got: %v", err)
	}
}

func TestGuardCLIMounts_warnsOnSystemDirAndHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var buf bytes.Buffer
	if err := guardCLIMounts(&buf, &config.RunConfig{}, []string{"/etc:/etc:rw", home + ":" + home + ":rw"}); err != nil {
		t.Fatalf("host-ro mounts are allowed with a warning, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "system directory") {
		t.Errorf("expected a system-directory warning, got: %q", out)
	}
	if !strings.Contains(out, "persistence vector") {
		t.Errorf("expected a home warning, got: %q", out)
	}
}

func TestGuardCLIMounts_ordinaryMountsAreSilent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := t.TempDir()
	var buf bytes.Buffer
	if err := guardCLIMounts(&buf, &config.RunConfig{}, []string{src + ":" + src + ":rw", "/opt/tools:/opt/tools"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("no output expected for ordinary mounts, got: %q", buf.String())
	}
}
