package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

type failingIsolator struct {
	code int
}

func (f *failingIsolator) Build(config.RunConfig) (*exec.Cmd, error) {
	return exec.Command("sh", "-c", fmt.Sprintf("exit %d", f.code)), nil
}
func (f *failingIsolator) Available() (bool, string) { return true, "fake" }

func newRunProfileTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	oldFetchRunProfileURL := fetchRunProfileURL
	client := srv.Client()
	fetchRunProfileURL = func(rawURL string) ([]byte, error) {
		resp, err := client.Get(rawURL) //nolint:noctx
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching %s: HTTP %d", rawURL, resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	}
	t.Cleanup(func() {
		fetchRunProfileURL = oldFetchRunProfileURL
		srv.Close()
	})
	return srv
}

// newRunTestApp returns an App with a fake isolator and a profile in tempdir.
func newRunTestApp(t *testing.T) (*App, string) {
	t.Helper()
	app, dir := newTestApp(t)
	app.isolatorFn = func() (isolator.Isolator, error) { return &fakeIsolator{}, nil }
	app.launcherFn = executor.New
	return app, dir
}

// minimalProfile writes a bare-minimum profile to dir/profiles/NAME.toml.
func minimalProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "profiles", name+".toml"), content)
}

// ── parseMount ────────────────────────────────────────────────────────────────

func TestParseMount_destTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	m, err := parseMount("/src:~/sandbox")
	if err != nil {
		t.Fatalf("parseMount: %v", err)
	}
	want := filepath.Join(homeDir, "sandbox")
	if m.Dest != want {
		t.Errorf("Dest = %q, want %q", m.Dest, want)
	}
}

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

func TestApplyOverrides_envFlag_expandsHostVar(t *testing.T) {
	t.Setenv("INNER_TEST_VAR", "/opt/jdk/jdk-21")
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{env: []string{"JAVA_HOME=${INNER_TEST_VAR}"}}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Env.Set["JAVA_HOME"] != "/opt/jdk/jdk-21" {
		t.Errorf("Env.Set[JAVA_HOME] = %q, want /opt/jdk/jdk-21", rc.Env.Set["JAVA_HOME"])
	}
}

func TestApplyOverrides_envFlag_plainValueUnchanged(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{env: []string{"X=plain-value"}}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Env.Set["X"] != "plain-value" {
		t.Errorf("Env.Set[X] = %q, want plain-value", rc.Env.Set["X"])
	}
}

func TestApplyOverrides_envFlag_expandsTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{env: []string{"X=~/dir"}}, nil); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeDir, "dir")
	if rc.Env.Set["X"] != want {
		t.Errorf("Env.Set[X] = %q, want %q", rc.Env.Set["X"], want)
	}
}

func TestApplyOverrides_prompt(t *testing.T) {
	// --prompt is deprecated but must still work for backward compatibility.
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{prompt: "do the thing"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(rc.Entrypoint.Args) == 0 || rc.Entrypoint.Args[0] != "do the thing" {
		t.Errorf("expected prompt in args, got: %v", rc.Entrypoint.Args)
	}
}

func TestApplyOverrides_entrypointTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{entrypoint: "~/bin/mytool"}, nil); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeDir, "bin/mytool")
	if rc.Entrypoint.Cmd != want {
		t.Errorf("Entrypoint.Cmd = %q, want %q", rc.Entrypoint.Cmd, want)
	}
}

func TestApplyOverrides_entrypoint(t *testing.T) {
	rc := &config.RunConfig{
		Entrypoint: config.Entrypoint{
			Cmd:  "old-cmd",
			Args: []string{"--profile-arg"},
		},
	}
	if err := applyOverrides(rc, runCLIFlags{entrypoint: "/usr/bin/python3"}, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Entrypoint.Cmd != "/usr/bin/python3" {
		t.Errorf("Cmd = %q, want /usr/bin/python3", rc.Entrypoint.Cmd)
	}
	// Profile args must be cleared — new entrypoint, clean slate.
	if len(rc.Entrypoint.Args) != 0 {
		t.Errorf("Args should be empty after entrypoint override, got: %v", rc.Entrypoint.Args)
	}
}

func TestApplyOverrides_entrypointWithArgs(t *testing.T) {
	// --entrypoint clears profile args; extraArgs come before --arg flags.
	rc := &config.RunConfig{
		Entrypoint: config.Entrypoint{
			Cmd:  "old-cmd",
			Args: []string{"--profile-arg"},
		},
	}
	if err := applyOverrides(rc, runCLIFlags{
		entrypoint: "/usr/bin/python3",
		args:       []string{"script.py"},
	}, []string{"--verbose"}); err != nil {
		t.Fatal(err)
	}
	// order: extraArgs (fixed structure) → --arg flags (variable input)
	want := []string{"--verbose", "script.py"}
	if len(rc.Entrypoint.Args) != 2 ||
		rc.Entrypoint.Args[0] != "--verbose" ||
		rc.Entrypoint.Args[1] != "script.py" {
		t.Errorf("unexpected args: %v, want %v", rc.Entrypoint.Args, want)
	}
}

func TestApplyOverrides_arg(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyOverrides(rc, runCLIFlags{args: []string{"fix the bug"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(rc.Entrypoint.Args) == 0 || rc.Entrypoint.Args[0] != "fix the bug" {
		t.Errorf("expected arg in entrypoint args, got: %v", rc.Entrypoint.Args)
	}
}

func TestApplyOverrides_argMultiple(t *testing.T) {
	rc := &config.RunConfig{Entrypoint: config.Entrypoint{Args: []string{"--existing"}}}
	flags := runCLIFlags{args: []string{"first", "second"}}
	if err := applyOverrides(rc, flags, nil); err != nil {
		t.Fatal(err)
	}
	// order: profile args → extraArgs (none here) → --arg flags
	want := []string{"--existing", "first", "second"}
	if len(rc.Entrypoint.Args) != 3 || rc.Entrypoint.Args[0] != "--existing" ||
		rc.Entrypoint.Args[1] != "first" || rc.Entrypoint.Args[2] != "second" {
		t.Errorf("unexpected entrypoint args: %v, want %v", rc.Entrypoint.Args, want)
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

// ── loadArgsFile ──────────────────────────────────────────────────────────────

func TestLoadArgsFile_basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue.md")
	if err := os.WriteFile(path, []byte("fix the bug\ndetails here"), 0o644); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	content, err := loadArgsFile(&w, path)
	if err != nil {
		t.Fatalf("loadArgsFile: %v", err)
	}
	if content != "fix the bug\ndetails here" {
		t.Errorf("unexpected content: %q", content)
	}
	if w.Len() != 0 {
		t.Errorf("expected no warning for small file, got: %s", w.String())
	}
}

func TestLoadArgsFile_missing(t *testing.T) {
	var w bytes.Buffer
	_, err := loadArgsFile(&w, "/nonexistent/path/issue.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadArgsFile_tooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.md")
	// Write a file just over 2 MB (fill with 'x' to avoid null bytes).
	data := bytes.Repeat([]byte("x"), argsFileMaxSize+1)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	_, err := loadArgsFile(&w, path)
	if err == nil {
		t.Fatal("expected error for file exceeding max size")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error should mention 'too large', got: %v", err)
	}
}

func TestLoadArgsFile_binaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	// Embed a null byte — would truncate the argument at the kernel execve layer.
	if err := os.WriteFile(path, []byte("text\x00binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	_, err := loadArgsFile(&w, path)
	if err == nil {
		t.Fatal("expected error for binary file with null bytes")
	}
	if !strings.Contains(err.Error(), "null bytes") {
		t.Errorf("error should mention null bytes, got: %v", err)
	}
}

func TestLoadArgsFile_warnThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	// Write a file just over the warning threshold but under the max.
	// Fill with 'x' to avoid null bytes (which would trigger the binary-file check).
	data := bytes.Repeat([]byte("x"), argsFileWarnSize+1)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	_, err := loadArgsFile(&w, path)
	if err != nil {
		t.Fatalf("loadArgsFile: %v", err)
	}
	if !strings.Contains(w.String(), "warning") {
		t.Errorf("expected warning for large file, got: %q", w.String())
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

func TestRunSandbox_nonZeroExitReleasesWorkspaceLock(t *testing.T) {
	app, dir := newRunTestApp(t)
	app.isolatorFn = func() (isolator.Isolator, error) { return &failingIsolator{code: 7}, nil }

	srcDir := t.TempDir()
	workspacesPath := t.TempDir()
	workspaceDest := filepath.Join(workspacesPath, "project")
	minimalProfile(t, dir, "nonzero", fmt.Sprintf(`
workspaces_path = %q

[entrypoint]
cmd = "sh"
interactive = false

[mounts]
%q = { dest = "${workspaces_path}/project", mode = "rw" }
`, workspacesPath, srcDir))

	err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{profile: "nonzero"}, nil)
	if err == nil {
		t.Fatal("expected non-zero sandbox exit error, got nil")
	}
	var exitErr exitCoder
	if !errors.As(err, &exitErr) {
		t.Fatalf("error = %T %v, want exit code error", err, err)
	}
	if exitErr.ExitCode() != 7 {
		t.Fatalf("ExitCode = %d, want 7", exitErr.ExitCode())
	}

	locks, globErr := filepath.Glob(filepath.Join(workspacesPath, ".inner-*.lock"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(locks) != 0 {
		t.Fatalf("workspace locks were not released: %v", locks)
	}
	if _, statErr := os.Stat(workspaceDest); !os.IsNotExist(statErr) {
		t.Fatalf("workspace dest was not cleaned up, stat err = %v", statErr)
	}
}

func TestRunSandbox_dryRun_withPrompt(t *testing.T) {
	// --prompt is deprecated but must still appear in dry-run output.
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

func TestRunSandbox_dryRun_withArg(t *testing.T) {
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
		args:   []string{"security audit"},
	}, nil)
	if err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if !strings.Contains(buf.String(), "security audit") {
		t.Errorf("expected arg in dry-run output, got: %s", buf.String())
	}
}

func TestRunSandbox_dryRun_withArgsFile(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "default", `
[entrypoint]
cmd = "claude"
args = ["--dangerously-skip-permissions"]
interactive = false
`)

	issueFile := filepath.Join(t.TempDir(), "012-user-problem.md")
	if err := os.WriteFile(issueFile, []byte("fix the login bug"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{
		dryRun:   true,
		argsFile: issueFile,
	}, nil)
	if err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if !strings.Contains(buf.String(), "fix the login bug") {
		t.Errorf("expected file content in dry-run output, got: %s", buf.String())
	}
}

func TestRunSandbox_dryRun_argsFileTilde(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "default", `
[entrypoint]
cmd         = "claude"
interactive = false
`)

	argsPath := filepath.Join(homeDir, "myprompt.txt")
	if err := os.WriteFile(argsPath, []byte("fix the tilde bug"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{
		dryRun:   true,
		argsFile: "~/myprompt.txt",
	}, nil)
	if err != nil {
		t.Fatalf("runSandbox with ~/args-file: %v", err)
	}
	if !strings.Contains(buf.String(), "fix the tilde bug") {
		t.Errorf("expected file content in dry-run output, got: %s", buf.String())
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

func TestRunSandbox_unknownAllowKey_blocked(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "badallow", `
[entrypoint]
interactive = true
[sandbox]
allow = ["ssh-keys", "not-a-valid-key"]
`)
	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{profile: "badallow", dryRun: true}, nil)
	if err == nil {
		t.Fatal("expected error for unknown allow key, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-valid-key") {
		t.Errorf("error should mention the bad key, got: %v", err)
	}
}

// ── cursor_fix ────────────────────────────────────────────────────────────────

func TestRunSandbox_cursorFixShell_injectsPromptCommand(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "fix-shell", `
[entrypoint]
cmd        = "/bin/bash"
interactive = true
cursor_fix = "shell"
`)
	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{dryRun: true, profile: "fix-shell"}, nil); err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "PROMPT_COMMAND") {
		t.Errorf("expected PROMPT_COMMAND in dry-run output for cursor_fix=shell, got:\n%s", out)
	}
}

func TestRunSandbox_cursorFixNone_noPromptCommand(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "fix-none", `
[entrypoint]
cmd         = "/bin/bash"
interactive = true
`)
	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{dryRun: true, profile: "fix-none"}, nil); err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if strings.Contains(buf.String(), "PROMPT_COMMAND") {
		t.Errorf("unexpected PROMPT_COMMAND in dry-run output when cursor_fix is unset")
	}
}

func TestRunSandbox_cursorFixNewlines_noPromptCommand(t *testing.T) {
	// cursor_fix="newlines" prints \r\n on exit but does NOT inject PROMPT_COMMAND.
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "fix-newlines", `
[entrypoint]
cmd        = "/bin/bash"
interactive = true
cursor_fix = "newlines"
`)
	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{dryRun: true, profile: "fix-newlines"}, nil); err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if strings.Contains(buf.String(), "PROMPT_COMMAND") {
		t.Errorf("unexpected PROMPT_COMMAND in dry-run output for cursor_fix=newlines")
	}
}

// ── URL profile in runSandbox ─────────────────────────────────────────────────

const urlProfileTOML = `schema_version = "1"
name = "url-profile"

[entrypoint]
cmd = "/bin/sh"
interactive = false
`

func TestRunSandbox_URLProfile(t *testing.T) {
	srv := newRunProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(urlProfileTOML))
	}))

	app, _ := newRunTestApp(t)
	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{dryRun: true, profile: srv.URL + "/url-profile.toml"}, nil); err != nil {
		t.Fatalf("runSandbox with URL profile: %v", err)
	}
}

func TestRunSandbox_URLProfile_fetchError(t *testing.T) {
	srv := newRunProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	app, _ := newRunTestApp(t)
	err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{profile: srv.URL + "/missing.toml"}, nil)
	if err == nil {
		t.Fatal("expected error for 404 URL profile, got nil")
	}
	if !strings.Contains(err.Error(), "downloading profile") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPrepareNestedUserNs_insertsBeforeFirstSeparatorOnly(t *testing.T) {
	// The entrypoint args contain a literal "--" of their own; the bwrap flags
	// must be inserted only before the first separator.
	cmd := exec.Command("bwrap", "--ro-bind", "/", "/", "--", "claude", "--", "foo")
	postStart, err := prepareNestedUserNs(cmd)
	if err != nil {
		t.Fatalf("prepareNestedUserNs: %v", err)
	}
	// Unblock the pipes so the test does not leak goroutine state if postStart
	// were ever invoked; here we only inspect args.
	_ = postStart

	want := []string{"bwrap", "--ro-bind", "/", "/", "--userns-block-fd", "3", "--info-fd", "4", "--", "claude", "--", "foo"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", cmd.Args, want)
		}
	}
	if len(cmd.ExtraFiles) != 2 {
		t.Fatalf("ExtraFiles = %d, want 2", len(cmd.ExtraFiles))
	}
}

func TestPrepareNestedUserNs_missingSeparator(t *testing.T) {
	cmd := exec.Command("bwrap", "--ro-bind", "/", "/")
	if _, err := prepareNestedUserNs(cmd); err == nil {
		t.Fatal("expected error for args without -- separator, got nil")
	}
	if len(cmd.ExtraFiles) != 0 {
		t.Fatalf("ExtraFiles = %d, want 0 (cmd must not be mutated on error)", len(cmd.ExtraFiles))
	}
}

// ── wrapWithLimits tests ──────────────────────────────────────────────────────

func TestWrapWithLimits_zeroLimits(t *testing.T) {
	// Zero limits → cmd is returned unchanged.
	orig := exec.Command("bwrap", "--ro-bind", "/", "/", "--", "/bin/sh")
	got, err := wrapWithLimits(io.Discard, orig, config.ResourceLimits{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != orig {
		t.Error("expected same cmd pointer when limits are zero")
	}
}

func TestWrapWithLimits_nestedUserNs_skipsWrapping(t *testing.T) {
	// nestedUserNs=true → no systemd-run wrapping (warning printed, cmd unchanged).
	limits := config.ResourceLimits{Memory: "2G", CPU: "200%", Pids: 256}
	orig := exec.Command("bwrap", "--", "/bin/sh")
	var buf bytes.Buffer
	got, err := wrapWithLimits(&buf, orig, limits, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != orig {
		t.Error("expected same cmd pointer when nestedUserNs is true")
	}
	if !strings.Contains(buf.String(), "nested-user-ns") {
		t.Errorf("expected warning about nested-user-ns, got: %q", buf.String())
	}
}

func TestWrapWithLimits_noSystemdRun(t *testing.T) {
	// systemd-run not in PATH → warning, cmd returned unchanged.
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir()) // empty PATH → systemd-run not found
	defer os.Setenv("PATH", origPATH)

	limits := config.ResourceLimits{Memory: "2G"}
	orig := exec.Command("bwrap", "--", "/bin/sh")
	var buf bytes.Buffer
	got, err := wrapWithLimits(&buf, orig, limits, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != orig {
		t.Error("expected same cmd pointer when systemd-run is absent")
	}
	if !strings.Contains(buf.String(), "systemd-run not found") {
		t.Errorf("expected warning about systemd-run, got: %q", buf.String())
	}
}

func TestWrapWithLimits_wrapsCorrectly(t *testing.T) {
	// Simulate an active systemd user session.
	orig := systemdUserSessionAvailableFn
	systemdUserSessionAvailableFn = func() bool { return true }
	t.Cleanup(func() { systemdUserSessionAvailableFn = orig })

	// Create a fake systemd-run in a temp dir.
	fakeDir := t.TempDir()
	fakeBin := filepath.Join(fakeDir, "systemd-run")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	limits := config.ResourceLimits{Memory: "4G", CPU: "200%", Pids: 512}
	bwrapArgs := []string{"bwrap", "--ro-bind", "/", "/", "--", "/bin/sh"}
	bwrapCmd := exec.Command(bwrapArgs[0], bwrapArgs[1:]...)

	got, err := wrapWithLimits(io.Discard, bwrapCmd, limits, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == bwrapCmd {
		t.Fatal("expected a new wrapped cmd")
	}
	args := got.Args
	if !strings.HasSuffix(args[0], "systemd-run") {
		t.Errorf("args[0] = %q, want systemd-run", args[0])
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--user",
		"--scope",
		"--property=MemoryMax=4G",
		"--property=CPUQuota=200%",
		"--property=TasksMax=512",
		"-- bwrap",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q in: %s", want, joined)
		}
	}
}

func TestWrapWithLimits_cpuNormalization(t *testing.T) {
	// "2.0" cores should become "200%" in the systemd-run arg.
	orig := systemdUserSessionAvailableFn
	systemdUserSessionAvailableFn = func() bool { return true }
	t.Cleanup(func() { systemdUserSessionAvailableFn = orig })

	fakeDir := t.TempDir()
	fakeBin := filepath.Join(fakeDir, "systemd-run")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	limits := config.ResourceLimits{CPU: "2.0"}
	bwrapCmd := exec.Command("bwrap", "--", "/bin/sh")
	got, err := wrapWithLimits(io.Discard, bwrapCmd, limits, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(got.Args, " ")
	if !strings.Contains(joined, "CPUQuota=200%") {
		t.Errorf("expected CPUQuota=200%%, got args: %s", joined)
	}
}

// ── workdirCoversHome ─────────────────────────────────────────────────────────

func TestWorkdirCoversHome(t *testing.T) {
	const home = "/home/user"
	tests := []struct {
		name    string
		workdir string
		want    bool
	}{
		{"workdir is home", "/home/user", true},
		{"workdir is parent of home", "/home", true},
		{"workdir is filesystem root", "/", true},
		{"workdir is subdirectory of home", "/home/user/project", false},
		{"unrelated path", "/srv/data", false},
		{"sibling with common prefix", "/home/username", false},
		{"empty workdir", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := workdirCoversHome(tc.workdir, home); got != tc.want {
				t.Errorf("workdirCoversHome(%q, %q) = %v, want %v", tc.workdir, home, got, tc.want)
			}
		})
	}
	if workdirCoversHome("/home/user", "") {
		t.Error("empty home must never be covered")
	}
}

// ── containers.conf override (rootless podman) ───────────────────────────────

func TestApplyContainersConf_generatesForNestedUserNs(t *testing.T) {
	rc := &config.RunConfig{Allow: []string{"nested-user-ns"}}
	cleanup, err := applyContainersConf(rc)
	if err != nil {
		t.Fatalf("applyContainersConf: %v", err)
	}
	defer cleanup()

	if rc.ContainersConfPath == "" {
		t.Fatal("expected a generated containers.conf path")
	}
	b, err := os.ReadFile(rc.ContainersConfPath)
	if err != nil {
		t.Fatalf("reading generated file: %v", err)
	}
	if !strings.Contains(string(b), `cgroup_manager = "cgroupfs"`) {
		t.Errorf("expected cgroupfs override, got:\n%s", b)
	}

	cleanup()
	if _, err := os.Stat(rc.ContainersConfPath); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove the generated file, stat err = %v", err)
	}
}

func TestApplyContainersConf_noopWithoutNestedUserNs(t *testing.T) {
	rc := &config.RunConfig{}
	cleanup, err := applyContainersConf(rc)
	if err != nil {
		t.Fatalf("applyContainersConf: %v", err)
	}
	defer cleanup()
	if rc.ContainersConfPath != "" {
		t.Errorf("expected no override without nested-user-ns, got %q", rc.ContainersConfPath)
	}
}

func TestApplyContainersConf_optOutWithSystemd(t *testing.T) {
	rc := &config.RunConfig{Allow: []string{"nested-user-ns"}, CgroupManager: "systemd"}
	cleanup, err := applyContainersConf(rc)
	if err != nil {
		t.Fatalf("applyContainersConf: %v", err)
	}
	defer cleanup()
	if rc.ContainersConfPath != "" {
		t.Errorf(`cgroup_manager = "systemd" must skip the override, got %q`, rc.ContainersConfPath)
	}
}

func TestApplyContainersConf_profileEnvSetTakesOver(t *testing.T) {
	rc := &config.RunConfig{
		Allow: []string{"nested-user-ns"},
		Env:   config.EnvConfig{Set: map[string]string{"CONTAINERS_CONF_OVERRIDE": "/custom/containers.conf"}},
	}
	cleanup, err := applyContainersConf(rc)
	if err != nil {
		t.Fatalf("applyContainersConf: %v", err)
	}
	defer cleanup()
	if rc.ContainersConfPath != "" {
		t.Errorf("profile-provided CONTAINERS_CONF_OVERRIDE must skip generation, got %q", rc.ContainersConfPath)
	}
}

func TestApplyContainersConf_explicitCgroupfs(t *testing.T) {
	rc := &config.RunConfig{Allow: []string{"nested-user-ns"}, CgroupManager: "cgroupfs"}
	cleanup, err := applyContainersConf(rc)
	if err != nil {
		t.Fatalf("applyContainersConf: %v", err)
	}
	defer cleanup()
	if rc.ContainersConfPath == "" {
		t.Error("expected a generated containers.conf path")
	}
}
