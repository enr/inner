package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/sandbox"
	"github.com/enr/inner/internal/shim"
	"github.com/spf13/cobra"
)

// ── Business logic ────────────────────────────────────────────────────────────

// runVerifyOutside is called from the host. It builds a sandbox with the inner
// binary itself as the entrypoint and passes --inside so the binary re-executes
// the checks from within the sandboxed environment.
func (a *App) runVerifyOutside(w io.Writer, profileName string, suggest bool) error {
	if profileName == "" {
		profileName = "default"
	}

	rc, err := a.loader.Build(profileName)
	if err != nil {
		return err
	}

	// Build shim dir if the profile has noop entries.
	var cleanups []func() error
	if len(rc.Noop.Block) > 0 || len(rc.Noop.Rewrite) > 0 {
		shimDir, err := shim.Builder{}.Build(rc.Noop)
		if err != nil {
			return fmt.Errorf("building shim dir: %w", err)
		}
		rc.ShimDir = shimDir
		cleanups = append(cleanups, func() error { return os.RemoveAll(shimDir) })
	}

	// Generate the containers.conf override for rootless podman inside the
	// sandbox, so [verify.custom] checks see the same environment as inner run.
	cleanupContainers, err := applyContainersConf(rc)
	if err != nil {
		return fmt.Errorf("containers config: %w", err)
	}
	defer cleanupContainers()

	// Resolve the inner binary path so it can be invoked inside the sandbox.
	// os.Executable() returns the real path; with --ro-bind / / it is accessible
	// at the same path inside.
	innerBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine inner binary path: %w", err)
	}

	// With home = "isolated" the empty home tmpfs hides two things the inside
	// invocation needs: the inner binary itself (when installed under ~/.local/bin)
	// and the profiles directory it re-reads to know which checks apply. Both are
	// re-exposed read-only, and only for verify — `inner run` never gets them.
	if rc.HomeIsolated() {
		if home, err := os.UserHomeDir(); err == nil {
			for _, p := range []string{innerBin, a.loader.Dir} {
				if strings.HasPrefix(p, home+string(os.PathSeparator)) {
					rc.HomeAllow = append(rc.HomeAllow, p)
				}
			}
		}
	}

	// Override entrypoint: run `inner verify --inside [--suggest]`.
	innerArgs := []string{"verify", "--inside"}
	if suggest {
		innerArgs = append(innerArgs, "--suggest")
	}
	rc.Entrypoint = config.Entrypoint{
		Cmd:         innerBin,
		Args:        innerArgs,
		Interactive: false,
	}

	// Pass context to the inside invocation via environment.
	if rc.Env.Set == nil {
		rc.Env.Set = make(map[string]string)
	}
	rc.Env.Set["INNER_VERIFY_INSIDE"] = "1"
	rc.Env.Set["INNER_VERIFY_PROFILE"] = profileName
	// The home mode travels through the environment rather than being re-read
	// from the profile inside: the check must run even when the profiles
	// directory is not readable from within the sandbox.
	if rc.HomeMode != "" {
		rc.Env.Set["INNER_VERIFY_HOME_MODE"] = rc.HomeMode
	}

	// Build sandbox command.
	iso, err := a.isolatorFn()
	if err != nil {
		return fmt.Errorf("isolator: %w", err)
	}
	cmd, err := iso.Build(*rc)
	if err != nil {
		return fmt.Errorf("building sandbox command: %w", err)
	}

	// Launch non-interactive; output is forwarded directly to the terminal.
	launcher := a.launcherFn()
	result, err := launcher.Run(cmd, executor.RunOptions{Cleanups: cleanups})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return exitCodeError{code: result.ExitCode}
	}
	return nil
}

// runVerifyInside is called when inner is already running inside the sandbox
// (detected via INNER_VERIFY_INSIDE env var or --inside flag).
// It reads the profile to get Allow/Custom/Network settings, runs all checks,
// renders the report, and exits with a non-zero code if the sandbox is not
// conformant.
func (a *App) runVerifyInside(w io.Writer, suggest bool) error {
	var allow []string
	var custom []config.CustomCheck
	networkEnabled := false
	shimsExpected := false

	profileName := os.Getenv("INNER_VERIFY_PROFILE")
	if profileName == "" {
		profileName = "default"
	}
	homeMode := os.Getenv("INNER_VERIFY_HOME_MODE")
	if p, err := a.loader.LoadProfileAuto(profileName); err == nil {
		allow = p.Sandbox.Allow
		custom = p.Verify.Custom.Checks
		networkEnabled = p.Sandbox.Network
		shimsExpected = len(p.Noop.Block) > 0 || len(p.Noop.Rewrite) > 0
		// The env var set by the host side wins: it describes the sandbox that
		// was actually built, while the profile may not even be readable here.
		if homeMode == "" {
			homeMode = p.Sandbox.Home
		}
	}

	checker := &sandbox.Checker{
		Allow:          allow,
		Custom:         custom,
		NetworkEnabled: networkEnabled,
		ShimsExpected:  shimsExpected,
		HomeIsolated:   homeMode == config.HomeIsolated,
	}

	report := checker.Run()
	report.Render(w, suggest)

	if !report.Conformant() {
		return exitCodeError{code: 1}
	}
	return nil
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) newVerifyCmd() *cobra.Command {
	var flags struct {
		profile string
		suggest bool
		inside  bool
	}

	cmd := &cobra.Command{
		Use:   "verify [-p PROFILE] [--suggest]",
		Short: "Verify sandbox configuration",
		Long: `Verify runs built-in and custom checks inside the sandbox to detect
security misconfigurations and unexpectedly exposed sensitive resources.

Run from the host: inner verify [-p PROFILE] [--suggest]
  Builds the sandbox and executes checks from within.

--suggest adds TOML snippets to failed checks so you can copy them
directly into your profile.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			inside := flags.inside || os.Getenv("INNER_VERIFY_INSIDE") == "1"
			if inside {
				return a.runVerifyInside(cmd.OutOrStdout(), flags.suggest)
			}
			return a.runVerifyOutside(cmd.OutOrStdout(), flags.profile, flags.suggest)
		},
	}

	cmd.Flags().StringVarP(&flags.profile, "profile", "p", "", `Profile to use (default: "default")`)
	cmd.Flags().BoolVar(&flags.suggest, "suggest", false, "Show TOML snippets for failed checks")
	cmd.Flags().BoolVar(&flags.inside, "inside", false, "Run checks directly (internal flag, set automatically inside the sandbox)")
	_ = cmd.Flags().MarkHidden("inside")

	_ = cmd.RegisterFlagCompletionFunc("profile", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return a.loader.ProfileNames(), cobra.ShellCompDirectiveDefault
	})

	return cmd
}
