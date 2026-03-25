package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ── prepareCursor ─────────────────────────────────────────────────────────────

func TestPrepareCursor_copiesCliConfig(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "cli-config.json"), []byte(`{"autoAccept":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareCursor(src)
	if err != nil {
		t.Fatalf("prepareCursor: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(dst, "cli-config.json"))
	if err != nil {
		t.Fatalf("cli-config.json not found in sandbox: %v", err)
	}
	if string(data) != `{"autoAccept":true}` {
		t.Errorf("cli-config content = %q, want {\"autoAccept\":true}", data)
	}
}

func TestPrepareCursor_missingCliConfig_noError(t *testing.T) {
	src := t.TempDir() // no cli-config.json

	_, cleanup, err := prepareCursor(src)
	if err != nil {
		t.Fatalf("expected no error when cli-config.json is absent, got: %v", err)
	}
	cleanup()
}

func TestPrepareCursor_createsFreshDirs(t *testing.T) {
	src := t.TempDir()

	dst, cleanup, err := prepareCursor(src)
	if err != nil {
		t.Fatalf("prepareCursor: %v", err)
	}
	defer cleanup()

	for _, d := range []string{"ai-tracking", "extensions", "projects"} {
		info, err := os.Stat(filepath.Join(dst, d))
		if err != nil {
			t.Errorf("expected fresh dir %s in sandbox: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", d)
		}
	}
}

func TestPrepareCursor_copiesSkillsCursor(t *testing.T) {
	src := t.TempDir()
	skillsDir := filepath.Join(src, "skills-cursor")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "my-skill.md"), []byte("# skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareCursor(src)
	if err != nil {
		t.Fatalf("prepareCursor: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dst, "skills-cursor", "my-skill.md")); err != nil {
		t.Error("skills-cursor/ not copied into cursor sandbox")
	}
}

func TestPrepareCursor_doesNotCopyOtherFiles(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "session.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareCursor(src)
	if err != nil {
		t.Fatalf("prepareCursor: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dst, "session.json")); err == nil {
		t.Error("session.json should not be copied into cursor sandbox")
	}
}

// ── prepareCursorConfig ───────────────────────────────────────────────────────

func TestPrepareCursorConfig_copiesAuthJson(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "auth.json"), []byte(`{"token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareCursorConfig(src)
	if err != nil {
		t.Fatalf("prepareCursorConfig: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(dst, "auth.json"))
	if err != nil {
		t.Fatalf("auth.json not found in sandbox: %v", err)
	}
	if string(data) != `{"token":"secret"}` {
		t.Errorf("auth.json content = %q, want {\"token\":\"secret\"}", data)
	}
}

func TestPrepareCursorConfig_missingAuth_returnsError(t *testing.T) {
	src := t.TempDir() // no auth.json

	_, cleanup, err := prepareCursorConfig(src)
	if err == nil {
		cleanup()
		t.Fatal("expected error for missing auth.json, got nil")
	}
}

func TestPrepareCursorConfig_cleanupRemovesSandbox(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareCursorConfig(src)
	if err != nil {
		t.Fatalf("prepareCursorConfig: %v", err)
	}
	cleanup()

	if _, err := os.Stat(dst); err == nil {
		t.Error("sandbox directory should be removed after cleanup")
	}
}
