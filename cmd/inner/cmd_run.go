package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/git"
	"github.com/enr/inner/internal/profile"
	"github.com/enr/inner/internal/setup"
	"github.com/enr/inner/internal/shim"
	"github.com/spf13/cobra"
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
	prompt        string
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
		profileName = "default"
	}
	rc, err := a.loader.Build(profileName)
	if err != nil {
		return err
	}

	// 3. Apply CLI overrides.
	if err := applyOverrides(rc, flags, extraArgs); err != nil {
		return err
	}

	// 4. Set sandbox PS1 so the user can tell they are inside inner.
	//    Only applied if PS1 is not already explicitly set in the profile.
	if _, ok := rc.Env.Set["PS1"]; !ok {
		if rc.Env.Set == nil {
			rc.Env.Set = make(map[string]string)
		}
		rc.Env.Set["PS1"] = sandboxPS1()
	}

	// 5. For interactive bash: inject --init-file so PS1 survives ~/.bashrc.
	if err := prepareInteractiveShell(rc, a.loader.Dir, rc.Env.Set["PS1"]); err != nil {
		return fmt.Errorf("preparing interactive shell: %w", err)
	}

	// 6. Validate — print warnings, never fatal.
	if p, err := a.loader.LoadProfile(profileName); err == nil {
		for _, issue := range profile.Validate(p).Issues {
			fmt.Fprintf(w, "profile %s\n", issue)
		}
	}

	// 7. Build shim dir from [noop] config.
	var cleanups []func() error
	if len(rc.Noop.Block) > 0 || len(rc.Noop.Rewrite) > 0 {
		shimDir, err := shim.Builder{}.Build(rc.Noop)
		if err != nil {
			return fmt.Errorf("building shim dir: %w", err)
		}
		rc.ShimDir = shimDir
		cleanups = append(cleanups, func() error { return os.RemoveAll(shimDir) })
	}

	// 8. Sanitize gitconfig if configured.
	if rc.Git != nil {
		gitPath, err := git.Sanitize(rc.Git)
		if err != nil {
			return fmt.Errorf("sanitizing gitconfig: %w", err)
		}
		defer os.Remove(gitPath)
		rc.GitConfigPath = gitPath
	}

	// 8. Sandbox ~/.claude — replace the real dir with a temporary clone that
	//    contains only auth credentials, settings, and skills; everything else
	//    (sessions, history, projects, …) starts fresh.
	cleanupClaude, err := applyClaude(rc)
	if err != nil {
		return fmt.Errorf("sandboxing claude home: %w", err)
	}
	defer cleanupClaude()

	// 9. Create isolator and build the sandbox command.
	iso, err := a.isolatorFn()
	if err != nil {
		return fmt.Errorf("isolator: %w", err)
	}
	cmd, err := iso.Build(*rc)
	if err != nil {
		return fmt.Errorf("building sandbox command: %w", err)
	}

	// 8. Dry-run: print the full command and exit.
	if flags.dryRun {
		fmt.Fprintln(w, strings.Join(cmd.Args, " "))
		return nil
	}

	// 9. Launch.
	launcher := a.launcherFn()
	result, err := launcher.Run(cmd, executor.RunOptions{
		Interactive: rc.Entrypoint.Interactive,
		Timeout:     rc.Timeout,
		LogDir:      rc.LogDir,
		Cleanups:    cleanups,
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

	// Workdir (-w PATH) → mount at /workspace rw.
	if flags.workdir != "" {
		rc.Mounts = append(rc.Mounts, config.Mount{
			Src:  config.ExpandPath(flags.workdir),
			Dest: "/workspace",
			Mode: "rw",
		})
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

	// Prompt → appended to entrypoint args.
	if flags.prompt != "" {
		rc.Entrypoint.Args = append(rc.Entrypoint.Args, flags.prompt)
	}

	// Extra args after --.
	rc.Entrypoint.Args = append(rc.Entrypoint.Args, extraArgs...)

	return nil
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
		Dest: parts[1],
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
	cmd.Flags().StringVarP(&flags.workdir, "workdir", "w", "", "Mount PATH as /workspace (rw)")
	cmd.Flags().BoolVar(&flags.networkOn, "network", false, "Enable network access")
	cmd.Flags().BoolVar(&flags.networkOff, "no-network", false, "Disable network access")
	cmd.Flags().BoolVarP(&flags.interactive, "interactive", "i", false, "Force interactive mode")
	cmd.Flags().BoolVar(&flags.noInteractive, "no-interactive", false, "Force non-interactive mode")
	cmd.Flags().StringArrayVarP(&flags.mounts, "mount", "m", nil, "Additional mount: SRC:DEST[:MODE]")
	cmd.Flags().StringArrayVarP(&flags.env, "env", "e", nil, "Set env variable: KEY=VAL")
	cmd.Flags().StringVar(&flags.prompt, "prompt", "", "Append prompt text to entrypoint args")
	cmd.Flags().IntVar(&flags.timeout, "timeout", 0, "Timeout in seconds (0 = none)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print the sandbox command without executing it")

	return cmd
}
