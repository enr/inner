package workspace

import (
	"errors"
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
	created  []string // dirs we created (removed on Release)
}

// Prepare ensures each directory in dests exists under workspacesPath, then
// writes a lock file recording this run. It fails if another live process
// has already locked any of the same directories.
func Prepare(workspacesPath string, dests []string, info RunInfo) (*Manager, error) {
	if _, err := os.Stat(workspacesPath); err != nil {
		return nil, fmt.Errorf("workspaces_path %q does not exist: %w", workspacesPath, err)
	}

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
		if !isAlive(ld.Pid) {
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
			if err := os.MkdirAll(dest, 0o755); err != nil {
				// Roll back already-created dirs.
				for _, d := range created {
					os.Remove(d) //nolint:errcheck
				}
				return nil, fmt.Errorf("creating workspace directory %q: %w", dest, err)
			}
			created = append(created, dest)
		}
	}

	// Write lock file.
	pid := os.Getpid()
	lockPath := filepath.Join(workspacesPath, ".inner-"+strconv.Itoa(pid)+".lock")
	ld := lockData{
		Profile: info.Profile,
		Command: info.Command,
		Pid:     pid,
		Started: time.Now().UTC().Format(time.RFC3339),
		Dests:   dests,
	}
	f, err := os.Create(lockPath)
	if err != nil {
		for _, d := range created {
			os.Remove(d) //nolint:errcheck
		}
		return nil, fmt.Errorf("creating lock file %q: %w", lockPath, err)
	}
	if err := toml.NewEncoder(f).Encode(ld); err != nil {
		f.Close()
		os.Remove(lockPath) //nolint:errcheck
		for _, d := range created {
			os.Remove(d) //nolint:errcheck
		}
		return nil, fmt.Errorf("writing lock file %q: %w", lockPath, err)
	}
	f.Close()

	return &Manager{lockPath: lockPath, created: created}, nil
}

// Release removes the lock file and any workspace directories that were
// created by Prepare. Errors are silently ignored (best-effort cleanup).
func (m *Manager) Release() {
	os.Remove(m.lockPath) //nolint:errcheck
	for _, d := range m.created {
		os.Remove(d) //nolint:errcheck
	}
}

// isAlive reports whether the given pid refers to a running process.
// Returns true both when the process exists and when we lack permission to
// signal it (the process exists but belongs to another user).
func isAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
