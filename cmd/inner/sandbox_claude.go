package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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

	// Pre-populate shell history so the user can recall useful commands
	// immediately with the up-arrow key. Commands are injected oldest-first
	// so the last entry in the list sits at the top of the history stack.
	for _, cmd := range rc.Entrypoint.History {
		content += "history -s " + fmt.Sprintf("%q", cmd) + "\n"
	}

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

// claudeTokenExpired reports whether the OAuth token in credPath appears to be
// expired based on a Unix-timestamp "expires_at" field in the JSON. Returns
// (false, nil) if the file cannot be read, cannot be parsed, or contains no
// recognisable expiry field — the caller must treat an indeterminate result as
// "not expired" (be optimistic).
func claudeTokenExpired(credPath string) (bool, error) {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return false, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, nil // unparseable — be optimistic
	}
	ts := extractExpiresAt(raw)
	if ts == 0 {
		return false, nil // no recognisable expiry field — be optimistic
	}
	return time.Now().Unix() >= ts, nil
}

// extractExpiresAt looks for an expiry timestamp in the decoded credential
// object and returns it as Unix seconds. It recognises:
//   - "expiresAt"         top-level, milliseconds  (Claude Code current format)
//   - "expires_at"        top-level, seconds        (older / alternative format)
//   - "oauth_token.expires_at"  nested, seconds
//
// Values > 1e10 are treated as milliseconds and divided by 1000.
func extractExpiresAt(raw map[string]json.RawMessage) int64 {
	if v, ok := raw["expiresAt"]; ok {
		var ts int64
		if json.Unmarshal(v, &ts) == nil && ts > 0 {
			return normaliseTimestamp(ts)
		}
	}
	if v, ok := raw["expires_at"]; ok {
		var ts int64
		if json.Unmarshal(v, &ts) == nil && ts > 0 {
			return normaliseTimestamp(ts)
		}
	}
	if nested, ok := raw["oauth_token"]; ok {
		var obj map[string]json.RawMessage
		if json.Unmarshal(nested, &obj) == nil {
			if v, ok := obj["expires_at"]; ok {
				var ts int64
				if json.Unmarshal(v, &ts) == nil && ts > 0 {
					return normaliseTimestamp(ts)
				}
			}
		}
	}
	return 0
}

// normaliseTimestamp converts a timestamp to Unix seconds.
// Values above 1e10 are assumed to be milliseconds (Claude Code stores ms).
func normaliseTimestamp(ts int64) int64 {
	if ts > 10_000_000_000 {
		return ts / 1000
	}
	return ts
}

// ensureClaudeTokenFresh runs "claude --version" on the host with a short
// timeout so that Claude's startup sequence can silently refresh an expired
// OAuth token before the sandbox copies .credentials.json. This is a
// best-effort operation: errors are reported as warnings, not failures.
func ensureClaudeTokenFresh(w io.Writer) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(w, "inner: warning: claude not found in PATH; cannot pre-refresh token")
		return
	}
	fmt.Fprintln(w, "inner: OAuth token expired — refreshing via host claude...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudePath, "--version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "inner: warning: host claude exited with error: %v\n", err)
	}
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

	// (Option A) If the token is detectably expired at this point
	// (ensureClaudeTokenFresh was already attempted), fail fast with an
	// actionable message rather than letting the sandbox start and receive a 401.
	if expired, err := claudeTokenExpired(credSrc); err == nil && expired {
		cleanup()
		return "", nil, fmt.Errorf(
			"Claude OAuth token is expired and could not be refreshed automatically.\n" +
				"Run 'claude' on the host machine to renew it, then relaunch inner.",
		)
	}

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

// applyClaude injects the claude sandbox mounts into rc.
// It creates a sandboxed temporary clone of ~/.claude and — when ~/.claude.json
// exists on the host — a temporary copy of that file, then appends both mounts
// to rc.Mounts.
//
// Token refresh: if the OAuth token is detectably expired, "claude --version"
// is run on the host before copying .credentials.json so the freshest token is
// always captured in the sandbox. Warnings are written to os.Stderr.
func applyClaude(rc *config.RunConfig) (func(), error) {
	claudeDir := claudeHomeDir()

	// Pre-flight: attempt a token refresh if the stored token is expired.
	credPath := filepath.Join(claudeDir, ".credentials.json")
	if expired, _ := claudeTokenExpired(credPath); expired {
		ensureClaudeTokenFresh(os.Stderr)
		// Re-read the file: if the token is still expired after the refresh
		// attempt, fail fast with an actionable message rather than letting
		// the sandbox start and receive a 401.
		if stillExpired, _ := claudeTokenExpired(credPath); stillExpired {
			return nil, fmt.Errorf(
				"Claude OAuth token is expired and could not be refreshed automatically.\n" +
					"Run 'claude' on the host machine to renew it, then relaunch inner.",
			)
		}
	}

	var cleanups []func()

	sandboxed, cleanup, err := prepareClaude(claudeDir)
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, cleanup)
	rc.Mounts = append(rc.Mounts, config.Mount{
		Src:  sandboxed,
		Dest: claudeDir,
		Mode: "rw",
	})

	// Bind ~/.claude.json writable so claude can update UI state (numStartups,
	// tips history, …) regardless of whether the workdir makes the home
	// directory writable.  Without this, running with -w <subdir> leaves the
	// home read-only and claude hangs trying to write the file at startup.
	// We mount a temporary copy so the real file is never modified by the sandbox.
	claudeJsonPath := claudeDir + ".json"
	if _, err := os.Stat(claudeJsonPath); err == nil {
		tmpJson, err := os.CreateTemp("", "inner-claude-*.json")
		if err != nil {
			for _, fn := range cleanups {
				fn()
			}
			return nil, fmt.Errorf("creating claude.json sandbox copy: %w", err)
		}
		tmpJson.Close()
		tmpJsonPath := tmpJson.Name()
		cleanups = append(cleanups, func() { os.Remove(tmpJsonPath) })
		if err := copyFile(claudeJsonPath, tmpJsonPath); err != nil {
			for _, fn := range cleanups {
				fn()
			}
			return nil, fmt.Errorf("copying .claude.json: %w", err)
		}
		rc.Mounts = append(rc.Mounts, config.Mount{
			Src:  tmpJsonPath,
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
