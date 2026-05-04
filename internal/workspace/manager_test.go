package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// TestPIDReuseStale verifies that a lock file whose recorded PID belongs to a
// live-but-unrelated process is detected as stale (issue #10). The old kill(0)
// check returns "alive" for PID 1 (init), causing a false conflict; the new
// flock-based check succeeds in acquiring the lock → stale → removed.
func TestPIDReuseStale(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ws", "myapp")

	// Write a fake lock file claiming PID=1 (init — always alive).
	// No flock is held on it, so the flock-based check must identify it as stale.
	fakeLock := filepath.Join(dir, ".inner-1.lock")
	ld := lockData{
		Profile: "fakepro",
		Command: "sleep 999",
		Pid:     1,
		Started: time.Now().UTC().Format(time.RFC3339),
		Dests:   []string{dest},
	}
	f, err := os.OpenFile(fakeLock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("creating fake lock: %v", err)
	}
	if err := toml.NewEncoder(f).Encode(ld); err != nil {
		f.Close()
		t.Fatalf("writing fake lock: %v", err)
	}
	f.Close() // no flock held

	mgr, err := Prepare(dir, []string{dest}, RunInfo{Profile: "test", Command: "cmd"})
	if err != nil {
		t.Fatalf("Prepare failed (PID-reuse false positive): %v", err)
	}
	mgr.Release()
}

// TestLockFilePermissions verifies that the lock file is created with mode 0600
// so it cannot be read by other users on a shared host (issue #10).
func TestLockFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ws", "myapp")

	mgr, err := Prepare(dir, []string{dest}, RunInfo{Profile: "test", Command: "cmd"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer mgr.Release()

	info, err := os.Stat(mgr.lockPath)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("lock file permissions = %04o, want 0600", perm)
	}
}

// TestDoublePrepareConflict verifies that a second concurrent Prepare for the
// same dest is rejected while the first Manager is still live (issue #10).
// With the old kill(0) approach this also worked for the local process, but the
// flock-based approach must preserve it and is the mechanism that makes
// PID-reuse detection correct for remote processes.
func TestDoublePrepareConflict(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "ws", "myapp")

	mgr, err := Prepare(dir, []string{dest}, RunInfo{Profile: "p1", Command: "c1"})
	if err != nil {
		t.Fatalf("first Prepare: %v", err)
	}
	defer mgr.Release()

	_, err = Prepare(dir, []string{dest}, RunInfo{Profile: "p2", Command: "c2"})
	if err == nil {
		t.Fatal("second Prepare with overlapping dest should fail while first Manager is live")
	}
}
