package config

import "path/filepath"

// Profile represents a loaded .toml profile file from ~/.config/inner/profiles/<name>.toml.
type Profile struct {
	SchemaVersion string `toml:"schema_version"`
	Name          string `toml:"name"`
	Description   string `toml:"description"`
	// Extends names a base profile to inherit from. The current profile is
	// merged on top: scalars override, slices are unioned, maps are merged.
	Extends string `toml:"extends"`
	// WorkspacesPath overrides the global workspaces_path for this profile.
	// When set, ${workspaces_path} in mount dest fields expands to this value.
	WorkspacesPath string `toml:"workspaces_path"`
	// Experimental marks a profile as not yet ready for use.
	// inner run refuses to start with an explicit error message.
	Experimental bool `toml:"experimental"`
	// Capabilities lists the named tool integrations to activate at runtime
	// (e.g. "claude", "gemini", "cursor"). Each name maps to a handler that
	// injects mounts, runs pre-flight checks, and provides an Explain output.
	// Valid values are listed in ValidCapabilities.
	// Inherited from base profiles via extends (union, no duplicates).
	Capabilities []string              `toml:"capabilities"`
	Sandbox      SandboxConfig         `toml:"sandbox"`
	Mounts       map[string]MountEntry `toml:"mounts"`
	Env          EnvConfig             `toml:"env"`
	Git          *GitConfig            `toml:"git"`
	Entrypoint   EntrypointConfig      `toml:"entrypoint"`
	Output       OutputConfig          `toml:"output"`
	Noop         NoopConfig            `toml:"noop"`
	Verify       VerifyConfig          `toml:"verify"`
}

// ValidAllowKeys is the exhaustive set of keys accepted in [sandbox].allow.
//
// Most keys are resource-hiding keys: listing one keeps the corresponding
// sensitive path visible inside the sandbox (see the isolator's sensitive
// table). A few keys have no hide action and only downgrade the matching
// `inner verify` check to INFO ("verify-only" keys, e.g. env-secrets,
// shims-active, network-policy); "nested-user-ns" likewise has no hide action
// (it grants caps in the namespace block). The critical host-integrity checks
// (no-root, usr-readonly) are intentionally NOT declassifiable.
var ValidAllowKeys = []string{
	"ssh-keys", "git-credentials", "gpg-keys",
	"docker-socket", "podman-socket", "nested-user-ns", "netrc",
	"aws-credentials", "gcloud-credentials", "kube-config", "azure-credentials",
	"docker-config", "npmrc", "pypirc", "cargo-credentials", "gh-config",
	"terraform-credentials", "maven-settings", "gradle-properties",
	"helm-config", "pgpass", "mysql-config",
	"password-store", "keyrings", "onepassword-config", "browser-profiles",
	// Verify-only declassification keys (no filesystem hide action).
	"env-secrets", "shims-active", "network-policy",
}

// SensitiveResource is a host resource hidden from the sandbox by default.
// Listing Key in [sandbox] allow keeps the resource visible.
//
// A key may appear on several entries when one logical secret lives in more
// than one place (e.g. "browser-profiles" covers one directory per browser):
// every entry with that key is hidden, and allowing the key un-hides all of
// them at once.
type SensitiveResource struct {
	Key  string
	Path string // absolute host path
	Dir  bool   // true → hidden with a tmpfs overlay; false → bind of /dev/null
}

// SensitiveResources returns the resources the isolator hides by default, for
// a given home directory and numeric uid. It is the single source of truth for
// the hide list: the isolator emits the mounts, the profile validator uses it
// to warn when [sandbox] home_allow re-exposes one of them, and `inner verify`
// derives one check per key from it.
//
// This is a denylist, and a denylist rots: it only protects paths someone
// thought of. `home = "isolated"` (the allowlist model) is the real answer for
// profiles that do not need the host home — this list is the floor for the
// profiles that stay on home = "host-ro". When adding a tool here, also add
// its path to the canary list in TestSensitiveResources_coverWellKnownSecrets.
func SensitiveResources(home, uid string) []SensitiveResource {
	join := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	return []SensitiveResource{
		{"ssh-keys", join(".ssh"), true},
		{"gpg-keys", join(".gnupg"), true},
		{"git-credentials", join(".git-credentials"), false},
		{"netrc", join(".netrc"), false},
		{"docker-socket", "/var/run/docker.sock", false},
		{"podman-socket", "/run/user/" + uid + "/podman/podman.sock", false},
		{"bash-history", join(".bash_history"), false},
		{"zsh-history", join(".zsh_history"), false},
		// Cloud provider credentials.
		{"aws-credentials", join(".aws"), true},
		{"gcloud-credentials", join(".config", "gcloud"), true},
		{"kube-config", join(".kube"), true},
		{"azure-credentials", join(".azure"), true},
		// Package manager / registry tokens.
		{"docker-config", join(".docker", "config.json"), false},
		{"npmrc", join(".npmrc"), false},
		{"pypirc", join(".pypirc"), false},
		{"cargo-credentials", join(".cargo", "credentials"), false},
		{"cargo-credentials", join(".cargo", "credentials.toml"), false},
		// Developer tool tokens.
		{"gh-config", join(".config", "gh"), true},
		{"terraform-credentials", join(".terraform.d"), true},
		// Only the credential files of ~/.m2 and ~/.gradle: the rest of those
		// directories is the local artifact cache, and hiding it would break
		// every offline build for no security gain.
		{"maven-settings", join(".m2", "settings.xml"), false},
		{"maven-settings", join(".m2", "settings-security.xml"), false},
		{"gradle-properties", join(".gradle", "gradle.properties"), false},
		{"helm-config", join(".config", "helm"), true},
		// Database credentials.
		{"pgpass", join(".pgpass"), false},
		{"mysql-config", join(".my.cnf"), false},
		// Password managers and secret stores.
		{"password-store", join(".password-store"), true},
		{"keyrings", join(".local", "share", "keyrings"), true},
		{"onepassword-config", join(".config", "op"), true},
		// Browser profiles: cookie jars and saved-password databases are a
		// session-hijacking primitive, not just "config".
		{"browser-profiles", join(".mozilla"), true},
		{"browser-profiles", join(".config", "google-chrome"), true},
		{"browser-profiles", join(".config", "chromium"), true},
		{"browser-profiles", join(".config", "BraveSoftware"), true},
		{"browser-profiles", join(".config", "microsoft-edge"), true},
		{"browser-profiles", join(".config", "vivaldi"), true},
		{"browser-profiles", join(".config", "opera"), true},
	}
}

// ValidCgroupManagers is the exhaustive set of values accepted in
// [sandbox] cgroup_manager. Empty (unset) means "auto"; see
// SandboxConfig.CgroupManager.
var ValidCgroupManagers = []string{"cgroupfs", "systemd"}

// Home mode values accepted in [sandbox] home. See SandboxConfig.Home.
const (
	// HomeHostRO keeps the historical behaviour: $HOME is visible read-only
	// through the root bind, minus the hard-coded sensitive paths (denylist).
	HomeHostRO = "host-ro"
	// HomeIsolated replaces $HOME with an empty tmpfs; only the paths named in
	// [sandbox] home_allow, the profile [mounts] and the workdir are re-exposed
	// (allowlist).
	HomeIsolated = "isolated"
)

// ValidHomeModes is the exhaustive set of values accepted in [sandbox] home.
// Empty (unset) means HomeHostRO.
var ValidHomeModes = []string{HomeHostRO, HomeIsolated}

// ValidCapabilities is the exhaustive set of named capabilities accepted in
// the profile capabilities field.
var ValidCapabilities = []string{"claude", "gemini", "cursor", "opencode"}

// CapabilityHostDirs maps each capability name to the host directories it
// sandboxes at runtime. Used by the validator to detect missing directories
// and conflicts with explicit profile mounts.
var CapabilityHostDirs = map[string][]string{
	"claude":   {"~/.claude"},
	"gemini":   {"~/.gemini"},
	"cursor":   {"~/.cursor", "~/.config/cursor"},
	"opencode": {"~/.config/opencode", "~/.local/share/opencode"},
}

// ResourceLimits constrains CPU, memory and process count for the sandbox.
// Empty string / 0 means "not set" (inherit from a lower-priority source or
// fall back to auto-detection). Fields use string for memory and cpu to allow
// unit suffixes; pids is an integer for clarity.
//
// Accepted formats:
//
//	Memory: "512M", "4G", "1024M" — passed to systemd MemoryMax
//	CPU:    "200%" or "2.0"       — cores as a float or a systemd-style
//	                                percentage; normalised to "N%" before use
//	Pids:   positive integer      — max processes+threads; maps to TasksMax
type ResourceLimits struct {
	Memory string `toml:"memory"`
	CPU    string `toml:"cpu"`
	Pids   int    `toml:"pids"`
}

// IsZero reports whether all limit fields are unset.
func (r ResourceLimits) IsZero() bool {
	return r.Memory == "" && r.CPU == "" && r.Pids == 0
}

// SandboxConfig controls high-level sandbox capabilities.
type SandboxConfig struct {
	// Network is the legacy on/off switch. It is still honoured, but
	// NetworkMode is the field to reach for: see ResolveNetworkMode for how
	// the two combine, and mergeProfiles for how they combine across extends.
	Network bool `toml:"network"`
	// NetworkMode selects the network model applied to the sandbox.
	// Valid values are listed in ValidNetworkModes; empty means "fall back to
	// the Network bool", which keeps every pre-existing profile working
	// unchanged (the same shape as [sandbox] home defaulting to HomeHostRO).
	//
	//   "off"       private, empty network namespace — no outbound traffic.
	//   "full"      the host network namespace — the sandbox reaches anything
	//               the host can reach. This is the exfiltration surface every
	//               agent profile currently signs up for.
	//   "allowlist" RESERVED, not implemented yet — see NetworkAllowlist.
	NetworkMode string `toml:"network_mode"`
	Clipboard   bool   `toml:"clipboard"`
	// PidNamespace controls whether the sandbox gets its own PID namespace
	// (bwrap --unshare-pid). Defaults to true (nil = unset = enabled).
	//
	// With PID isolation enabled the sandbox cannot see host processes: this
	// blocks reading /proc/<pid>/environ of same-UID host processes (which
	// would leak secrets such as AWS_* or GITHUB_TOKEN exported in other
	// shells) and prevents sending signals to host processes.
	//
	// Set pid_namespace = false ONLY as an emergency escape hatch if an
	// interactive TUI application misbehaves on a specific kernel/bwrap
	// combination. Verified on bubblewrap >= 0.9: --unshare-pid alone keeps
	// the controlling terminal (the TTY-breaking flag is --new-session,
	// which inner never passes), so TUI apps work with PID isolation on.
	PidNamespace *bool `toml:"pid_namespace"`
	// Allow lists sensitive resources that are normally hidden but explicitly
	// permitted in this sandbox. Valid keys are listed in ValidAllowKeys.
	Allow []string `toml:"allow"`
	// Home selects the filesystem model applied to the user's home directory.
	// Valid values are listed in ValidHomeModes; empty means HomeHostRO.
	//
	//   "host-ro"  (default) the host root is bind-mounted read-only, so the
	//              whole $HOME is readable inside the sandbox except the
	//              hard-coded sensitive paths hidden by the isolator. This is
	//              a DENYLIST: anything not on that list (browser profiles,
	//              ~/.config/gh, .env files, documents) stays readable.
	//
	//   "isolated" $HOME is replaced by an empty writable tmpfs. Nothing from
	//              the host home is visible unless it is re-exposed on purpose
	//              by [sandbox] home_allow, a profile [mount], a capability, or
	//              the workdir. This is an ALLOWLIST and is the recommended
	//              mode for agent profiles.
	//
	// Only $HOME inverts: system paths (/usr, /etc, /lib*, …) stay read-only
	// bind-mounted in both modes so toolchains keep working.
	Home string `toml:"home"`
	// HomeAllow lists host paths under $HOME that are re-exposed read-only
	// inside the isolated home. Entries support ~ and ${VAR} expansion.
	// Paths that do not exist on the host are skipped silently, so one shared
	// profile can list the toolchain locations of several machines.
	//
	// Ignored (with a validation warning) when home is not "isolated". For a
	// writable re-exposure use a [mounts] entry with mode "rw" / "safe-rw".
	HomeAllow []string `toml:"home_allow"`
	// NetworkAllow lists the egress destinations reachable when NetworkMode is
	// NetworkAllowlist. Entries are host patterns, not URLs:
	//
	//   "example.com"        exactly that name, on ports 443 and 80
	//   "example.com:8443"   exactly that name, on exactly that port
	//   "*.example.com"      any subdomain at any depth, but NOT the apex
	//   "10.0.0.7"           an IP literal
	//
	// A bare entry authorising only 443/80 is deliberate: without it,
	// network_allow = ["github.com"] would also authorise CONNECT github.com:22.
	//
	// This is one layer of several. It is unioned with what the profile's
	// capabilities contribute and with whatever a base profile declared —
	// see ResolveNetworkAllow. Inherited via extends (union, no duplicates).
	//
	// Ignored (with a validation warning) when network_mode is not "allowlist".
	NetworkAllow []string `toml:"network_allow"`
	// NetworkDeny subtracts from that union. It is the one valve for dropping
	// something a capability contributed — opting out of a tool's telemetry
	// endpoint without losing its API endpoint — since the layers only ever
	// add.
	//
	// Deny entries use the same pattern syntax, with one difference: a bare
	// host denies EVERY port, not just 443/80. A subtraction being broader than
	// the matching addition is the safe direction.
	//
	// Denies are evaluated per-request, before the allow list, rather than
	// being string-subtracted from it when the config is loaded: that is what
	// lets "*.internal.example.com" carve a hole out of "*.example.com", which
	// no list subtraction could express.
	NetworkDeny []string `toml:"network_deny"`
	// CgroupManager selects the cgroup manager used by rootless container
	// runtimes (podman) started INSIDE the sandbox. Only meaningful when
	// "nested-user-ns" is in Allow. Valid values are listed in
	// ValidCgroupManagers; empty means "auto".
	//
	// Auto resolves to "cgroupfs", because podman's own default ("systemd")
	// cannot work inside the sandbox: creating the transient scope goes
	// through StartTransientUnit on the user D-Bus, and polkit resolves the
	// caller via /proc in the HOST pid namespace — the sandbox has its own
	// (bwrap --unshare-pid), so allow_active never matches and the call is
	// denied. inner injects a containers.conf override (merged on top of the
	// user's files, which stay untouched) selecting cgroupfs.
	//
	// Set cgroup_manager = "systemd" to opt out of the injection entirely,
	// e.g. on a sandbox that shares the host PID namespace
	// (pid_namespace = false) and has the user systemd socket available.
	CgroupManager string `toml:"cgroup_manager"`
	// Limits sets per-run resource caps for this profile. Fields left empty
	// fall back to GlobalConfig.DefaultLimits, then auto-detection.
	Limits *ResourceLimits `toml:"limits"`
}

// NoopConfig controls command shimming inside the sandbox.
// Block replaces a binary with a script that prints an error and exits 1.
// Rewrite replaces a binary with a script that delegates to another command.
// There are no built-in noop defaults: shims are generated only from what a
// profile declares. With extends, Block lists are unioned across the chain
// and Rewrite maps are merged per key, with the child profile overriding
// the base on conflicting keys (see merge.go).
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
	Mode string `toml:"mode"` // "ro", "rw", "safe-rw", or "tmpfs"; defaults to "ro"
}

// EnvConfig describes how environment variables are handled inside the sandbox.
// Shared between Profile TOML and RunConfig.
//
// The sandbox clears the host environment by default. Set InheritAll=true (TOML:
// inherit_all = true) to opt into full host env inheritance — doing so leaks
// all host secrets (AWS_*, GITHUB_TOKEN, etc.) into the sandbox.
type EnvConfig struct {
	// Clear is kept for backward compatibility with existing profiles that set
	// clearenv = true; it has no effect on sandbox behaviour (clearing is the
	// default). Use inherit_all = true to opt into full env inheritance instead.
	Clear      bool              `toml:"clearenv"`
	InheritAll bool              `toml:"inherit_all"`
	Inherit    []string          `toml:"inherit"`
	Set        map[string]string `toml:"set"`
	// PathPrepend lists directories to prepend to PATH inside the sandbox,
	// in the given order (first entry ends up first in PATH, i.e. highest
	// priority). Use this to pin a specific toolchain (e.g. a JDK under
	// /opt/jdk/jdk-21/bin) without having to know or repeat the rest of
	// PATH. Composes with extends: see mergeProfiles for the merge order
	// (child entries take priority over the base's).
	PathPrepend []string `toml:"path_prepend"`
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
	// TUI marks the entrypoint as a TUI application built on a runtime
	// (e.g. Node.js/libuv) that probes terminal capabilities during module
	// initialisation, before the app calls setRawMode itself. When true,
	// the launcher puts the host terminal in raw mode before the child starts
	// so those early capability queries are not buffered by the line discipline.
	// Plain interactive shells (bash, zsh) must NOT set this: they configure
	// the terminal themselves and pre-raw mode breaks bracketed-paste echo.
	TUI bool `toml:"tui"`
	// CursorFix selects a strategy to repair cursor position after a TUI app
	// exits without fully restoring the terminal. Allowed values:
	//   ""         – no fix (default)
	//   "newlines" – print \r\n after inner's child exits; suitable when the
	//                entrypoint is a TUI app that doesn't need ForceRawMode.
	//   "shell"    – inject PROMPT_COMMAND so bash resets the cursor and clears
	//                stale TUI content before each prompt, and also print \r\n
	//                after inner's child (the shell) exits. Use this when the
	//                entrypoint is bash/zsh that will run TUI children.
	CursorFix string `toml:"cursor_fix"`
	// Workdir sets the default working directory inside the sandbox.
	// Overridable at runtime via --workdir / -w; if neither is set the
	// caller's cwd is used. Supports ~ expansion and ${workspaces_path}.
	Workdir string `toml:"workdir"`
	// History is a list of commands pre-loaded into the shell history so the
	// user can recall them immediately with the up-arrow key. Supported for
	// interactive bash sessions; silently ignored for other shells.
	History []string `toml:"history"`
}

// OutputConfig controls logging and summarization.
type OutputConfig struct {
	Summary        bool   `toml:"summary"`
	Log            string `toml:"log"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

// GlobalConfig represents the merged result of all loaded config files.
type GlobalConfig struct {
	LogDir         string            `toml:"log_dir"`
	DefaultProfile string            `toml:"default_profile"`
	Aliases        map[string]string `toml:"aliases"`
	// WorkspacesPath is the host directory where workspace mount-point
	// directories are pre-created before running bwrap. Required when any
	// profile mount uses the ${workspaces_path} token in its dest field.
	WorkspacesPath string `toml:"workspaces_path"`
	// DefaultLimits provides fallback resource limits applied to every run
	// unless overridden by a profile's [sandbox.limits] or a CLI flag.
	// Set in the user config (~/.config/inner/config.toml) for machine-wide
	// defaults, or in a project config (.config/inner.toml) for per-repo caps.
	DefaultLimits *ResourceLimits `toml:"default_limits"`
}
