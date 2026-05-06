package main

import "github.com/enr/inner/internal/config"

// Capability bundles the runtime handler and the static description for a
// named tool integration (e.g. "claude", "gemini", "cursor").
//
// Apply is called by applyCapabilities at sandbox start-up. It modifies rc
// in-place (injecting mounts) and returns a cleanup function that removes
// temporary directories when the sandbox exits.
//
// Explain is a pure function (no I/O) that returns a human-readable
// description of what Apply injects. Used by `profile show --explain`.
type Capability struct {
	Apply   func(rc *config.RunConfig) (cleanup func(), err error)
	Explain func() CapabilityExplain
}

// capabilityRegistry maps capability names (as declared in profile
// capabilities = [...]) to their handler. The validator guarantees that only
// names present here reach Apply, so the map look-up never needs a guard.
var capabilityRegistry = map[string]Capability{
	"claude": {Apply: applyClaude, Explain: claudeExplain},
	"gemini": {Apply: applyGemini, Explain: geminiExplain},
	"cursor": {Apply: applyCursor, Explain: cursorExplain},
}

// CapabilityMount describes a single filesystem mount injected by a capability.
// Src and Dest use ~ notation (not expanded); Mode is "rw" or "ro".
// Detail is a human-readable description of what is copied vs left fresh.
type CapabilityMount struct {
	Src    string // host path, e.g. "~/.claude"
	Dest   string // sandbox destination
	Mode   string // "rw" or "ro"
	Detail string // e.g. "copies: .credentials.json, settings.json, skills/"
}

// CapabilityExplain is the static description returned by a capability's
// Explain function. It lists the mounts injected at runtime, any pre-run
// actions (e.g. token refresh), and free-form notes.
type CapabilityExplain struct {
	Mounts []CapabilityMount
	PreRun []string // human-readable pre-run actions
	Notes  []string
}

// ── Explain functions (pure, no I/O) ─────────────────────────────────────────

// claudeExplain returns the static description of what the "claude" capability
// injects at runtime.
func claudeExplain() CapabilityExplain {
	return CapabilityExplain{
		Mounts: []CapabilityMount{
			{
				Src:    "~/.claude",
				Dest:   "~/.claude",
				Mode:   "rw",
				Detail: "copies: .credentials.json, settings.json (mcpServers stripped), skills/; fresh: sessions/, cache/, projects/, …",
			},
			{
				Src:    "~/.claude.json",
				Dest:   "~/.claude.json",
				Mode:   "rw",
				Detail: "temp copy for UI state (numStartups, tips history, …)",
			},
		},
		PreRun: []string{
			"Token refresh / credential unlock: runs 'claude -p /try-login' in background (output hidden) to trigger the OS keyring graphical unlock dialog and refresh any expired OAuth token; inner then waits for Enter before continuing (skip with --yes)",
			"Near-expiry warning: prints a warning when the token will expire within 30 minutes so the user can plan for re-authentication before starting a long session",
			"D-Bus passthrough: inherits DBUS_SESSION_BUS_ADDRESS into the sandbox so Claude's libsecret can reach the OS keyring for mid-session token refresh; prevents 401 errors during long sessions",
		},
	}
}

// geminiExplain returns the static description of what the "gemini" capability
// injects at runtime.
func geminiExplain() CapabilityExplain {
	return CapabilityExplain{
		Mounts: []CapabilityMount{
			{
				Src:    "~/.gemini",
				Dest:   "~/.gemini",
				Mode:   "rw",
				Detail: "copies: settings.json; auth via GEMINI_API_KEY env var",
			},
		},
	}
}

// cursorExplain returns the static description of what the "cursor" capability
// injects at runtime.
func cursorExplain() CapabilityExplain {
	return CapabilityExplain{
		Mounts: []CapabilityMount{
			{
				Src:    "~/.cursor",
				Dest:   "~/.cursor",
				Mode:   "rw",
				Detail: "copies: cli-config.json, skills-cursor/; fresh: ai-tracking/, extensions/, projects/",
			},
			{
				Src:    "~/.config/cursor",
				Dest:   "~/.config/cursor",
				Mode:   "rw",
				Detail: "copies: auth.json (OAuth token)",
			},
		},
	}
}
