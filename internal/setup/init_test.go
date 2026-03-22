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
	for _, name := range []string{"claude-interactive", "claude-one-shot", "shell", "claude-containers", "gemini-interactive", "gemini-one-shot", "shell-containers"} {
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

func TestAgentContainersProfile_uidExpansion(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Load the profile via config.Loader and verify ${UID} was expanded.
	// We import config indirectly: just read the installed file and check that
	// the raw TOML no longer contains the literal "${UID}" after Init, but
	// the raw embedded file does (confirming the template uses it).
	embeddedData, err := fs.ReadFile(DefaultProfilesFS(), "claude-containers.toml")
	if err != nil {
		t.Fatalf("reading embedded profile: %v", err)
	}
	if !strings.Contains(string(embeddedData), "${UID}") {
		t.Error("embedded claude-containers.toml should contain ${UID} placeholder")
	}

	// The installed file is a byte-for-byte copy — expansion happens at load time
	// via config.ExpandPath, not at install time.
	installed, err := os.ReadFile(filepath.Join(dir, "profiles", "claude-containers.toml"))
	if err != nil {
		t.Fatalf("reading installed profile: %v", err)
	}
	if !strings.Contains(string(installed), "${UID}") {
		t.Error("installed claude-containers.toml should still contain ${UID} (expanded at load time)")
	}

	// Verify the expected path template is present in the profile.
	if !strings.Contains(string(embeddedData), "/run/user/${UID}/podman") {
		t.Error("expected /run/user/${UID}/podman path template in embedded profile")
	}
}

func TestAgentContainersProfile_hasVerifyCustomChecks(t *testing.T) {
	data, err := fs.ReadFile(DefaultProfilesFS(), "claude-containers.toml")
	if err != nil {
		t.Fatalf("reading embedded profile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[verify.custom]") {
		t.Error("claude-containers.toml should have [verify.custom] section")
	}
	if !strings.Contains(content, "podman socket raggiungibile") {
		t.Error("claude-containers.toml should have podman socket check")
	}
}

func TestAgentContainersProfile_hasNoopBlock(t *testing.T) {
	data, err := fs.ReadFile(DefaultProfilesFS(), "claude-containers.toml")
	if err != nil {
		t.Fatalf("reading embedded profile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[noop]") {
		t.Error("claude-containers.toml should have [noop] section")
	}
	if !strings.Contains(content, `"docker" = "podman"`) {
		t.Error("claude-containers.toml should have docker→podman rewrite")
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
