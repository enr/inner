package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── configShow ────────────────────────────────────────────────────────────────

func TestConfigShow_fileExists(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "config.toml"), `log_dir = "/my/logs"`)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `log_dir`) {
		t.Errorf("expected config content in output, got: %s", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("expected config path in output header, got: %s", out)
	}
}

func TestConfigShow_fileAbsent(t *testing.T) {
	app, _ := newTestApp(t)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow with missing file should not error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "No global config") {
		t.Errorf("expected friendly message, got: %s", out)
	}
	if !strings.Contains(out, "defaults") {
		t.Errorf("expected mention of defaults, got: %s", out)
	}
}

// ── configEdit ────────────────────────────────────────────────────────────────

func TestConfigEdit_createsFileIfMissing(t *testing.T) {
	app, dir := newTestApp(t)
	app.editorFn = func(string) error { return nil }

	if err := app.configEdit(&bytes.Buffer{}); err != nil {
		t.Fatalf("configEdit: %v", err)
	}

	path := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected config.toml to be created: %v", err)
	}
}

func TestConfigEdit_editorCalledWithCorrectPath(t *testing.T) {
	app, dir := newTestApp(t)
	var calledWith string
	app.editorFn = func(path string) error {
		calledWith = path
		return nil
	}

	if err := app.configEdit(&bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, "config.toml")
	if calledWith != expected {
		t.Errorf("editorFn called with %q, want %q", calledWith, expected)
	}
}

func TestConfigEdit_existingFileNotOverwritten(t *testing.T) {
	app, dir := newTestApp(t)
	app.editorFn = func(string) error { return nil }

	original := `log_dir = "/existing"`
	writeTestFile(t, filepath.Join(dir, "config.toml"), original)

	if err := app.configEdit(&bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(data) != original {
		t.Errorf("existing config was overwritten: got %q", string(data))
	}
}

// ── globalConfigTemplate ──────────────────────────────────────────────────────

func TestGlobalConfigTemplate_notEmpty(t *testing.T) {
	tmpl := globalConfigTemplate()
	if strings.TrimSpace(tmpl) == "" {
		t.Error("expected non-empty global config template")
	}
}
