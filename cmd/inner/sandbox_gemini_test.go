package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ── prepareGemini ─────────────────────────────────────────────────────────────

func TestPrepareGemini_copiesSettings(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "settings.json"), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareGemini(src)
	if err != nil {
		t.Fatalf("prepareGemini: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(dst, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not found in sandbox: %v", err)
	}
	if string(data) != `{"theme":"dark"}` {
		t.Errorf("settings content = %q, want {\"theme\":\"dark\"}", data)
	}
}

func TestPrepareGemini_missingSettings_noError(t *testing.T) {
	src := t.TempDir() // no settings.json

	_, cleanup, err := prepareGemini(src)
	if err != nil {
		t.Fatalf("expected no error when settings.json is absent, got: %v", err)
	}
	cleanup()
}

func TestPrepareGemini_sandboxIsEmptyExceptSettings(t *testing.T) {
	src := t.TempDir()
	// Put a file that should NOT be copied.
	if err := os.WriteFile(filepath.Join(src, "history.jsonl"), []byte(`{"secret":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dst, cleanup, err := prepareGemini(src)
	if err != nil {
		t.Fatalf("prepareGemini: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dst, "history.jsonl")); err == nil {
		t.Error("history.jsonl should not be copied into the gemini sandbox")
	}
}

func TestPrepareGemini_cleanupRemovesSandbox(t *testing.T) {
	src := t.TempDir()

	dst, cleanup, err := prepareGemini(src)
	if err != nil {
		t.Fatalf("prepareGemini: %v", err)
	}
	cleanup()

	if _, err := os.Stat(dst); err == nil {
		t.Error("sandbox directory should be removed after cleanup")
	}
}
