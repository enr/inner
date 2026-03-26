package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/enr/inner/internal/config"
)

// cursorHomeDir returns the canonical (expanded) path to ~/.cursor.
func cursorHomeDir() string {
	return config.ExpandPath("~/.cursor")
}

// cursorConfigDir returns the path to cursor-agent's XDG config directory.
// cursor-agent uses app name "cursor", so the path is:
//
//	$XDG_CONFIG_HOME/cursor   (if XDG_CONFIG_HOME is set)
//	~/.config/cursor          (otherwise)
//
// Note: the sandbox profile does NOT inherit XDG_CONFIG_HOME, so inside the
// sandbox cursor-agent always resolves to ~/.config/cursor — matching the mount.
func cursorConfigDir() string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = config.ExpandPath("~/.config")
	}
	return filepath.Join(xdg, "cursor")
}

// prepareCursor creates a sandboxed clone of src (usually ~/.cursor) in a
// temporary directory. Only user settings and skill definitions are copied;
// everything else starts fresh, ensuring the sandboxed agent cannot read or
// corrupt previous sessions or project state.
//
// Contents of the sandbox directory:
//   - cli-config.json   copied from src  (optional — settings, permissions)
//   - skills-cursor/    copied from src  (optional — user skill definitions)
//   - ai-tracking/, extensions/, projects/  empty directories
//
// Returns the sandbox path and a cleanup function that removes it.
// The caller must always invoke cleanup (typically via defer).
func prepareCursor(src string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "inner-cursor-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating cursor sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// ── Optional: user settings ───────────────────────────────────────────────
	_ = copyFile(filepath.Join(src, "cli-config.json"), filepath.Join(tmp, "cli-config.json"))

	// ── Optional: skill definitions ───────────────────────────────────────────
	_ = copyDir(filepath.Join(src, "skills-cursor"), filepath.Join(tmp, "skills-cursor"))

	// ── Fresh empty directories ───────────────────────────────────────────────
	freshDirs := []string{"ai-tracking", "extensions", "projects"}
	for _, d := range freshDirs {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("creating %s in cursor sandbox: %w", d, err)
		}
	}

	return tmp, cleanup, nil
}

// prepareCursorConfig creates a sandboxed clone of ~/.config/cursor containing
// only auth.json, which holds the OAuth tokens cursor-agent needs to call
// the Cursor API. All other files start fresh.
//
// Returns the sandbox path and a cleanup function that removes it.
func prepareCursorConfig(src string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "inner-cursor-config-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating cursor config sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// ── Required: auth token ──────────────────────────────────────────────────
	authSrc := filepath.Join(src, "auth.json")
	authDst := filepath.Join(tmp, "auth.json")
	if err := copyFile(authSrc, authDst); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copying cursor auth.json: %w (is cursor-agent authenticated?)", err)
	}

	return tmp, cleanup, nil
}

// applyCursor injects sandbox mounts for ~/.cursor and ~/.config/cursor into rc.
// It creates sandboxed temporary clones of both directories and appends their
// mounts to rc.Mounts. Returns the cleanup function; the caller must defer it.
func applyCursor(rc *config.RunConfig) (func(), error) {
	homeDir := cursorHomeDir()
	cfgDir := cursorConfigDir()

	var cleanups []func()

	sandboxedHome, cleanupHome, err := prepareCursor(homeDir)
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, cleanupHome)
	rc.Mounts = append(rc.Mounts, config.Mount{
		Src:  sandboxedHome,
		Dest: homeDir,
		Mode: "rw",
	})

	sandboxedCfg, cleanupCfg, err := prepareCursorConfig(cfgDir)
	if err != nil {
		for _, fn := range cleanups {
			fn()
		}
		return nil, err
	}
	cleanups = append(cleanups, cleanupCfg)
	rc.Mounts = append(rc.Mounts, config.Mount{
		Src:  sandboxedCfg,
		Dest: cfgDir,
		Mode: "rw",
	})

	return func() {
		for _, fn := range cleanups {
			fn()
		}
	}, nil
}
