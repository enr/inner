package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── configShow ────────────────────────────────────────────────────────────────

func TestConfigShow_globalFileExists(t *testing.T) {
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
	if !strings.Contains(out, "Global") {
		t.Errorf("expected 'Global' label in output, got: %s", out)
	}
}

func TestConfigShow_globalFileAbsent(t *testing.T) {
	app, _ := newTestApp(t)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow with missing file should not error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Global") {
		t.Errorf("expected 'Global' label, got: %s", out)
	}
	if !strings.Contains(out, "defaults") {
		t.Errorf("expected mention of defaults, got: %s", out)
	}
}

func TestConfigShow_localFileShown(t *testing.T) {
	app, dir := newTestApp(t)
	workDir := t.TempDir()
	app.loader.WorkDir = workDir
	writeTestFile(t, filepath.Join(dir, "config.toml"), `log_dir = "/global/logs"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner.toml"), `default_profile = "local-prof"`)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Global") {
		t.Errorf("expected 'Global' section, got: %s", out)
	}
	if !strings.Contains(out, "Local") {
		t.Errorf("expected 'Local' section, got: %s", out)
	}
	if !strings.Contains(out, "local-prof") {
		t.Errorf("expected local config content in output, got: %s", out)
	}
}

func TestConfigShow_noLocalSection_whenNoProjectFiles(t *testing.T) {
	// WorkDir is set but no project config files exist → only Global shown,
	// plus a placeholder for the primary project config path.
	app, dir := newTestApp(t)
	workDir := t.TempDir()
	app.loader.WorkDir = workDir
	writeTestFile(t, filepath.Join(dir, "config.toml"), `log_dir = "/logs"`)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow: %v", err)
	}
	out := buf.String()
	if count := strings.Count(out, "# Global:"); count != 1 {
		t.Errorf("expected exactly one '# Global:' header, got %d: %s", count, out)
	}
	// A placeholder "Local" section is shown pointing to the primary project path.
	if !strings.Contains(out, "Local") {
		t.Errorf("expected a 'Local' placeholder section, got: %s", out)
	}
}

func TestConfigShow_noLocalSection_whenWorkDirUnset(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "config.toml"), `log_dir = "/logs"`)
	// WorkDir not set → no local section

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "# Local:") {
		t.Errorf("expected no '# Local:' section when WorkDir unset, got: %s", out)
	}
}

func TestConfigShow_resolvedLimits_autoDetected(t *testing.T) {
	// With no [default_limits] in config, the resolved block shows
	// auto-detected values, annotated as such.
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "config.toml"), `log_dir = "/logs"`)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Resolved default limits") {
		t.Errorf("expected resolved limits block, got: %s", out)
	}
	if !strings.Contains(out, "(auto-detected)") {
		t.Errorf("expected auto-detected annotation, got: %s", out)
	}
}

func TestConfigShow_resolvedLimits_fromConfig(t *testing.T) {
	// Explicit [default_limits] values appear without the auto-detected marker.
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "config.toml"), `
[default_limits]
memory = "3G"
cpu    = "150%"
pids   = 99
`)

	var buf bytes.Buffer
	if err := app.configShow(&buf); err != nil {
		t.Fatalf("configShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"memory: 3G", "cpu   : 150%", "pids:   99"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in resolved limits, got: %s", want, out)
		}
	}
}

// ── configEdit ────────────────────────────────────────────────────────────────

func TestConfigEdit_globalCreatesFileIfMissing(t *testing.T) {
	app, dir := newTestApp(t)
	app.editorFn = func(string) error { return nil }

	if err := app.configEdit(&bytes.Buffer{}, false); err != nil {
		t.Fatalf("configEdit: %v", err)
	}

	path := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected config.toml to be created: %v", err)
	}
}

func TestConfigEdit_globalEditorCalledWithCorrectPath(t *testing.T) {
	app, dir := newTestApp(t)
	var calledWith string
	app.editorFn = func(path string) error {
		calledWith = path
		return nil
	}

	if err := app.configEdit(&bytes.Buffer{}, false); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(dir, "config.toml")
	if calledWith != expected {
		t.Errorf("editorFn called with %q, want %q", calledWith, expected)
	}
}

func TestConfigEdit_globalExistingFileNotOverwritten(t *testing.T) {
	app, dir := newTestApp(t)
	app.editorFn = func(string) error { return nil }

	original := `log_dir = "/existing"`
	writeTestFile(t, filepath.Join(dir, "config.toml"), original)

	if err := app.configEdit(&bytes.Buffer{}, false); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(data) != original {
		t.Errorf("existing config was overwritten: got %q", string(data))
	}
}

func TestConfigEdit_localCreatesFileInWorkDir(t *testing.T) {
	app, _ := newTestApp(t)
	workDir := t.TempDir()
	app.loader.WorkDir = workDir
	app.editorFn = func(string) error { return nil }

	if err := app.configEdit(&bytes.Buffer{}, true); err != nil {
		t.Fatalf("configEdit --local: %v", err)
	}

	path := filepath.Join(workDir, ".config", "inner.toml")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected .config/inner.toml to be created: %v", err)
	}
}

func TestConfigEdit_localEditorCalledWithCorrectPath(t *testing.T) {
	app, _ := newTestApp(t)
	workDir := t.TempDir()
	app.loader.WorkDir = workDir
	var calledWith string
	app.editorFn = func(path string) error {
		calledWith = path
		return nil
	}

	if err := app.configEdit(&bytes.Buffer{}, true); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(workDir, ".config", "inner.toml")
	if calledWith != expected {
		t.Errorf("editorFn called with %q, want %q", calledWith, expected)
	}
}

func TestConfigEdit_localErrorsWhenNoWorkDir(t *testing.T) {
	app, _ := newTestApp(t)
	// WorkDir not set

	err := app.configEdit(&bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected error when WorkDir is not set, got nil")
	}
}

// ── templates ─────────────────────────────────────────────────────────────────

func TestGlobalConfigTemplate_notEmpty(t *testing.T) {
	tmpl := globalConfigTemplate()
	if strings.TrimSpace(tmpl) == "" {
		t.Error("expected non-empty global config template")
	}
}

func TestLocalConfigTemplate_notEmpty(t *testing.T) {
	tmpl := localConfigTemplate()
	if strings.TrimSpace(tmpl) == "" {
		t.Error("expected non-empty local config template")
	}
}
