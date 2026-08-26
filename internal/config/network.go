package config

import (
	"slices"
	"strings"
)

// Network model values accepted in [sandbox] network_mode.
// See SandboxConfig.NetworkMode.
const (
	// NetworkOff gives the sandbox a private, empty network namespace
	// (bwrap --unshare-net): no interface but loopback, so no outbound
	// connection can be made at all. Equivalent to the legacy network = false.
	NetworkOff = "off"
	// NetworkFull leaves the host network namespace in place: the sandboxed
	// process reaches anything the host can reach. Equivalent to the legacy
	// network = true.
	NetworkFull = "full"
	// NetworkAllowlist gives the sandbox a private network namespace like
	// NetworkOff, and routes its HTTP(S) traffic through a host-side proxy that
	// enforces a domain allowlist (see internal/netproxy and network.md).
	//
	// Every consumer that switches on a mode must treat anything that is not
	// NetworkFull as fail-closed (no direct network), so a mode a given binary
	// cannot enforce can never silently mean "open network".
	NetworkAllowlist = "allowlist"
)

// ValidNetworkModes is the exhaustive set of values accepted in
// [sandbox] network_mode. Empty (unset) means "resolve from the legacy
// network bool" — see ResolveNetworkMode.
var ValidNetworkModes = []string{NetworkOff, NetworkFull, NetworkAllowlist}

// NetworkModeFromBool maps the legacy [sandbox] network bool onto a mode.
func NetworkModeFromBool(enabled bool) string {
	if enabled {
		return NetworkFull
	}
	return NetworkOff
}

// ResolveNetworkMode returns the effective network model for a sandbox
// configuration. It is the single place the legacy fallback lives, used by the
// loader, the validator and `inner verify` — mirroring how an empty
// [sandbox] home means HomeHostRO.
//
// An explicit network_mode always wins; when it is empty the legacy
// network = true/false bool decides. mergeProfiles already collapses the two
// fields into a coherent pair along an extends chain (see the comment there),
// so by the time a merged profile reaches here there is nothing left to
// arbitrate.
func ResolveNetworkMode(sb SandboxConfig) string {
	if sb.NetworkMode != "" {
		return sb.NetworkMode
	}
	return NetworkModeFromBool(sb.Network)
}

// CapabilityNetworkAllow maps a capability to the egress destinations its tool
// needs, so a profile declaring capabilities = ["claude"] does not have to know
// which endpoints Claude Code talks to. It lives here rather than in the
// cmd/inner capability registry for the same reason CapabilityHostDirs does:
// the loader, the validator, printDryRun and `inner verify` all need to read it,
// and none of them can import package main.
//
// VERIFY BEFORE TRUSTING. These lists are vendor-published data that rots
// silently: when a tool adds an endpoint, the symptom inside the sandbox is an
// opaque connection error, not a clear message. Re-check the list against the
// vendor's current documentation whenever the matching capability is touched.
//
// A capability with no entry here contributes nothing, and the validator says
// so rather than letting the profile fail mysteriously at runtime: better an
// explicit "list the domains in network_allow" than a silent empty default.
// Only entries actually confirmed against vendor documentation belong here —
// an invented domain is worse than an absent one, because it looks verified.
var CapabilityNetworkAllow = map[string][]string{
	// Checked against Claude Code's "Network access requirements" table
	// (code.claude.com/docs/en/network-config) on 2026-08-26. Every entry here
	// is a row of that table; every row NOT here is listed in the comment at
	// the end, with the reason, so the next re-check compares two lists rather
	// than re-deriving the decision.
	"claude": {
		// Needed to work at all: model traffic, feature flags and the WebFetch
		// domain-safety preflight all go to the API host.
		"api.anthropic.com",
		// Sign-in and token lifecycle. platform.claude.com does the OAuth
		// exchange/refresh/revoke for BOTH claude.ai and Console accounts, so a
		// session that starts fine still 401s mid-run without it.
		"claude.ai",
		"claude.com",
		"platform.claude.com",
		// Update checks, the native updater, and plugin executables. Without it
		// the CLI works but reports "Auto-update failed".
		"downloads.claude.ai",
		// The changelog the CLI shows after updating, and /release-notes.
		"raw.githubusercontent.com",
		// MCP connectors from claude.ai, which are on by default for claude.ai
		// accounts. Drop with network_deny + ENABLE_CLAUDEAI_MCP_SERVERS=false.
		"mcp-proxy.anthropic.com",
		// Artifact content reads, when the Artifact tool is enabled for the
		// account. Drop with network_deny + CLAUDE_CODE_DISABLE_ARTIFACT=1.
		"*.frame.claudeusercontent.com",
		// Documentation lookups by the built-in claude-code-guide agent and by
		// pre-approved WebFetch requests.
		"code.claude.com",
		// Operational telemetry and error reports. Safe to drop with
		// network_deny = ["*.datadoghq.com", "browser-intake-us5-datadoghq.com"]
		// — or in the tool itself with DISABLE_TELEMETRY=1.
		"http-intake.logs.us5.datadoghq.com",
		"browser-intake-us5-datadoghq.com",

		// Deliberately NOT included, though the vendor table lists them:
		//
		//   storage.googleapis.com  plugin metadata, and the native installer
		//                           before v2.1.116. One name covering every
		//                           Google Cloud Storage bucket in existence is
		//                           too broad to hand out by default.
		//   registry.npmjs.org      plugin installs, npx-launched MCP servers,
		//                           npm/bun self-updates. A profile that wants
		//                           the sandbox installing packages says so.
		//   bridge.claudeusercontent.com  the Claude in Chrome bridge; there is
		//                           no browser extension inside the sandbox.
		//   formulae.brew.sh        Homebrew update checks; inner is Linux-only.
		//
		// Also removed on the 2026-08-26 re-check, having left that table:
		// statsig.anthropic.com and sentry.io (feature flags and error
		// reporting now go to api.anthropic.com and the Datadog intake hosts),
		// and console.anthropic.com (Console auth is platform.claude.com now).
		// A profile pinned to an older CLI can list them in network_allow.
	},
	// gemini / cursor / opencode: intentionally absent until their egress
	// domains are confirmed from vendor documentation. Profiles using them
	// under network_mode = "allowlist" must list the domains themselves.
}

// ResolveNetworkAllow computes the effective allow list for a run and records
// where each entry came from.
//
// The layers only ever ADD (see network.md → Allowlist layers):
//
//	L1  capability defaults    CapabilityNetworkAllow[name]
//	L2  base profile           network_allow, already unioned by mergeProfiles
//	L3  child profile          ditto
//
// so a profile inherits its capabilities' destinations and extends them, and
// never has to restate them. Narrowing is a separate, explicit act:
// [sandbox] network_deny, which is evaluated per-request by the proxy rather
// than subtracted here — that is what lets a deny pattern carve a hole out of a
// broader allow pattern.
//
// The returned origins map answers "why is this domain open?", which is the
// question a layered allowlist gets asked constantly. Values are
// comma-separated when more than one layer contributed the same entry, because
// "the profile also lists it" and "only the capability brings it" call for
// different edits.
func ResolveNetworkAllow(sb SandboxConfig, capabilities []string) (allow []string, origins map[string]string) {
	origins = make(map[string]string)

	add := func(entry, origin string) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return
		}
		if prev, seen := origins[entry]; seen {
			if !strings.Contains(prev, origin) {
				origins[entry] = prev + ", " + origin
			}
			return
		}
		origins[entry] = origin
		allow = append(allow, entry)
	}

	// Capabilities first, in a deterministic order: the effective list is shown
	// in --dry-run and compared in tests, so it must not depend on the order
	// the profile happened to list its capabilities in.
	caps := append([]string(nil), capabilities...)
	slices.Sort(caps)
	for _, name := range caps {
		for _, entry := range CapabilityNetworkAllow[name] {
			add(entry, "capability:"+name)
		}
	}
	for _, entry := range sb.NetworkAllow {
		add(entry, "profile")
	}
	return allow, origins
}

// CapabilitiesWithoutNetworkDefaults returns the capabilities that contribute
// no egress destinations, so callers can say which ones the profile still has
// to cover by hand.
func CapabilitiesWithoutNetworkDefaults(capabilities []string) []string {
	var missing []string
	for _, name := range capabilities {
		if len(CapabilityNetworkAllow[name]) == 0 {
			missing = append(missing, name)
		}
	}
	return missing
}
