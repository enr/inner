package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInit_createsDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, subdir := range []string{"profiles", "logs", "directives"} {
		if _, err := os.Stat(filepath.Join(dir, subdir)); err != nil {
			t.Errorf("expected %s/ to be created: %v", subdir, err)
		}
	}
}

func TestInit_installsDefaultProfiles(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, name := range []string{"default", "agent-interactive", "one-shot", "shell"} {
		p := filepath.Join(dir, "profiles", name+".toml")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected profile %s.toml to be installed: %v", name, err)
		}
	}
}

func TestInit_doesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := `name = "my-custom"`
	dest := filepath.Join(dir, "profiles", "default.toml")
	if err := os.WriteFile(dest, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	data, _ := os.ReadFile(dest)
	if string(data) != custom {
		t.Errorf("Init overwrote existing profile; got: %q", string(data))
	}
}

func TestInit_idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := Init(dir); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestDefaultProfilesFS_containsTomlFiles(t *testing.T) {
	fsys := DefaultProfilesFS()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one embedded profile")
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".toml") {
			t.Errorf("unexpected non-toml file in embedded profiles: %s", e.Name())
		}
	}
}

func TestDefaultProfiles_nonEmpty(t *testing.T) {
	fsys := DefaultProfilesFS()
	entries, _ := fs.ReadDir(fsys, ".")
	for _, e := range entries {
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			t.Errorf("reading embedded profile %s: %v", e.Name(), err)
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			t.Errorf("embedded profile %s is empty", e.Name())
		}
	}
}
