package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/isolator"
)

// fakeIsolator is a test double that builds a simple no-op exec.Cmd.
type fakeIsolator struct{}

func (f *fakeIsolator) Build(cfg config.RunConfig) (*exec.Cmd, error) {
	args := []string{"--fake-sandbox", "--", cfg.Entrypoint.Cmd}
	args = append(args, cfg.Entrypoint.Args...)
	return exec.Command("echo", args...), nil
}
func (f *fakeIsolator) Available() (bool, string) { return true, "fake" }

// newRunTestApp returns an App with a fake isolator and a profile in tempdir.
func newRunTestApp(t *testing.T) (*App, string) {
	t.Helper()
	app, dir := newTestApp(t)
	app.isolatorFn = func() (isolator.Isolator, error) { return &fakeIsolator{}, nil }
	return app, dir
}

// minimalProfile writes a bare-minimum profile to dir/profiles/NAME.toml.
func minimalProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "profiles", name+".toml"), content)
}

// ── parseMount ────────────────────────────────────────────────────────────────

func TestParseMount_srcDest(t *testing.T) {
	m, err := parseMount("/src:/dest")
	if err != nil {
		t.Fatalf("parseMount: %v", err)
	}
	if m.Src != "/src" || m.Dest != "/dest" || m.Mode != "ro" {
		t.Errorf("got %+v, want {Src:/src Dest:/dest Mode:ro}", m)
	}
}

func TestParseMount_withMode(t *testing.T) {
	m, err := parseMount("/src:/dest:rw")
	if err != nil {
		t.Fatalf("parseMount: %v", err)
	}
	if m.Mode != "rw" {
		t.Errorf("Mode = %q, want rw", m.Mode)
	}
}

func TestParseMount_invalidFormat(t *testing.T) {
	if _, err := parseMount("onlysrc"); err == nil {
		t.Error("expected error for missing dest")
	}
}

func TestParseMount_invalidMode(t *testing.T) {
	if _, err := parseMount("/src:/dest:readwrite"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

// ── parseEnvVar ───────────────────────────────────────────────────────────────

func TestParseEnvVar_simple(t *testing.T) {
	k, v, err := parseEnvVar("FOO=bar")
	if err != nil || k != "FOO" || v != "bar" {
		t.Errorf("got (%q, %q, %v), want (FOO, bar, nil)", k, v, err)
	}
}

func TestParseEnvVar_valueWithEquals(t *testing.T) {
	k, v, err := parseEnvVar("KEY=a=b=c")
	if err != nil || k != "KEY" || v != "a=b=c" {
		t.Errorf("got (%q, %q, %v), want (KEY, a=b=c, nil)", k, v, err)
	}
}

func TestParseEnvVar_noEquals(t *testing.T) {
	if _, _, err := parseEnvVar("NOEQUALS"); err == nil {
		t.Error("expected error for missing =")
	}
}

// ── applyOverrides ────────────────────────────────────────────────────────────

func TestApplyOverrides_network(t *testing.T) {
	rc := &config.RunConfig{Network: false}
	if err := applyOverrides(rc, runCLIFlags{networkOn: true}, nil); err != nil {
		t.Fatal(err)
	}
	if !rc.Network {
		t.Error("expected Network = true after --network override")
	}
}

func TestApplyOverrides_noNetwork(t *testing.T) {
	rc := &config.RunConfig{Network: true}
	if err := applyOverrides(rc, runCLIFlags{networkOff: true}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Network {
		t.Error("expected Network = false after --no-network override")
	}
}

func TestApplyOverrides_interactive(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{interactive: true}, nil); err != nil {
		t.Fatal(err)
	}
	if !rc.Entrypoint.Interactive {
		t.Error("expected Interactive = true")
	}
}

func TestApplyOverrides_noInteractive(t *testing.T) {
	rc := &config.RunConfig{Entrypoint: config.Entrypoint{Interactive: true}}
	if err := applyOverrides(rc, runCLIFlags{noInteractive: true}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Entrypoint.Interactive {
		t.Error("expected Interactive = false")
	}
}

func TestApplyOverrides_timeout(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{timeout: 120}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Timeout != 120 {
		t.Errorf("Timeout = %d, want 120", rc.Timeout)
	}
}

func TestApplyOverrides_workdir(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{workdir: "/my/project"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(rc.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(rc.Mounts))
	}
	m := rc.Mounts[0]
	// dest == src: bwrap cannot mkdir on a ro root, so workdir is mounted
	// at the same path as the host (which already exists via --ro-bind / /).
	if m.Src != "/my/project" || m.Dest != "/my/project" || m.Mode != "rw" {
		t.Errorf("unexpected mount: %+v", m)
	}
}

func TestApplyOverrides_mountFlag(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{mounts: []string{"/a:/b:rw"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(rc.Mounts) == 0 || rc.Mounts[0].Mode != "rw" {
		t.Errorf("unexpected mounts: %+v", rc.Mounts)
	}
}

func TestApplyOverrides_envFlag(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{env: []string{"MY_VAR=hello"}}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Env.Set["MY_VAR"] != "hello" {
		t.Errorf("Env.Set[MY_VAR] = %q, want hello", rc.Env.Set["MY_VAR"])
	}
}

func TestApplyOverrides_prompt(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{prompt: "do the thing"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(rc.Entrypoint.Args) == 0 || rc.Entrypoint.Args[0] != "do the thing" {
		t.Errorf("expected prompt in args, got: %v", rc.Entrypoint.Args)
	}
}

func TestApplyOverrides_extraArgs(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{}, []string{"--flag", "val"}); err != nil {
		t.Fatal(err)
	}
	if len(rc.Entrypoint.Args) != 2 || rc.Entrypoint.Args[0] != "--flag" {
		t.Errorf("unexpected entrypoint args: %v", rc.Entrypoint.Args)
	}
}

// ── runSandbox --dry-run ──────────────────────────────────────────────────────

func TestRunSandbox_dryRun_printsCommand(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "default", `
[entrypoint]
cmd = "sh"
interactive = false
`)

	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{dryRun: true}, nil)
	if err != nil {
		t.Fatalf("runSandbox --dry-run: %v", err)
	}
	out := buf.String()
	if strings.TrimSpace(out) == "" {
		t.Error("expected non-empty dry-run output")
	}
}

func TestRunSandbox_dryRun_noExecution(t *testing.T) {
	// Verify that --dry-run does not call the launcher.
	app, dir := newRunTestApp(t)
	launcherCalled := false
	app.launcherFn = func() *executor.Launcher {
		launcherCalled = true
		return executor.New() //nolint:staticcheck
	}
	_ = launcherCalled
	minimalProfile(t, dir, "default", `
[entrypoint]
cmd = "sh"
interactive = false
`)

	var buf bytes.Buffer
	app.runSandbox(&buf, runCLIFlags{dryRun: true}, nil) //nolint:errcheck
	if launcherCalled {
		t.Error("launcher should not be called with --dry-run")
	}
}

func TestRunSandbox_dryRun_withPrompt(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "default", `
[entrypoint]
cmd = "claude"
args = ["--dangerously-skip-permissions"]
interactive = false
`)

	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{
		dryRun: true,
		prompt: "security audit",
	}, nil)
	if err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if !strings.Contains(buf.String(), "security audit") {
		t.Errorf("expected prompt in dry-run output, got: %s", buf.String())
	}
}

func TestRunSandbox_missingProfile(t *testing.T) {
	app, _ := newRunTestApp(t)
	err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{
		profile: "nonexistent",
		dryRun:  true,
	}, nil)
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}
