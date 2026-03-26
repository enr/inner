package config

// RunConfig is the backend-agnostic representation of a sandbox run.
// It speaks in terms of intent, never backend-specific flags.
// Produced by the Loader; consumed by the Isolator.
type RunConfig struct {
	// Name is the logical profile name (from the profile's name field, or
	// derived from the filename). It may differ from the CLI argument used
	// to select the profile (which can be a file path).
	Name          string
	Mounts        []Mount
	Env           EnvConfig
	Network       bool
	Clipboard     bool
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
	// Capabilities lists the named tool integrations active for this run.
	// Populated from Profile.Capabilities; inherited via extends.
	Capabilities []string
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
	// WorkspacesPath is the host directory used to pre-create workspace dirs.
	WorkspacesPath string
	// WorkspaceDests are the resolved mount dest paths that were produced by
	// expanding the ${workspaces_path} token. The workspace manager creates
	// these directories on the host before bwrap starts and removes them after.
	WorkspaceDests []string
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
