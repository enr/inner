package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/enr/inner/internal/config"
)

// opencodeConfigDir returns the path to OpenCode's XDG config directory.
// OpenCode uses app name "opencode", so the path is:
//
//	$XDG_CONFIG_HOME/opencode   (if XDG_CONFIG_HOME is set)
//	~/.config/opencode          (otherwise)
//
// Note: the sandbox profile does NOT inherit XDG_CONFIG_HOME, so inside the
// sandbox OpenCode always resolves to ~/.config/opencode — matching the mount.
func opencodeConfigDir() string {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = config.ExpandPath("~/.config")
	}
	return filepath.Join(xdg, "opencode")
}

// opencodeDataDir returns the path to OpenCode's XDG data directory, which
// holds the auth token and per-session state:
//
//	$XDG_DATA_HOME/opencode      (if XDG_DATA_HOME is set)
//	~/.local/share/opencode      (otherwise)
//
// As with the config dir, the sandbox profile does not inherit XDG_DATA_HOME,
// so inside the sandbox OpenCode always resolves to ~/.local/share/opencode.
func opencodeDataDir() string {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		xdg = config.ExpandPath("~/.local/share")
	}
	return filepath.Join(xdg, "opencode")
}

// prepareOpencodeConfig creates a sandboxed clone of src (usually
// ~/.config/opencode) in a temporary directory. Only user configuration is
// copied; everything else starts fresh.
//
// Contents of the sandbox directory:
//   - config.json    copied from src  (optional — user config)
//   - opencode.json  copied from src  (optional — alternative config filename)
//   - themes/        copied from src  (optional — custom themes)
//
// Returns the sandbox path and a cleanup function that removes it.
// The caller must always invoke cleanup (typically via defer).
func prepareOpencodeConfig(src string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "inner-opencode-config-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating opencode config sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// ── Optional: user configuration ──────────────────────────────────────────
	_ = copyFile(filepath.Join(src, "config.json"), filepath.Join(tmp, "config.json"))
	_ = copyFile(filepath.Join(src, "opencode.json"), filepath.Join(tmp, "opencode.json"))

	// ── Optional: custom themes ───────────────────────────────────────────────
	_ = copyDir(filepath.Join(src, "themes"), filepath.Join(tmp, "themes"))

	return tmp, cleanup, nil
}

// prepareOpencodeData creates a sandboxed clone of src (usually
// ~/.local/share/opencode) in a temporary directory. Only the auth token is
// copied; sessions, logs, storage and project state start fresh, ensuring the
// sandboxed agent cannot read or corrupt previous runs.
//
// Contents of the sandbox directory:
//   - auth.json   copied from src  (best-effort — provider credentials)
//   - storage/, log/, project/, snapshot/   empty directories
//
// auth.json is copied best-effort rather than required: OpenCode also supports
// API-key auth via provider environment variables, so a missing auth.json is
// not necessarily an error.
//
// Returns the sandbox path and a cleanup function that removes it.
func prepareOpencodeData(src string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "inner-opencode-data-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating opencode data sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// ── Best-effort: provider credentials ─────────────────────────────────────
	_ = copyFile(filepath.Join(src, "auth.json"), filepath.Join(tmp, "auth.json"))

	// ── Fresh empty directories ───────────────────────────────────────────────
	freshDirs := []string{"storage", "log", "project", "snapshot"}
	for _, d := range freshDirs {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("creating %s in opencode data sandbox: %w", d, err)
		}
	}

	return tmp, cleanup, nil
}

// applyOpencode injects sandbox mounts for ~/.config/opencode and
// ~/.local/share/opencode into rc. It creates sandboxed temporary clones of
// both directories and appends their mounts to rc.Mounts. Returns the cleanup
// function; the caller must defer it.
func applyOpencode(rc *config.RunConfig) (func(), error) {
	cfgDir := opencodeConfigDir()
	dataDir := opencodeDataDir()

	var cleanups []func()

	sandboxedCfg, cleanupCfg, err := prepareOpencodeConfig(cfgDir)
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, cleanupCfg)
	rc.Mounts = append(rc.Mounts, config.Mount{
		Src:  sandboxedCfg,
		Dest: cfgDir,
		Mode: "rw",
	})

	sandboxedData, cleanupData, err := prepareOpencodeData(dataDir)
	if err != nil {
		for _, fn := range cleanups {
			fn()
		}
		return nil, err
	}
	cleanups = append(cleanups, cleanupData)
	rc.Mounts = append(rc.Mounts, config.Mount{
		Src:  sandboxedData,
		Dest: dataDir,
		Mode: "rw",
	})

	return func() {
		for _, fn := range cleanups {
			fn()
		}
	}, nil
}
