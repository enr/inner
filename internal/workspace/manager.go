package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// RunInfo holds metadata written into the lock file.
type RunInfo struct {
	Profile string
	Command string
}

// lockData is serialised as TOML into the lock file.
type lockData struct {
	Profile string   `toml:"profile"`
	Command string   `toml:"command"`
	Pid     int      `toml:"pid"`
	Started string   `toml:"started"`
	Dests   []string `toml:"dests"`
}

// Manager holds state for a single sandbox run's workspace directories.
type Manager struct {
	lockPath string
	lockFile *os.File // kept open to hold the exclusive flock
	created  []string // dirs we created (removed on Release)
}

// Prepare ensures each directory in dests exists under workspacesPath, then
// writes a lock file recording this run. It fails if another live process
// has already locked any of the same directories.
func Prepare(workspacesPath string, dests []string, info RunInfo) (*Manager, error) {
	if _, err := os.Stat(workspacesPath); err != nil {
		return nil, fmt.Errorf("workspaces_path %q does not exist: %w", workspacesPath, err)
	}

	// Acquire a global sentinel flock so the scan+mkdir+write sequence is atomic
	// across concurrent inner invocations (fixes TOCTOU, issue #10).
	sentinelPath := filepath.Join(workspacesPath, ".inner-sentinel.flock")
	sentinel, err := os.OpenFile(sentinelPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening sentinel lock %q: %w", sentinelPath, err)
	}
	defer sentinel.Close()
	if err := syscall.Flock(int(sentinel.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("acquiring sentinel lock: %w", err)
	}
	defer syscall.Flock(int(sentinel.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Scan existing lock files; remove stale ones; fail on live conflicts.
	entries, err := os.ReadDir(workspacesPath)
	if err != nil {
		return nil, fmt.Errorf("reading workspaces_path %q: %w", workspacesPath, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".inner-") || !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		lockPath := filepath.Join(workspacesPath, e.Name())
		var ld lockData
		if _, err := toml.DecodeFile(lockPath, &ld); err != nil {
			// Unreadable lock — treat as stale.
			os.Remove(lockPath) //nolint:errcheck
			continue
		}
		// Use flock to detect liveness instead of kill(pid, 0): acquiring
		// LOCK_EX|LOCK_NB succeeds only if no live process holds the lock,
		// which eliminates the PID-reuse false-positive (issue #10).
		if tryFlockStale(lockPath) {
			os.Remove(lockPath) //nolint:errcheck
			continue
		}
		// Live process — check for dest overlap.
		for _, existing := range ld.Dests {
			for _, want := range dests {
				if existing == want {
					return nil, fmt.Errorf("workspace directory %q is already in use by pid %d (profile %q)", want, ld.Pid, ld.Profile)
				}
			}
		}
	}

	// Pre-create workspace directories.
	var created []string
	for _, dest := range dests {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			newDirs := dirsToCreate(dest)
			if err := os.MkdirAll(dest, 0o755); err != nil {
				removeDeepestFirst(created)
				return nil, fmt.Errorf("creating workspace directory %q: %w", dest, err)
			}
			created = append(created, newDirs...)
		}
	}

	// Create lock file with O_EXCL (TOCTOU) and 0600 (no world-readable leakage).
	// Hold the flock for the process lifetime so stale detection works for us too.
	pid := os.Getpid()
	lockPath := filepath.Join(workspacesPath, ".inner-"+strconv.Itoa(pid)+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		removeDeepestFirst(created)
		return nil, fmt.Errorf("creating lock file %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		os.Remove(lockPath) //nolint:errcheck
		removeDeepestFirst(created)
		return nil, fmt.Errorf("flocking lock file %q: %w", lockPath, err)
	}
	ld := lockData{
		Profile: info.Profile,
		Command: info.Command,
		Pid:     pid,
		Started: time.Now().UTC().Format(time.RFC3339),
		Dests:   dests,
	}
	if err := toml.NewEncoder(f).Encode(ld); err != nil {
		f.Close()
		os.Remove(lockPath) //nolint:errcheck
		removeDeepestFirst(created)
		return nil, fmt.Errorf("writing lock file %q: %w", lockPath, err)
	}

	return &Manager{lockPath: lockPath, lockFile: f, created: created}, nil
}

// Release removes the lock file and any workspace directories that were
// created by Prepare. Errors are silently ignored (best-effort cleanup).
func (m *Manager) Release() {
	os.Remove(m.lockPath) //nolint:errcheck
	if m.lockFile != nil {
		m.lockFile.Close() // also releases the flock
	}
	removeDeepestFirst(m.created)
}

// dirsToCreate returns the directories that os.MkdirAll(dest) would create,
// ordered shallowest-first. It walks dest upward until it finds an existing
// ancestor, then returns the chain from that ancestor's immediate child down
// to dest. Returns nil if dest already exists.
func dirsToCreate(dest string) []string {
	dest = filepath.Clean(dest)
	// Find deepest existing ancestor.
	existing := dest
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break // reached filesystem root
		}
		existing = parent
	}
	if existing == dest {
		return nil
	}
	// Collect from dest up to (but not including) existing, then reverse.
	var dirs []string
	for cur := dest; cur != existing; cur = filepath.Dir(cur) {
		dirs = append(dirs, cur)
	}
	// Reverse to get shallowest-first order.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// removeDeepestFirst removes directories in reverse order (deepest first) so
// that children are removed before their parents.
func removeDeepestFirst(dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) //nolint:errcheck
	}
}

// tryFlockStale reports whether the lock file at path is stale (not held by a
// live process). It opens the file and attempts a non-blocking exclusive flock;
// success means nobody is currently holding it.
func tryFlockStale(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true // can't open → treat as stale
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		return true                                 // acquired → no live holder
	}
	return false // EWOULDBLOCK → live holder
}
