package main

import (
	"fmt"
	"os"

	"github.com/enr/inner/internal/config"
)

// applyGenericSafeMounts processes all mounts with Mode == "safe-rw":
// for each such mount it copies the source directory to a temporary directory,
// replaces the mount's Src with the temp path, and sets Mode to "rw" so the
// isolator treats it as a regular writable bind mount.
//
// Must be called AFTER applyCapabilities so that capability-injected mounts
// (which are already "rw") are not double-processed.
//
// Returns a combined cleanup that removes all temporary directories.
func applyGenericSafeMounts(rc *config.RunConfig) (func(), error) {
	var cleanups []func()

	for i, m := range rc.Mounts {
		if m.Mode != "safe-rw" {
			continue
		}
		tmp, err := os.MkdirTemp("", "inner-safe-*")
		if err != nil {
			for _, fn := range cleanups {
				fn()
			}
			return nil, fmt.Errorf("creating safe-rw sandbox dir for %q: %w", m.Src, err)
		}
		cleanup := func() { os.RemoveAll(tmp) }
		cleanups = append(cleanups, cleanup)

		if err := copyDir(m.Src, tmp); err != nil {
			for _, fn := range cleanups {
				fn()
			}
			return nil, fmt.Errorf("copying safe-rw mount %q: %w", m.Src, err)
		}
		rc.Mounts[i].Src = tmp
		rc.Mounts[i].Mode = "rw"
	}

	return func() {
		for _, fn := range cleanups {
			fn()
		}
	}, nil
}
