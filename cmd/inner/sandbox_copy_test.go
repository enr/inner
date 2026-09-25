package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/enr/inner/internal/config"
)

// A FIFO planted in a copied tree must neither hang inner (a blocking open)
// nor end up in the copy.
func TestCopyFile_refusesFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- copyFile(fifo, filepath.Join(dir, "dst")) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("err = %v, want a refusal of the FIFO", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("copyFile blocked on a FIFO")
	}
}

func TestCopyDir_skipsFIFO(t *testing.T) {
	src := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(src, "fifo"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644) //nolint:errcheck
	dst := filepath.Join(t.TempDir(), "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "fifo")); !os.IsNotExist(err) {
		t.Errorf("FIFO reached the copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Errorf("regular file missing: %v", err)
	}
}

func TestCopyFile_dropsSpecialModeBits(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tool")
	os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755) //nolint:errcheck
	if err := os.Chmod(src, 0o755|os.ModeSetuid); err != nil {
		t.Skip(err)
	}
	dst := filepath.Join(dir, "copy")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dst)
	if fi.Mode()&os.ModeSetuid != 0 {
		t.Error("setuid bit carried over to the copy")
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("perm = %v, want 0755", fi.Mode().Perm())
	}
}

// A dotfile-managed settings.json (a symlink into a dotfiles repo) keeps
// working: the claude settings are not lost from the sandbox.
func TestCopySettingsStripped_followsDotfileSymlink(t *testing.T) {
	dotfiles := t.TempDir()
	real := filepath.Join(dotfiles, "settings.json")
	os.WriteFile(real, []byte(`{"model":"opus","mcpServers":{"x":{}}}`), 0o644) //nolint:errcheck
	claudeDir := t.TempDir()
	link := filepath.Join(claudeDir, "settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "settings.json")
	if err := copySettingsStripped(link, dst); err != nil {
		t.Fatalf("copySettingsStripped: %v", err)
	}
	data, _ := os.ReadFile(dst)
	if !strings.Contains(string(data), `"model":"opus"`) || strings.Contains(string(data), "mcpServers") {
		t.Errorf("copy = %s", data)
	}
}

// ...but not when the link leads into a path the sandbox hides: a JSON
// credential file (~/.docker/config.json) must not come in as "settings".
func TestCopySettingsStripped_refusesLinkIntoHiddenPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dockerCfg := filepath.Join(home, ".docker", "config.json")
	os.MkdirAll(filepath.Dir(dockerCfg), 0o700)                                   //nolint:errcheck
	os.WriteFile(dockerCfg, []byte(`{"auths":{"r":{"auth":"c2VjcmV0"}}}`), 0o600) //nolint:errcheck
	link := filepath.Join(home, ".claude-settings.json")
	if err := os.Symlink(dockerCfg, link); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "settings.json")
	if err := copySettingsStripped(link, dst); err == nil {
		t.Fatal("expected a refusal for a link into ~/.docker/config.json")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("the credential file was copied as settings")
	}
}

// safe-rw copies the tree behind a symlinked source, as a bind mount of it
// would expose — not an empty directory.
func TestApplyGenericSafeMounts_symlinkedSource(t *testing.T) {
	realDir := t.TempDir()
	os.WriteFile(filepath.Join(realDir, "f.txt"), []byte("data"), 0o644) //nolint:errcheck
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	rc := &config.RunConfig{Mounts: []config.Mount{{Src: link, Dest: "/work", Mode: "safe-rw"}}}
	cleanup, err := applyGenericSafeMounts(rc)
	if err != nil {
		t.Fatalf("applyGenericSafeMounts: %v", err)
	}
	defer cleanup()
	data, err := os.ReadFile(filepath.Join(rc.Mounts[0].Src, "f.txt"))
	if err != nil || string(data) != "data" {
		t.Errorf("copy of the symlinked source is missing its content: %q %v", data, err)
	}
}

// The error for a symlinked required file says what to do about it.
func TestCopyFile_symlinkErrorIsActionable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "real"), []byte("x"), 0o600) //nolint:errcheck
	link := filepath.Join(dir, ".credentials.json")
	os.Symlink(filepath.Join(dir, "real"), link) //nolint:errcheck
	err := copyFile(link, filepath.Join(dir, "dst"))
	if err == nil || !strings.Contains(err.Error(), "replace it with a regular file") {
		t.Errorf("err = %v", err)
	}
}
