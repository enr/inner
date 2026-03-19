package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/enr/inner/internal/runtime"
	"github.com/enr/inner/internal/setup"
	"github.com/spf13/cobra"
)

// ── Business logic ────────────────────────────────────────────────────────────

// doctor runs all diagnostic checks and writes the report to w.
func (a *App) doctor(w io.Writer) error {
	info := runtime.Detect()

	// ── bwrap ────────────────────────────────────────────────────────────────
	if info.BwrapAvailable {
		fmt.Fprintf(w, "[ok]  bwrap: v%s (%s)\n", info.BwrapVersion, info.BwrapPath)
	} else {
		fmt.Fprintln(w, "[!!]  bwrap: not found in PATH")
	}

	// ── user namespaces ──────────────────────────────────────────────────────
	if info.UserNSEnabled {
		fmt.Fprintln(w, "[ok]  user namespaces: enabled")
	} else {
		fmt.Fprintln(w, "[!!]  user namespaces: disabled (bwrap requires unprivileged user namespaces)")
	}

	// ── auto-init and profiles dir ───────────────────────────────────────────
	_ = setup.Init(a.loader.Dir) // best-effort: ensure dir exists before checking

	profilesDir := a.loader.ProfilesDir()
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		fmt.Fprintf(w, "[!!]  profiles: cannot read %s: %v\n", profilesDir, err)
	} else {
		count := 0
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				count++
			}
		}
		fmt.Fprintf(w, "[ok]  profiles: %d profile(s) in %s\n", count, profilesDir)
	}

	// ── logs dir ─────────────────────────────────────────────────────────────
	logsDir := filepath.Join(a.loader.Dir, "logs")
	if _, err := os.Stat(logsDir); err == nil {
		fmt.Fprintf(w, "[ok]  logs dir: %s\n", logsDir)
	} else {
		fmt.Fprintf(w, "[??]  logs dir: %s (will be created on first run)\n", logsDir)
	}

	// ── Claude ───────────────────────────────────────────────────────────────
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		fmt.Fprintln(w, "[ok]  ANTHROPIC_API_KEY: set")
	} else {
		fmt.Fprintln(w, "[??]  ANTHROPIC_API_KEY: not set (required for Claude agents)")
	}
	if claudePath, err := exec.LookPath("claude"); err == nil {
		fmt.Fprintf(w, "[ok]  claude: %s\n", claudePath)
	} else {
		fmt.Fprintln(w, "[??]  claude: not found in PATH (required for agent profiles)")
	}

	// ── Gemini ───────────────────────────────────────────────────────────────
	if os.Getenv("GEMINI_API_KEY") != "" {
		fmt.Fprintln(w, "[ok]  GEMINI_API_KEY: set")
	} else {
		fmt.Fprintln(w, "[??]  GEMINI_API_KEY: not set (required for Gemini agents)")
	}
	if geminiPath, err := exec.LookPath("gemini"); err == nil {
		fmt.Fprintf(w, "[ok]  gemini: %s\n", geminiPath)
	} else {
		fmt.Fprintln(w, "[??]  gemini: not found in PATH (required for Gemini agent profiles)")
	}

	// ── display server ───────────────────────────────────────────────────────
	switch info.Display {
	case runtime.DisplayWayland:
		fmt.Fprintf(w, "[ok]  display: wayland (%s)\n", info.WaylandSocket)
	case runtime.DisplayX11:
		fmt.Fprintf(w, "[ok]  display: x11 (%s)\n", info.X11Display)
	default:
		fmt.Fprintln(w, "[??]  display: no display server detected (clipboard forwarding unavailable)")
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
