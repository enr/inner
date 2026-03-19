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
	Noop          NoopConfig            `toml:"noop"`
	Verify        VerifyConfig          `toml:"verify"`
}

// ValidAllowKeys is the exhaustive set of keys accepted in [sandbox].allow.
var ValidAllowKeys = []string{
	"ssh-keys", "git-credentials", "gpg-keys",
	"docker-socket", "podman-socket", "nested-user-ns", "netrc",
}

// SandboxConfig controls high-level sandbox capabilities.
type SandboxConfig struct {
	Network   bool     `toml:"network"`
	Clipboard bool     `toml:"clipboard"`
	// Allow lists sensitive resources that are normally hidden but explicitly
	// permitted in this sandbox. Valid keys are listed in ValidAllowKeys.
	Allow []string `toml:"allow"`
}

// NoopConfig controls command shimming inside the sandbox.
// Block replaces a binary with a script that prints an error and exits 1.
// Rewrite replaces a binary with a script that delegates to another command.
// A user-declared [noop] section replaces the built-in defaults entirely.
type NoopConfig struct {
	Block   []string          `toml:"block"`
	Rewrite map[string]string `toml:"rewrite"`
}

// VerifyConfig holds custom sandbox verification checks declared in the profile.
type VerifyConfig struct {
	Custom VerifyCustomConfig `toml:"custom"`
}

// VerifyCustomConfig is the [verify.custom] sub-table.
type VerifyCustomConfig struct {
	Checks []CustomCheck `toml:"checks"`
}

// CustomCheck is a user-defined sandbox check executed by inner verify.
// Cmd is a shell expression; exit 0 means pass, non-zero means fail.
// Severity is one of "critical", "high", "medium".
type CustomCheck struct {
	Name     string `toml:"name"`
	Cmd      string `toml:"cmd"`
	Severity string `toml:"severity"`
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
