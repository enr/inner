package profile

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/enr/inner/internal/config"
)

// validateLimits checks the syntax of [sandbox.limits] fields.
// Memory must be a positive integer followed by M or G (e.g. "512M", "4G").
// CPU must be parseable by NormalizeCPUQuota.
// Pids must be positive.
func validateLimits(r *Result, l *config.ResourceLimits) {
	if l == nil {
		return
	}
	if l.Memory != "" {
		m := strings.ToUpper(strings.TrimSpace(l.Memory))
		var num string
		if strings.HasSuffix(m, "G") {
			num = m[:len(m)-1]
		} else if strings.HasSuffix(m, "M") {
			num = m[:len(m)-1]
		} else {
			r.addError(fmt.Sprintf("[sandbox.limits] memory %q must end with M or G (e.g. \"512M\", \"4G\")", l.Memory))
			num = ""
		}
		if num != "" {
			var n int
			if _, err := fmt.Sscanf(num, "%d", &n); err != nil || n <= 0 {
				r.addError(fmt.Sprintf("[sandbox.limits] memory %q: numeric part must be a positive integer", l.Memory))
			}
		}
	}
	if l.CPU != "" {
		if _, err := config.NormalizeCPUQuota(l.CPU); err != nil {
			r.addError(fmt.Sprintf("[sandbox.limits] cpu: %v", err))
		}
	}
	if l.Pids < 0 {
		r.addError(fmt.Sprintf("[sandbox.limits] pids %d must be a non-negative integer (0 = unset)", l.Pids))
	}
}

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

	// Build the set of expanded mount dests once; reused below by the [env] set
	// path check (2d) and by capability conflict detection (step 6).
	expandedMountDests := make(map[string]bool, len(p.Mounts))
	for _, entry := range p.Mounts {
		if entry.Dest != "" {
			expandedMountDests[config.ExpandPath(entry.Dest)] = true
		}
	}

	// 2b. Warn on [env] set values that reference host environment variables
	// which are not defined on this machine: ExpandPath (loader.go) silently
	// resolves undefined references to an empty string, which can produce a
	// working-looking but broken value (e.g. JAVA_HOME="").
	{
		keys := make([]string, 0, len(p.Env.Set))
		for k := range p.Env.Set {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			for _, name := range config.UndefinedVarRefs(p.Env.Set[k]) {
				r.addWarning(fmt.Sprintf("[env] set %s references undefined host variable $%s (expands to empty string)", k, name))
			}
		}
	}

	// 2c. Validate [env] path_prepend entries: reject empty strings (they
	// would collapse to an empty PATH component, e.g. "::"), and warn when a
	// directory does not exist on the host. Warning rather than error because
	// the directory may only exist inside the sandbox via a profile mount.
	for _, entry := range p.Env.PathPrepend {
		if entry == "" {
			r.addError("[env] path_prepend contains an empty entry")
			continue
		}
		expanded := config.ExpandPath(entry)
		if _, err := os.Stat(expanded); err != nil && os.IsNotExist(err) {
			r.addWarning(fmt.Sprintf("[env] path_prepend %q does not exist on host (expanded: %q)", entry, expanded))
		}
	}

	// 2d. Warn on [env] set values that look like absolute host paths and do
	// not exist: catches typos and stale pinned-version paths (e.g. a JDK
	// that was uninstalled) before they surface as an opaque "command not
	// found" the first time something runs inside the sandbox.
	//
	// A value is only checked if, after ExpandPath, it starts with "/" and
	// contains no ":" — the latter excludes PATH-like multi-entry values and
	// URL/socket values such as "unix:///run/..." or "https://...", which
	// always contain a ":" themselves. Values referencing ${workspaces_path}
	// or ${workdir} in their original (pre-expansion) form are skipped: those
	// tokens are not resolved by ExpandPath and only apply to mount fields,
	// so checking them here would either false-positive or check the wrong
	// path. Values matching an explicit mount dest are skipped too: they
	// exist only inside the sandbox, not on the host running this check.
	{
		keys := make([]string, 0, len(p.Env.Set))
		for k := range p.Env.Set {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			v := p.Env.Set[k]
			if strings.Contains(v, workspacesPathToken) || strings.Contains(v, workdirToken) {
				continue
			}
			expanded := config.ExpandPath(v)
			if !strings.HasPrefix(expanded, "/") || strings.Contains(expanded, ":") {
				continue
			}
			if expandedMountDests[expanded] {
				continue
			}
			if _, err := os.Stat(expanded); err != nil && os.IsNotExist(err) {
				r.addWarning(fmt.Sprintf("[env] set %s=%q: path does not exist on host (expanded: %q)", k, v, expanded))
			}
		}
	}

	// 3. Warn on unknown sandbox.allow keys and dangerous known keys.
	for _, key := range p.Sandbox.Allow {
		if !slices.Contains(config.ValidAllowKeys, key) {
			r.addWarning(fmt.Sprintf("unknown allow key %q (valid keys: %v)", key, config.ValidAllowKeys))
		}
	}
	if slices.Contains(p.Sandbox.Allow, "nested-user-ns") {
		r.addWarning("nested-user-ns grants CAP_SETUID/CAP_SETGID to the sandbox; do not enable for untrusted agents")
		if p.Sandbox.Network {
			r.addWarning("nested-user-ns combined with network access increases privilege-escalation risk; review carefully")
		}
	}

	// 3b. Validate [sandbox] cgroup_manager. An unknown value is an error:
	// silently falling back to the default would hide a typo that only shows
	// up as an obscure podman failure inside the sandbox.
	if cm := p.Sandbox.CgroupManager; cm != "" {
		if !slices.Contains(config.ValidCgroupManagers, cm) {
			r.addError(fmt.Sprintf("invalid cgroup_manager %q (valid values: %v)", cm, config.ValidCgroupManagers))
		} else if !slices.Contains(p.Sandbox.Allow, "nested-user-ns") {
			r.addWarning("cgroup_manager has no effect without nested-user-ns in [sandbox] allow")
		} else if cm == "systemd" {
			r.addWarning(`cgroup_manager = "systemd" disables the containers.conf override: rootless podman inside the sandbox will fail to create its transient scope unless the sandbox shares the host PID namespace and can reach the user systemd socket`)
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

	// 5b. Validate [sandbox.limits] syntax.
	validateLimits(&r, p.Sandbox.Limits)

	// 6. Validate capabilities (step 1g). Reuses expandedMountDests built above.
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
