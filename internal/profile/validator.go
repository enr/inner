package profile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

// validateHome checks [sandbox] home and [sandbox] home_allow.
//
// An unknown home mode is an error: silently falling back to the permissive
// default would turn a typo ("isolate", "isolated ") into a sandbox that is
// weaker than the profile claims.
func validateHome(r *Result, p *config.Profile) {
	mode := p.Sandbox.Home
	if mode != "" && !slices.Contains(config.ValidHomeModes, mode) {
		r.addError(fmt.Sprintf("invalid [sandbox] home %q (valid values: %v)", mode, config.ValidHomeModes))
		return
	}

	if len(p.Sandbox.HomeAllow) == 0 {
		return
	}
	if mode != config.HomeIsolated {
		// Not an error — the keys are simply inert — but never silent: a profile
		// listing paths it wants back has clearly assumed an isolated home, and
		// is instead running with the whole home readable.
		r.addWarning(fmt.Sprintf(
			`[sandbox] home_allow lists %d path(s) but home = %s: the whole home directory is readable and the list is ignored — set home = "isolated" to apply it, or remove home_allow`,
			len(p.Sandbox.HomeAllow), effectiveHomeMode(mode)))
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return // cannot reason about the paths on this machine
	}
	home = filepath.Clean(home)
	// Reuse the isolator's hide list so this warning cannot drift from it.
	sensitive := config.SensitiveResources(home, strconv.Itoa(os.Getuid()))

	for _, entry := range p.Sandbox.HomeAllow {
		if entry == "" {
			r.addError("[sandbox] home_allow contains an empty entry")
			continue
		}
		expanded := filepath.Clean(config.ExpandPath(entry))
		if config.PathCoveredBy([]string{expanded}, home) {
			// The entry is the home itself or an ancestor of it: the isolator
			// would bind the whole host home back on top of the tmpfs, leaving a
			// profile that claims isolation while providing none.
			r.addError(fmt.Sprintf(
				"[sandbox] home_allow %q (expanded: %q) puts the entire home directory back inside the sandbox, cancelling home = \"isolated\" — list only the subdirectories the run needs",
				entry, expanded))
			continue
		}
		if !strings.HasPrefix(expanded, home+"/") {
			r.addWarning(fmt.Sprintf("[sandbox] home_allow %q is outside the home directory (expanded: %q) — it is already visible through the read-only root bind; use [mounts] instead", entry, expanded))
			continue
		}
		// A home_allow entry that covers a resource the sandbox hides by default
		// does not actually re-expose it: the isolator still applies the hide
		// rule on top (that is what keeps [sandbox] allow the single switch for
		// sensitive resources). Say so, because the profile clearly expected the
		// path to be readable.
		for _, s := range sensitive {
			if s.Path == expanded || strings.HasPrefix(s.Path, expanded+"/") {
				if !slices.Contains(p.Sandbox.Allow, s.Key) {
					r.addWarning(fmt.Sprintf("[sandbox] home_allow %q covers %q, which stays hidden by the %q rule — add allow = [%q] if the run needs it", entry, s.Path, s.Key, s.Key))
				}
			}
		}
		// Deliberately NOT warned about here: an entry missing on this host.
		// The lists are meant to cover several install layouts (native, npm,
		// nvm, asdf) and the isolator skips what is absent, so warning on every
		// run would be pure noise. `inner run --dry-run` shows which entries are
		// skipped, and an entrypoint the allowlist fails to cover is reported by
		// runSandbox before the sandbox starts.
	}
}

// effectiveHomeMode renders the home mode for a user-facing message, naming the
// default explicitly so an unset key never shows up as an empty string.
func effectiveHomeMode(mode string) string {
	if mode == "" {
		return config.HomeHostRO + " (the default)"
	}
	return mode
}

// homeReexposingPrefixes returns the subtrees a profile puts back inside an
// isolated home: home_allow entries, non-tmpfs mount destinations, and the
// directories its capabilities inject at runtime. Paths are expanded and
// cleaned. Mirrors config.RunConfig.ReexposedInHome, at profile level (before
// CLI overrides and capability handlers have run).
func homeReexposingPrefixes(p *config.Profile) []string {
	var prefixes []string
	for _, entry := range p.Sandbox.HomeAllow {
		if entry != "" {
			prefixes = append(prefixes, filepath.Clean(config.ExpandPath(entry)))
		}
	}
	for _, m := range p.Mounts {
		if m.Mode == "tmpfs" || m.Dest == "" {
			continue
		}
		prefixes = append(prefixes, filepath.Clean(config.ExpandPath(m.Dest)))
	}
	for _, cap := range p.Capabilities {
		for _, dir := range config.CapabilityHostDirs[cap] {
			prefixes = append(prefixes, filepath.Clean(config.ExpandPath(dir)))
		}
	}
	return prefixes
}

// validateAllowUnderIsolatedHome reports [sandbox] allow keys that the isolated
// home silently neutralises.
//
// This is the trap when adding home = "isolated" to a profile that already had
// allow = ["ssh-keys"]: the key stops hiding ~/.ssh, but the tmpfs removed the
// directory anyway, so the run loses an access it explicitly asked for — and
// finds out through an obscure failure inside the sandbox (git push asking for
// a password, gpg unable to sign).
func validateAllowUnderIsolatedHome(r *Result, p *config.Profile) {
	if p.Sandbox.Home != config.HomeIsolated || len(p.Sandbox.Allow) == 0 {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	home = filepath.Clean(home)
	prefixes := homeReexposingPrefixes(p)

	for _, res := range config.SensitiveResources(home, strconv.Itoa(os.Getuid())) {
		if !slices.Contains(p.Sandbox.Allow, res.Key) {
			continue
		}
		if !config.PathCoveredBy([]string{home}, res.Path) {
			continue // outside the home: the mode does not affect it
		}
		if config.PathCoveredBy(prefixes, res.Path) {
			continue // something puts it back, the allow key does its job
		}
		r.addWarning(fmt.Sprintf(
			"[sandbox] allow %q has no effect under home = \"isolated\": %s is removed with the rest of the home — add that path to home_allow (or a [mounts] entry) to make it reachable, or drop the allow key",
			res.Key, res.Path))
	}
}

// validateEntrypointReachableInHome warns when home = "isolated" hides the
// entrypoint binary itself. Agent CLIs are routinely installed inside the home
// directory (~/.local/bin from a native installer, ~/.nvm or ~/.npm-global from
// npm), and the resulting failure inside the sandbox is an opaque
// "command not found", so the message names the home_allow entry that fixes it.
//
// bin is the host path the entrypoint resolves to. Its symlink target is
// checked too: native installers put a small link in ~/.local/bin pointing at a
// versioned payload elsewhere under the home, and allowlisting only the link
// leaves a dangling symlink inside the sandbox.
//
// A warning, not an error: PATH inside the sandbox can differ from the host's,
// and `inner run -m SRC:DEST` can add coverage this profile does not declare.
func validateEntrypointReachableInHome(r *Result, p *config.Profile, bin string) {
	if p.Sandbox.Home != config.HomeIsolated {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	home = filepath.Clean(home)

	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}
	candidates := []string{filepath.Clean(bin)}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		if resolved = filepath.Clean(resolved); resolved != candidates[0] {
			candidates = append(candidates, resolved)
		}
	}

	prefixes := homeReexposingPrefixes(p)
	for _, path := range candidates {
		if !config.PathCoveredBy([]string{home}, path) {
			continue // outside the home: unaffected by the mode
		}
		if config.PathCoveredBy(prefixes, path) {
			continue
		}
		r.addWarning(fmt.Sprintf(
			"entrypoint command %q resolves to %q, inside the home directory replaced by home = \"isolated\", and nothing in this profile re-exposes it — the sandbox will fail with \"command not found\"; add home_allow = [%q]",
			p.Entrypoint.Cmd, path, filepath.Dir(path)))
	}
}

// validateMountSafety reports mounts that hand the sandbox more than a
// workspace: write access to the host system, or a home directory that
// contradicts the profile's own home mode.
func validateMountSafety(r *Result, p *config.Profile) {
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(h)
	}

	// Deterministic order: the map iteration order would otherwise shuffle the
	// messages between runs.
	srcs := make([]string, 0, len(p.Mounts))
	for src := range p.Mounts {
		srcs = append(srcs, src)
	}
	slices.Sort(srcs)

	for _, src := range srcs {
		entry := p.Mounts[src]
		mode := entry.Mode
		if mode == "" {
			mode = "ro"
		}
		if mode == "tmpfs" {
			continue // an empty overlay, nothing from the host is exposed
		}
		dest := filepath.Clean(config.ExpandPath(entry.Dest))
		writable := mode == "rw" // safe-rw writes to a throwaway copy, not the host

		switch {
		case writable && config.IsFilesystemRoot(dest):
			r.addError(fmt.Sprintf(
				"mount %q has dest %q with mode \"rw\": it makes the entire host filesystem writable from inside the sandbox — mount only the directory the run needs",
				src, entry.Dest))
		case writable && config.IsSystemDir(dest):
			r.addWarning(fmt.Sprintf(
				"mount %q has dest %q with mode \"rw\": the sandbox can rewrite host system files there (binaries, configuration) and the changes persist after the run — use mode \"ro\", or narrow the dest to the subdirectory actually needed",
				src, entry.Dest))
		}

		if home == "" || !config.PathCoveredBy([]string{dest}, home) {
			continue
		}
		switch {
		case p.Sandbox.Home == config.HomeIsolated:
			r.addError(fmt.Sprintf(
				"mount %q has dest %q, which covers the home directory, but the profile sets home = \"isolated\": the mount is applied after the home tmpfs and would put the whole host home back inside the sandbox — mount only the subdirectory the run needs",
				src, entry.Dest))
		case writable:
			r.addWarning(fmt.Sprintf(
				"mount %q has dest %q with mode \"rw\": every dotfile in the home directory (.bashrc, .profile, .config/…) becomes writable inside the sandbox — a persistence vector for an agent; narrow the dest to the project directory",
				src, entry.Dest))
		}
	}
}

// validateRiskyCombinations reports settings that are individually legal but
// together remove a protection the user probably still assumes is on.
func validateRiskyCombinations(r *Result, p *config.Profile) {
	// Full environment inheritance: every host secret exported in the calling
	// shell (AWS_*, GITHUB_TOKEN, …) is handed to the sandboxed process.
	if p.Env.InheritAll {
		msg := "[env] inherit_all = true forwards the entire host environment into the sandbox, including every exported secret (AWS_*, GITHUB_TOKEN, …) — list the variables the run needs in [env] inherit instead"
		if p.Sandbox.Network {
			msg += "; with network = true those secrets can also leave the machine"
		}
		r.addWarning(msg)
		if p.Sandbox.Home == config.HomeIsolated {
			r.addWarning(`[env] inherit_all = true contradicts home = "isolated": the profile hides the home directory but still hands the sandbox every host secret present in the environment`)
		}
	}

	// Readable credentials + open network is the exfiltration setup.
	if p.Sandbox.Network {
		var creds []string
		for _, key := range p.Sandbox.Allow {
			if slices.Contains(config.CredentialAllowKeys, key) {
				creds = append(creds, key)
			}
		}
		if len(creds) > 0 {
			r.addWarning(fmt.Sprintf(
				"network = true together with allow = %v: the sandbox can read those credentials and send them anywhere — keep the allow list to what the run truly needs",
				creds))
		}
	}

	// PID namespace opt-out: documented as an emergency escape hatch.
	if p.Sandbox.PidNamespace != nil && !*p.Sandbox.PidNamespace {
		r.addWarning("[sandbox] pid_namespace = false lets the sandbox see host processes and read /proc/<pid>/environ of your other shells, which defeats the cleared environment — only keep it while working around a terminal regression")
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
	// With home = "isolated" a dest under $HOME lands inside the tmpfs, where
	// bwrap creates the mount point itself, so it is exempt too — unless a
	// home_allow entry re-binds that subtree read-only, in which case the mount
	// point must exist on the host exactly as it must under host-ro.
	homeForDest := ""
	if p.Sandbox.Home == config.HomeIsolated {
		if h, err := os.UserHomeDir(); err == nil {
			homeForDest = filepath.Clean(h)
		}
	}
	insideAllowlistedSubtree := func(dest string) bool {
		for _, entry := range p.Sandbox.HomeAllow {
			prefix := filepath.Clean(config.ExpandPath(entry))
			if prefix != "" && strings.HasPrefix(dest, prefix+"/") {
				return true
			}
		}
		return false
	}
	for _, entry := range p.Mounts {
		if strings.Contains(entry.Dest, workspacesPathToken) {
			continue
		}
		dest := config.ExpandPath(entry.Dest)
		if homeForDest != "" {
			clean := filepath.Clean(dest)
			if strings.HasPrefix(clean, homeForDest+"/") && !insideAllowlistedSubtree(clean) {
				continue
			}
		}
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

	// 3a-bis. Validate [sandbox] home / home_allow, and the coherence between the
	// home mode and the rest of the profile.
	validateHome(&r, p)
	validateAllowUnderIsolatedHome(&r, p)

	// 3a-ter. Report mounts that hand out write access to the host system or
	// contradict the declared home mode, and setting combinations that remove a
	// protection the user probably still assumes is on.
	validateMountSafety(&r, p)
	validateRiskyCombinations(&r, p)

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

	// 4. Verify entrypoint.cmd is reachable in PATH, and — under an isolated
	// home — that it is still reachable once the home tmpfs is in place.
	if p.Entrypoint.Cmd != "" {
		if bin, err := exec.LookPath(p.Entrypoint.Cmd); err != nil {
			r.addWarning(fmt.Sprintf("entrypoint command %q not found in PATH", p.Entrypoint.Cmd))
		} else {
			validateEntrypointReachableInHome(&r, p, bin)
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
