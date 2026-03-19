package profile

import (
	"fmt"
	"os"
	"os/exec"
	"slices"

	"github.com/enr/inner/internal/config"
)

// Level indicates the severity of a validation issue.
type Level string

const (
	LevelError   Level = "error"
	LevelWarning Level = "warning"
)

// Issue is a single validation finding.
type Issue struct {
	Level   Level
	Message string
}

func (i Issue) String() string {
	return fmt.Sprintf("[%s] %s", i.Level, i.Message)
}

// Result collects all issues found during validation.
type Result struct {
	Issues []Issue
}

// HasErrors reports whether any fatal errors were found.
func (r *Result) HasErrors() bool {
	for _, i := range r.Issues {
		if i.Level == LevelError {
			return true
		}
	}
	return false
}

func (r *Result) addError(msg string) {
	r.Issues = append(r.Issues, Issue{Level: LevelError, Message: msg})
}

func (r *Result) addWarning(msg string) {
	r.Issues = append(r.Issues, Issue{Level: LevelWarning, Message: msg})
}

// Validate checks the semantic consistency of a Profile.
// It does not execute anything and does not modify any state.
func Validate(p *config.Profile) Result {
	var r Result

	// 1. Verify mount source paths exist on the host (after expansion).
	for src, entry := range p.Mounts {
		if entry.Mode == "tmpfs" {
			// tmpfs mounts have no host source — skip existence check.
			continue
		}
		expanded := config.ExpandPath(src)
		if _, err := os.Stat(expanded); err != nil {
			if os.IsNotExist(err) {
				r.addError(fmt.Sprintf("mount source %q does not exist on host (expanded: %q)", src, expanded))
			} else {
				r.addError(fmt.Sprintf("mount source %q cannot be accessed: %v", src, err))
			}
		}
		if entry.Mode != "" && entry.Mode != "ro" && entry.Mode != "rw" {
			r.addError(fmt.Sprintf("mount %q has invalid mode %q (must be \"ro\", \"rw\", or \"tmpfs\")", src, entry.Mode))
		}
	}

	// 2. Warn on unknown sandbox.allow keys.
	for _, key := range p.Sandbox.Allow {
		if !slices.Contains(config.ValidAllowKeys, key) {
			r.addWarning(fmt.Sprintf("unknown allow key %q (valid keys: %v)", key, config.ValidAllowKeys))
		}
	}

	// 3. Verify entrypoint.cmd is reachable in PATH (warning only).
	if p.Entrypoint.Cmd != "" {
		if _, err := exec.LookPath(p.Entrypoint.Cmd); err != nil {
			r.addWarning(fmt.Sprintf("entrypoint command %q not found in PATH", p.Entrypoint.Cmd))
		}
	}

	// 3. Logical constraint: non-interactive with no timeout.
	if !p.Entrypoint.Interactive && p.Output.TimeoutSeconds == 0 {
		r.addWarning("entrypoint is non-interactive but no timeout is set (agent may run indefinitely)")
	}

	return r
}
