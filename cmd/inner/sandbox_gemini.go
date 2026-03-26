package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/enr/inner/internal/config"
)

// geminiHomeDir returns the canonical (expanded) path to ~/.gemini.
func geminiHomeDir() string {
	return config.ExpandPath("~/.gemini")
}

// prepareGemini creates a sandboxed clone of src (usually ~/.gemini) in a
// temporary directory. Only user settings are copied; everything else starts
// fresh, ensuring the sandboxed agent cannot read or corrupt previous
// sessions and history.
//
// Unlike Claude, Gemini CLI authenticates via the GEMINI_API_KEY environment
// variable (inherited through the profile), so no credentials file needs to
// be copied.
//
// Contents of the sandbox directory:
//   - settings.json   copied from src  (optional — user preferences)
//   - (all other dirs) empty
//
// Returns the sandbox path and a cleanup function that removes it.
// The caller must always invoke cleanup (typically via defer).
func prepareGemini(src string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "inner-gemini-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating gemini sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// ── Optional: user settings ───────────────────────────────────────────────
	_ = copyFile(filepath.Join(src, "settings.json"), filepath.Join(tmp, "settings.json"))

	return tmp, cleanup, nil
}

// applyGemini injects the gemini sandbox mount into rc.
// It creates a sandboxed temporary clone of ~/.gemini and appends the mount
// to rc.Mounts. Returns the cleanup function; the caller must defer it.
func applyGemini(rc *config.RunConfig) (func(), error) {
	geminiDir := geminiHomeDir()

	sandboxed, cleanup, err := prepareGemini(geminiDir)
	if err != nil {
		return nil, err
	}
	rc.Mounts = append(rc.Mounts, config.Mount{
		Src:  sandboxed,
		Dest: geminiDir,
		Mode: "rw",
	})
	return cleanup, nil
}
