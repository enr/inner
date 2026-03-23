package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	writeTestFile(t, filepath.Join(workDir, ".inner", "profiles", "local.toml"),
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
	// default_profile set to a file path like ".inner/profiles/local-prof.toml"
	app, _, workDir := newTestAppWithWorkDir(t)
	writeTestFile(t, filepath.Join(workDir, ".inner", "config.toml"),
		`default_profile = ".inner/profiles/local-prof.toml"`)
	writeTestFile(t, filepath.Join(workDir, ".inner", "profiles", "local-prof.toml"),
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
	writeTestFile(t, filepath.Join(workDir, ".inner", "config.toml"), `default_profile = "local-prof"`)
	writeTestFile(t, filepath.Join(workDir, ".inner", "profiles", "local-prof.toml"),
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
	localPath := filepath.Join(workDir, ".inner", "profiles", "local-only.toml")
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
	localPath := filepath.Join(workDir, ".inner", "profiles", "shared.toml")
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
	writeTestFile(t, filepath.Join(workDir, ".inner", "profiles", "foo.toml"),
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
	writeTestFile(t, filepath.Join(workDir, ".inner", "profiles", "foo.toml"),
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
	writeTestFile(t, filepath.Join(workDir, ".inner", "profiles", "beta.toml"),
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
