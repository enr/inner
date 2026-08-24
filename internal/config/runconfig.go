package config

import "strings"

// RunConfig is the backend-agnostic representation of a sandbox run.
// It speaks in terms of intent, never backend-specific flags.
// Produced by the Loader; consumed by the Isolator.
type RunConfig struct {
	// Name is the logical profile name (from the profile's name field, or
	// derived from the filename). It may differ from the CLI argument used
	// to select the profile (which can be a file path).
	Name      string
	Mounts    []Mount
	Env       EnvConfig
	Network   bool
	Clipboard bool
	// PidNamespace requests a private PID namespace for the sandbox
	// (bwrap --unshare-pid). True by default; resolved by toRunConfig from
	// SandboxConfig.PidNamespace (nil = unset = true).
	//
	// When false the sandbox shares the host PID namespace: every host
	// process is visible under /proc, and /proc/<pid>/environ of same-UID
	// processes is readable — defeating the clearenv secret hygiene. Only
	// disable as an escape hatch for TUI/terminal regressions on unusual
	// kernel/bwrap combinations (see SandboxConfig.PidNamespace).
	PidNamespace  bool
	Entrypoint    Entrypoint
	Git           *GitConfig // sanitization config (strip sections, overrides)
	GitConfigPath string     // path to pre-sanitized gitconfig temp file (set by git.Sanitizer)
	LogDir        string
	LogSummary    bool
	Timeout       int // seconds; 0 = no timeout

	// Noop describes which commands to block or rewrite inside the sandbox.
	Noop NoopConfig
	// Allow lists sensitive resources explicitly permitted in this sandbox.
	// See SandboxConfig.Allow for valid keys.
	Allow []string
	// HomeMode is the filesystem model applied to $HOME, as declared in
	// [sandbox] home. Empty means HomeHostRO. See SandboxConfig.Home.
	HomeMode string
	// HomeAllow are the expanded host paths re-exposed read-only inside the
	// isolated home. Only consulted when HomeMode is HomeIsolated; entries
	// that do not exist on the host are skipped by the isolator.
	HomeAllow []string
	// Capabilities lists the named tool integrations active for this run.
	// Populated from Profile.Capabilities; inherited via extends.
	Capabilities []string
	// CgroupManager is the cgroup manager rootless container runtimes should
	// use inside the sandbox, as declared in [sandbox] cgroup_manager.
	// Empty means "auto" (see SandboxConfig.CgroupManager). Only consulted
	// when "nested-user-ns" is in Allow.
	CgroupManager string
	// ContainersConfPath is the path to the generated containers.conf override
	// injected into the sandbox for rootless podman. Empty when no override is
	// active. Set by cmd_run.go / cmd_verify.go via applyContainersConf().
	ContainersConfPath string
	// ShimDir is the path to the directory containing shim scripts.
	// Empty if no noop config is active. Set by cmd_run.go after shim.Builder.Build().
	ShimDir string
	// Workdir is the directory to chdir into inside the sandbox after mounting.
	// Empty means no chdir (process inherits the caller's working directory).
	Workdir string
	// VerifyCustomChecks holds user-defined checks declared in [verify.custom].
	VerifyCustomChecks []CustomCheck
	// Experimental is true when the profile is marked experimental = true.
	// inner run refuses to start when this is set.
	Experimental bool
	// Limits are the effective resource caps for this run, resolved from the
	// priority chain: CLI flag > profile [sandbox.limits] > GlobalConfig
	// [default_limits] > auto-detection. A zero-value field means no cap.
	Limits ResourceLimits
	// WorkspacesPath is the host directory used to pre-create workspace dirs.
	WorkspacesPath string
	// WorkspaceDests are the resolved mount dest paths that were produced by
	// expanding the ${workspaces_path} token. The workspace manager creates
	// these directories on the host before bwrap starts and removes them after.
	WorkspaceDests []string
	// AutoConfirm skips interactive "press Enter to continue" prompts that
	// inner shows before starting the sandbox (e.g. the OS keyring unlock
	// message for the claude capability). Set by --yes on the CLI.
	AutoConfirm bool
}

// HomeIsolated reports whether this run replaces $HOME with an empty tmpfs
// (allowlist model) instead of exposing the host home read-only (denylist).
func (c RunConfig) HomeIsolated() bool {
	return c.HomeMode == HomeIsolated
}

// ReexposedInHome reports whether path is put back inside an isolated home by
// something this run declares: a HomeAllow entry, or a mount destination that
// is path itself or one of its ancestors. tmpfs mounts do not count — they
// empty a subtree, they never bring host content back.
//
// Two callers rely on it: the isolator, to decide whether a sensitive resource
// still needs its hide mount (a profile mounting ~/.cargo also carries
// ~/.cargo/credentials), and `inner run`, to warn when the entrypoint binary
// itself is left outside the allowlist.
func (c RunConfig) ReexposedInHome(path string) bool {
	covers := func(prefix string) bool {
		return prefix != "" && (path == prefix || strings.HasPrefix(path, prefix+"/"))
	}
	for _, entry := range c.HomeAllow {
		if covers(entry) {
			return true
		}
	}
	for _, m := range c.Mounts {
		if m.Mode == "tmpfs" {
			continue
		}
		if covers(m.Dest) {
			return true
		}
	}
	return false
}

// Mount describes a single filesystem bind mount.
type Mount struct {
	Src  string
	Dest string
	Mode string // "ro" or "rw"
}

// Entrypoint describes the command to run inside the sandbox.
type Entrypoint struct {
	Cmd         string
	Args        []string
	Interactive bool
	// TUI marks the entrypoint as a TUI application that probes terminal
	// capabilities during initialisation. See EntrypointConfig.TUI.
	TUI bool
	// CursorFix selects a cursor-repair strategy. See EntrypointConfig.CursorFix.
	// Values: "" (off), "newlines", "shell".
	CursorFix string
	// History is a list of commands pre-loaded into the shell history.
	// See EntrypointConfig.History.
	History []string
}
