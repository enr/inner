package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/containers"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/git"
	"github.com/enr/inner/internal/profile"
	"github.com/enr/inner/internal/setup"
	"github.com/enr/inner/internal/shim"
	"github.com/enr/inner/internal/workspace"
	"github.com/spf13/cobra"
)

// Size thresholds for --args-file.
const (
	argsFileWarnSize = 512 * 1024      // 512 KB: print a warning
	argsFileMaxSize  = 2 * 1024 * 1024 // 2 MB: refuse to load
)

// runCLIFlags holds every flag accepted by `inner run`.
type runCLIFlags struct {
	profile       string
	workdir       string
	networkOn     bool
	networkOff    bool
	interactive   bool
	noInteractive bool
	mounts        []string // each: "src:dest[:mode]"
	env           []string // each: "KEY=VAL"
	entrypoint    string   // override entrypoint cmd (resets profile args)
	args          []string // each appended to entrypoint args (--arg, repeatable)
	argsFile      string   // path to file whose content is appended as a single arg
	prompt        string   // deprecated: use --arg
	timeout       int
	dryRun        bool
	yes           bool
	// Remote-profile trust (only meaningful when the profile is a URL).
	allowRemote bool   // consent to run the downloaded profile
	trustRemote bool   // additionally skip the hardening applied to remote profiles
	sha256      string // pin: refuse to run if the fetched bytes hash differently
	// Resource limit overrides (highest priority in the resolution chain).
	limitMemory string // e.g. "4G", "512M"
	limitCPU    string // e.g. "200%" or "2.0" (cores)
	limitPids   int    // max processes; 0 = no override
}

// ── Business logic ────────────────────────────────────────────────────────────

var fetchRunProfileURL = config.FetchURL

// runSandbox is the full pipeline for `inner run`.
// It is an App method so tests can inject a fake isolator via a.isolatorFn.
func (a *App) runSandbox(w io.Writer, flags runCLIFlags, extraArgs []string) error {
	// 1. Auto-init ~/.config/inner (idempotent, best-effort).
	if err := setup.Init(a.loader.Dir); err != nil {
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "warning")+": setup init: %v\n", err)
	}

	// 2. Load RunConfig from profile.
	profileName := flags.profile
	if profileName == "" {
		profileName = a.loader.DefaultProfileName()
	}
	// If the profile is a URL, download it to a temp file and use that path.
	// The bytes are untrusted: they are pinned (when --sha256 was passed) here,
	// then hardened and confirmed below, once the merged RunConfig shows what
	// the profile actually asks for. See remote_profile.go.
	var remote remoteSource
	if config.IsURL(profileName) {
		data, fetchErr := fetchRunProfileURL(profileName)
		if fetchErr != nil {
			return fmt.Errorf("downloading profile: %w", fetchErr)
		}
		remote = remoteSource{url: profileName, digest: sha256Hex(data)}
		if err := checkProfileDigest(flags.sha256, remote.digest, remote.url); err != nil {
			return err
		}
		tmp, tmpErr := os.CreateTemp("", "inner-profile-*.toml")
		if tmpErr != nil {
			return fmt.Errorf("creating temp profile file: %w", tmpErr)
		}
		defer os.Remove(tmp.Name())
		if _, writeErr := tmp.Write(data); writeErr != nil {
			tmp.Close()
			return fmt.Errorf("writing temp profile file: %w", writeErr)
		}
		tmp.Close()
		profileName = tmp.Name()
	} else if flags.sha256 != "" {
		return fmt.Errorf("--sha256 pins the content of a downloaded profile, but %q is a local profile", profileName)
	}
	rc, err := a.loader.Build(profileName)
	if err != nil {
		return err
	}
	if rc.Experimental {
		return fmt.Errorf("profile %q is marked experimental and is not ready for use", profileName)
	}

	// 2b. Consent gate for a downloaded profile: harden it, show what it still
	//     asks for, and require an explicit yes. Runs before any CLI override so
	//     the user's own flags are applied on top of the hardened profile.
	if remote.isRemote() {
		proceed, gateErr := gateRemoteProfile(w, os.Stdin, rc, remote, flags, stdinIsTerminal())
		if gateErr != nil {
			return gateErr
		}
		if !proceed {
			fmt.Fprintln(w, "aborted.")
			return nil
		}
	}

	// 3. Resolve effective workdir (priority: --workdir flag > profile workdir > cwd).
	// applyOverrides is a pure function (no I/O), so we resolve the cwd here.
	// workdirImplicit records that the workdir came from the cwd fallback (the
	// user never chose it); the home-coverage guard below prompts in that case.
	workdirImplicit := false
	if flags.workdir == "" {
		if rc.Workdir != "" {
			flags.workdir = rc.Workdir // profile entrypoint.workdir takes over
		} else if cwd, err := os.Getwd(); err == nil {
			flags.workdir = cwd
			workdirImplicit = true
		}
	}

	// 4. Load --args-file content and append it to extraArgs before applying overrides.
	if flags.argsFile != "" {
		content, err := loadArgsFile(w, config.ExpandPath(flags.argsFile))
		if err != nil {
			return err
		}
		extraArgs = append(extraArgs, content)
	}

	// 5. Apply CLI overrides.
	if err := applyOverrides(rc, flags, extraArgs); err != nil {
		return err
	}

	// 5a. Warn if a declared ro mount is a subpath of the workdir.
	// bwrap emits the workdir --bind rw after declared mounts (slice order),
	// so a parent-directory rw bind silently shadows a child ro-bind.
	if rc.Workdir != "" {
		for _, m := range rc.Mounts {
			if m.Mode != "ro" {
				continue
			}
			if strings.HasPrefix(m.Dest+"/", rc.Workdir+"/") {
				fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "warning")+": mount %q is declared ro but is under workdir %q — the ro constraint will not be enforced\n", m.Dest, rc.Workdir)
			}
		}
	}

	// 5a-bis. Guard the mounts added on the command line. They never go through
	// the profile validator (which only sees the file), so the same rules are
	// applied here.
	if err := guardCLIMounts(w, rc, flags.mounts); err != nil {
		return err
	}

	// 5a-ter. Guard against a workdir that hands the sandbox more than a
	// workspace: the workdir is bind-mounted read-write, so choosing the wrong
	// one silently undoes what the sandbox is for.
	proceed, err := guardWorkdir(w, rc, workdirImplicit, flags.dryRun)
	if err != nil {
		return err
	}
	if !proceed {
		fmt.Fprintln(w, "aborted.")
		return nil
	}

	// 5b. For interactive sessions, print which profile is being used.
	if rc.Entrypoint.Interactive {
		profilePath := a.loader.ResolveProfilePath(profileName)
		if home, err := os.UserHomeDir(); err == nil {
			profilePath = strings.Replace(profilePath, home, "~", 1)
		}
		fmt.Fprintf(w, "inner starting using profile %q %s\n", rc.Name, profilePath)
	}

	// 5. Resolve empty entrypoint cmd to $SHELL (same logic as the isolator).
	//    Must happen before prepareInteractiveShell so it can detect the binary.
	if rc.Entrypoint.Cmd == "" {
		rc.Entrypoint.Cmd = os.Getenv("SHELL")
		if rc.Entrypoint.Cmd == "" {
			rc.Entrypoint.Cmd = "/bin/sh"
		}
	}

	// 5. Set sandbox PS1 so the user can tell they are inside inner.
	//    Only applied if PS1 is not already explicitly set in the profile.
	if _, ok := rc.Env.Set["PS1"]; !ok {
		if rc.Env.Set == nil {
			rc.Env.Set = make(map[string]string)
		}
		rc.Env.Set["PS1"] = sandboxPS1(rc.Name)
	}

	// 5d. For shell sessions with cursor_fix = "shell", inject PROMPT_COMMAND
	//     so that bash resets the cursor and clears stale TUI content (left by
	//     e.g. claude after CTRL+C) before showing each prompt. Prepended to
	//     any existing PROMPT_COMMAND so user customisation is preserved.
	if rc.Entrypoint.CursorFix == "shell" {
		const fix = `printf '\r\n\033[J'`
		if rc.Env.Set == nil {
			rc.Env.Set = make(map[string]string)
		}
		if existing, ok := rc.Env.Set["PROMPT_COMMAND"]; ok && existing != "" {
			rc.Env.Set["PROMPT_COMMAND"] = fix + "; " + existing
		} else {
			rc.Env.Set["PROMPT_COMMAND"] = fix
		}
	}

	// 5f. For TUI sessions, inject COLUMNS and LINES from the actual host
	//     terminal so that Node.js apps (cursor-agent, claude) get the correct
	//     dimensions even when clearenv removes the parent-shell values and
	//     TIOCGWINSZ is unavailable or returns 0 inside the sandbox.
	if rc.Entrypoint.TUI {
		if cols, rows, err := term.GetSize(int(os.Stdout.Fd())); err == nil && cols > 0 {
			if rc.Env.Set == nil {
				rc.Env.Set = make(map[string]string)
			}
			if _, ok := rc.Env.Set["COLUMNS"]; !ok {
				rc.Env.Set["COLUMNS"] = strconv.Itoa(cols)
			}
			if _, ok := rc.Env.Set["LINES"]; !ok {
				rc.Env.Set["LINES"] = strconv.Itoa(rows)
			}
		}
	}

	// 6. For interactive bash: inject --init-file so PS1 survives ~/.bashrc.
	if err := prepareInteractiveShell(rc, a.loader.Dir, rc.Env.Set["PS1"]); err != nil {
		return fmt.Errorf("preparing interactive shell: %w", err)
	}

	// 7. Validate — print all issues; block on unknown allow keys or errors.
	if p, err := a.loader.LoadProfileAuto(profileName); err == nil {
		result := profile.Validate(p, a.loader.WorkDir)
		for _, issue := range result.Issues {
			var marker string
			if issue.Level == profile.LevelError {
				marker = colorizeW(w, ansiBoldRed, "[error]")
			} else {
				marker = colorizeW(w, ansiBoldYellow, "[warning]")
			}
			fmt.Fprintf(w, "%s %s\n", marker, issue.Message)
		}
		if result.HasErrors() {
			return fmt.Errorf("profile %q has errors, aborting", profileName)
		}
		for _, key := range p.Sandbox.Allow {
			if !slices.Contains(config.ValidAllowKeys, key) {
				return fmt.Errorf("profile %q: unknown allow key %q — fix the profile or remove the key", profileName, key)
			}
		}
	}

	// 8. Build shim dir from [noop] config.
	var cleanups []func() error
	if len(rc.Noop.Block) > 0 || len(rc.Noop.Rewrite) > 0 {
		shimDir, err := shim.Builder{}.Build(rc.Noop)
		if err != nil {
			return fmt.Errorf("building shim dir: %w", err)
		}
		rc.ShimDir = shimDir
		cleanups = append(cleanups, func() error { return os.RemoveAll(shimDir) })
	}

	// 8b. Generate the containers.conf override for rootless podman inside the
	//     sandbox (no-op unless nested-user-ns is allowed).
	cleanupContainers, err := applyContainersConf(rc)
	if err != nil {
		return fmt.Errorf("containers config: %w", err)
	}
	defer cleanupContainers()

	// 9. Sanitize gitconfig if configured.
	if rc.Git != nil {
		gitPath, err := git.Sanitize(rc.Git)
		if err != nil {
			return fmt.Errorf("sanitizing gitconfig: %w", err)
		}
		defer os.Remove(gitPath)
		rc.GitConfigPath = gitPath
	}

	// 10–11b. Sandbox capability directories (claude, gemini, cursor).
	cleanupCaps, err := applyCapabilities(rc)
	if err != nil {
		return err
	}
	defer cleanupCaps()

	// 11c. Apply generic safe-rw mounts (copy src → tmp, set mode to rw).
	cleanupSafe, err := applyGenericSafeMounts(rc)
	if err != nil {
		return err
	}
	defer cleanupSafe()

	// 12. Create isolator and build the sandbox command.
	iso, err := a.isolatorFn()
	if err != nil {
		return fmt.Errorf("isolator: %w", err)
	}
	cmd, err := iso.Build(*rc)
	if err != nil {
		return fmt.Errorf("building sandbox command: %w", err)
	}

	// 13. If nested user namespaces are allowed, configure bwrap's --userns-block-fd
	//     so we can call newuidmap from outside to give bwrap's namespace the full
	//     subuid range before execution starts.
	var postStart func() error
	nestedUserNs := slices.Contains(rc.Allow, "nested-user-ns")
	if nestedUserNs {
		postStart, err = prepareNestedUserNs(cmd)
		if err != nil {
			return fmt.Errorf("preparing nested user namespace: %w", err)
		}
	}

	// 13b. Wrap the bwrap command in a systemd-run scope for resource limits.
	//      Must happen AFTER prepareNestedUserNs: that function inserts
	//      bwrap-specific flags by scanning for "--" in cmd.Args and wires
	//      ExtraFiles (fds 3,4). If we wrapped before, the "--" search would
	//      match the systemd-run/bwrap separator instead. We also skip wrapping
	//      when nested-user-ns is active because systemd-run closes non-standard
	//      fds (O_CLOEXEC semantics) before exec'ing bwrap, which would sever
	//      the block/info pipe pair that prepareNestedUserNs set up.
	cmd, err = wrapWithLimits(w, cmd, rc.Limits, nestedUserNs)
	if err != nil {
		return fmt.Errorf("resource limits: %w", err)
	}

	// 14. Dry-run: print profile, effective config, and sandbox command.
	if flags.dryRun {
		printDryRun(w, a.loader.ResolveProfilePath(profileName), a.loader.GlobalConfigPath(), a.loader.LocalConfigPath(), rc, cmd.Args)
		return nil
	}

	// 14b. Pre-create workspace directories and write lock file.
	if len(rc.WorkspaceDests) > 0 {
		wm, err := workspace.Prepare(rc.WorkspacesPath, rc.WorkspaceDests, workspace.RunInfo{
			Profile: profileName,
			Command: rc.Entrypoint.Cmd,
		})
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		defer wm.Release()
	}

	// 15. Launch.
	launcher := a.launcherFn()
	result, err := launcher.Run(cmd, executor.RunOptions{
		Interactive:  rc.Entrypoint.Interactive,
		ForceRawMode: rc.Entrypoint.TUI,
		CursorFix:    rc.Entrypoint.CursorFix,
		PostStart:    postStart,
		Timeout:      rc.Timeout,
		LogDir:       rc.LogDir,
		Cleanups:     cleanups,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return exitCodeError{code: result.ExitCode}
	}
	return nil
}

// applyContainersConf generates the containers.conf override that rootless
// podman needs inside the sandbox and records it on rc. It is a no-op unless
// "nested-user-ns" is allowed (nothing runs containers inside the sandbox
// otherwise). It is also a no-op when the profile opts out with
// [sandbox] cgroup_manager = "systemd", or already sets CONTAINERS_CONF_OVERRIDE
// in [env] set — in both cases the profile has taken over the decision.
//
// Returns a cleanup that removes the generated file; the cleanup is never nil.
func applyContainersConf(rc *config.RunConfig) (func(), error) {
	noop := func() {}
	if !slices.Contains(rc.Allow, "nested-user-ns") {
		return noop, nil
	}
	if rc.CgroupManager == containers.CgroupManagerSystemd {
		return noop, nil
	}
	if _, ok := rc.Env.Set[containers.OverrideEnvVar]; ok {
		return noop, nil
	}

	manager := rc.CgroupManager
	if manager == "" {
		manager = containers.CgroupManagerCgroupfs // auto
	}
	path, err := containers.WriteOverride(manager)
	if err != nil {
		return noop, err
	}
	rc.ContainersConfPath = path
	return func() { os.RemoveAll(filepath.Dir(path)) }, nil //nolint:errcheck
}

// applyCapabilities applies the sandbox transformations for each capability
// declared in rc.Capabilities. Each registered Apply function may inject mounts
// and create temporary directories; it returns a cleanup that removes them.
// Returns a combined cleanup that removes all temporary directories.
// The validator guarantees every name in rc.Capabilities is in the registry.
func applyCapabilities(rc *config.RunConfig) (func(), error) {
	var cleanups []func()
	// push accumulates an Apply result. On error it runs all prior cleanups so
	// no temporary directories are leaked. Contract: every Apply function MUST
	// return a non-nil cleanup when err is nil; a nil cleanup with nil error
	// is a programming mistake and will panic here rather than silently corrupt
	// state later when the combined cleanup is called.
	push := func(fn func(), err error) error {
		if err != nil {
			for _, c := range cleanups {
				c()
			}
			return err
		}
		if fn == nil {
			panic("capability Apply returned nil cleanup with nil error — violates Capability.Apply contract")
		}
		cleanups = append(cleanups, fn)
		return nil
	}

	for _, name := range rc.Capabilities {
		cap := capabilityRegistry[name]
		if err := push(cap.Apply(rc)); err != nil {
			return nil, fmt.Errorf("capability %q: %w", name, err)
		}
	}

	return func() {
		for _, c := range cleanups {
			c()
		}
	}, nil
}

// applyOverrides merges CLI flags into rc.
// Pure function: no I/O, no side effects — primary target for unit tests.
func applyOverrides(rc *config.RunConfig, flags runCLIFlags, extraArgs []string) error {
	// Network (--network / --no-network). The flags move NetworkMode, not the
	// legacy bool: setting rc.Network alone would leave the two fields
	// disagreeing, and every consumer that switches on the mode (the isolator,
	// verify) would keep applying the profile's model while the dry-run output
	// claimed otherwise.
	if flags.networkOn {
		rc.NetworkMode = config.NetworkFull
	} else if flags.networkOff {
		rc.NetworkMode = config.NetworkOff
	}
	rc.Network = rc.EffectiveNetworkMode() != config.NetworkOff

	// Interactive (-i / --no-interactive).
	if flags.interactive {
		rc.Entrypoint.Interactive = true
	} else if flags.noInteractive {
		rc.Entrypoint.Interactive = false
	}

	// Timeout.
	if flags.timeout > 0 {
		rc.Timeout = flags.timeout
	}

	// --yes: skip interactive confirmation prompts.
	if flags.yes {
		rc.AutoConfirm = true
	}

	// Resource limit CLI overrides (highest priority).
	if flags.limitMemory != "" {
		rc.Limits.Memory = flags.limitMemory
	}
	if flags.limitCPU != "" {
		rc.Limits.CPU = flags.limitCPU
	}
	if flags.limitPids > 0 {
		rc.Limits.Pids = flags.limitPids
	}

	// Workdir (-w PATH) → mount at the same path rw.
	// We use src==dest because bwrap's base is --ro-bind / /, so the path
	// already exists in the sandbox; bwrap can bind-mount rw over it without
	// needing to create a new directory (which would fail on a ro root).
	if flags.workdir != "" {
		expanded := config.ExpandPath(flags.workdir)
		if abs, err := filepath.Abs(expanded); err == nil {
			expanded = abs
		}
		rc.Mounts = append(rc.Mounts, config.Mount{
			Src:  expanded,
			Dest: expanded,
			Mode: "rw",
		})
		rc.Workdir = expanded
	}

	// Additional mounts (-m SRC:DEST[:MODE]).
	for _, m := range flags.mounts {
		mount, err := parseMount(m)
		if err != nil {
			return err
		}
		rc.Mounts = append(rc.Mounts, mount)
	}

	// Env overrides (-e KEY=VAL). Values are expanded the same way as
	// profile [env] set values (~, $VAR, ${VAR}, $UID) for consistency.
	for _, e := range flags.env {
		k, v, err := parseEnvVar(e)
		if err != nil {
			return err
		}
		if rc.Env.Set == nil {
			rc.Env.Set = make(map[string]string)
		}
		rc.Env.Set[k] = config.ExpandPath(v)
	}

	// --entrypoint → replace cmd and reset profile args (same semantics as docker --entrypoint).
	if flags.entrypoint != "" {
		rc.Entrypoint.Cmd = config.ExpandPath(flags.entrypoint)
		rc.Entrypoint.Args = nil
	}

	// Extra args after -- (includes --args-file content, pre-loaded by runSandbox).
	// These come first so they form the fixed command structure (flags, subcommands);
	// --arg values follow as the variable input (prompt, positional arguments).
	rc.Entrypoint.Args = append(rc.Entrypoint.Args, extraArgs...)

	// --arg flags → each appended as a separate entrypoint arg.
	rc.Entrypoint.Args = append(rc.Entrypoint.Args, flags.args...)

	// --prompt (deprecated) → appended to entrypoint args.
	if flags.prompt != "" {
		rc.Entrypoint.Args = append(rc.Entrypoint.Args, flags.prompt)
	}

	return nil
}

// loadArgsFile reads path and returns its content as a single string to be
// appended to entrypoint args. It warns on w if the file exceeds argsFileWarnSize
// and returns an error if it exceeds argsFileMaxSize.
func loadArgsFile(w io.Writer, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("args file: %w", err)
	}
	size := info.Size()
	if size > argsFileMaxSize {
		return "", fmt.Errorf("args file %q is too large (%d bytes, limit %d bytes)", path, size, argsFileMaxSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading args file: %w", err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("args file %q contains null bytes; binary files are not supported", path)
	}
	if size > argsFileWarnSize {
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "warning")+": args file %q is large (%d bytes); consider passing the file path as a mount instead\n", path, size)
	}
	return string(data), nil
}

// workdirCoversHome reports whether mounting workdir read-write exposes the
// entire home directory: true when workdir is home itself or an ancestor of
// it (e.g. /home or /). Both paths are expected to be absolute and cleaned
// (the workdir has been through ExpandPath + filepath.Abs in applyOverrides).
func workdirCoversHome(workdir, home string) bool {
	if workdir == "" || home == "" {
		return false
	}
	// Ancestor check: home falls under workdir. Append the separator only
	// when missing so that workdir "/" ("prefix "/") still matches.
	prefix := workdir
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return workdir == home || strings.HasPrefix(home+"/", prefix)
}

// guardCLIMounts applies the dangerous-mount rules of the profile validator to
// the mounts added with -m on the command line, which the validator never sees.
//
// The specs are re-parsed here (applyOverrides has already put the parsed
// mounts on rc) so that the messages can quote what the user actually typed;
// parseMount is pure, so the second parse costs nothing and cannot diverge. A
// spec that fails to parse is skipped: applyOverrides has already rejected it.
func guardCLIMounts(w io.Writer, rc *config.RunConfig, specs []string) error {
	if len(specs) == 0 {
		return nil
	}
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(h)
	}

	for _, spec := range specs {
		m, err := parseMount(spec)
		if err != nil {
			continue
		}
		dest := filepath.Clean(m.Dest)
		writable := m.Mode == "rw"

		if writable && config.IsFilesystemRoot(dest) {
			return fmt.Errorf("refusing mount %q: mounting the filesystem root read-write makes every file on this machine writable from inside the sandbox — mount the directory the run actually needs", spec)
		}
		if home != "" && config.PathCoveredBy([]string{dest}, home) && rc.HomeIsolated() {
			return fmt.Errorf("refusing mount %q: its destination covers your home directory and the profile sets home = \"isolated\", so the mount would restore the host home on top of the isolated one — mount the specific subdirectory the run needs", spec)
		}
		switch {
		case writable && config.IsSystemDir(dest):
			fmt.Fprintf(w, "%s: mount %q writes into a host system directory — changes made there persist after the run\n",
				colorizeW(w, ansiBoldYellow, "warning"), spec)
		case writable && home != "" && config.PathCoveredBy([]string{dest}, home):
			fmt.Fprintf(w, "%s: mount %q makes every dotfile in your home directory writable inside the sandbox — a persistence vector for an agent\n",
				colorizeW(w, ansiBoldYellow, "warning"), spec)
		}
	}
	return nil
}

// guardWorkdir checks the resolved workdir before the sandbox is built and
// reports whether the run may proceed.
//
// The workdir is bind-mounted read-write at the same path, after every other
// mount, so it is the single setting that can quietly undo the sandbox:
//
//   - "/"                     → the whole host filesystem becomes writable;
//   - a system directory      → host binaries and configuration become writable,
//     and the changes survive the run;
//   - $HOME (or an ancestor)  → every dotfile becomes a persistence vector, and
//     under home = "isolated" the host home is restored
//     on top of the tmpfs, cancelling the isolation.
//
// The first and the last (under an isolated home) are refused outright: no
// invocation of an agent sandbox wants them, and a warning would be read as
// noise. The others warn, and additionally require confirmation when the user
// never chose the workdir — it came from the current directory — unless --yes
// or --dry-run was passed.
func guardWorkdir(w io.Writer, rc *config.RunConfig, implicit, dryRun bool) (proceed bool, err error) {
	if rc.Workdir == "" {
		return true, nil
	}

	if config.IsFilesystemRoot(rc.Workdir) {
		return false, fmt.Errorf("refusing to run with workdir %q: it is bind-mounted read-write, "+
			"so the sandboxed process could modify any file on this machine — the sandbox would protect nothing.\n"+
			"Pass -w PATH with the directory the run should work in", rc.Workdir)
	}

	home, homeErr := os.UserHomeDir()
	coversHome := homeErr == nil && workdirCoversHome(rc.Workdir, home)

	if coversHome && rc.HomeIsolated() {
		return false, fmt.Errorf("refusing to run with workdir %q: it covers your home directory and the profile sets home = \"isolated\".\n"+
			"The read-write workdir bind is applied after the home tmpfs, so it would restore the host home on top of the isolated one and cancel the isolation.\n"+
			"Pass -w PATH with a directory below your home (e.g. -w ~/projects/foo), or drop home = \"isolated\" from the profile", rc.Workdir)
	}

	var warning, hint string
	switch {
	case coversHome:
		warning = fmt.Sprintf("workdir %q covers your home directory — all dotfiles (.bashrc, .profile, .config/…) will be writable inside the sandbox", rc.Workdir)
		hint = "pass -w PATH to scope the mount to a project directory"
	case config.IsSystemDir(rc.Workdir):
		warning = fmt.Sprintf("workdir %q is a system directory — it is mounted read-write, so the sandboxed process can modify host files there and the changes persist after the run", rc.Workdir)
		hint = "pass -w PATH with a project directory instead"
	default:
		return true, nil
	}

	fmt.Fprintf(w, "%s: %s\n", colorizeW(w, ansiBoldYellow, "warning"), warning)
	if !implicit || dryRun || rc.AutoConfirm {
		return true, nil
	}
	fmt.Fprintf(w, "         (workdir defaulted to the current directory; %s, or --yes to skip this prompt)\n", hint)
	fmt.Fprint(w, "continue? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.ToLower(strings.TrimSpace(answer)) == "y", nil
}

// parseMount parses "src:dest[:mode]" into a Mount.
// parseMount parses "src:dest[:mode]" into a Mount. mode is one of "ro" (the
// default), "rw" or "safe-rw" — the same modes a profile's [mounts] table
// accepts, except "tmpfs": that mode has no host source (it materializes an
// empty directory at dest), so it does not fit the src:dest shape of this
// flag. Declare a tmpfs mount in the profile's [mounts] table instead.
func parseMount(s string) (config.Mount, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return config.Mount{}, fmt.Errorf("invalid mount %q: expected src:dest[:mode]", s)
	}
	mode := "ro"
	if len(parts) == 3 {
		mode = parts[2]
	}
	switch mode {
	case "ro", "rw", "safe-rw":
	case "tmpfs":
		return config.Mount{}, fmt.Errorf("invalid mount mode %q in %q: tmpfs has no host source, so it cannot be expressed as src:dest:tmpfs — declare it in the profile's [mounts] table instead", mode, s)
	default:
		return config.Mount{}, fmt.Errorf("invalid mount mode %q in %q: must be ro, rw or safe-rw", mode, s)
	}
	return config.Mount{
		Src:  config.ExpandPath(parts[0]),
		Dest: config.ExpandPath(parts[1]),
		Mode: mode,
	}, nil
}

// parseEnvVar parses "KEY=VAL" into (key, value).
func parseEnvVar(s string) (string, string, error) {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid env override %q: expected KEY=VAL", s)
	}
	return parts[0], parts[1], nil
}

// printDryRun writes a human-readable summary of what inner run would do.
func printDryRun(w io.Writer, profilePath, globalConfigPath, localConfigPath string, rc *config.RunConfig, cmdArgs []string) {
	fmt.Fprintf(w, "profile:       %s\n", rc.Name)
	fmt.Fprintf(w, "profile path:  %s\n", profilePath)
	fmt.Fprintf(w, "global config: %s\n", globalConfigPath)
	if localConfigPath != "" {
		if _, err := os.Stat(localConfigPath); err == nil {
			fmt.Fprintf(w, "local config:  %s\n", localConfigPath)
		} else {
			fmt.Fprintf(w, "local config:  %s (missing)\n", localConfigPath)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "entrypoint: %s\n", rc.Entrypoint.Cmd)
	if len(rc.Entrypoint.Args) > 0 {
		fmt.Fprintf(w, "args:       %s\n", strings.Join(rc.Entrypoint.Args, " "))
	}
	fmt.Fprintf(w, "interactive: %v\n", rc.Entrypoint.Interactive)
	if rc.Workdir != "" {
		fmt.Fprintf(w, "workdir:     %s\n", rc.Workdir)
	}
	homeMode := rc.HomeMode
	if homeMode == "" {
		homeMode = config.HomeHostRO
	}
	fmt.Fprintf(w, "home:        %s\n", homeMode)
	fmt.Fprintf(w, "network:     %s\n", rc.EffectiveNetworkMode())
	fmt.Fprintf(w, "pid-ns:      %v\n", rc.PidNamespace)
	if rc.ContainersConfPath != "" {
		fmt.Fprintf(w, "containers:  cgroup_manager override at %s\n", rc.ContainersConfPath)
	}
	fmt.Fprintf(w, "clipboard:   %v\n", rc.Clipboard)
	if rc.Timeout > 0 {
		fmt.Fprintf(w, "timeout:     %ds\n", rc.Timeout)
	} else {
		fmt.Fprintln(w, "timeout:     none")
	}
	fmt.Fprintln(w)

	if rc.HomeIsolated() && len(rc.HomeAllow) > 0 {
		fmt.Fprintln(w, "home_allow (read-only inside the isolated home):")
		for _, p := range rc.HomeAllow {
			if _, err := os.Stat(p); err != nil {
				fmt.Fprintf(w, "  %s (missing on host — skipped)\n", p)
				continue
			}
			fmt.Fprintf(w, "  %s\n", p)
		}
		fmt.Fprintln(w)
	}

	if len(rc.Mounts) > 0 {
		fmt.Fprintln(w, "mounts:")
		for _, m := range rc.Mounts {
			fmt.Fprintf(w, "  %s -> %s (%s)\n", m.Src, m.Dest, m.Mode)
		}
		fmt.Fprintln(w)
	}

	if rc.Env.InheritAll || len(rc.Env.Inherit) > 0 || len(rc.Env.Set) > 0 {
		fmt.Fprintln(w, "env:")
		if rc.Env.InheritAll {
			fmt.Fprintln(w, "  inherit_all: true  # WARNING: full host env inherited")
		}
		if len(rc.Env.Inherit) > 0 {
			fmt.Fprintf(w, "  inherit: %s\n", strings.Join(rc.Env.Inherit, ", "))
		}
		for k, v := range rc.Env.Set {
			fmt.Fprintf(w, "  %s = %s\n", k, v)
		}
		fmt.Fprintln(w)
	}

	if len(rc.Noop.Block) > 0 || len(rc.Noop.Rewrite) > 0 {
		fmt.Fprintln(w, "noop:")
		if len(rc.Noop.Block) > 0 {
			fmt.Fprintf(w, "  block: %s\n", strings.Join(rc.Noop.Block, ", "))
		}
		for from, to := range rc.Noop.Rewrite {
			fmt.Fprintf(w, "  rewrite: %s -> %s\n", from, to)
		}
		fmt.Fprintln(w)
	}

	if len(rc.Allow) > 0 {
		fmt.Fprintf(w, "allow: %s\n", strings.Join(rc.Allow, ", "))
		fmt.Fprintln(w)
	}

	if rc.LogDir != "" {
		fmt.Fprintf(w, "log dir: %s\n", rc.LogDir)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "resource limits:")
	if rc.Limits.Memory != "" {
		fmt.Fprintf(w, "  memory: %s\n", rc.Limits.Memory)
	} else {
		fmt.Fprintln(w, "  memory: (none)")
	}
	if rc.Limits.CPU != "" {
		if q, err := config.NormalizeCPUQuota(rc.Limits.CPU); err == nil && q != "" {
			fmt.Fprintf(w, "  cpu:    %s (systemd CPUQuota)\n", q)
		} else {
			fmt.Fprintf(w, "  cpu:    %s\n", rc.Limits.CPU)
		}
	} else {
		fmt.Fprintln(w, "  cpu:    (none)")
	}
	if rc.Limits.Pids > 0 {
		fmt.Fprintf(w, "  pids:   %d\n", rc.Limits.Pids)
	} else {
		fmt.Fprintln(w, "  pids:   (none)")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "bwrap command:")
	fmt.Fprintf(w, "  %s\n", strings.Join(cmdArgs, " "))
}

// ── Nested user namespace setup ───────────────────────────────────────────────

// prepareNestedUserNs adds --userns-block-fd and --info-fd to the bwrap command
// so that we can call newuidmap/newgidmap from the host (where the setuid bit is
// fully effective) before bwrap proceeds. This gives bwrap's user namespace the
// full subuid range, allowing rootless container runtimes (podman) to create
// nested containers.
//
// Flow:
//  1. bwrap creates its user namespace and blocks (reading from blockR/fd 3).
//  2. bwrap writes {"child-pid": N} to infoW (fd 4) then closes it.
//  3. The returned postStart goroutine reads the PID, calls newuidmap/newgidmap,
//     then closes blockW, which unblocks bwrap.
//  4. bwrap continues with uid 0→hostUID and uid 1..N→subuidRange mapped.
func prepareNestedUserNs(cmd *exec.Cmd) (func() error, error) {
	blockR, blockW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("block pipe: %w", err)
	}
	infoR, infoW, err := os.Pipe()
	if err != nil {
		blockR.Close()
		blockW.Close()
		return nil, fmt.Errorf("info pipe: %w", err)
	}

	// Insert --userns-block-fd 3 --info-fd 4 into bwrap args before the "--"
	// separator. Only the first "--" is bwrap's separator; any later ones
	// belong to the entrypoint args and must be left untouched.
	sep := slices.Index(cmd.Args, "--")
	if sep < 0 {
		blockR.Close()
		blockW.Close()
		infoR.Close()
		infoW.Close()
		return nil, fmt.Errorf("bwrap args missing %q separator", "--")
	}
	cmd.Args = slices.Insert(cmd.Args, sep, "--userns-block-fd", "3", "--info-fd", "4")

	// ExtraFiles[0] = fd 3 (blockR: bwrap blocks reading this until we close blockW)
	// ExtraFiles[1] = fd 4 (infoW: bwrap writes {"child-pid": N} then closes it)
	cmd.ExtraFiles = append(cmd.ExtraFiles, blockR, infoW)

	uid := os.Getuid()
	gid := os.Getgid()

	postStart := func() error {
		// Close parent's copies of the child-side fds immediately.
		blockR.Close()
		infoW.Close()
		defer infoR.Close()
		defer blockW.Close() // always close to unblock bwrap, even on error

		// Read bwrap's child PID from the info pipe.
		data, err := io.ReadAll(infoR)
		if err != nil {
			return fmt.Errorf("reading bwrap info: %w", err)
		}
		var info struct {
			ChildPid int `json:"child-pid"`
		}
		if err := json.Unmarshal(data, &info); err != nil || info.ChildPid == 0 {
			return fmt.Errorf("parsing bwrap info %q: %w", data, err)
		}
		pid := info.ChildPid

		// Set up uid map: uid 0 → real uid, uid 1..N → subuid range.
		if out, err := exec.Command("newuidmap", buildIDMapArgs(pid, uid, "/etc/subuid")...).CombinedOutput(); err != nil {
			return fmt.Errorf("newuidmap: %s: %w", strings.TrimSpace(string(out)), err)
		}
		// Set up gid map.
		if out, err := exec.Command("newgidmap", buildIDMapArgs(pid, gid, "/etc/subgid")...).CombinedOutput(); err != nil {
			return fmt.Errorf("newgidmap: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	}

	return postStart, nil
}

// buildIDMapArgs returns args for newuidmap/newgidmap that preserve the real
// uid inside the sandbox and pass through the subuid range unchanged:
//
//	pid <id> <id> 1 [<substart> <substart> <subcount>]
//
// This keeps the process uid = real host uid so rootless tools (podman) use
// user-mode storage paths. The subuid range is mapped 1:1 so that nested
// container uid maps resolve to the correct host uids.
func buildIDMapArgs(pid, id int, subIDFile string) []string {
	idStr := strconv.Itoa(id)
	args := []string{strconv.Itoa(pid), idStr, idStr, "1"}
	if start, count, err := readSubIDRange(subIDFile, id); err == nil {
		startStr := strconv.Itoa(start)
		args = append(args, startStr, startStr, strconv.Itoa(count))
	}
	return args
}

// readSubIDRange finds the first sub-id range for id in path (/etc/subuid or /etc/subgid).
func readSubIDRange(path string, id int) (start, count int, err error) {
	idStr := strconv.Itoa(id)
	username := ""
	if u, err := user.LookupId(idStr); err == nil {
		username = u.Username
	}

	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] != username && parts[0] != idStr {
			continue
		}
		s, e1 := strconv.Atoi(parts[1])
		c, e2 := strconv.Atoi(parts[2])
		if e1 != nil || e2 != nil {
			continue
		}
		return s, c, nil
	}
	return 0, 0, fmt.Errorf("no entry for %q in %s", idStr, path)
}

// ── Resource limits ───────────────────────────────────────────────────────────

// wrapWithLimits wraps cmd inside a `systemd-run --user --scope` invocation
// when any resource limit is set and systemd-run is available on the host.
// Returns cmd unchanged when:
//   - all limits are zero (nothing to enforce),
//   - systemd-run is not found (warning printed to w), or
//   - nestedUserNs is true — wrapping would sever the block/info pipe pair
//     wired by prepareNestedUserNs, because systemd-run closes non-standard
//     fds before exec'ing the command.
func wrapWithLimits(w io.Writer, cmd *exec.Cmd, limits config.ResourceLimits, nestedUserNs bool) (*exec.Cmd, error) {
	if limits.IsZero() {
		return cmd, nil
	}
	if nestedUserNs {
		fmt.Fprintf(w, "%s: resource limits are not applied when nested-user-ns is active (systemd-run cannot safely pass through the userns pipe fds)\n", colorizeW(w, ansiBoldYellow, "warning"))
		return cmd, nil
	}

	sdPath, err := exec.LookPath("systemd-run")
	if err != nil {
		fmt.Fprintf(w, "%s: resource limits requested but systemd-run not found — limits will not be enforced\n", colorizeW(w, ansiBoldYellow, "warning"))
		return cmd, nil
	}
	if !systemdUserSessionAvailableFn() {
		fmt.Fprintf(w, "%s: resource limits requested but no systemd user session found — limits will not be enforced\n", colorizeW(w, ansiBoldYellow, "warning"))
		return cmd, nil
	}

	var args []string
	args = append(args, "--user", "--scope")

	if limits.Memory != "" {
		args = append(args, "--property=MemoryMax="+limits.Memory)
	}
	if limits.CPU != "" {
		quota, err := config.NormalizeCPUQuota(limits.CPU)
		if err != nil {
			return nil, err
		}
		if quota != "" {
			args = append(args, "--property=CPUQuota="+quota)
		}
	}
	if limits.Pids > 0 {
		args = append(args, fmt.Sprintf("--property=TasksMax=%d", limits.Pids))
	}

	args = append(args, "--")
	args = append(args, cmd.Args...)

	wrapped := exec.Command(sdPath, args...)
	wrapped.Env = cmd.Env
	wrapped.Dir = cmd.Dir
	// ExtraFiles intentionally NOT copied: no extra fds are present at this
	// point (nested-user-ns is handled above), and systemd-run in --scope
	// mode may close non-standard fds before exec'ing its child.
	return wrapped, nil
}

// systemdUserSessionAvailableFn is the function used by wrapWithLimits to
// check whether a systemd user session is active. It is a variable so tests
// can override it to simulate presence or absence of a session without needing
// a real D-Bus socket.
var systemdUserSessionAvailableFn = defaultSystemdUserSessionAvailable

// defaultSystemdUserSessionAvailable is the real implementation: it checks for
// the D-Bus socket that systemd-logind creates under XDG_RUNTIME_DIR (or the
// default /run/user/<uid> path). Without an active session, `systemd-run
// --user --scope` fails immediately with "Failed to connect to bus".
func defaultSystemdUserSessionAvailable() bool {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	_, err := os.Stat(filepath.Join(runtimeDir, "bus"))
	return err == nil
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) newRunCmd() *cobra.Command {
	var flags runCLIFlags

	cmd := &cobra.Command{
		Use:   "run [-p PROFILE] [-w PATH] [flags] [-- extra-args]",
		Short: "Run a command in an isolated sandbox",
		Long: `Run launches the configured entrypoint inside a bubblewrap sandbox.

Flags override the loaded profile. Extra arguments after -- are appended
to the entrypoint command.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runSandbox(cmd.OutOrStdout(), flags, args)
		},
	}

	cmd.Flags().StringVarP(&flags.profile, "profile", "p", "", "Profile to use (default: \"default\")")
	cmd.Flags().StringVarP(&flags.workdir, "workdir", "w", "", "Mount PATH read-write in the sandbox (at the same path)")
	cmd.Flags().BoolVar(&flags.networkOn, "network", false, "Enable network access")
	cmd.Flags().BoolVar(&flags.networkOff, "no-network", false, "Disable network access")
	cmd.Flags().BoolVarP(&flags.interactive, "interactive", "i", false, "Force interactive mode")
	cmd.Flags().BoolVar(&flags.noInteractive, "no-interactive", false, "Force non-interactive mode")
	cmd.Flags().StringArrayVarP(&flags.mounts, "mount", "m", nil, "Additional mount: SRC:DEST[:MODE] (mode: ro, rw or safe-rw; default ro)")
	cmd.Flags().StringArrayVarP(&flags.env, "env", "e", nil, "Set env variable: KEY=VAL (~, $VAR and ${VAR} are expanded against the host environment)")
	cmd.Flags().StringVar(&flags.entrypoint, "entrypoint", "", "Override entrypoint command (resets profile args, like docker --entrypoint)")
	cmd.Flags().StringArrayVarP(&flags.args, "arg", "a", nil, "Append argument to entrypoint (repeatable)")
	cmd.Flags().StringVar(&flags.argsFile, "args-file", "", "Read file and append its content as a single entrypoint argument")
	cmd.Flags().StringVar(&flags.prompt, "prompt", "", "")
	_ = cmd.Flags().MarkHidden("prompt")
	_ = cmd.Flags().MarkDeprecated("prompt", "use --arg instead")
	cmd.Flags().IntVar(&flags.timeout, "timeout", 0, "Timeout in seconds (0 = none)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print the sandbox command without executing it")
	cmd.Flags().BoolVarP(&flags.yes, "yes", "y", false, "Skip interactive confirmation prompts (e.g. keyring unlock)")
	cmd.Flags().BoolVar(&flags.allowRemote, "allow-remote", false, "Consent to running a profile downloaded from a URL (--yes does not)")
	cmd.Flags().BoolVar(&flags.trustRemote, "trust-remote", false, "Run a downloaded profile unhardened: it may inherit host env and un-hide credentials (implies --allow-remote)")
	cmd.Flags().StringVar(&flags.sha256, "sha256", "", "Pin the sha256 of a profile downloaded from a URL; abort on mismatch")
	cmd.Flags().StringVar(&flags.limitMemory, "limit-memory", "", "Override memory limit (e.g. \"4G\", \"512M\")")
	cmd.Flags().StringVar(&flags.limitCPU, "limit-cpu", "", "Override CPU limit (e.g. \"200%\" or \"2.0\" cores)")
	cmd.Flags().IntVar(&flags.limitPids, "limit-pids", 0, "Override max process count (0 = no override)")

	_ = cmd.RegisterFlagCompletionFunc("profile", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return a.loader.ProfileNames(), cobra.ShellCompDirectiveDefault
	})

	return cmd
}
