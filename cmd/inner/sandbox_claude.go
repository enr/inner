package main

import (
	"bufio"
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

// hostHome captures the real user home directory at process start, before any
// test or runtime code can alter the HOME environment variable. It is used by
// unlockClaudeCredentials so the subprocess always targets the host credential
// store and never a test-injected fake home.
var hostHome = os.Getenv("HOME")

// claudeWarningWriter is the destination for applyClaude warnings (e.g. "claude
// not found in PATH"). It defaults to os.Stderr; integration tests redirect it
// to io.Discard so that expected warnings do not pollute test output.
var claudeWarningWriter io.Writer = os.Stderr

// claudeAutoConfirmDelay is the time unlockClaudeCredentials waits in
// autoConfirm mode before killing the subprocess. Override in tests to avoid
// the 1-second sleep.
var claudeAutoConfirmDelay = 1 * time.Second

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
// Example prompt:  (inner) enrico@myprofile:~/project $
//
// Colour codes (bash \[…\] non-printing wrappers):
//   - bold yellow  →  (inner)
//   - bold green   →  user@profile
//   - bold blue    →  working directory
func sandboxPS1(profileName string) string {
	return `\[\033[1;33m\](inner)\[\033[0m\] \[\033[1;32m\]\u@` + profileName + `\[\033[0m\]:\[\033[1;34m\]\w\[\033[0m\]\$ `
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

// claudeTokenExpiresWithin reports whether the OAuth token in credPath will
// expire within d from now. Returns false if the file cannot be read, cannot
// be parsed, or contains no recognisable expiry field.
func claudeTokenExpiresWithin(credPath string, d time.Duration) bool {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	ts := extractExpiresAt(raw)
	if ts == 0 {
		return false
	}
	return time.Until(time.Unix(ts, 0)) < d
}

// extractExpiresAt looks for an expiry timestamp in the decoded credential
// object and returns it as Unix seconds. It recognises:
//   - "expiresAt"                  top-level, milliseconds  (Claude Code current format)
//   - "expires_at"                 top-level, seconds        (older / alternative format)
//   - "oauth_token.expires_at"     nested under "oauth_token", seconds
//   - "<any-key>.expiresAt"        nested under any top-level object (e.g. "claudeAiOauth")
//   - "<any-key>.expires_at"       nested under any top-level object
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
	// Scan all top-level keys that decode to objects — handles both the legacy
	// "oauth_token" shape and the current "claudeAiOauth" shape.
	for _, val := range raw {
		var obj map[string]json.RawMessage
		if json.Unmarshal(val, &obj) != nil {
			continue
		}
		for _, field := range []string{"expiresAt", "expires_at"} {
			if v, ok := obj[field]; ok {
				var ts int64
				if json.Unmarshal(v, &ts) == nil && ts > 0 {
					return normaliseTimestamp(ts)
				}
			}
		}
	}
	return 0
}

// msThreshold is the boundary above which a timestamp is treated as
// milliseconds rather than seconds. 1e10 seconds ≈ year 2286, so any value
// above this is unambiguously a millisecond timestamp for the foreseeable future.
const msThreshold int64 = 10_000_000_000

// normaliseTimestamp converts a timestamp to Unix seconds.
// Values above msThreshold are assumed to be milliseconds (Claude Code stores ms).
func normaliseTimestamp(ts int64) int64 {
	if ts > msThreshold {
		return ts / 1000
	}
	return ts
}

// formatTokenExpiry returns a human-readable expiry string from a credentials
// file, e.g. "2026-04-11 02:13 UTC". Returns "" if the file cannot be read or
// contains no recognisable expiry field.
func formatTokenExpiry(credPath string) string {
	data, err := os.ReadFile(credPath)
	if err != nil {
		return ""
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	ts := extractExpiresAt(raw)
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04 UTC")
}

// unlockClaudeCredentials runs "claude -p '/try-login'" in the background on
// the host. The process output is hidden; its purpose is solely to trigger the
// OS native credential storage (keyring/Keychain/libsecret) graphical unlock
// dialog before inner copies .credentials.json into the sandbox.
//
// credPath is the path to ~/.claude/.credentials.json used to display the
// token expiry in the interactive prompt.
//
// After launching:
//   - normal mode: inner prints a message and waits for the user to press Enter
//     (or up to 800 seconds as a safety timeout), then kills the process.
//   - autoConfirm mode (--yes): inner waits 1 second for the keyring dialog to
//     be triggered, then kills the process and proceeds without user input.
//
// Errors are reported as warnings, not failures.
func unlockClaudeCredentials(w io.Writer, autoConfirm bool, credPath string) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(w, colorizeW(w, ansiBoldYellow, "inner: warning")+": claude not found in PATH; cannot unlock credentials")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudePath, "-p", "/try-login")
	// Explicitly pin HOME to the host home captured at process start so that
	// test code (which overrides HOME with a temp dir) cannot redirect the
	// credential unlock to a fake environment.
	cmd.Env = append(append([]string{}, os.Environ()...), "HOME="+hostHome)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "inner: warning")+": could not start claude for credential unlock: %v\n", err)
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // reap to avoid leaving a zombie process
	}()

	if autoConfirm {
		// Allow enough time for the OS keyring dialog to be triggered before
		// killing the process and proceeding.
		time.Sleep(claudeAutoConfirmDelay)
		return
	}

	msg := "inner: enter your password in the system dialog if prompted, then press Enter to continue"
	if expiry := formatTokenExpiry(credPath); expiry != "" {
		msg = "inner: Claude token expires " + expiry + " — enter your password in the system dialog if prompted, then press Enter to continue"
	}
	fmt.Fprintln(w, msg)

	// Block until Enter. bufio.ReadString drains the full line so no stray
	// bytes leak into the subsequent sandbox stdin. The subprocess is killed
	// by the 800-second context timeout if still running, but we keep waiting
	// here regardless — use --yes to skip this prompt entirely.
	bufio.NewReader(os.Stdin).ReadString('\n') //nolint:errcheck
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

	// Fail fast if the token is still expired at copy time (applyClaude already
	// attempted an unlock/refresh before calling prepareClaude).
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

// tokenNearExpiryThreshold is the window within which a still-valid token is
// considered "near expiry". When the token will expire within this duration,
// applyClaude prints a warning so the user knows to plan for re-authentication.
const tokenNearExpiryThreshold = 30 * time.Minute

// applyClaude injects the claude sandbox mounts into rc.
// It creates a sandboxed temporary clone of ~/.claude and — when ~/.claude.json
// exists on the host — a temporary copy of that file, then appends both mounts
// to rc.Mounts.
//
// Credential unlock: triggered when the OAuth token is expired or unreadable.
// A near-expiry warning is printed when the token will expire within
// tokenNearExpiryThreshold. Warnings are written to os.Stderr.
//
// D-Bus passthrough: DBUS_SESSION_BUS_ADDRESS is inherited into the sandbox so
// that Claude's libsecret can reach the OS keyring for mid-session token
// refresh. Without it, an expired token inside a long session causes a 401
// because the sandbox environment is otherwise fully cleared.
func applyClaude(rc *config.RunConfig) (func(), error) {
	claudeDir := claudeHomeDir()
	w := claudeWarningWriter

	credPath := filepath.Join(claudeDir, ".credentials.json")

	// ── Diagnose token state before doing anything ────────────────────────────
	expired, expiryErr := claudeTokenExpired(credPath)
	expiry := formatTokenExpiry(credPath)

	switch {
	case expiryErr != nil:
		// File missing or unreadable — cannot determine state.
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "inner: warning")+": cannot read credentials: %v — attempting unlock\n", expiryErr)
		unlockClaudeCredentials(w, rc.AutoConfirm, credPath)
	case expiry == "":
		// File exists but has no recognisable expiry field — be optimistic but
		// log it so we can understand what format we're seeing.
		fmt.Fprintln(w, "inner: claude: credentials present but expiry field not found — skipping unlock (optimistic)")
	case expired:
		// Token is detectably expired — run the unlock/refresh flow.
		fmt.Fprintf(w, "inner: claude: token expired at %s — attempting unlock/refresh\n", expiry)
		unlockClaudeCredentials(w, rc.AutoConfirm, credPath)
		// After the unlock attempt, fail fast if the token is still expired so
		// the sandbox does not start and receive a 401.
		if stillExpired, _ := claudeTokenExpired(credPath); stillExpired {
			return nil, fmt.Errorf(
				"Claude OAuth token is expired and could not be refreshed automatically.\n" +
					"Run 'claude' on the host machine to renew it, then relaunch inner.",
			)
		}
	case claudeTokenExpiresWithin(credPath, tokenNearExpiryThreshold):
		// Token is still valid but expires very soon. Warn so the user can
		// plan: if the session outlasts the token and the in-sandbox refresh
		// fails for any reason, Claude will return 401.
		fmt.Fprintf(w, "inner: claude: token expires soon (%s) — consider relaunching after renewal if the session is expected to run past expiry\n", expiry)
	default:
		// Token is fresh — skip unlock entirely.
		fmt.Fprintf(w, "inner: claude: token valid (expires %s) — skipping unlock\n", expiry)
	}

	// ── D-Bus passthrough for mid-session token refresh ───────────────────────
	// The sandbox clears the environment. Without DBUS_SESSION_BUS_ADDRESS,
	// Claude's libsecret cannot locate the keyring daemon and cannot refresh
	// an expired OAuth token mid-session, causing a 401 after long sessions.
	// We inherit only this one variable (not XDG_RUNTIME_DIR) to minimise the
	// attack surface: the D-Bus socket is already reachable via the ro-bind of
	// /, and connect(2) on a Unix socket does not require write permission on
	// the socket file itself.
	if v := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); v != "" {
		rc.Env.Inherit = append(rc.Env.Inherit, "DBUS_SESSION_BUS_ADDRESS")
	}

	var cleanups []func()

	fmt.Fprintf(w, "inner: claude: preparing sandbox clone of %s\n", claudeDir)
	sandboxed, cleanup, err := prepareClaude(claudeDir)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(w, "inner: claude: sandbox dir: %s\n", sandboxed)
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
		fmt.Fprintf(w, "inner: claude: mounted .claude.json (temp copy: %s)\n", tmpJsonPath)
	} else {
		fmt.Fprintln(w, "inner: claude: .claude.json not found on host — skipping mount")
	}

	fmt.Fprintln(w, "inner: claude: sandbox ready — launching")
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
