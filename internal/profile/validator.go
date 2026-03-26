package profile

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

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
// workDir is the directory from which inner was invoked; it is substituted
// for the ${workdir} token in mount source paths before path expansion.
func Validate(p *config.Profile, workDir string) Result {
	var r Result

	const workdirToken = "${workdir}"
	const workspacesPathToken = "${workspaces_path}"

	// 1. Verify mount source paths exist on the host (after expansion).
	for src, entry := range p.Mounts {
		if entry.Mode == "tmpfs" {
			// tmpfs mounts have no host source — skip existence check.
			continue
		}
		if strings.Contains(src, workdirToken) {
			if workDir == "" {
				// No workdir context available; skip existence check.
				continue
			}
			src = strings.ReplaceAll(src, workdirToken, workDir)
		}
		expanded := config.ExpandPath(src)
		if _, err := os.Stat(expanded); err != nil {
			if os.IsNotExist(err) {
				r.addError(fmt.Sprintf("mount source %q does not exist on host (expanded: %q)", src, expanded))
			} else {
				r.addError(fmt.Sprintf("mount source %q cannot be accessed: %v", src, err))
			}
		}
		if entry.Mode != "" && entry.Mode != "ro" && entry.Mode != "rw" && entry.Mode != "safe-rw" {
			r.addError(fmt.Sprintf("mount %q has invalid mode %q (must be \"ro\", \"rw\", \"safe-rw\", or \"tmpfs\")", src, entry.Mode))
		}
	}

	// 2. Verify mount dest paths exist on the host (after expansion).
	// Dests using ${workspaces_path} are pre-created by the workspace manager
	// (os.MkdirAll) before bwrap starts, so they are exempt from this check.
	for _, entry := range p.Mounts {
		if strings.Contains(entry.Dest, workspacesPathToken) {
			continue
		}
		dest := config.ExpandPath(entry.Dest)
		if _, err := os.Stat(dest); err != nil {
			if os.IsNotExist(err) {
				r.addError(fmt.Sprintf("mount dest %q does not exist on host (expanded: %q)", entry.Dest, dest))
			} else {
				r.addError(fmt.Sprintf("mount dest %q cannot be accessed: %v", entry.Dest, err))
			}
		}
	}

	// 3. Warn on unknown sandbox.allow keys.
	for _, key := range p.Sandbox.Allow {
		if !slices.Contains(config.ValidAllowKeys, key) {
			r.addWarning(fmt.Sprintf("unknown allow key %q (valid keys: %v)", key, config.ValidAllowKeys))
		}
	}

	// 4. Verify entrypoint.cmd is reachable in PATH (warning only).
	if p.Entrypoint.Cmd != "" {
		if _, err := exec.LookPath(p.Entrypoint.Cmd); err != nil {
			r.addWarning(fmt.Sprintf("entrypoint command %q not found in PATH", p.Entrypoint.Cmd))
		}
	}

	// 5. Logical constraint: non-interactive with no timeout.
	if !p.Entrypoint.Interactive && p.Output.TimeoutSeconds == 0 {
		r.addWarning("entrypoint is non-interactive but no timeout is set (agent may run indefinitely)")
	}

	// 6. Validate capabilities (step 1g).
	//    Build the set of expanded mount dests for conflict detection.
	expandedMountDests := make(map[string]bool, len(p.Mounts))
	for _, entry := range p.Mounts {
		if entry.Dest != "" {
			expandedMountDests[config.ExpandPath(entry.Dest)] = true
		}
	}
	for _, cap := range p.Capabilities {
		if !slices.Contains(config.ValidCapabilities, cap) {
			r.addError(fmt.Sprintf("unknown capability %q (valid values: %v)", cap, config.ValidCapabilities))
			continue
		}
		dirs, ok := config.CapabilityHostDirs[cap]
		if !ok {
			continue
		}
		// Warn if the primary host directory does not exist on this machine.
		primary := config.ExpandPath(dirs[0])
		if _, err := os.Stat(primary); os.IsNotExist(err) {
			r.addWarning(fmt.Sprintf("capability %q: host directory %q does not exist — runtime will fail", cap, dirs[0]))
		}
		// Error if any capability directory conflicts with an explicit mount dest.
		for _, dir := range dirs {
			expanded := config.ExpandPath(dir)
			if expandedMountDests[expanded] {
				r.addError(fmt.Sprintf("capability %q and explicit mount both target dest %q — remove the explicit mount (capability injects it)", cap, dir))
			}
		}
	}

	return r
}
