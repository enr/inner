package config

// RunConfig is the backend-agnostic representation of a sandbox run.
// It speaks in terms of intent, never backend-specific flags.
// Produced by the Loader; consumed by the Isolator.
type RunConfig struct {
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
}
