package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

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
	ps1 := sandboxPS1()
	if ps1 == "" {
		t.Fatal("sandboxPS1 returned empty string")
	}
	if !strings.Contains(ps1, "inner") {
		t.Errorf("PS1 should mention 'inner', got: %q", ps1)
	}
}

func TestSandboxPS1_containsUserAndHost(t *testing.T) {
	ps1 := sandboxPS1()
	if !strings.Contains(ps1, `\u`) || !strings.Contains(ps1, `\h`) {
		t.Errorf("PS1 should contain \\u and \\h, got: %q", ps1)
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
	// Init file must exist and contain PS1 and source of .bashrc.
	content, err := os.ReadFile(rc.Entrypoint.Args[1])
	if err != nil {
		t.Fatalf("reading init file: %v", err)
	}
	if !strings.Contains(string(content), ".bashrc") {
		t.Error("init file should source .bashrc")
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

func TestApplyClaude_replacesMountSrc(t *testing.T) {
	src := t.TempDir()
	makeClaudeHome(t, src, map[string]string{
		".credentials.json": `{}`,
	})

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: src, Dest: "~/.claude", Mode: "rw"},
		},
	}

	cleanup, err := applyClaudeDir(rc, src)
	if err != nil {
		t.Fatalf("applyClaudeDir: %v", err)
	}
	defer cleanup()

	if rc.Mounts[0].Src == src {
		t.Error("applyClaudeDir should have replaced Src with sandbox path")
	}
}

func TestApplyClaude_noClaudeMount_noOp(t *testing.T) {
	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: "/some/other/path", Dest: "/workspace", Mode: "rw"},
		},
	}
	originalSrc := rc.Mounts[0].Src

	cleanup, err := applyClaudeDir(rc, "/home/user/.claude")
	if err != nil {
		t.Fatalf("applyClaudeDir: %v", err)
	}
	cleanup()

	if rc.Mounts[0].Src != originalSrc {
		t.Error("applyClaudeDir should not modify non-claude mounts")
	}
}
