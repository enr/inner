package containers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOverride_cgroupfs(t *testing.T) {
	path, err := WriteOverride(CgroupManagerCgroupfs)
	if err != nil {
		t.Fatalf("WriteOverride: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(path))

	if filepath.Base(path) != "containers.conf" {
		t.Errorf("expected file named containers.conf, got %q", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated file: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "[engine]") || !strings.Contains(content, `cgroup_manager = "cgroupfs"`) {
		t.Errorf("unexpected content:\n%s", content)
	}
}

func TestWriteOverride_systemd(t *testing.T) {
	path, err := WriteOverride(CgroupManagerSystemd)
	if err != nil {
		t.Fatalf("WriteOverride: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(path))

	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `cgroup_manager = "systemd"`) {
		t.Errorf("unexpected content:\n%s", b)
	}
}

func TestWriteOverride_unknownManager(t *testing.T) {
	if _, err := WriteOverride("kubelet"); err == nil {
		t.Error("expected error for unknown cgroup manager")
	}
}

// The generated file must be readable by the sandboxed process (it is bound
// read-only, but a 0600 file created by another uid mapping would still fail).
func TestWriteOverride_worldReadable(t *testing.T) {
	path, err := WriteOverride(CgroupManagerCgroupfs)
	if err != nil {
		t.Fatalf("WriteOverride: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(path))

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm()&0o044 == 0 {
		t.Errorf("expected group/other-readable file, got mode %v", fi.Mode().Perm())
	}
}
