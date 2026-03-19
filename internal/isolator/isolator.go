package isolator

import (
	"os/exec"

	"github.com/enr/inner/internal/config"
)

// Isolator translates a RunConfig into an exec.Cmd ready to be launched.
// All backend-specific logic lives inside the implementation; callers
// operate only on this interface.
type Isolator interface {
	// Build translates cfg into a ready-to-run *exec.Cmd.
	// It is a pure transformation: no side effects, no execution.
	Build(cfg config.RunConfig) (*exec.Cmd, error)

	// Available reports whether the backend is usable on this host.
	// Returns (true, diagnostic) on success or (false, reason) on failure.
	// Used by `inner doctor`.
	Available() (bool, string)
}
