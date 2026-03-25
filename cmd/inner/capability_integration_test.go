package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// makeFakeHome creates a temp directory and sets HOME to it for the duration of
// the test. Returns the absolute path of the fake home.
func makeFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// buildRCAndApply loads a named profile from app.loader, builds a RunConfig,
// applies all capability transformations, and returns the final RunConfig plus
// a cleanup function. The caller must always call cleanup (typically via defer).
func buildRCAndApply(t *testing.T, app *App, profileName string) (*config.RunConfig, func()) {
	t.Helper()
	rc, err := app.loader.Build(profileName)
	if err != nil {
		t.Fatalf("loader.Build(%q): %v", profileName, err)
	}
	cleanup, err := applyCapabilities(rc)
	if err != nil {
		t.Fatalf("applyCapabilities: %v", err)
	}
	return rc, cleanup
}

// findMount returns the first mount in rc.Mounts whose Dest equals dest, or nil.
func findMount(rc *config.RunConfig, dest string) *config.Mount {
	for i, m := range rc.Mounts {
		if m.Dest == dest {
			return &rc.Mounts[i]
		}
	}
	return nil
}

// profileWithClaudeMount returns a minimal claude-interactive-style profile TOML
// that declares an explicit ~/.claude mount (current, pre-capability style).
const profileWithClaudeMount = `
schema_version = "1"
name           = "claude-interactive"

[mounts]
"~/.claude" = { dest = "~/.claude", mode = "rw" }

[entrypoint]
cmd = "claude"
interactive = true
`

// ── 0a integration pipeline tests ─────────────────────────────────────────────

// TestPipeline_ClaudeProfile_MountDestIsClaudeHome verifies that after the full
// capability pipeline the RunConfig contains a mount whose Dest is ~/.claude and
// whose Src is a sandboxed temporary directory (not the real ~/.claude).
func TestPipeline_ClaudeProfile_MountDestIsClaudeHome(t *testing.T) {
	fakeHome := makeFakeHome(t)

	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{"access_token":"abc"}`,
	})

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), profileWithClaudeMount)

	rc, cleanup := buildRCAndApply(t, app, "claude-interactive")
	defer cleanup()

	m := findMount(rc, claudeDir)
	if m == nil {
		t.Fatalf("expected mount with Dest=%s; mounts: %v", claudeDir, rc.Mounts)
	}
	if m.Src == claudeDir {
		t.Error("mount Src should be a sandboxed tmp path, not the real ~/.claude")
	}
}

// TestPipeline_ClaudeProfile_CredentialsCopiedToSandbox verifies that
// .credentials.json is present in the sandboxed directory after the pipeline.
func TestPipeline_ClaudeProfile_CredentialsCopiedToSandbox(t *testing.T) {
	fakeHome := makeFakeHome(t)

	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{"access_token":"abc"}`,
	})

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), profileWithClaudeMount)

	rc, cleanup := buildRCAndApply(t, app, "claude-interactive")
	defer cleanup()

	m := findMount(rc, claudeDir)
	if m == nil {
		t.Fatalf("expected mount with Dest=%s; mounts: %v", claudeDir, rc.Mounts)
	}
	if _, err := os.Stat(filepath.Join(m.Src, ".credentials.json")); err != nil {
		t.Error(".credentials.json not found in sandboxed directory")
	}
	// sessions/ must exist but be empty — no cross-contamination.
	entries, _ := os.ReadDir(filepath.Join(m.Src, "sessions"))
	if len(entries) != 0 {
		t.Errorf("sessions/ should be empty in sandbox, got %d entries", len(entries))
	}
}

// TestPipeline_ClaudeProfile_ClaudeJsonMountInjected verifies that when
// ~/.claude.json exists on the host, the pipeline injects an additional mount
// for it backed by a temporary copy (not the real file).
func TestPipeline_ClaudeProfile_ClaudeJsonMountInjected(t *testing.T) {
	fakeHome := makeFakeHome(t)

	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{}`,
	})
	// Create ~/.claude.json next to ~/.claude.
	claudeJsonPath := claudeDir + ".json"
	if err := os.WriteFile(claudeJsonPath, []byte(`{"numStartups":3}`), 0o600); err != nil {
		t.Fatalf("writing .claude.json: %v", err)
	}

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), profileWithClaudeMount)

	rc, cleanup := buildRCAndApply(t, app, "claude-interactive")
	defer cleanup()

	m := findMount(rc, claudeJsonPath)
	if m == nil {
		t.Fatalf("expected a mount for .claude.json (dest=%s); mounts: %v", claudeJsonPath, rc.Mounts)
	}
	if m.Src == claudeJsonPath {
		t.Error("mount Src should be a temp copy, not the real .claude.json")
	}
	// Content must be copied.
	data, err := os.ReadFile(m.Src)
	if err != nil {
		t.Fatalf("reading temp copy of .claude.json: %v", err)
	}
	if string(data) != `{"numStartups":3}` {
		t.Errorf("temp copy content = %q, want {\"numStartups\":3}", data)
	}
}

// TestPipeline_GeminiProfile_MountDestIsGeminiHome verifies that the gemini
// pipeline sandboxes ~/.gemini: the final mount has Dest=~/.gemini and a
// temporary Src that contains the copied settings.json.
func TestPipeline_GeminiProfile_MountDestIsGeminiHome(t *testing.T) {
	fakeHome := makeFakeHome(t)

	geminiDir := filepath.Join(fakeHome, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "gemini-interactive.toml"), `
schema_version = "1"
name           = "gemini-interactive"

[mounts]
"~/.gemini" = { dest = "~/.gemini", mode = "rw" }

[entrypoint]
cmd = "gemini"
`)

	rc, cleanup := buildRCAndApply(t, app, "gemini-interactive")
	defer cleanup()

	m := findMount(rc, geminiDir)
	if m == nil {
		t.Fatalf("expected mount with Dest=%s; mounts: %v", geminiDir, rc.Mounts)
	}
	if m.Src == geminiDir {
		t.Error("mount Src should be a sandboxed tmp path, not the real ~/.gemini")
	}
	// settings.json must be in the sandbox.
	if _, err := os.Stat(filepath.Join(m.Src, "settings.json")); err != nil {
		t.Error("settings.json not found in sandboxed gemini directory")
	}
}

// TestPipeline_CursorProfile_HasBothCursorMounts verifies that both ~/.cursor
// and ~/.config/cursor are sandboxed by the pipeline.
func TestPipeline_CursorProfile_HasBothCursorMounts(t *testing.T) {
	fakeHome := makeFakeHome(t)

	cursorDir := filepath.Join(fakeHome, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursorDir, "cli-config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cursorCfgDir := filepath.Join(fakeHome, ".config", "cursor")
	if err := os.MkdirAll(cursorCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cursorCfgDir, "auth.json"), []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "cursor-interactive.toml"), `
schema_version = "1"
name           = "cursor-interactive"

[mounts]
"~/.cursor"        = { dest = "~/.cursor",        mode = "rw" }
"~/.config/cursor" = { dest = "~/.config/cursor", mode = "rw" }

[entrypoint]
cmd = "cursor-agent"
`)

	rc, cleanup := buildRCAndApply(t, app, "cursor-interactive")
	defer cleanup()

	mHome := findMount(rc, cursorDir)
	if mHome == nil {
		t.Fatalf("expected mount with Dest=%s; mounts: %v", cursorDir, rc.Mounts)
	}
	if mHome.Src == cursorDir {
		t.Error("~/.cursor mount Src should be a sandboxed tmp path")
	}

	mCfg := findMount(rc, cursorCfgDir)
	if mCfg == nil {
		t.Fatalf("expected mount with Dest=%s; mounts: %v", cursorCfgDir, rc.Mounts)
	}
	if mCfg.Src == cursorCfgDir {
		t.Error("~/.config/cursor mount Src should be a sandboxed tmp path")
	}

	// auth.json must be copied.
	if _, err := os.Stat(filepath.Join(mCfg.Src, "auth.json")); err != nil {
		t.Error("auth.json not found in sandboxed ~/.config/cursor")
	}
}

// ── applyCapabilities direct tests ────────────────────────────────────────────

// TestApplyCapabilities_noopForIrrelevantMounts verifies that a RunConfig
// whose mounts have no paths matching any capability dir passes through
// applyCapabilities unchanged (shell-profile style).
func TestApplyCapabilities_noopForIrrelevantMounts(t *testing.T) {
	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: "/some/host/path", Dest: "/workspace", Mode: "rw"},
		},
	}
	originalSrc := rc.Mounts[0].Src

	cleanup, err := applyCapabilities(rc)
	if err != nil {
		t.Fatalf("applyCapabilities with irrelevant mounts: %v", err)
	}
	cleanup() // must not panic

	if rc.Mounts[0].Src != originalSrc {
		t.Error("non-capability mount Src should not be modified")
	}
}

// TestApplyCapabilities_claudeFails_returnsError verifies that when applyClaude
// fails (missing .credentials.json), applyCapabilities propagates the error.
func TestApplyCapabilities_claudeFails_returnsError(t *testing.T) {
	fakeHome := makeFakeHome(t)

	// ~/.claude dir exists but has no .credentials.json → prepareClaude fails.
	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: claudeDir, Dest: claudeDir, Mode: "rw"},
		},
	}

	_, err := applyCapabilities(rc)
	if err == nil {
		t.Fatal("expected error when .credentials.json is missing, got nil")
	}
}

// countTmpDirsByPrefix returns the number of directories in os.TempDir() whose
// names start with prefix. Used to detect tmp directory leaks after errors.
func countTmpDirsByPrefix(prefix string) int {
	entries, _ := os.ReadDir(os.TempDir())
	count := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			count++
		}
	}
	return count
}

// TestApplyCapabilities_cursorFails_claudeCleanedUp verifies that when a later
// capability (cursor) fails, the cleanup for an already-succeeded capability
// (claude) is executed — no inner-claude-* tmp dir is left on disk.
func TestApplyCapabilities_cursorFails_claudeCleanedUp(t *testing.T) {
	fakeHome := makeFakeHome(t)
	t.Setenv("XDG_CONFIG_HOME", "") // pin cursorConfigDir to ~/.config/cursor

	// Valid claude setup: applyClaude will succeed and create a tmp dir.
	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{"access_token":"abc"}`,
	})

	// cursor config dir exists but has no auth.json → applyCursor will fail.
	cursorCfgDir := cursorConfigDir() // = fakeHome/.config/cursor (HOME=fakeHome, XDG unset)
	if err := os.MkdirAll(cursorCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	beforeClaude := countTmpDirsByPrefix("inner-claude-")

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: claudeDir, Dest: claudeDir, Mode: "rw"},
			{Src: cursorCfgDir, Dest: cursorCfgDir, Mode: "rw"},
		},
	}

	_, err := applyCapabilities(rc)
	if err == nil {
		t.Fatal("expected error when cursor auth.json is missing, got nil")
	}

	afterClaude := countTmpDirsByPrefix("inner-claude-")
	if afterClaude != beforeClaude {
		t.Errorf("inner-claude-* tmp dirs leaked after cursor failure: before=%d after=%d",
			beforeClaude, afterClaude)
	}
}

// TestApplyCapabilities_cleanupRemovesAllTmpDirs verifies that the combined
// cleanup returned on success removes all tmp dirs created by the capabilities.
func TestApplyCapabilities_cleanupRemovesAllTmpDirs(t *testing.T) {
	fakeHome := makeFakeHome(t)
	t.Setenv("XDG_CONFIG_HOME", "")

	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{"access_token":"abc"}`,
	})

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: claudeDir, Dest: claudeDir, Mode: "rw"},
		},
	}

	beforeClaude := countTmpDirsByPrefix("inner-claude-")

	cleanup, err := applyCapabilities(rc)
	if err != nil {
		t.Fatalf("applyCapabilities: %v", err)
	}

	// Before cleanup: the claude tmp dir should exist.
	if countTmpDirsByPrefix("inner-claude-") != beforeClaude+1 {
		t.Error("expected exactly one new inner-claude-* dir after applyCapabilities success")
	}

	cleanup()

	afterClaude := countTmpDirsByPrefix("inner-claude-")
	if afterClaude != beforeClaude {
		t.Errorf("inner-claude-* tmp dirs leaked after cleanup: before=%d after=%d",
			beforeClaude, afterClaude)
	}
}

// TestPipeline_ClaudeContainers_InheritsClaudeMount verifies that a profile
// extending claude-interactive inherits the claude mount transformation even
// though it does not declare the mount directly.
func TestPipeline_ClaudeContainers_InheritsClaudeMount(t *testing.T) {
	fakeHome := makeFakeHome(t)

	claudeDir := filepath.Join(fakeHome, ".claude")
	makeClaudeHome(t, claudeDir, map[string]string{
		".credentials.json": `{"access_token":"abc"}`,
	})

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), profileWithClaudeMount)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-containers.toml"), `
schema_version = "1"
name           = "claude-containers"
extends        = "claude-interactive"
`)

	rc, cleanup := buildRCAndApply(t, app, "claude-containers")
	defer cleanup()

	m := findMount(rc, claudeDir)
	if m == nil {
		t.Fatalf("expected mount with Dest=%s (inherited from claude-interactive); mounts: %v", claudeDir, rc.Mounts)
	}
	if m.Src == claudeDir {
		t.Error("inherited ~/.claude mount Src should be a sandboxed tmp path, not the real dir")
	}
}
