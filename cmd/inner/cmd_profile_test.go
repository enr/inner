package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/enr/inner/internal/config"
)

// newTestApp returns an App wired to a temp directory with a no-op editorFn.
func newTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	return &App{
		loader:   config.NewLoader(dir),
		editorFn: func(string) error { return nil },
	}, dir
}

// newTestAppWithWorkDir returns an App wired to a temp config dir and a separate work dir.
func newTestAppWithWorkDir(t *testing.T) (*App, string, string) {
	t.Helper()
	dir := t.TempDir()
	workDir := t.TempDir()
	return &App{
		loader:   config.NewLoaderWithWorkDir(dir, workDir),
		editorFn: func(string) error { return nil },
	}, dir, workDir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newProfileTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	oldFetchProfileURL := fetchProfileURL
	client := srv.Client()
	fetchProfileURL = func(rawURL string) ([]byte, error) {
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
		fetchProfileURL = oldFetchProfileURL
		srv.Close()
	})
	return srv
}

// ── profileList ───────────────────────────────────────────────────────────────

func TestProfileList_empty(t *testing.T) {
	app, dir := newTestApp(t)
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	if !strings.Contains(buf.String(), "NAME") {
		t.Errorf("expected header in output, got: %s", buf.String())
	}
}

func TestProfileList_withProfiles(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "alpha.toml"),
		`name = "alpha"\ndescription = "Alpha profile"`)
	writeTestFile(t, filepath.Join(dir, "profiles", "beta.toml"),
		`name = "beta"\ndescription = "Beta profile"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected 'alpha' in output, got: %s", out)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("expected 'beta' in output, got: %s", out)
	}
}

func TestProfileList_noDir(t *testing.T) {
	app, _ := newTestApp(t)
	// profiles dir does not exist

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("expected no error when profiles dir missing: %v", err)
	}
	if !strings.Contains(buf.String(), "No profiles") {
		t.Errorf("expected friendly message, got: %s", buf.String())
	}
}

func TestProfileList_ignoresNonToml(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "real.toml"), `name = "real"`)
	writeTestFile(t, filepath.Join(dir, "profiles", "README.md"), `# ignore me`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "README") {
		t.Errorf("expected README.md to be ignored, got: %s", out)
	}
}

func TestProfileList_localProfiles(t *testing.T) {
	app, dir, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "global.toml"),
		`name = "global"\ndescription = "Global profile"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "local.toml"),
		`name = "local"\ndescription = "Local profile"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "global") {
		t.Errorf("expected 'global' in output, got: %s", out)
	}
	if !strings.Contains(out, "local") {
		t.Errorf("expected 'local' in output, got: %s", out)
	}
	if !strings.Contains(out, "[local]") {
		t.Errorf("expected '[local]' tag in output, got: %s", out)
	}
}

func TestProfileList_defaultMarked(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "config.toml"), `default_profile = "alpha"`)
	writeTestFile(t, filepath.Join(dir, "profiles", "alpha.toml"),
		`name = "alpha"\ndescription = "Alpha profile"`)
	writeTestFile(t, filepath.Join(dir, "profiles", "beta.toml"),
		`name = "beta"\ndescription = "Beta profile"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "*") {
		t.Errorf("expected '*' marker in output, got: %s", out)
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "alpha") && !strings.Contains(line, "*") {
			t.Errorf("expected alpha line to have '*' marker, got: %s", line)
		}
		if strings.Contains(line, "beta") && strings.Contains(line, "*") {
			t.Errorf("expected beta line to NOT have '*' marker, got: %s", line)
		}
	}
}

func TestProfileList_defaultAsPath(t *testing.T) {
	// default_profile set to a file path like ".config/inner/profiles/local-prof.toml"
	app, _, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner.toml"),
		`default_profile = ".config/inner/profiles/local-prof.toml"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "local-prof.toml"),
		`name = "local-prof"\ndescription = "My local profile"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "*") {
		t.Errorf("expected '*' marker when default_profile is a path, got: %s", out)
	}
}

func TestProfileList_localDefaultMarked(t *testing.T) {
	app, _, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner.toml"), `default_profile = "local-prof"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "local-prof.toml"),
		`name = "local-prof"\ndescription = "My local profile"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "*") {
		t.Errorf("expected '*' marker for local default, got: %s", out)
	}
	if !strings.Contains(out, "[local]") {
		t.Errorf("expected '[local]' tag, got: %s", out)
	}
}

// ── profileShow ───────────────────────────────────────────────────────────────

func TestProfileShow_exists(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "myprof.toml"), `name = "myprof"`)

	var buf bytes.Buffer
	if err := app.profileShow(&buf, "myprof"); err != nil {
		t.Fatalf("profileShow: %v", err)
	}
	if !strings.Contains(buf.String(), `name = "myprof"`) {
		t.Errorf("expected profile content, got: %s", buf.String())
	}
}

func TestProfileShow_notFound(t *testing.T) {
	app, _ := newTestApp(t)
	err := app.profileShow(&bytes.Buffer{}, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestProfileShow_byPath(t *testing.T) {
	app, _ := newTestApp(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.toml")
	writeTestFile(t, path, `name = "custom"`)

	var buf bytes.Buffer
	if err := app.profileShow(&buf, path); err != nil {
		t.Fatalf("profileShow by path: %v", err)
	}
	if !strings.Contains(buf.String(), `name = "custom"`) {
		t.Errorf("expected profile content, got: %s", buf.String())
	}
}

// ── resolveValidateNames ──────────────────────────────────────────────────────

func TestResolveValidateNames_fromArgs(t *testing.T) {
	app, _ := newTestApp(t)
	names, err := app.resolveValidateNames([]string{"foo", "bar"}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 || names[0] != "foo" || names[1] != "bar" {
		t.Errorf("got %v, want [foo bar]", names)
	}
}

func TestResolveValidateNames_noArgsNoAll(t *testing.T) {
	app, _ := newTestApp(t)
	_, err := app.resolveValidateNames(nil, false)
	if err == nil {
		t.Fatal("expected error when no args and --all not set")
	}
}

func TestResolveValidateNames_all(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "a.toml"), ``)
	writeTestFile(t, filepath.Join(dir, "profiles", "b.toml"), ``)

	names, err := app.resolveValidateNames(nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("got %d names, want 2: %v", len(names), names)
	}
}

// ── profileValidate ───────────────────────────────────────────────────────────

func TestProfileValidate_ok(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "good.toml"), `
[entrypoint]
interactive = true
`)

	var buf bytes.Buffer
	anyError, err := app.profileValidate(&buf, []string{"good"})
	if err != nil {
		t.Fatalf("profileValidate: %v", err)
	}
	if anyError {
		t.Errorf("expected no errors for valid profile, output: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Errorf("expected 'ok' in output, got: %s", buf.String())
	}
}

func TestProfileValidate_loadError(t *testing.T) {
	app, _ := newTestApp(t)

	var buf bytes.Buffer
	anyError, err := app.profileValidate(&buf, []string{"missing"})
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !anyError {
		t.Error("expected anyError=true for missing profile")
	}
	if !strings.Contains(buf.String(), "load error") {
		t.Errorf("expected load error message, got: %s", buf.String())
	}
}

func TestProfileValidate_mountMissing(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "bad.toml"), `
[mounts]
"/nonexistent/path/xyz" = { dest = "/workspace", mode = "rw" }
[entrypoint]
interactive = true
`)

	var buf bytes.Buffer
	anyError, _ := app.profileValidate(&buf, []string{"bad"})
	if !anyError {
		t.Errorf("expected anyError=true for profile with missing mount, output: %s", buf.String())
	}
}

// ── profileNew ────────────────────────────────────────────────────────────────

func TestProfileNew_creates(t *testing.T) {
	app, dir := newTestApp(t)

	var buf bytes.Buffer
	if err := app.profileNew(&buf, "fresh"); err != nil {
		t.Fatalf("profileNew: %v", err)
	}

	path := filepath.Join(dir, "profiles", "fresh.toml")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected profile file to exist at %s: %v", path, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `name = "fresh"`) {
		t.Errorf("expected template content with name, got: %s", data)
	}
}

func TestProfileNew_alreadyExists(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "exists.toml"), `name = "exists"`)

	err := app.profileNew(&bytes.Buffer{}, "exists")
	if err == nil {
		t.Fatal("expected error when profile already exists")
	}
}

func TestProfileNew_editorCalled(t *testing.T) {
	called := false
	app, _ := newTestApp(t)
	app.editorFn = func(path string) error {
		called = true
		return nil
	}

	if err := app.profileNew(&bytes.Buffer{}, "newone"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("expected editorFn to be called")
	}
}

// ── profileEdit ───────────────────────────────────────────────────────────────

func TestProfileEdit_exists(t *testing.T) {
	called := false
	app, dir := newTestApp(t)
	app.editorFn = func(path string) error {
		called = true
		return nil
	}
	writeTestFile(t, filepath.Join(dir, "profiles", "edit-me.toml"), `name = "edit-me"`)

	if err := app.profileEdit(&bytes.Buffer{}, "edit-me"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("expected editorFn to be called")
	}
}

func TestProfileEdit_notFound(t *testing.T) {
	app, _ := newTestApp(t)
	err := app.profileEdit(&bytes.Buffer{}, "ghost")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestProfileEdit_localProfile(t *testing.T) {
	// Regression: profileEdit used ProfilePath (global only) instead of
	// ResolveProfilePath, so local-only profiles were reported as not found.
	called := false
	var editedPath string
	app, _, workDir := newTestAppWithWorkDir(t)
	app.editorFn = func(path string) error {
		called = true
		editedPath = path
		return nil
	}
	localPath := filepath.Join(workDir, ".config", "inner", "profiles", "local-only.toml")
	writeTestFile(t, localPath, `name = "local-only"`)

	if err := app.profileEdit(&bytes.Buffer{}, "local-only"); err != nil {
		t.Fatalf("profileEdit: %v", err)
	}
	if !called {
		t.Error("expected editorFn to be called")
	}
	if editedPath != localPath {
		t.Errorf("expected editor to open %s, got %s", localPath, editedPath)
	}
}

func TestProfileEdit_localOverridesGlobal(t *testing.T) {
	// When a local and a global profile share the same name, edit should open
	// the local one (same precedence as show/run).
	var editedPath string
	app, dir, workDir := newTestAppWithWorkDir(t)
	app.editorFn = func(path string) error {
		editedPath = path
		return nil
	}
	writeTestFile(t, filepath.Join(dir, "profiles", "shared.toml"), `name = "shared"`)
	localPath := filepath.Join(workDir, ".config", "inner", "profiles", "shared.toml")
	writeTestFile(t, localPath, `name = "shared"`)

	if err := app.profileEdit(&bytes.Buffer{}, "shared"); err != nil {
		t.Fatalf("profileEdit: %v", err)
	}
	if editedPath != localPath {
		t.Errorf("expected local profile path %s, got %s", localPath, editedPath)
	}
}

// ── profileClone ──────────────────────────────────────────────────────────────

func TestProfileClone_ok(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "orig.toml"), `name = "orig"`)

	var buf bytes.Buffer
	if err := app.profileClone(&buf, "orig", "copy"); err != nil {
		t.Fatalf("profileClone: %v", err)
	}

	dstPath := filepath.Join(dir, "profiles", "copy.toml")
	if _, err := os.Stat(dstPath); err != nil {
		t.Errorf("expected cloned profile at %s: %v", dstPath, err)
	}
	if !strings.Contains(buf.String(), "orig") {
		t.Errorf("expected clone message, got: %s", buf.String())
	}
}

func TestProfileClone_srcNotFound(t *testing.T) {
	app, _ := newTestApp(t)
	err := app.profileClone(&bytes.Buffer{}, "ghost", "copy")
	if err == nil {
		t.Fatal("expected error when src not found")
	}
}

func TestProfileClone_dstExists(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "a.toml"), `name = "a"`)
	writeTestFile(t, filepath.Join(dir, "profiles", "b.toml"), `name = "b"`)

	err := app.profileClone(&bytes.Buffer{}, "a", "b")
	if err == nil {
		t.Fatal("expected error when dst already exists")
	}
}

// ── profileShowExplain ────────────────────────────────────────────────────────

// TestProfileShowExplain_claudeInteractive_showsCapabilitySection verifies
// that profileShowExplain appends a capability section after the raw TOML.
func TestProfileShowExplain_claudeInteractive_showsCapabilitySection(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), `
schema_version = "1"
name           = "claude-interactive"
capabilities   = ["claude"]

[entrypoint]
cmd = "claude"
`)

	var buf bytes.Buffer
	if err := app.profileShowExplain(&buf, "claude-interactive"); err != nil {
		t.Fatalf("profileShowExplain: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `capabilities   = ["claude"]`) {
		t.Error("expected raw TOML to appear in output")
	}
	if !strings.Contains(out, "capability: claude") {
		t.Errorf("expected 'capability: claude' section; got:\n%s", out)
	}
	if !strings.Contains(out, "mounts injected") {
		t.Errorf("expected 'mounts injected' in output; got:\n%s", out)
	}
	if !strings.Contains(out, "~/.claude") {
		t.Errorf("expected '~/.claude' in capability section; got:\n%s", out)
	}
}

// TestProfileShowExplain_claudeContainers_showsInheritedCapability verifies
// that capabilities inherited via extends appear in the explain output, even
// though the child profile file does not declare them directly.
func TestProfileShowExplain_claudeContainers_showsInheritedCapability(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), `
schema_version = "1"
name         = "claude-interactive"
capabilities = ["claude"]

[entrypoint]
cmd = "claude"
`)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-containers.toml"), `
schema_version = "1"
name    = "claude-containers"
extends = "claude-interactive"
`)

	var buf bytes.Buffer
	if err := app.profileShowExplain(&buf, "claude-containers"); err != nil {
		t.Fatalf("profileShowExplain: %v", err)
	}
	out := buf.String()

	// Raw TOML shown is the child's file, not the parent's.
	if !strings.Contains(out, `name    = "claude-containers"`) {
		t.Errorf("expected child's raw TOML; got:\n%s", out)
	}
	// Capability inherited from parent must appear in the explain section.
	if !strings.Contains(out, "capability: claude") {
		t.Errorf("expected inherited 'capability: claude' section; got:\n%s", out)
	}
}

// TestProfileShowExplain_noCapabilities_noSection verifies that profiles
// without capabilities produce no capability section in the explain output.
func TestProfileShowExplain_noCapabilities_noSection(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "shell.toml"), `
schema_version = "1"
name = "shell"

[entrypoint]
cmd = "bash"
`)

	var buf bytes.Buffer
	if err := app.profileShowExplain(&buf, "shell"); err != nil {
		t.Fatalf("profileShowExplain: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "capability:") {
		t.Errorf("expected no capability section for profile without capabilities; got:\n%s", out)
	}
	if !strings.Contains(out, `name = "shell"`) {
		t.Errorf("expected raw TOML in output; got:\n%s", out)
	}
}

// TestProfileShowExplain_withoutFlag_rawTOMLOnly verifies that profileShow
// (without --explain) returns only the raw TOML even when capabilities are set.
func TestProfileShowExplain_withoutFlag_rawTOMLOnly(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "claude-interactive.toml"), `
schema_version = "1"
name         = "claude-interactive"
capabilities = ["claude"]

[entrypoint]
cmd = "claude"
`)

	var buf bytes.Buffer
	if err := app.profileShow(&buf, "claude-interactive"); err != nil {
		t.Fatalf("profileShow: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "capability:") {
		t.Errorf("profileShow without --explain must not include capability section; got:\n%s", out)
	}
}

// ── profileShowResolved ───────────────────────────────────────────────────────

// TestProfileShowResolved_simpleProfile verifies that --resolved outputs valid
// TOML reflecting the profile fields.
func TestProfileShowResolved_simpleProfile(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "shell.toml"), `
schema_version = "1"
name = "shell"

[entrypoint]
cmd = "bash"
`)

	var buf bytes.Buffer
	if err := app.profileShowResolved(&buf, "shell"); err != nil {
		t.Fatalf("profileShowResolved: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `"bash"`) {
		t.Errorf("expected entrypoint cmd in resolved output; got:\n%s", out)
	}
}

// TestProfileShowResolved_extendsResolved verifies that capabilities inherited
// via extends appear in the resolved output even though the child file omits them.
func TestProfileShowResolved_extendsResolved(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "base.toml"), `
schema_version = "1"
name         = "base"
capabilities = ["claude"]

[entrypoint]
cmd = "claude"
`)
	writeTestFile(t, filepath.Join(dir, "profiles", "child.toml"), `
schema_version = "1"
name    = "child"
extends = "base"
`)

	var buf bytes.Buffer
	if err := app.profileShowResolved(&buf, "child"); err != nil {
		t.Fatalf("profileShowResolved: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "claude") {
		t.Errorf("expected inherited capability 'claude' in resolved output; got:\n%s", out)
	}
	// The raw child file has no entrypoint; the resolved one must have it from base.
	if !strings.Contains(out, `"claude"`) {
		t.Errorf("expected inherited entrypoint cmd in resolved output; got:\n%s", out)
	}
}

// ── profileTemplate ───────────────────────────────────────────────────────────

func TestProfileTemplate_containsName(t *testing.T) {
	tmpl := profileTemplate("myname")
	if !strings.Contains(tmpl, `name = "myname"`) {
		t.Errorf("template missing name field, got:\n%s", tmpl)
	}
}

func TestProfileTemplate_isValidTOML(t *testing.T) {
	// Verify the template can be loaded by the config loader.
	dir := t.TempDir()
	app := &App{loader: config.NewLoader(dir), editorFn: func(string) error { return nil }}
	writeTestFile(t, filepath.Join(dir, "profiles", "tpl.toml"), profileTemplate("tpl"))

	if _, err := app.loader.LoadProfile("tpl"); err != nil {
		t.Errorf("profileTemplate produces invalid TOML: %v", err)
	}
}

// ── profileList: same-name conflict (local wins, global shadowed) ─────────────

func TestProfileList_sameNameNormalMode(t *testing.T) {
	app, dir, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "foo.toml"),
		`name = "foo"\ndescription = "global foo"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "foo.toml"),
		`name = "foo"\ndescription = "local foo"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, false); err != nil {
		t.Fatalf("profileList: %v", err)
	}
	out := buf.String()

	// Only one row for "foo" — the local one.
	count := strings.Count(out, "foo")
	if count != 1 {
		t.Errorf("expected exactly 1 'foo' row in normal mode, got %d occurrences in:\n%s", count, out)
	}
	if !strings.Contains(out, "[local]") {
		t.Errorf("expected '[local]' tag for the surviving row, got:\n%s", out)
	}
	if strings.Contains(out, "global foo") {
		t.Errorf("shadowed global should not appear in normal mode, got:\n%s", out)
	}
}

func TestProfileList_wideShadowed(t *testing.T) {
	app, dir, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "foo.toml"),
		`name = "foo"\ndescription = "global foo"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "foo.toml"),
		`name = "foo"\ndescription = "local foo"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, true); err != nil {
		t.Fatalf("profileList wide: %v", err)
	}
	out := buf.String()

	// Both rows present in wide mode.
	if !strings.Contains(out, "global") {
		t.Errorf("expected 'global' scope column in wide mode, got:\n%s", out)
	}
	if !strings.Contains(out, "local") {
		t.Errorf("expected 'local' scope column in wide mode, got:\n%s", out)
	}
	if !strings.Contains(out, "[shadowed]") {
		t.Errorf("expected '[shadowed]' on the global row, got:\n%s", out)
	}
}

func TestProfileList_wideMode(t *testing.T) {
	app, dir, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "alpha.toml"),
		`name = "alpha"\ndescription = "Alpha"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "beta.toml"),
		`name = "beta"\ndescription = "Beta"`)

	var buf bytes.Buffer
	if err := app.profileList(&buf, true); err != nil {
		t.Fatalf("profileList wide: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "SCOPE") {
		t.Errorf("expected SCOPE header in wide mode, got:\n%s", out)
	}
	if !strings.Contains(out, "PATH") {
		t.Errorf("expected PATH header in wide mode, got:\n%s", out)
	}
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected 'alpha' in wide output, got:\n%s", out)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("expected 'beta' in wide output, got:\n%s", out)
	}
}

// ── profileInstallFromURL ─────────────────────────────────────────────────────

const minimalProfileTOML = `schema_version = "1"
name = "remote-test"
description = "downloaded profile"
`

func TestProfileInstallFromURL_basic(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalProfileTOML))
	}))

	app, dir := newTestApp(t)
	var buf bytes.Buffer
	url := srv.URL + "/remote-test.toml"
	if err := app.profileInstallFromURL(&buf, url, "", false, ""); err != nil {
		t.Fatalf("profileInstallFromURL: %v", err)
	}

	dest := filepath.Join(dir, "profiles", "remote-test.toml")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading installed profile: %v", err)
	}
	if string(data) != minimalProfileTOML {
		t.Errorf("unexpected content: %q", data)
	}
	if !strings.Contains(buf.String(), "remote-test") {
		t.Errorf("expected confirmation in output, got: %q", buf.String())
	}
}

func TestProfileInstallFromURL_customName(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalProfileTOML))
	}))

	app, dir := newTestApp(t)
	if err := app.profileInstallFromURL(&bytes.Buffer{}, srv.URL+"/some.toml", "custom", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dest := filepath.Join(dir, "profiles", "custom.toml")
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("expected file at %s: %v", dest, err)
	}
}

func TestProfileInstallFromURL_conflictNoForce(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalProfileTOML))
	}))

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "remote-test.toml"), `name = "existing"`)

	err := app.profileInstallFromURL(&bytes.Buffer{}, srv.URL+"/remote-test.toml", "", false, "")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProfileInstallFromURL_forceOverwrite(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalProfileTOML))
	}))

	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "remote-test.toml"), `name = "old"`)

	if err := app.profileInstallFromURL(&bytes.Buffer{}, srv.URL+"/remote-test.toml", "", true, ""); err != nil {
		t.Fatalf("unexpected error with --force: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "profiles", "remote-test.toml"))
	if string(data) != minimalProfileTOML {
		t.Errorf("expected overwritten content, got: %q", data)
	}
}

func TestProfileInstallFromURL_invalidTOML(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not = [valid toml"))
	}))

	app, _ := newTestApp(t)
	err := app.profileInstallFromURL(&bytes.Buffer{}, srv.URL+"/bad.toml", "", false, "")
	if err == nil {
		t.Fatal("expected TOML parse error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid TOML") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProfileInstallFromURL_http404(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	app, _ := newTestApp(t)
	err := app.profileInstallFromURL(&bytes.Buffer{}, srv.URL+"/missing.toml", "", false, "")
	if err == nil {
		t.Fatal("expected HTTP error, got nil")
	}
}

func TestProfileInstallFromURL_notURL(t *testing.T) {
	app, _ := newTestApp(t)
	err := app.profileInstallFromURL(&bytes.Buffer{}, "not-a-url", "", false, "")
	if err == nil {
		t.Fatal("expected error for non-URL, got nil")
	}
}

func TestProfileInstallFromURL_nameFromURLSegment(t *testing.T) {
	srv := newProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalProfileTOML))
	}))

	tests := []struct {
		urlPath  string
		wantName string
	}{
		{"/my-profile.toml", "my-profile"},
		{"/claude-one-shot.toml", "claude-one-shot"},
		{"/foo", "foo"},
	}
	for _, tt := range tests {
		app, dir := newTestApp(t)
		if err := app.profileInstallFromURL(&bytes.Buffer{}, srv.URL+tt.urlPath, "", false, ""); err != nil {
			t.Errorf("URL %s: unexpected error: %v", tt.urlPath, err)
			continue
		}
		dest := filepath.Join(dir, "profiles", tt.wantName+".toml")
		if _, err := os.Stat(dest); err != nil {
			t.Errorf("URL %s: expected file at %s: %v", tt.urlPath, dest, err)
		}
	}
}

// ── profile show: no argument, network section ───────────────────────────────

// `inner profile show` with no argument shows the profile a bare `inner run`
// would use: default_profile from the project config in the conventional path.
func TestProfileShowCmd_noArgs_usesCurrentProfile(t *testing.T) {
	app, dir, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "other.toml"), `name = "other"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner.toml"), `default_profile = "project-one"`)
	writeTestFile(t, filepath.Join(workDir, ".config", "inner", "profiles", "project-one.toml"), `name = "project-one"`)

	root := buildRootCmd(app)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"profile", "show"})
	if err := root.Execute(); err != nil {
		t.Fatalf("profile show: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `name = "project-one"`) {
		t.Errorf("expected the current profile's content, got:\n%s", out)
	}
	if !strings.Contains(out, "# current profile: project-one") {
		t.Errorf("expected a header naming the current profile, got:\n%s", out)
	}
}

// With no default_profile configured anywhere, the current profile is "default";
// when that does not exist the command must say so rather than print nothing.
func TestProfileShowCmd_noArgs_noDefaultProfile(t *testing.T) {
	app, _ := newTestApp(t)

	root := buildRootCmd(app)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"profile", "show"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error when the current profile does not exist")
	}
	if !strings.Contains(err.Error(), `"default"`) {
		t.Errorf("error should name the missing profile, got: %v", err)
	}
}

// The allow list a run enforces is assembled from capability defaults, "@group"
// references and profile entries; --explain must show that effective list, with
// the layer each entry came from, since none of it is visible in the raw TOML.
func TestProfileShowExplain_showsEffectiveNetwork(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "netprof.toml"), `
schema_version = "1"
name           = "netprof"
capabilities   = ["claude"]

[sandbox]
network_mode  = "allowlist"
network_allow = ["@npm", "example.com"]
network_deny  = ["telemetry.example.com"]
`)

	var buf bytes.Buffer
	if err := app.profileShowExplain(&buf, "netprof"); err != nil {
		t.Fatalf("profileShowExplain: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"── network ",
		"mode: allowlist",
		"api.anthropic.com",
		"[capability:claude]",
		"registry.npmjs.org",
		"[group:npm]",
		"example.com",
		"[profile]",
		"telemetry.example.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}
}

// A profile with no capabilities still gets the network section: "mode: off" is
// the answer to "what does this profile do about network?".
func TestProfileShowExplain_noCapabilities_stillShowsNetwork(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "plain.toml"), `
schema_version = "1"
name = "plain"

[sandbox]
network = false
`)

	var buf bytes.Buffer
	if err := app.profileShowExplain(&buf, "plain"); err != nil {
		t.Fatalf("profileShowExplain: %v", err)
	}
	if !strings.Contains(buf.String(), "mode: off") {
		t.Errorf("expected the network section, got:\n%s", buf.String())
	}
}

// --resolved is the effective profile, so its network_allow must be the resolved
// union (groups expanded, capability defaults included), not the raw entries.
func TestProfileShowResolved_expandsNetworkAllow(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "netprof.toml"), `
schema_version = "1"
name           = "netprof"
capabilities   = ["claude"]

[sandbox]
network_mode  = "allowlist"
network_allow = ["@npm"]
`)

	var buf bytes.Buffer
	if err := app.profileShowResolved(&buf, "netprof"); err != nil {
		t.Fatalf("profileShowResolved: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "@npm") {
		t.Errorf("group reference should be expanded in the resolved profile:\n%s", out)
	}
	for _, want := range []string{"registry.npmjs.org", "api.anthropic.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("resolved profile missing %q:\n%s", want, out)
		}
	}
}

// A legacy profile that says nothing about network still resolves to a network
// model — "off", fail-closed — and plain `profile show` must say so, since the
// file itself gives the reader no clue.
func TestProfileShow_legacyProfile_showsResolvedNetwork(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "legacy.toml"), `
schema_version = "1"
name = "legacy"

[entrypoint]
cmd = "bash"
`)

	var buf bytes.Buffer
	if err := app.profileShow(&buf, "legacy"); err != nil {
		t.Fatalf("profileShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "# resolved network: off") {
		t.Errorf("expected the resolved network comment, got:\n%s", out)
	}
	// The block must stay comments so the output is still parseable as TOML.
	if _, err := toml.Decode(out, &config.Profile{}); err != nil {
		t.Errorf("show output is no longer valid TOML: %v\n%s", err, out)
	}
}

// The legacy network = true bool resolves to "full"; plain show must name the
// resolved mode, not echo the bool.
func TestProfileShow_legacyNetworkBool_showsFull(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "legacy-net.toml"), `
schema_version = "1"
name = "legacy-net"

[sandbox]
network = true
`)

	var buf bytes.Buffer
	if err := app.profileShow(&buf, "legacy-net"); err != nil {
		t.Fatalf("profileShow: %v", err)
	}
	if !strings.Contains(buf.String(), "# resolved network: full") {
		t.Errorf("expected mode full, got:\n%s", buf.String())
	}
}

// In allowlist mode the comment block carries the effective destinations and
// the layer each came from, like the --explain section.
func TestProfileShow_allowlist_commentsListDestinations(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "al.toml"), `
schema_version = "1"
name           = "al"
capabilities   = ["claude"]

[sandbox]
network_mode  = "allowlist"
network_allow = ["@npm"]
network_deny  = ["telemetry.example.com"]
`)

	var buf bytes.Buffer
	if err := app.profileShow(&buf, "al"); err != nil {
		t.Fatalf("profileShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"# resolved network: allowlist",
		"#   allow: api.anthropic.com [capability:claude]",
		"#   allow: registry.npmjs.org [group:npm]",
		"#   deny:  telemetry.example.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
}

// --explain prints its own network section; the raw TOML above it must not
// repeat the same thing as comments.
func TestProfileShowExplain_doesNotRepeatNetworkComment(t *testing.T) {
	app, dir := newTestApp(t)
	writeTestFile(t, filepath.Join(dir, "profiles", "plain2.toml"), `
schema_version = "1"
name = "plain2"
`)

	var buf bytes.Buffer
	if err := app.profileShowExplain(&buf, "plain2"); err != nil {
		t.Fatalf("profileShowExplain: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "# resolved network:") {
		t.Errorf("--explain should print the network section only once:\n%s", out)
	}
	if !strings.Contains(out, "mode: off") {
		t.Errorf("expected the network section, got:\n%s", out)
	}
}
