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
	// NetworkOff and routes its HTTP(S) traffic through a host-side proxy that
	// enforces a domain allowlist.
	//
	// RESERVED, NOT IMPLEMENTED YET (see network.md / NONO_COMPARISON.md §S2).
	// The name is defined here so the merge and resolution rules below are
	// written once and stay stable; the validator rejects it until the proxy
	// ships. Every consumer that switches on a mode must treat it as
	// fail-closed (no network) rather than falling through to NetworkFull.
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
	"claude": {
		// Needed to work at all.
		"api.anthropic.com",
		"console.anthropic.com",
		// Feature flags; the CLI degrades but keeps working without it.
		"statsig.anthropic.com",
		// Telemetry. Safe to drop with network_deny = ["sentry.io"].
		"sentry.io",
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
