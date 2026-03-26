package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enr/inner/internal/config"
)

// ── applyGenericSafeMounts ────────────────────────────────────────────────────

func TestApplyGenericSafeMounts_transformsSafeRw(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "data.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: src, Dest: "/sandbox/data", Mode: "safe-rw"},
		},
	}

	cleanup, err := applyGenericSafeMounts(rc)
	if err != nil {
		t.Fatalf("applyGenericSafeMounts: %v", err)
	}
	defer cleanup()

	// Src must have been replaced with a tmp path.
	if rc.Mounts[0].Src == src {
		t.Error("mount Src should be a sandboxed tmp path, not the original src")
	}
	// Mode must be "rw" after transformation.
	if rc.Mounts[0].Mode != "rw" {
		t.Errorf("mount Mode should be \"rw\" after transformation, got %q", rc.Mounts[0].Mode)
	}
	// Content must be accessible in the tmp dir.
	data, err := os.ReadFile(filepath.Join(rc.Mounts[0].Src, "data.txt"))
	if err != nil {
		t.Fatalf("data.txt not found in sandbox: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("data.txt content = %q, want \"hello\"", data)
	}
}

func TestApplyGenericSafeMounts_cleanupRemovesTmpDir(t *testing.T) {
	src := t.TempDir()

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: src, Dest: "/sandbox/data", Mode: "safe-rw"},
		},
	}

	cleanup, err := applyGenericSafeMounts(rc)
	if err != nil {
		t.Fatalf("applyGenericSafeMounts: %v", err)
	}

	tmpPath := rc.Mounts[0].Src
	cleanup()

	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("tmp dir should be removed after cleanup")
	}
	// Original src must still exist.
	if _, err := os.Stat(src); err != nil {
		t.Error("original src should not be removed by cleanup")
	}
}

func TestApplyGenericSafeMounts_noopForOtherModes(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: src, Dest: dest, Mode: "rw"},
			{Src: src, Dest: dest, Mode: "ro"},
		},
	}

	cleanup, err := applyGenericSafeMounts(rc)
	if err != nil {
		t.Fatalf("applyGenericSafeMounts: %v", err)
	}
	cleanup()

	for _, m := range rc.Mounts {
		if m.Src != src {
			t.Errorf("non-safe-rw mount Src should be unchanged, got %s", m.Src)
		}
	}
}

func TestApplyGenericSafeMounts_multipleSafeRw(t *testing.T) {
	src1 := t.TempDir()
	src2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(src1, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src2, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	rc := &config.RunConfig{
		Mounts: []config.Mount{
			{Src: src1, Dest: "/s1", Mode: "safe-rw"},
			{Src: src2, Dest: "/s2", Mode: "safe-rw"},
		},
	}

	cleanup, err := applyGenericSafeMounts(rc)
	if err != nil {
		t.Fatalf("applyGenericSafeMounts: %v", err)
	}
	defer cleanup()

	if rc.Mounts[0].Src == src1 || rc.Mounts[1].Src == src2 {
		t.Error("both safe-rw mounts should have sandboxed tmp paths")
	}
	if _, err := os.Stat(filepath.Join(rc.Mounts[0].Src, "a.txt")); err != nil {
		t.Error("a.txt not found in first sandbox")
	}
	if _, err := os.Stat(filepath.Join(rc.Mounts[1].Src, "b.txt")); err != nil {
		t.Error("b.txt not found in second sandbox")
	}
}
