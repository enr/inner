package config

// Profile represents a loaded .toml profile file from ~/.inner/profiles/<name>.toml.
type Profile struct {
	SchemaVersion string                `toml:"schema_version"`
	Name          string                `toml:"name"`
	Description   string                `toml:"description"`
	Sandbox       SandboxConfig         `toml:"sandbox"`
	Mounts        map[string]MountEntry `toml:"mounts"`
	Env           EnvConfig             `toml:"env"`
	Git           *GitConfig            `toml:"git"`
	Entrypoint    EntrypointConfig      `toml:"entrypoint"`
	Output        OutputConfig          `toml:"output"`
}

// SandboxConfig controls high-level sandbox capabilities.
type SandboxConfig struct {
	Network   bool `toml:"network"`
	Clipboard bool `toml:"clipboard"`
}

// MountEntry is a single entry in the [mounts] table.
// The map key is the host source path.
type MountEntry struct {
	Dest string `toml:"dest"`
	Mode string `toml:"mode"` // "ro" or "rw"; defaults to "ro"
}

// EnvConfig describes how environment variables are handled inside the sandbox.
// Shared between Profile TOML and RunConfig.
type EnvConfig struct {
	Clear   bool              `toml:"clearenv"`
	Inherit []string          `toml:"inherit"`
	Set     map[string]string `toml:"set"`
}

// GitConfig describes how the host gitconfig is sanitized before injection.
// Shared between Profile TOML and RunConfig.
type GitConfig struct {
	StripSections []string          `toml:"strip_sections"`
	Overrides     map[string]string `toml:"overrides"`
}

// EntrypointConfig describes what runs inside the sandbox.
type EntrypointConfig struct {
	Cmd         string   `toml:"cmd"`
	Args        []string `toml:"args"`
	Interactive bool     `toml:"interactive"`
}

// OutputConfig controls logging and summarization.
type OutputConfig struct {
	Summary        bool   `toml:"summary"`
	Log            string `toml:"log"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

// GlobalConfig represents ~/.inner/config.toml.
type GlobalConfig struct {
	LogDir string `toml:"log_dir"`
}
