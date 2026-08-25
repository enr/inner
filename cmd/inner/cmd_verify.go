package main

import (
	"encoding/json"
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
	appendHomeAllowIfHidden(rc, innerBin, a.loader.Dir)

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
	// Everything the checks need travels through the environment rather than
	// being re-read from the profile inside the sandbox. The profile file is
	// routinely unreachable there — under home = "isolated" it lives in the
	// hidden home, and `inner verify` sets no workdir, so nothing binds it back.
	// Re-reading it silently produced a default context (no network, no shims,
	// no allow keys, no custom checks), which made every agent profile fail the
	// network check and made [sandbox] allow declassification a no-op.
	//
	// These values describe the sandbox that was actually built, which is what
	// the checks should be judged against anyway.
	rc.Env.Set["INNER_VERIFY_HOME_MODE"] = rc.HomeMode
	rc.Env.Set["INNER_VERIFY_NETWORK_MODE"] = rc.EffectiveNetworkMode()
	// The legacy boolean is still written for one release so an inner binary
	// older than this one, re-invoked as the --inside entrypoint, keeps seeing
	// the context it expects. INNER_VERIFY_NETWORK_MODE is authoritative.
	rc.Env.Set["INNER_VERIFY_NETWORK"] = boolEnv(rc.Network)
	rc.Env.Set["INNER_VERIFY_SHIMS"] = boolEnv(rc.ShimDir != "")
	rc.Env.Set["INNER_VERIFY_ALLOW"] = strings.Join(rc.Allow, ",")
	rc.Env.Set["INNER_VERIFY_CUSTOM"] = ""
	if len(rc.VerifyCustomChecks) > 0 {
		encoded, err := json.Marshal(rc.VerifyCustomChecks)
		if err != nil {
			return fmt.Errorf("encoding custom checks: %w", err)
		}
		rc.Env.Set["INNER_VERIFY_CUSTOM"] = string(encoded)
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
	profileName := os.Getenv("INNER_VERIFY_PROFILE")
	if profileName == "" {
		profileName = "default"
	}
	// The host side describes the sandbox it built through the environment.
	// The profile is only re-read to fill in what the environment does not
	// carry — it may well be invisible from in here (see runVerifyOutside).
	homeMode, homeModeSet := os.LookupEnv("INNER_VERIFY_HOME_MODE")
	networkMode, networkSet := os.LookupEnv("INNER_VERIFY_NETWORK_MODE")
	if !networkSet {
		// Fall back to the legacy boolean channel (an older host-side binary).
		if enabled, ok := lookupBoolEnv("INNER_VERIFY_NETWORK"); ok {
			networkMode, networkSet = config.NetworkModeFromBool(enabled), true
		}
	}
	shimsExpected, shimsSet := lookupBoolEnv("INNER_VERIFY_SHIMS")
	allow, allowSet := lookupListEnv("INNER_VERIFY_ALLOW")
	custom, customSet := lookupCustomChecksEnv("INNER_VERIFY_CUSTOM")

	if !homeModeSet || !networkSet || !shimsSet || !allowSet || !customSet {
		if p, err := a.loader.LoadProfileAuto(profileName); err == nil {
			if !allowSet {
				allow = p.Sandbox.Allow
			}
			if !customSet {
				custom = p.Verify.Custom.Checks
			}
			if !networkSet {
				// Must go through ResolveNetworkMode, not p.Sandbox.Network:
				// reading the bare bool reports "open network" for every mode
				// that is not "off", which would make the network-policy check
				// skip itself on exactly the mediated modes it should probe.
				networkMode = config.ResolveNetworkMode(p.Sandbox)
			}
			if !shimsSet {
				shimsExpected = len(p.Noop.Block) > 0 || len(p.Noop.Rewrite) > 0
			}
			if !homeModeSet {
				homeMode = p.Sandbox.Home
			}
		}
	}

	checker := &sandbox.Checker{
		Allow:         allow,
		Custom:        custom,
		NetworkMode:   networkMode,
		ShimsExpected: shimsExpected,
		HomeIsolated:  homeMode == config.HomeIsolated,
	}

	report := checker.Run()
	report.Render(w, suggest)

	if !report.Conformant() {
		return exitCodeError{code: 1}
	}
	return nil
}

// ── Verify context passed through the environment ─────────────────────────────

// boolEnv renders a bool for one of the INNER_VERIFY_* variables.
func boolEnv(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// lookupBoolEnv reads a boolEnv value, reporting whether the variable was set.
func lookupBoolEnv(name string) (bool, bool) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	return v == "1", true
}

// lookupListEnv reads a comma-separated list, reporting whether the variable
// was set. An empty variable is a set-but-empty list, not an absent one.
func lookupListEnv(name string) ([]string, bool) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	if v == "" {
		return nil, true
	}
	return strings.Split(v, ","), true
}

// lookupCustomChecksEnv decodes the JSON-encoded [verify.custom] checks. A
// malformed value is treated as absent so the profile fallback can still run.
func lookupCustomChecksEnv(name string) ([]config.CustomCheck, bool) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil, false
	}
	if v == "" {
		return nil, true
	}
	var checks []config.CustomCheck
	if err := json.Unmarshal([]byte(v), &checks); err != nil {
		return nil, false
	}
	return checks, true
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
