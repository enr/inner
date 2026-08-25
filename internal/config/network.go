package config

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
