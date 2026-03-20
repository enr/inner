package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/enr/inner/internal/config"
)

// ── Interactive bash: PS1 override via --init-file ────────────────────────────

// prepareInteractiveShell injects `--init-file` into bash entrypoints so our
// sandbox PS1 wins even after ~/.bashrc runs.
//
// It writes ~/.inner/shell-init.sh (accessible inside the sandbox via the root
// ro-bind, no extra mounts needed), then prepends ["--init-file", path] to the
// entrypoint args. It is a no-op for non-bash or non-interactive configs.
func prepareInteractiveShell(rc *config.RunConfig, innerDir, ps1 string) error {
	if !rc.Entrypoint.Interactive {
		return nil
	}
	if filepath.Base(rc.Entrypoint.Cmd) != "bash" {
		return nil
	}
	// Respect an explicit --init-file / --rcfile already set in the profile.
	for _, arg := range rc.Entrypoint.Args {
		if arg == "--init-file" || arg == "--rcfile" {
			return nil
		}
	}

	initPath := filepath.Join(innerDir, "shell-init.sh")
	// Do NOT source ~/.bashrc: it could export personal data (API keys, tokens,
	// private paths) that clearenv+inherit is designed to keep out of the sandbox.
	// bash --init-file replaces ~/.bashrc, so only this file runs.
	content := "# inner sandbox — shell initialization\n" +
		"PS1=" + fmt.Sprintf("%q", ps1) + "\n"

	if err := os.WriteFile(initPath, []byte(content), 0o755); err != nil {
		return fmt.Errorf("writing shell-init.sh: %w", err)
	}

	rc.Entrypoint.Args = append([]string{"--init-file", initPath}, rc.Entrypoint.Args...)
	return nil
}

// sandboxPS1 returns a bash PS1 that makes it immediately visible that the
// shell is running inside an inner sandbox.
//
// Example prompt:  (inner) enrico@mymachine:~/project $
//
// Colour codes (bash \[…\] non-printing wrappers):
//   - bold yellow  →  (inner)
//   - bold green   →  user@host
//   - bold blue    →  working directory
func sandboxPS1() string {
	return `\[\033[1;33m\](inner)\[\033[0m\] \[\033[1;32m\]\u@\h\[\033[0m\]:\[\033[1;34m\]\w\[\033[0m\]\$ `
}

// claudeHomeDir returns the canonical (expanded) path to ~/.claude.
func claudeHomeDir() string {
	return config.ExpandPath("~/.claude")
}

// prepareClaude creates a sandboxed clone of src (usually ~/.claude) in a
// temporary directory. Only authentication and user preferences are copied;
// everything else starts fresh, ensuring the sandboxed agent cannot read or
// corrupt previous sessions, history, or project state.
//
// Contents of the sandbox directory:
//   - .credentials.json  copied from src  (required — auth)
//   - settings.json      copied from src  (optional — user prefs)
//   - skills/            copied from src  (optional — skill definitions)
//   - sessions/, cache/, projects/, …  empty directories
//
// Returns the sandbox path and a cleanup function that removes it.
// The caller must always invoke cleanup (typically via defer).
func prepareClaude(src string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "inner-claude-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating claude sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	// ── Required: auth credentials ────────────────────────────────────────────
	credSrc := filepath.Join(src, ".credentials.json")
	credDst := filepath.Join(tmp, ".credentials.json")
	if err := copyFile(credSrc, credDst); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copying .credentials.json: %w (is claude authenticated?)", err)
	}

	// ── Optional: user settings ───────────────────────────────────────────────
	// Copy settings.json but strip keys that would start external processes
	// (plugins, MCP servers) which hang inside the sandbox.
	_ = copySettingsStripped(filepath.Join(src, "settings.json"), filepath.Join(tmp, "settings.json"))

	// ── Optional: skill definitions ───────────────────────────────────────────
	_ = copyDir(filepath.Join(src, "skills"), filepath.Join(tmp, "skills"))

	// ── Fresh empty directories ───────────────────────────────────────────────
	freshDirs := []string{
		"backups", "cache", "debug", "downloads", "file-history",
		"paste-cache", "plans", "plugins", "projects", "session-env",
		"sessions", "shell-snapshots", "tasks", "telemetry", "todos",
	}
	for _, d := range freshDirs {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("creating %s in claude sandbox: %w", d, err)
		}
	}

	return tmp, cleanup, nil
}

// applyClaude replaces any mount whose Src is ~/.claude with a sandboxed
// temporary clone. Returns the cleanup function; the caller must defer it.
func applyClaude(rc *config.RunConfig) (func(), error) {
	return applyClaudeDir(rc, claudeHomeDir())
}

// applyClaudeDir is the testable core of applyClaude: it matches mounts by
// comparing their Src against claudeDir instead of the real ~/.claude path.
func applyClaudeDir(rc *config.RunConfig, claudeDir string) (func(), error) {
	var cleanups []func()

	for i, m := range rc.Mounts {
		if m.Src != claudeDir {
			continue
		}
		sandboxed, cleanup, err := prepareClaude(m.Src)
		if err != nil {
			for _, fn := range cleanups {
				fn()
			}
			return nil, err
		}
		cleanups = append(cleanups, cleanup)
		rc.Mounts[i].Src = sandboxed
	}

	// Bind ~/.claude.json writable so claude can update UI state (numStartups,
	// tips history, …) regardless of whether the workdir makes the home
	// directory writable.  Without this, running with -w <subdir> leaves the
	// home read-only and claude hangs trying to write the file at startup.
	claudeJsonPath := claudeDir + ".json"
	if _, err := os.Stat(claudeJsonPath); err == nil {
		rc.Mounts = append(rc.Mounts, config.Mount{
			Src:  claudeJsonPath,
			Dest: claudeJsonPath,
			Mode: "rw",
		})
	}

	return func() {
		for _, fn := range cleanups {
			fn()
		}
	}, nil
}

// ── File / dir copy helpers ───────────────────────────────────────────────────

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode())
}

// copySettingsStripped copies src to dst as JSON with keys that would start
// external processes (enabledPlugins, mcpServers) removed. These cause MCP
// servers to be launched at interactive startup, which hangs inside the sandbox.
// If src doesn't exist or can't be parsed, dst is left as a minimal empty object.
func copySettingsStripped(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	delete(settings, "enabledPlugins")
	delete(settings, "mcpServers")
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		dstPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		return copyFile(path, dstPath)
	})
}
