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

// ── copyDir symlink handling (ISS-02 / SECURITY_REVIEW #3 regression tests) ───
//
// copyDir must NEVER dereference a symlink found inside a copied tree: doing
// so reads the target on the host, before sandbox path-hiding applies, turning
// a planted link (e.g. ~/.claude/skills/evil -> ~/.ssh/id_rsa) into a read
// primitive for arbitrary host files — and copies arbitrarily large targets
// into RAM/tmp. These tests pin the safe behaviour: links are recreated
// verbatim, and the copy carries zero bytes of target content.

func TestCopyDir_symlinkToFile_notDereferenced(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY MATERIAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(base, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "evil")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(base, "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	copied := filepath.Join(dst, "evil")
	info, err := os.Lstat(copied)
	if err != nil {
		t.Fatalf("copied entry missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// Regression: the entry was dereferenced into a regular file — the
		// secret's CONTENT is now in the copy, bypassing sandbox hiding.
		data, _ := os.ReadFile(copied)
		t.Fatalf("symlink was dereferenced into a regular file (content: %q); must be recreated as a symlink", data)
	}
	target, err := os.Readlink(copied)
	if err != nil {
		t.Fatal(err)
	}
	if target != secret {
		t.Errorf("recreated link target = %q, want %q (verbatim)", target, secret)
	}
}

func TestCopyDir_symlinkToDir_recreatedAndWalkContinues(t *testing.T) {
	base := t.TempDir()
	heavy := filepath.Join(base, "heavy-dir")
	if err := os.MkdirAll(heavy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(heavy, "blob"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(base, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	// "a-link" sorts before "normal.txt": before the fix the walk aborted on
	// the link (EISDIR from os.ReadFile) and normal.txt was never copied.
	if err := os.Symlink(heavy, filepath.Join(src, "a-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "normal.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(base, "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir must not fail on a symlink to a directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "normal.txt")); err != nil {
		t.Errorf("normal.txt not copied (walk aborted?): %v", err)
	}
	info, err := os.Lstat(filepath.Join(dst, "a-link"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("a-link should be recreated as a symlink, got mode %v err %v", info, err)
	}
	// The heavy target must NOT have been copied into dst.
	if _, err := os.Stat(filepath.Join(dst, "a-link", "blob")); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(filepath.Join(dst, "a-link")); rerr == nil && resolved != heavy {
			t.Error("heavy directory content was copied into dst instead of linked")
		}
	}
}

func TestCopyDir_danglingSymlink_nonFatal(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "does-not-exist"), filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "normal.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(base, "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir must not fail on a dangling symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "normal.txt")); err != nil {
		t.Errorf("normal.txt not copied: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(dst, "dangling")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dangling link should be recreated verbatim, got %v err %v", info, err)
	}
}

func TestCopyDir_relativeSymlinkPreserved(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(src, "alias")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(base, "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	target, err := os.Readlink(filepath.Join(dst, "alias"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "real.txt" {
		t.Errorf("relative link target = %q, want \"real.txt\"", target)
	}
	// Intra-tree relative link must still resolve inside the copy.
	data, err := os.ReadFile(filepath.Join(dst, "alias"))
	if err != nil || string(data) != "content" {
		t.Errorf("alias should resolve inside the copy, got %q err %v", data, err)
	}
}

func TestCopyDir_rootSymlink_rejected(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(link, filepath.Join(base, "dst")); err == nil {
		t.Fatal("copyDir on a symlinked root should be refused with an explicit error")
	}
}

func TestApplyGenericSafeMounts_symlinkContentNotCopied(t *testing.T) {
	// End-to-end acceptance for ISS-02: a symlink planted in a safe-rw source
	// tree must not smuggle the target's content into the sandbox copy.
	base := t.TempDir()
	secret := filepath.Join(base, "hosts.yml")
	if err := os.WriteFile(secret, []byte("oauth_token: SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "tree")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "planted")); err != nil {
		t.Fatal(err)
	}

	rc := &config.RunConfig{
		Mounts: []config.Mount{{Src: src, Dest: "/sandbox/tree", Mode: "safe-rw"}},
	}
	cleanup, err := applyGenericSafeMounts(rc)
	if err != nil {
		t.Fatalf("applyGenericSafeMounts: %v", err)
	}
	defer cleanup()

	info, err := os.Lstat(filepath.Join(rc.Mounts[0].Src, "planted"))
	if err != nil {
		t.Fatalf("planted entry missing from copy: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("planted symlink was dereferenced: secret content copied into the sandbox mount source")
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
