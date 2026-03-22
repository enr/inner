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

	"github.com/enr/inner/internal/config"
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
}

// ── Business logic ────────────────────────────────────────────────────────────

// runSandbox is the full pipeline for `inner run`.
// It is an App method so tests can inject a fake isolator via a.isolatorFn.
func (a *App) runSandbox(w io.Writer, flags runCLIFlags, extraArgs []string) error {
	// 1. Auto-init ~/.inner (idempotent, best-effort).
	if err := setup.Init(a.loader.Dir); err != nil {
		fmt.Fprintf(w, "warning: setup init: %v\n", err)
	}

	// 2. Load RunConfig from profile.
	profileName := flags.profile
	if profileName == "" {
		profileName = a.loader.DefaultProfileName()
	}
	rc, err := a.loader.Build(profileName)
	if err != nil {
		return err
	}
	if rc.Experimental {
		return fmt.Errorf("profile %q is marked experimental and is not ready for use", profileName)
	}

	// 3. Resolve effective workdir (priority: --workdir flag > profile workdir > cwd).
	// applyOverrides is a pure function (no I/O), so we resolve the cwd here.
	if flags.workdir == "" {
		if rc.Workdir != "" {
			flags.workdir = rc.Workdir // profile entrypoint.workdir takes over
		} else if cwd, err := os.Getwd(); err == nil {
			flags.workdir = cwd
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

	// 5b. For interactive sessions, print which profile is being used.
	if rc.Entrypoint.Interactive {
		profilePath := a.loader.ResolveProfilePath(profileName)
		if home, err := os.UserHomeDir(); err == nil {
			profilePath = strings.Replace(profilePath, home, "~", 1)
		}
		fmt.Fprintf(w, "inner starting using profile %q %s\n", profileName, profilePath)
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
		rc.Env.Set["PS1"] = sandboxPS1()
	}

	// 6. For interactive bash: inject --init-file so PS1 survives ~/.bashrc.
	if err := prepareInteractiveShell(rc, a.loader.Dir, rc.Env.Set["PS1"]); err != nil {
		return fmt.Errorf("preparing interactive shell: %w", err)
	}

	// 7. Validate — print all issues; block on unknown allow keys or errors.
	if p, err := a.loader.LoadProfileAuto(profileName); err == nil {
		result := profile.Validate(p)
		for _, issue := range result.Issues {
			fmt.Fprintf(w, "profile %s\n", issue)
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

	// 9. Sanitize gitconfig if configured.
	if rc.Git != nil {
		gitPath, err := git.Sanitize(rc.Git)
		if err != nil {
			return fmt.Errorf("sanitizing gitconfig: %w", err)
		}
		defer os.Remove(gitPath)
		rc.GitConfigPath = gitPath
	}

	// 10. Sandbox ~/.claude — replace the real dir with a temporary clone that
	//    contains only auth credentials, settings, and skills; everything else
	//    (sessions, history, projects, …) starts fresh.
	cleanupClaude, err := applyClaude(rc)
	if err != nil {
		return fmt.Errorf("sandboxing claude home: %w", err)
	}
	defer cleanupClaude()

	// 11. Sandbox ~/.gemini — replace the real dir with a temporary clone that
	//    contains only settings.json; auth is handled via GEMINI_API_KEY env var.
	cleanupGemini, err := applyGemini(rc)
	if err != nil {
		return fmt.Errorf("sandboxing gemini home: %w", err)
	}
	defer cleanupGemini()

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
	if slices.Contains(rc.Allow, "nested-user-ns") {
		postStart, err = prepareNestedUserNs(cmd)
		if err != nil {
			return fmt.Errorf("preparing nested user namespace: %w", err)
		}
	}

	// 14. Dry-run: print profile, effective config, and sandbox command.
	if flags.dryRun {
		printDryRun(w, profileName, a.loader.ResolveProfilePath(profileName), a.loader.GlobalConfigPath(), rc, cmd.Args)
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
		PostStart:    postStart,
		Timeout:      rc.Timeout,
		LogDir:       rc.LogDir,
		Cleanups:     cleanups,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
	return nil
}

// applyOverrides merges CLI flags into rc.
// Pure function: no I/O, no side effects — primary target for unit tests.
func applyOverrides(rc *config.RunConfig, flags runCLIFlags, extraArgs []string) error {
	// Network (--network / --no-network).
	if flags.networkOn {
		rc.Network = true
	} else if flags.networkOff {
		rc.Network = false
	}

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

	// Env overrides (-e KEY=VAL).
	for _, e := range flags.env {
		k, v, err := parseEnvVar(e)
		if err != nil {
			return err
		}
		if rc.Env.Set == nil {
			rc.Env.Set = make(map[string]string)
		}
		rc.Env.Set[k] = v
	}

	// --entrypoint → replace cmd and reset profile args (same semantics as docker --entrypoint).
	if flags.entrypoint != "" {
		rc.Entrypoint.Cmd = config.ExpandPath(flags.entrypoint)
		rc.Entrypoint.Args = nil
	}

	// --arg flags → each appended as a separate entrypoint arg.
	rc.Entrypoint.Args = append(rc.Entrypoint.Args, flags.args...)

	// --prompt (deprecated) → appended to entrypoint args.
	if flags.prompt != "" {
		rc.Entrypoint.Args = append(rc.Entrypoint.Args, flags.prompt)
	}

	// Extra args after -- (includes --args-file content, pre-loaded by runSandbox).
	rc.Entrypoint.Args = append(rc.Entrypoint.Args, extraArgs...)

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
		fmt.Fprintf(w, "warning: args file %q is large (%d bytes); consider passing the file path as a mount instead\n", path, size)
	}
	return string(data), nil
}

// parseMount parses "src:dest[:mode]" into a Mount.
func parseMount(s string) (config.Mount, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return config.Mount{}, fmt.Errorf("invalid mount %q: expected src:dest[:mode]", s)
	}
	mode := "ro"
	if len(parts) == 3 {
		mode = parts[2]
	}
	if mode != "ro" && mode != "rw" {
		return config.Mount{}, fmt.Errorf("invalid mount mode %q in %q: must be ro or rw", mode, s)
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
func printDryRun(w io.Writer, profileName, profilePath, globalConfigPath string, rc *config.RunConfig, cmdArgs []string) {
	fmt.Fprintf(w, "profile:      %s\n", profileName)
	fmt.Fprintf(w, "profile path: %s\n", profilePath)
	fmt.Fprintf(w, "global config: %s\n", globalConfigPath)
	fmt.Fprintln(w)

	fmt.Fprintf(w, "entrypoint: %s\n", rc.Entrypoint.Cmd)
	if len(rc.Entrypoint.Args) > 0 {
		fmt.Fprintf(w, "args:       %s\n", strings.Join(rc.Entrypoint.Args, " "))
	}
	fmt.Fprintf(w, "interactive: %v\n", rc.Entrypoint.Interactive)
	if rc.Workdir != "" {
		fmt.Fprintf(w, "workdir:     %s\n", rc.Workdir)
	}
	fmt.Fprintf(w, "network:     %v\n", rc.Network)
	fmt.Fprintf(w, "clipboard:   %v\n", rc.Clipboard)
	if rc.Timeout > 0 {
		fmt.Fprintf(w, "timeout:     %ds\n", rc.Timeout)
	} else {
		fmt.Fprintln(w, "timeout:     none")
	}
	fmt.Fprintln(w)

	if len(rc.Mounts) > 0 {
		fmt.Fprintln(w, "mounts:")
		for _, m := range rc.Mounts {
			fmt.Fprintf(w, "  %s -> %s (%s)\n", m.Src, m.Dest, m.Mode)
		}
		fmt.Fprintln(w)
	}

	if rc.Env.Clear || len(rc.Env.Inherit) > 0 || len(rc.Env.Set) > 0 {
		fmt.Fprintln(w, "env:")
		if rc.Env.Clear {
			fmt.Fprintln(w, "  clearenv: true")
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

	// ExtraFiles[0] = fd 3 (blockR: bwrap blocks reading this until we close blockW)
	// ExtraFiles[1] = fd 4 (infoW: bwrap writes {"child-pid": N} then closes it)
	cmd.ExtraFiles = append(cmd.ExtraFiles, blockR, infoW)

	// Insert --userns-block-fd 3 --info-fd 4 into bwrap args before the "--" separator.
	newArgs := make([]string, 0, len(cmd.Args)+4)
	for _, arg := range cmd.Args {
		if arg == "--" {
			newArgs = append(newArgs, "--userns-block-fd", "3", "--info-fd", "4")
		}
		newArgs = append(newArgs, arg)
	}
	cmd.Args = newArgs

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
	cmd.Flags().StringArrayVarP(&flags.mounts, "mount", "m", nil, "Additional mount: SRC:DEST[:MODE]")
	cmd.Flags().StringArrayVarP(&flags.env, "env", "e", nil, "Set env variable: KEY=VAL")
	cmd.Flags().StringVar(&flags.entrypoint, "entrypoint", "", "Override entrypoint command (resets profile args, like docker --entrypoint)")
	cmd.Flags().StringArrayVarP(&flags.args, "arg", "a", nil, "Append argument to entrypoint (repeatable)")
	cmd.Flags().StringVar(&flags.argsFile, "args-file", "", "Read file and append its content as a single entrypoint argument")
	cmd.Flags().StringVar(&flags.prompt, "prompt", "", "")
	_ = cmd.Flags().MarkHidden("prompt")
	_ = cmd.Flags().MarkDeprecated("prompt", "use --arg instead")
	cmd.Flags().IntVar(&flags.timeout, "timeout", 0, "Timeout in seconds (0 = none)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print the sandbox command without executing it")

	return cmd
}
