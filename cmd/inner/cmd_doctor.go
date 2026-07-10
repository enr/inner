package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/enr/inner/internal/profile"
	"github.com/enr/inner/internal/runtime"
	"github.com/enr/inner/internal/setup"
	"github.com/spf13/cobra"
)

// ── Business logic ────────────────────────────────────────────────────────────

// doctor runs all diagnostic checks and writes the report to w.
func (a *App) doctor(w io.Writer) error {
	info := runtime.Detect()

	var nerrs, nwarns int

	okLine := func(line string) {
		fmt.Fprintln(w, colorizeW(w, ansiGreen, "[ok]")+"  "+line)
	}
	errLine := func(line string) {
		nerrs++
		fmt.Fprintln(w, colorizeW(w, ansiBoldRed, "[!!]")+"  "+line)
	}
	warnLine := func(line string) {
		nwarns++
		fmt.Fprintln(w, colorizeW(w, ansiBoldYellow, "[??]")+"  "+line)
	}

	// ── bwrap ────────────────────────────────────────────────────────────────
	if info.BwrapAvailable {
		okLine(fmt.Sprintf("bwrap: v%s (%s)", info.BwrapVersion, info.BwrapPath))
	} else {
		errLine("bwrap: not found in PATH")
	}

	// ── user namespaces ──────────────────────────────────────────────────────
	if info.UserNSEnabled {
		okLine("user namespaces: enabled")
	} else {
		errLine("user namespaces: disabled (bwrap requires unprivileged user namespaces)")
	}

	// ── auto-init and profiles dir ───────────────────────────────────────────
	_ = setup.Init(a.loader.Dir) // best-effort: ensure dir exists before checking

	profilesDir := a.loader.ProfilesDir()
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		errLine(fmt.Sprintf("profiles: cannot read %s: %v", profilesDir, err))
	} else {
		count := 0
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				count++
			}
		}
		okLine(fmt.Sprintf("profiles: %d profile(s) in %s", count, profilesDir))

		// Validate each profile and surface any issues.
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".toml")
			p, err := a.loader.LoadProfile(name)
			if err != nil {
				errLine(fmt.Sprintf("profile %q: %v", name, err))
				continue
			}
			for _, issue := range profile.Validate(p, a.loader.WorkDir).Issues {
				warnLine(fmt.Sprintf("profile %q: %s", name, issue))
			}
		}
	}

	// ── logs dir ─────────────────────────────────────────────────────────────
	logsDir := filepath.Join(a.loader.Dir, "logs")
	if _, err := os.Stat(logsDir); err == nil {
		okLine(fmt.Sprintf("logs dir: %s", logsDir))
	} else {
		warnLine(fmt.Sprintf("logs dir: %s (will be created on first run)", logsDir))
	}

	// ── Claude ───────────────────────────────────────────────────────────────
	// The claude capability authenticates primarily via OAuth credentials in
	// ~/.claude/.credentials.json; ANTHROPIC_API_KEY is only an alternative.
	// Only warn when neither is available.
	home, _ := os.UserHomeDir()
	claudeCreds := filepath.Join(home, ".claude", ".credentials.json")
	credInfo, credErr := os.Stat(claudeCreds)
	switch {
	case credErr == nil && credInfo.Size() > 0:
		okLine("claude auth: OAuth credentials present (~/.claude/.credentials.json)")
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		okLine("claude auth: ANTHROPIC_API_KEY set")
	default:
		warnLine("claude auth: no OAuth credentials or ANTHROPIC_API_KEY (run 'claude' on the host to authenticate)")
	}
	if claudePath, err := exec.LookPath("claude"); err == nil {
		okLine(fmt.Sprintf("claude: %s", claudePath))
	} else {
		warnLine("claude: not found in PATH (required for agent profiles)")
	}

	// ── Gemini ───────────────────────────────────────────────────────────────
	if os.Getenv("GEMINI_API_KEY") != "" {
		okLine("GEMINI_API_KEY: set")
	} else {
		warnLine("GEMINI_API_KEY: not set (required for Gemini agents)")
	}
	if geminiPath, err := exec.LookPath("gemini"); err == nil {
		okLine(fmt.Sprintf("gemini: %s", geminiPath))
	} else {
		warnLine("gemini: not found in PATH (required for Gemini agent profiles)")
	}

	// ── display server ───────────────────────────────────────────────────────
	switch info.Display {
	case runtime.DisplayWayland:
		okLine(fmt.Sprintf("display: wayland (%s)", info.WaylandSocket))
	case runtime.DisplayX11:
		okLine(fmt.Sprintf("display: x11 (%s)", info.X11Display))
	default:
		warnLine("display: no display server detected (clipboard forwarding unavailable)")
	}

	// ── summary ──────────────────────────────────────────────────────────────
	fmt.Fprintln(w)
	switch {
	case nerrs > 0:
		fmt.Fprintf(w, colorizeW(w, ansiBoldRed, "%d error(s), %d warning(s)\n"), nerrs, nwarns)
	case nwarns > 0:
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "%d warning(s)")+" — no errors\n", nwarns)
	default:
		fmt.Fprintln(w, colorizeW(w, ansiGreen, "all checks passed"))
	}

	return nil
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check host environment and configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.doctor(cmd.OutOrStdout())
		},
	}
}
