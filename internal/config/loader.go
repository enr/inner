package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Loader loads and merges configuration from the XDG-style config hierarchy.
type Loader struct {
	Dir       string // user config directory, e.g. ~/.config/inner
	SystemDir string // system config directory, e.g. /etc/inner; empty = skip
	WorkDir   string // current working directory for project config lookup; empty = no project config
}

// DefaultLoader returns a Loader pointing at the default config directories.
func DefaultLoader() (*Loader, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &Loader{
		Dir:       filepath.Join(home, ".config", "inner"),
		SystemDir: "/etc/inner",
	}, nil
}

// NewLoader returns a Loader with an explicit config directory (useful in tests).
func NewLoader(dir string) *Loader {
	return &Loader{Dir: dir}
}

// NewLoaderWithWorkDir returns a Loader with an explicit config directory and working directory.
func NewLoaderWithWorkDir(dir, workDir string) *Loader {
	return &Loader{Dir: dir, WorkDir: workDir}
}

// GlobalConfigPath returns the path to the user-level config file.
func (l *Loader) GlobalConfigPath() string {
	return filepath.Join(l.Dir, "config.toml")
}

// SystemConfigPath returns the path to the system-level config file.
// Returns empty string if SystemDir is not set.
func (l *Loader) SystemConfigPath() string {
	if l.SystemDir == "" {
		return ""
	}
	return filepath.Join(l.SystemDir, "config.toml")
}

// LocalConfigPath returns the path to the primary committed project config file.
// Returns empty string if WorkDir is not set.
func (l *Loader) LocalConfigPath() string {
	if l.WorkDir == "" {
		return ""
	}
	return filepath.Join(l.WorkDir, ".config", "inner.toml")
}

// LocalProfilesDir returns the path to the project-level profiles directory.
// Returns empty string if WorkDir is not set.
func (l *Loader) LocalProfilesDir() string {
	if l.WorkDir == "" {
		return ""
	}
	return filepath.Join(l.WorkDir, ".config", "inner", "profiles")
}

// ValidateProfileName rejects names that could escape the profiles directory
// via path-traversal sequences.
func ValidateProfileName(name string) error {
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid profile name %q: must not contain path separators", name)
	}
	if name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("invalid profile name %q: must not contain '..'", name)
	}
	return nil
}

// ProfilePath returns the path to a named profile file.
func (l *Loader) ProfilePath(name string) string {
	return filepath.Join(l.Dir, "profiles", name+".toml")
}

// LocalProfilePath returns the path to a named profile file in the project-level profiles directory.
// Returns empty string if WorkDir is not set.
func (l *Loader) LocalProfilePath(name string) string {
	if l.WorkDir == "" {
		return ""
	}
	return filepath.Join(l.WorkDir, ".config", "inner", "profiles", name+".toml")
}

// ProfilesDir returns the path to the profiles directory.
func (l *Loader) ProfilesDir() string {
	return filepath.Join(l.Dir, "profiles")
}

// ProfileNames returns the names of all available profiles.
// Local profiles (WorkDir/.config/inner/profiles) are listed first and shadow global ones.
func (l *Loader) ProfileNames() []string {
	seen := make(map[string]bool)
	var names []string

	addFrom := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".toml")
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}

	if localDir := l.LocalProfilesDir(); localDir != "" {
		addFrom(localDir)
	}
	addFrom(l.ProfilesDir())
	return names
}

// loadGlobalFrom reads a GlobalConfig from an explicit path.
// Returns nil (no error) if the file does not exist.
func (l *Loader) loadGlobalFrom(path string) (*GlobalConfig, error) {
	cfg := &GlobalConfig{}
	_, err := toml.DecodeFile(path, cfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	return cfg, nil
}

// pathAncestors returns the chain of directories from the filesystem root to
// absPath (inclusive), ordered root-first.
func pathAncestors(absPath string) []string {
	clean := filepath.Clean(absPath)
	var chain []string
	for {
		chain = append([]string{clean}, chain...)
		parent := filepath.Dir(clean)
		if parent == clean {
			break
		}
		clean = parent
	}
	return chain
}

// collectProjectConfigFiles returns the project-level config file paths that
// exist on disk, walking from the filesystem root to workDir (inclusive).
// Within each directory files are ordered lowest→highest precedence:
//
//	.config/inner.toml, .config/inner/config.toml,
//	.config/inner.local.toml, .config/inner/config.local.toml,
//	inner.toml, inner.local.toml
func collectProjectConfigFiles(workDir string) []string {
	dirs := pathAncestors(workDir)
	var files []string
	for _, dir := range dirs {
		for _, rel := range []string{
			filepath.Join(".config", "inner.toml"),
			filepath.Join(".config", "inner", "config.toml"),
			filepath.Join(".config", "inner.local.toml"),
			filepath.Join(".config", "inner", "config.local.toml"),
			"inner.toml",
			"inner.local.toml",
		} {
			p := filepath.Join(dir, rel)
			if _, err := os.Stat(p); err == nil {
				files = append(files, p)
			}
		}
	}
	return files
}

// LoadGlobal reads the user-level config file (~/.config/inner/config.toml).
// Returns an empty GlobalConfig (not an error) if the file does not exist.
func (l *Loader) LoadGlobal() (*GlobalConfig, error) {
	cfg, err := l.loadGlobalFrom(l.GlobalConfigPath())
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &GlobalConfig{}, nil
	}
	return cfg, nil
}

// LoadLocal reads and merges the project-level config files from WorkDir.
// Files are processed in order (lowest → highest precedence):
//
//	.config/inner.toml, .config/inner/config.toml,
//	.config/inner.local.toml, .config/inner/config.local.toml,
//	inner.toml, inner.local.toml
//
// Returns nil (no error) if WorkDir is not set or no files exist.
func (l *Loader) LoadLocal() (*GlobalConfig, error) {
	if l.WorkDir == "" {
		return nil, nil
	}
	names := []string{
		filepath.Join(".config", "inner.toml"),
		filepath.Join(".config", "inner", "config.toml"),
		filepath.Join(".config", "inner.local.toml"),
		filepath.Join(".config", "inner", "config.local.toml"),
		"inner.toml",
		"inner.local.toml",
	}
	var result *GlobalConfig
	for _, name := range names {
		overlay, err := l.loadGlobalFrom(filepath.Join(l.WorkDir, name))
		if err != nil {
			return nil, err
		}
		if overlay == nil {
			continue
		}
		if result == nil {
			result = overlay
		} else {
			result = mergeGlobalConfig(result, overlay)
		}
	}
	return result, nil
}

// LoadEffectiveGlobal loads and merges all config sources in precedence order:
//  1. System config (/etc/inner/config.toml)
//  2. User config (~/.config/inner/config.toml)
//  3. Project config files (root → WorkDir traversal, 4 files per directory)
//
// Exported so that commands outside this package (e.g. cmd_config.go) can
// display the resolved settings without building a full RunConfig.
func (l *Loader) LoadEffectiveGlobal() (*GlobalConfig, error) {
	return l.loadEffectiveGlobal()
}

// loadEffectiveGlobal is the unexported implementation used internally.
func (l *Loader) loadEffectiveGlobal() (*GlobalConfig, error) {
	cfg := &GlobalConfig{}

	if l.SystemDir != "" {
		sys, err := l.loadGlobalFrom(filepath.Join(l.SystemDir, "config.toml"))
		if err != nil {
			return nil, err
		}
		if sys != nil {
			cfg = mergeGlobalConfig(cfg, sys)
		}
	}

	user, err := l.LoadGlobal()
	if err != nil {
		return nil, err
	}
	cfg = mergeGlobalConfig(cfg, user)

	if l.WorkDir != "" {
		for _, path := range collectProjectConfigFiles(l.WorkDir) {
			proj, err := l.loadGlobalFrom(path)
			if err != nil {
				return nil, err
			}
			if proj != nil {
				cfg = mergeGlobalConfig(cfg, proj)
			}
		}
	}

	return cfg, nil
}

// ProjectConfigFiles returns the project-level config file paths that exist on
// disk, in load order (lowest → highest precedence). Returns nil if WorkDir is
// not set.
func (l *Loader) ProjectConfigFiles() []string {
	if l.WorkDir == "" {
		return nil
	}
	return collectProjectConfigFiles(l.WorkDir)
}

// LoadProfile reads a named profile file.
// Local profile (WorkDir/.config/inner/profiles) takes precedence over global (~/.config/inner/profiles).
// Returns an error if the profile is not found in either location.
func (l *Loader) LoadProfile(name string) (*Profile, error) {
	if err := ValidateProfileName(name); err != nil {
		return nil, err
	}
	// Local profile takes precedence over global.
	if localPath := l.LocalProfilePath(name); localPath != "" {
		if _, err := os.Stat(localPath); err == nil {
			abs, err := filepath.Abs(localPath)
			if err != nil {
				abs = localPath
			}
			p, err := l.loadProfilePath(abs, nil)
			if err != nil {
				return nil, fmt.Errorf("reading local profile %q: %w", name, err)
			}
			return p, nil
		}
	}
	// Fall back to global profiles directory.
	path := l.ProfilePath(name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("profile %q not found at %s", name, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	p, err := l.loadProfilePath(abs, nil)
	if err != nil {
		return nil, fmt.Errorf("reading profile %q: %w", name, err)
	}
	return p, nil
}

// LoadProfileFromPath reads a profile from an explicit file path.
func (l *Loader) LoadProfileFromPath(path string) (*Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return nil, fmt.Errorf("profile file not found: %s", abs)
	}
	p, err := l.loadProfilePath(abs, nil)
	if err != nil {
		return nil, fmt.Errorf("reading profile from %s: %w", path, err)
	}
	return p, nil
}

// ResolveProfilePath returns the actual file path for a name-or-path value.
// If nameOrPath is an existing file it is resolved to an absolute path.
// Otherwise it checks the local profiles directory first, then falls back to global.
// ~ is expanded before the file-existence check.
func (l *Loader) ResolveProfilePath(nameOrPath string) string {
	expanded := ExpandPath(nameOrPath)
	if _, err := os.Stat(expanded); err == nil {
		if abs, err := filepath.Abs(expanded); err == nil {
			return abs
		}
		return expanded
	}
	// Local profile takes precedence.
	if localPath := l.LocalProfilePath(nameOrPath); localPath != "" {
		if _, err := os.Stat(localPath); err == nil {
			if abs, err := filepath.Abs(localPath); err == nil {
				return abs
			}
			return localPath
		}
	}
	return l.ProfilePath(nameOrPath)
}

// LoadProfileAuto loads a profile given either a name or a file path.
// If nameOrPath points to an existing file (after ~ expansion) it is loaded
// directly; otherwise it is treated as a profile name looked up in the
// profiles directory.
func (l *Loader) LoadProfileAuto(nameOrPath string) (*Profile, error) {
	expanded := ExpandPath(nameOrPath)
	if _, err := os.Stat(expanded); err == nil {
		abs, err := filepath.Abs(expanded)
		if err != nil {
			abs = expanded
		}
		return l.loadProfilePath(abs, nil)
	}
	// If the value looks like a path (contains separators), also try resolving
	// it relative to WorkDir before falling through to bare-name lookup.
	if strings.ContainsAny(expanded, "/\\") {
		if l.WorkDir != "" {
			candidate := filepath.Join(l.WorkDir, expanded)
			if _, err := os.Stat(candidate); err == nil {
				abs, err := filepath.Abs(candidate)
				if err != nil {
					abs = candidate
				}
				return l.loadProfilePath(abs, nil)
			}
		}
		return nil, fmt.Errorf("profile file not found: %s", nameOrPath)
	}
	return l.LoadProfile(nameOrPath)
}

// Aliases returns the merged alias map from global and local config.
// Local aliases take precedence over global ones on key conflict.
// The ${workspaces_path} token in alias values is expanded to the effective
// workspaces_path (if configured). Returns nil if no aliases are defined.
func (l *Loader) Aliases() (map[string]string, error) {
	g, err := l.loadEffectiveGlobal()
	if err != nil {
		return nil, err
	}
	if len(g.Aliases) == 0 {
		return nil, nil
	}
	wp := ExpandPath(g.WorkspacesPath)
	if wp == "" {
		return g.Aliases, nil
	}
	// Check whether any alias value uses the token before allocating a new map.
	hasToken := false
	for _, v := range g.Aliases {
		if strings.Contains(v, "${workspaces_path}") {
			hasToken = true
			break
		}
	}
	if !hasToken {
		return g.Aliases, nil
	}
	expanded := make(map[string]string, len(g.Aliases))
	for k, v := range g.Aliases {
		expanded[k] = strings.ReplaceAll(v, "${workspaces_path}", wp)
	}
	return expanded, nil
}

// DefaultProfileName returns the effective default profile name.
// It reads DefaultProfile from the merged global+local config; if unset, returns "default".
func (l *Loader) DefaultProfileName() string {
	g, err := l.loadEffectiveGlobal()
	if err != nil || g.DefaultProfile == "" {
		return "default"
	}
	return g.DefaultProfile
}

// Build loads global config and the named profile, then produces a RunConfig.
// profileName defaults to GlobalConfig.DefaultProfile (or "default") if empty.
func (l *Loader) Build(profileName string) (*RunConfig, error) {
	global, err := l.loadEffectiveGlobal()
	if err != nil {
		return nil, err
	}
	if profileName == "" {
		profileName = global.DefaultProfile
	}
	if profileName == "" {
		profileName = "default"
	}
	profile, err := l.LoadProfileAuto(profileName)
	if err != nil {
		return nil, err
	}
	return toRunConfig(global, profile, l.WorkDir)
}

// toRunConfig converts a loaded Profile (and GlobalConfig) into a RunConfig,
// applying path expansion. Returns an error if a mount dest uses the
// ${workspaces_path} token but no workspaces_path is configured.
//
// workDir is the directory from which inner was invoked. It is substituted for
// the ${workdir} token in mount source paths, making local profiles portable
// across machines.
func toRunConfig(global *GlobalConfig, p *Profile, workDir string) (*RunConfig, error) {
	// Expand ~ and ${UID} in env.set values (e.g. DOCKER_HOST with socket paths).
	expandedEnv := p.Env
	if len(p.Env.Set) > 0 {
		expandedEnv.Set = make(map[string]string, len(p.Env.Set))
		for k, v := range p.Env.Set {
			expandedEnv.Set[k] = ExpandPath(v)
		}
	}
	// Expand ~ and ${VAR} in path_prepend entries (e.g. a JDK bin dir under ~/.sdkman).
	if len(p.Env.PathPrepend) > 0 {
		expandedEnv.PathPrepend = make([]string, len(p.Env.PathPrepend))
		for i, v := range p.Env.PathPrepend {
			expandedEnv.PathPrepend[i] = ExpandPath(v)
		}
	}

	// Resolve effective workspaces_path: profile takes precedence over global.
	workspacesPath := p.WorkspacesPath
	if workspacesPath == "" {
		workspacesPath = global.WorkspacesPath
	}
	workspacesPath = ExpandPath(workspacesPath)

	// Network model: resolved once here so every consumer reads the same
	// answer. An unknown value is refused rather than silently falling back to
	// a default — a typo ("allowist", "none") must not quietly produce a
	// sandbox with different network reach than the profile claims. The
	// validator reports the same error with more context, but it runs
	// best-effort in `inner run`, so the authoritative check lives here.
	networkMode := ResolveNetworkMode(p.Sandbox)
	if !slices.Contains(ValidNetworkModes, networkMode) {
		return nil, fmt.Errorf("invalid [sandbox] network_mode %q (valid values: %v)", networkMode, ValidNetworkModes)
	}

	// A "@name" group reference that names no group is refused for the same
	// reason an unknown mode is: it would expand to nothing and fail later as a
	// connection error inside the sandbox, which says nothing about the typo
	// that caused it. Authoritative here — the validator reports it too, but it
	// runs best-effort in `inner run`.
	if unknown := UnknownNetworkGroups(p.Sandbox.NetworkAllow, p.Sandbox.NetworkDeny); len(unknown) > 0 {
		return nil, fmt.Errorf("unknown network group %v in [sandbox] network_allow/network_deny (known groups: %v)",
			unknown, NetworkGroupNames())
	}

	// The allow list is resolved here, not inside a capability's Apply: dry-run,
	// the validator and `profile show --explain` must all see the effective
	// list without executing anything.
	networkAllow, networkAllowOrigin := ResolveNetworkAllow(p.Sandbox, p.Capabilities)

	cfg := &RunConfig{
		Name:         p.Name,
		NetworkMode:  networkMode,
		Network:      networkMode != NetworkOff,
		NetworkAllow: networkAllow,
		// Expanded like the allow list: the proxy matches patterns, and a bare
		// "@github" would match nothing at all — a deny that silently denies
		// nothing is the most dangerous shape this file can produce.
		NetworkDeny:        ExpandNetworkGroups(p.Sandbox.NetworkDeny),
		NetworkAllowOrigin: networkAllowOrigin,
		// PID namespace isolation defaults to ON: a nil (unset) value means
		// true; only an explicit pid_namespace = false in the profile
		// disables it. See SandboxConfig.PidNamespace for the rationale.
		PidNamespace:       p.Sandbox.PidNamespace == nil || *p.Sandbox.PidNamespace,
		Clipboard:          p.Sandbox.Clipboard,
		Env:                expandedEnv,
		Git:                p.Git,
		LogSummary:         p.Output.Summary,
		Timeout:            p.Output.TimeoutSeconds,
		Noop:               p.Noop,
		Allow:              p.Sandbox.Allow,
		HomeMode:           p.Sandbox.Home,
		CgroupManager:      p.Sandbox.CgroupManager,
		Capabilities:       p.Capabilities,
		VerifyCustomChecks: p.Verify.Custom.Checks,
		Experimental:       p.Experimental,
		WorkspacesPath:     workspacesPath,
	}

	// home_allow: expand ~ / ${VAR} so the isolator receives absolute host
	// paths. Existence is checked by the isolator (missing entries are skipped)
	// so a profile can list toolchain paths that only exist on some machines.
	if len(p.Sandbox.HomeAllow) > 0 {
		cfg.HomeAllow = make([]string, 0, len(p.Sandbox.HomeAllow))
		for _, entry := range p.Sandbox.HomeAllow {
			if entry == "" {
				continue
			}
			cfg.HomeAllow = append(cfg.HomeAllow, ExpandPath(entry))
		}
	}

	// Mounts: expand paths, default mode to "ro".
	// If dest contains ${workspaces_path}, substitute it and record the path
	// so the workspace manager can pre-create the directory on the host.
	// If src contains ${workdir}, substitute it with the invocation directory
	// so local profiles are portable across machines.
	const token = "${workspaces_path}"
	const workdirToken = "${workdir}"
	for src, entry := range p.Mounts {
		mode := entry.Mode
		if mode == "" {
			mode = "ro"
		}
		if strings.Contains(src, workdirToken) {
			src = strings.ReplaceAll(src, workdirToken, workDir)
		}
		dest := entry.Dest
		if strings.Contains(dest, token) {
			if workspacesPath == "" {
				return nil, fmt.Errorf("mount dest %q uses ${workspaces_path} but workspaces_path is not configured", dest)
			}
			dest = strings.ReplaceAll(dest, token, workspacesPath)
			dest = ExpandPath(dest)
			cfg.WorkspaceDests = append(cfg.WorkspaceDests, dest)
		} else {
			dest = ExpandPath(dest)
		}
		cfg.Mounts = append(cfg.Mounts, Mount{
			Src:  ExpandPath(src),
			Dest: dest,
			Mode: mode,
		})
	}

	// Entrypoint.
	cfg.Entrypoint = Entrypoint{
		Cmd:         p.Entrypoint.Cmd,
		Args:        p.Entrypoint.Args,
		Interactive: p.Entrypoint.Interactive,
		TUI:         p.Entrypoint.TUI,
		CursorFix:   p.Entrypoint.CursorFix,
		History:     p.Entrypoint.History,
	}

	// Workdir from profile (supports ${workspaces_path} token).
	if p.Entrypoint.Workdir != "" {
		wd := p.Entrypoint.Workdir
		if strings.Contains(wd, token) {
			if workspacesPath == "" {
				return nil, fmt.Errorf("entrypoint.workdir %q uses ${workspaces_path} but workspaces_path is not configured", wd)
			}
			wd = strings.ReplaceAll(wd, token, workspacesPath)
		}
		cfg.Workdir = ExpandPath(wd)
	}

	// Log directory: profile takes precedence over global.
	logDir := p.Output.Log
	if logDir == "" {
		logDir = global.LogDir
	}
	cfg.LogDir = ExpandPath(logDir)

	// Resource limits: resolved from the priority chain
	// auto-detect < global DefaultLimits < profile [sandbox.limits].
	// CLI flag overrides happen later in applyOverrides.
	cfg.Limits = resolveResourceLimits(global, p)

	return cfg, nil
}

// ResolveResourceLimits builds the effective ResourceLimits for a run.
// Priority (lowest → highest): auto-detection → GlobalConfig.DefaultLimits
// → Profile [sandbox.limits]. CLI flags are applied later in applyOverrides.
// Exported so callers outside the package (e.g. cmd_config.go) can display
// the resolved limits without executing a run.
func ResolveResourceLimits(global *GlobalConfig, p *Profile) ResourceLimits {
	return resolveResourceLimits(global, p)
}

// resolveResourceLimits is the unexported implementation.
func resolveResourceLimits(global *GlobalConfig, p *Profile) ResourceLimits {
	result := AutoLimits()

	if g := global.DefaultLimits; g != nil {
		if g.Memory != "" {
			result.Memory = g.Memory
		}
		if g.CPU != "" {
			result.CPU = g.CPU
		}
		if g.Pids > 0 {
			result.Pids = g.Pids
		}
	}

	if l := p.Sandbox.Limits; l != nil {
		if l.Memory != "" {
			result.Memory = l.Memory
		}
		if l.CPU != "" {
			result.CPU = l.CPU
		}
		if l.Pids > 0 {
			result.Pids = l.Pids
		}
	}

	return result
}

// loadRaw decodes a profile TOML file without resolving extends.
// It returns the profile, the TOML metadata (used to detect which keys were
// explicitly set), and any error.
func (l *Loader) loadRaw(path string) (*Profile, toml.MetaData, error) {
	p := &Profile{}
	meta, err := toml.DecodeFile(path, p)
	if err != nil {
		return nil, meta, err
	}
	// [sandbox] network and network_mode describe one decision through two
	// fields. Make them agree here, at the only place a profile enters the
	// process, so no struct with a contradictory pair ever exists: a profile
	// declaring only network_mode = "full" would otherwise carry Network=false,
	// and the next reader to reach for the bool would get the wrong answer.
	if p.Sandbox.NetworkMode != "" {
		p.Sandbox.Network = p.Sandbox.NetworkMode != NetworkOff
	} else {
		p.Sandbox.NetworkMode = NetworkModeFromBool(p.Sandbox.Network)
	}
	return p, meta, nil
}

// resolveExtendsPath converts an extends= value to an absolute file path.
// Values that look like paths (contain '/', start with '~') are expanded;
// bare names are looked up in the profiles directory.
// Returns "" for bare names that fail profile-name validation.
func (l *Loader) resolveExtendsPath(val string) string {
	if filepath.IsAbs(val) || strings.ContainsRune(val, '/') || strings.HasPrefix(val, "~") {
		return ExpandPath(val)
	}
	if ValidateProfileName(val) != nil {
		return ""
	}
	return l.ProfilePath(val)
}

// loadProfilePath loads a profile from an absolute path, recursively resolving
// any extends chain. visited tracks paths already in the current chain so that
// cycles are detected and reported as errors.
func (l *Loader) loadProfilePath(absPath string, visited map[string]bool) (*Profile, error) {
	// Canonicalize via symlinks so that two different path strings pointing to
	// the same file are treated as one entry by the cycle detector.
	if canonical, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = canonical
	}

	if visited[absPath] {
		return nil, fmt.Errorf("extends cycle detected at %q", absPath)
	}

	p, meta, err := l.loadRaw(absPath)
	if err != nil {
		return nil, err
	}

	// Back-fill name from filename if not set in the file.
	if p.Name == "" {
		base := filepath.Base(absPath)
		p.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	if p.Extends == "" {
		return p, nil
	}

	// Resolve the base profile path and load it recursively.
	basePath := l.resolveExtendsPath(p.Extends)
	if basePath == "" {
		return nil, fmt.Errorf("invalid extends value %q: bare name must not contain path separators or '..'", p.Extends)
	}
	absBasePath, err := filepath.Abs(basePath)
	if err != nil {
		absBasePath = basePath
	}

	// Add the current path to visited before recursing.
	newVisited := make(map[string]bool, len(visited)+1)
	for k := range visited {
		newVisited[k] = true
	}
	newVisited[absPath] = true

	baseProfile, err := l.loadProfilePath(absBasePath, newVisited)
	if err != nil {
		return nil, fmt.Errorf("extends %q: %w", p.Extends, err)
	}

	return mergeProfiles(baseProfile, p, meta), nil
}
