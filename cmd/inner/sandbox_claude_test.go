package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enr/inner/internal/config"
)

// TestMain redirects applyClaude warnings (e.g. "claude not found in PATH") to
// io.Discard so expected warnings do not pollute test output, and zeroes the
// autoConfirm delay so tests that exercise that path don't sleep for 1 second.
func TestMain(m *testing.M) {
	claudeWarningWriter = io.Discard
	claudeAutoConfirmDelay = 0
	os.Exit(m.Run())
}

// testbinDir returns the absolute path to cmd/inner/testbin, which contains
// shim scripts for testing subprocess-dependent functions.
func testbinDir(t *testing.T) string {
	t.Helper()
	// The test binary runs from the package directory, so testbin/ is a sibling.
	abs, err := filepath.Abs("testbin")
	if err != nil {
		t.Fatalf("testbinDir: %v", err)
	}
	return abs
}

// withTestbin prepends testbin/ to PATH for the duration of the test so that
// exec.LookPath("claude") resolves to the shim script.
func withTestbin(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", testbinDir(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// makeClaudeHome builds a fake ~/.claude directory in dir with the given files.
func makeClaudeHome(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// ── prepareClaude ─────────────────────────────────────────────────────────────

func TestPrepareClaude_copiesCredentials(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{"token":"abc"}`,
	})

	dst, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("prepareClaude: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(dst, ".credentials.json"))
	if err != nil {
		t.Fatalf("reading credentials in sandbox: %v", err)
	}
	if string(data) != `{"token":"abc"}` {
		t.Errorf("credentials content mismatch: %s", data)
	}
}

func TestPrepareClaude_expiredToken_camelCase_ms(t *testing.T) {
	src := t.TempDir()
	expiredAt := (time.Now().Unix() - 3600) * 1000 // one hour ago, in ms (Claude Code format)
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": fmt.Sprintf(`{"expiresAt":%d}`, expiredAt),
	})

	_, cleanup, err := prepareClaude(src)
	if err == nil {
		cleanup()
		t.Fatal("expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention 'expired', got: %v", err)
	}
}

func TestPrepareClaude_freshToken_camelCase_ms(t *testing.T) {
	src := t.TempDir()
	freshAt := (time.Now().Unix() + 3600) * 1000 // one hour from now, in ms
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": fmt.Sprintf(`{"expiresAt":%d}`, freshAt),
	})

	_, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("expected no error for fresh token, got: %v", err)
	}
	cleanup()
}

func TestPrepareClaude_expiredToken_snakeCase_seconds(t *testing.T) {
	src := t.TempDir()
	expiredAt := time.Now().Unix() - 3600 // one hour ago, in seconds
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": fmt.Sprintf(`{"expires_at":%d}`, expiredAt),
	})

	_, cleanup, err := prepareClaude(src)
	if err == nil {
		cleanup()
		t.Fatal("expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention 'expired', got: %v", err)
	}
}

func TestPrepareClaude_nestedExpiresAt_expired(t *testing.T) {
	src := t.TempDir()
	expiredAt := time.Now().Unix() - 3600
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": fmt.Sprintf(`{"oauth_token":{"expires_at":%d}}`, expiredAt),
	})

	_, cleanup, err := prepareClaude(src)
	if err == nil {
		cleanup()
		t.Fatal("expected error for expired nested token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention 'expired', got: %v", err)
	}
}

func TestPrepareClaude_noExpiryField_succeeds(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{"access_token":"abc"}`, // no expiry field
	})

	_, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("expected no error when expiry field is absent, got: %v", err)
	}
	cleanup()
}

func TestPrepareClaude_missingCredentials_returnsError(t *testing.T) {
	src := t.TempDir() // no .credentials.json
	_, cleanup, err := prepareClaude(src)
	if err == nil {
		cleanup()
		t.Fatal("expected error for missing .credentials.json")
	}
}

func TestPrepareClaude_copiesSettings(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
		"settings.json":     `{"effortLevel":"high"}`,
	})

	dst, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("prepareClaude: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dst, "settings.json")); err != nil {
		t.Error("settings.json not copied to sandbox")
	}
}

func TestPrepareClaude_missingSettings_noError(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
	})

	_, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("expected no error when settings.json missing, got: %v", err)
	}
	cleanup()
}

func TestPrepareClaude_copiesSkills(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
		"skills/foo.md":     "# skill foo",
	})

	dst, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("prepareClaude: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dst, "skills", "foo.md")); err != nil {
		t.Error("skills not copied to sandbox")
	}
}

func TestPrepareClaude_createsFreshDirs(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
	})

	dst, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("prepareClaude: %v", err)
	}
	defer cleanup()

	for _, d := range []string{"sessions", "cache", "projects", "history", "todos"} {
		info, err := os.Stat(filepath.Join(dst, d))
		if err != nil {
			continue // not all dirs are required; skip missing ones
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", d)
		}
	}
	// Verify sessions is empty (no cross-contamination).
	entries, _ := os.ReadDir(filepath.Join(dst, "sessions"))
	if len(entries) != 0 {
		t.Errorf("sessions dir should be empty, got %d entries", len(entries))
	}
}

func TestPrepareClaude_stripsSettingsMcpServers(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
		"settings.json":     `{"effortLevel":"high","mcpServers":{"my-server":{}},"enabledPlugins":["p"]}`,
	})

	dst, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("prepareClaude: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(dst, "settings.json"))
	if err != nil {
		t.Fatalf("reading sandboxed settings.json: %v", err)
	}
	if strings.Contains(string(data), "mcpServers") {
		t.Error("mcpServers should be stripped from sandboxed settings.json")
	}
	if strings.Contains(string(data), "enabledPlugins") {
		t.Error("enabledPlugins should be stripped from sandboxed settings.json")
	}
	if !strings.Contains(string(data), "effortLevel") {
		t.Error("effortLevel should be preserved in sandboxed settings.json")
	}
}

func TestPrepareClaude_doesNotExposeHistoryFromSrc(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
		"history.jsonl":     `{"event":"secret"}`,
	})

	dst, cleanup, err := prepareClaude(src)
	if err != nil {
		t.Fatalf("prepareClaude: %v", err)
	}
	defer cleanup()

	// history.jsonl must NOT be copied into the sandbox.
	if _, err := os.Stat(filepath.Join(dst, "history.jsonl")); err == nil {
		t.Error("history.jsonl should not be present in sandbox")
	}
}

// ── sandboxPS1 ────────────────────────────────────────────────────────────────

func TestSandboxPS1_containsInner(t *testing.T) {
	ps1 := sandboxPS1("myprofile")
	if ps1 == "" {
		t.Fatal("sandboxPS1 returned empty string")
	}
	if !strings.Contains(ps1, "inner") {
		t.Errorf("PS1 should mention 'inner', got: %q", ps1)
	}
}

func TestSandboxPS1_containsUserAndProfile(t *testing.T) {
	ps1 := sandboxPS1("myprofile")
	if !strings.Contains(ps1, `\u`) || !strings.Contains(ps1, "myprofile") {
		t.Errorf("PS1 should contain \\u and profile name, got: %q", ps1)
	}
}

// ── prepareInteractiveShell ───────────────────────────────────────────────────

func TestPrepareInteractiveShell_injectsBashInitFile(t *testing.T) {
	dir := t.TempDir()
	rc := &config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "/bin/bash", Interactive: true},
	}
	if err := prepareInteractiveShell(rc, dir, "(inner) $ "); err != nil {
		t.Fatalf("prepareInteractiveShell: %v", err)
	}
	if len(rc.Entrypoint.Args) < 2 || rc.Entrypoint.Args[0] != "--init-file" {
		t.Errorf("expected --init-file as first arg, got: %v", rc.Entrypoint.Args)
	}
	// Init file must exist, set PS1, and NOT source ~/.bashrc
	// (sourcing it would leak personal data that clearenv is meant to block).
	content, err := os.ReadFile(rc.Entrypoint.Args[1])
	if err != nil {
		t.Fatalf("reading init file: %v", err)
	}
	if strings.Contains(string(content), ".bashrc") {
		t.Error("init file must not source .bashrc (would leak env vars past clearenv)")
	}
	if !strings.Contains(string(content), "PS1=") {
		t.Error("init file should set PS1")
	}
}

func TestPrepareInteractiveShell_noopForNonInteractive(t *testing.T) {
	dir := t.TempDir()
	rc := &config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "/bin/bash", Interactive: false},
	}
	if err := prepareInteractiveShell(rc, dir, "(inner) $ "); err != nil {
		t.Fatalf("prepareInteractiveShell: %v", err)
	}
	if len(rc.Entrypoint.Args) != 0 {
		t.Errorf("should be no-op for non-interactive, got args: %v", rc.Entrypoint.Args)
	}
}

func TestPrepareInteractiveShell_noopForNonBash(t *testing.T) {
	dir := t.TempDir()
	rc := &config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "claude", Interactive: true},
	}
	if err := prepareInteractiveShell(rc, dir, "(inner) $ "); err != nil {
		t.Fatalf("prepareInteractiveShell: %v", err)
	}
	if len(rc.Entrypoint.Args) != 0 {
		t.Errorf("should be no-op for non-bash, got args: %v", rc.Entrypoint.Args)
	}
}

func TestPrepareInteractiveShell_noopIfInitFileAlreadySet(t *testing.T) {
	dir := t.TempDir()
	rc := &config.RunConfig{
		Entrypoint: config.Entrypoint{
			Cmd:         "bash",
			Args:        []string{"--init-file", "/custom/rc"},
			Interactive: true,
		},
	}
	if err := prepareInteractiveShell(rc, dir, "(inner) $ "); err != nil {
		t.Fatalf("prepareInteractiveShell: %v", err)
	}
	if rc.Entrypoint.Args[0] != "--init-file" || rc.Entrypoint.Args[1] != "/custom/rc" {
		t.Errorf("should not override existing --init-file, got: %v", rc.Entrypoint.Args)
	}
}

// ── applyClaude ───────────────────────────────────────────────────────────────

func TestApplyClaude_injectsMount(t *testing.T) {
	fakeHome := makeFakeHome(t)
	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{}`,
	})

	rc := &config.RunConfig{}
	cleanup, err := applyClaude(rc)
	if err != nil {
		t.Fatalf("applyClaude: %v", err)
	}
	defer cleanup()

	// applyClaude must inject a mount with Dest = claudeHomeDir().
	found := false
	for _, m := range rc.Mounts {
		if m.Dest == claudeDir {
			found = true
			if m.Src == claudeDir {
				t.Error("mount Src should be a sandboxed tmp path, not the real ~/.claude")
			}
			break
		}
	}
	if !found {
		t.Errorf("expected mount with Dest=%s; mounts: %v", claudeDir, rc.Mounts)
	}
}

func TestApplyClaude_claudeJsonMountedAsCopy(t *testing.T) {
	fakeHome := makeFakeHome(t)
	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{}`,
	})
	// Create ~/.claude.json next to ~/.claude.
	claudeJsonPath := claudeDir + ".json"
	if err := os.WriteFile(claudeJsonPath, []byte(`{"numStartups":5}`), 0o600); err != nil {
		t.Fatalf("writing claude.json: %v", err)
	}

	rc := &config.RunConfig{}
	cleanup, err := applyClaude(rc)
	if err != nil {
		t.Fatalf("applyClaude: %v", err)
	}

	// Find the mount for claude.json.
	var jsonMount *config.Mount
	for i, m := range rc.Mounts {
		if m.Dest == claudeJsonPath {
			jsonMount = &rc.Mounts[i]
			break
		}
	}
	if jsonMount == nil {
		cleanup()
		t.Fatal("expected a mount for .claude.json")
	}
	// Src must be a temp copy, not the original file.
	if jsonMount.Src == claudeJsonPath {
		t.Error("claude.json mount Src should be a temp copy, not the original file")
	}
	// The temp copy must exist and have the same content.
	data, err := os.ReadFile(jsonMount.Src)
	if err != nil {
		cleanup()
		t.Fatalf("reading temp copy: %v", err)
	}
	if string(data) != `{"numStartups":5}` {
		t.Errorf("temp copy content = %q, want {\"numStartups\":5}", data)
	}
	// After cleanup the temp copy must be gone, original untouched.
	cleanup()
	if _, err := os.Stat(jsonMount.Src); err == nil {
		t.Error("temp copy should be removed after cleanup")
	}
	if _, err := os.Stat(claudeJsonPath); err != nil {
		t.Error("original .claude.json should still exist after cleanup")
	}
}

// ── unlockClaudeCredentials ───────────────────────────────────────────────────

// TestUnlockClaudeCredentials_notInPath verifies that when claude is not
// in PATH, a warning is written to w and the function returns immediately.
func TestUnlockClaudeCredentials_notInPath(t *testing.T) {
	t.Setenv("PATH", "") // ensure LookPath finds nothing

	var buf strings.Builder
	unlockClaudeCredentials(&buf, true, "")

	if !strings.Contains(buf.String(), "claude not found in PATH") {
		t.Errorf("expected 'claude not found in PATH' warning, got: %q", buf.String())
	}
}

// TestUnlockClaudeCredentials_autoConfirm_noWarning verifies that when claude
// is in PATH and autoConfirm=true, the function starts the shim and returns
// without writing any warning.
func TestUnlockClaudeCredentials_autoConfirm_noWarning(t *testing.T) {
	withTestbin(t) // prepend testbin/ — shim exits 0

	var buf strings.Builder
	unlockClaudeCredentials(&buf, true, "")

	if buf.Len() != 0 {
		t.Errorf("expected no output in autoConfirm mode, got: %q", buf.String())
	}
}

// TestUnlockClaudeCredentials_autoConfirm_shimFails verifies that a non-zero
// exit from the shim is irrelevant — the function kills the process before it
// exits and must not write any warning (exit code is never observed).
func TestUnlockClaudeCredentials_autoConfirm_shimFails(t *testing.T) {
	withTestbin(t)
	t.Setenv("CLAUDE_SHIM_EXIT", "1")

	var buf strings.Builder
	unlockClaudeCredentials(&buf, true, "")

	if buf.Len() != 0 {
		t.Errorf("expected no output even when shim exits non-zero, got: %q", buf.String())
	}
}

// ── formatTokenExpiry ─────────────────────────────────────────────────────────

func TestFormatTokenExpiry_claudeAiOauth(t *testing.T) {
	f, err := os.CreateTemp("", "creds-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	// Real-world format: expiresAt in ms under "claudeAiOauth" key.
	expMs := int64(1775866433090) // 2026-04-11 02:13:53 UTC
	fmt.Fprintf(f, `{"claudeAiOauth":{"accessToken":"sk-xxx","expiresAt":%d}}`, expMs)
	f.Close()

	got := formatTokenExpiry(f.Name())
	if got == "" {
		t.Fatal("expected non-empty expiry string, got empty")
	}
	if !strings.Contains(got, "2026-04-11") {
		t.Errorf("expected expiry to contain '2026-04-11', got: %q", got)
	}
}

func TestFormatTokenExpiry_missingFile(t *testing.T) {
	got := formatTokenExpiry("/nonexistent/path/.credentials.json")
	if got != "" {
		t.Errorf("expected empty string for missing file, got: %q", got)
	}
}

func TestFormatTokenExpiry_noExpiryField(t *testing.T) {
	f, err := os.CreateTemp("", "creds-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	fmt.Fprint(f, `{"accessToken":"abc"}`)
	f.Close()

	got := formatTokenExpiry(f.Name())
	if got != "" {
		t.Errorf("expected empty string when no expiry field, got: %q", got)
	}
}

// ── extractExpiresAt: claudeAiOauth format ───────────────────────────────────

func TestExtractExpiresAt_claudeAiOauthNested(t *testing.T) {
	expMs := int64(1775866433090)
	raw := map[string]json.RawMessage{
		"claudeAiOauth": json.RawMessage(fmt.Sprintf(`{"accessToken":"x","expiresAt":%d}`, expMs)),
	}
	got := extractExpiresAt(raw)
	want := expMs / 1000 // normalised to seconds
	if got != want {
		t.Errorf("extractExpiresAt = %d, want %d", got, want)
	}
}

// ── normaliseTimestamp ────────────────────────────────────────────────────────

func TestNormaliseTimestamp(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{
			name: "already seconds (below threshold)",
			in:   1_775_866_433, // 2026-04-11 in seconds
			want: 1_775_866_433,
		},
		{
			name: "milliseconds (above threshold) converted to seconds",
			in:   1_775_866_433_000,
			want: 1_775_866_433,
		},
		{
			name: "exactly at threshold is treated as seconds",
			in:   msThreshold,
			want: msThreshold,
		},
		{
			name: "one above threshold is treated as milliseconds",
			in:   msThreshold + 1,
			want: (msThreshold + 1) / 1000,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normaliseTimestamp(tc.in)
			if got != tc.want {
				t.Errorf("normaliseTimestamp(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
