// Package netproxy implements the host-side proxy that mediates network access
// for a sandbox running with network_mode = "allowlist" (see network.md).
//
// This file is the pure decision layer: no sockets, no DNS, no I/O. It exists
// separately from the server for two reasons. It is exhaustively testable on
// its own, which matters because every one of the pattern rules below is either
// a bypass or a false negative when it is wrong. And it makes the ORDER of the
// decision chain structural rather than a comment: AllowsHost takes a Target
// (a name), AllowsAddr takes a net.IP (a resolved address), so a caller that
// resolves before consulting the allowlist has to write code that visibly does
// that, instead of getting it wrong by accident.
//
// The chain the server must run, in this order:
//
//  1. ParseTarget    — normalise and reject anything malformed.
//  2. AllowsHost     — the allowlist gate. REJECT HERE, BEFORE RESOLVING.
//  3. resolve        — once, via the injectable resolver.
//  4. AllowsAddr     — the always-deny address list, on every resolved IP.
//  5. dial the validated IP literal — never re-resolve.
//
// Step 2 before step 3 is not a preference. Resolving first turns the proxy
// into a DNS exfiltration channel: the sandbox asks to CONNECT to
// <secret-in-base32>.attacker.com, the proxy resolves it — a real query leaving
// the machine towards the attacker's nameserver — and only then refuses the TCP
// connection, by which point the data is already out.
package netproxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// defaultPorts are the ports a bare allowlist entry authorises.
//
// Without this, network_allow = ["github.com"] would also authorise
// CONNECT github.com:22 — a full SSH push channel on a profile that may well
// also allow ssh-keys. An entry that needs another port says so explicitly
// ("github.com:9418").
var defaultPorts = []int{80, 443}

// maxHostLen and maxLabelLen are the DNS limits, enforced so a pathological
// name cannot be used to probe for parser differences between us and the
// resolver.
const (
	maxHostLen  = 253
	maxLabelLen = 63
)

// Target is a normalised, syntactically valid CONNECT target.
//
// Normalisation is part of the security boundary, not cosmetics: "API.GitHub.com",
// "api.github.com." and "api.github.com" are the same name to a resolver, so
// they must be the same name to the matcher too.
type Target struct {
	// Host is the lower-cased, trailing-dot-stripped hostname, or the textual
	// form of an IP literal.
	Host string
	// IP is non-nil when Host was an IP literal rather than a name. A literal
	// target needs no resolution step, but still goes through AllowsAddr.
	IP net.IP
	// Port is the requested TCP port.
	Port int
}

// String renders the target the way it appeared, for log messages.
func (t Target) String() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// DenyError is returned for any request the policy refuses. Reason is meant to
// be shown to the user (on stderr, and as the body of the 403), so it says what
// was refused and why, never just "denied".
type DenyError struct {
	Target string
	Reason string
}

func (e *DenyError) Error() string {
	return fmt.Sprintf("blocked %s: %s", e.Target, e.Reason)
}

// ParseTarget normalises a "host:port" CONNECT target.
//
// Hostnames must be ASCII LDH (letters, digits, hyphen, separated by dots).
// A non-ASCII name is REJECTED, not converted: correct IDN handling means
// punycode normalisation on both the request and the config, which means an
// idna dependency on a security-critical normaliser. Rejecting is honest,
// testable and fail-closed — see network.md.
func ParseTarget(hostport string) (Target, error) {
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return Target{}, &DenyError{Target: hostport, Reason: "malformed target (want host:port)"}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return Target{}, &DenyError{Target: hostport, Reason: fmt.Sprintf("invalid port %q", portStr)}
	}

	host = normaliseHost(host)
	if host == "" {
		return Target{}, &DenyError{Target: hostport, Reason: "empty host"}
	}

	// An IP literal is a valid target: it skips resolution, not validation.
	if ip := net.ParseIP(host); ip != nil {
		return Target{Host: ip.String(), IP: ip, Port: port}, nil
	}
	if err := validHostname(host); err != nil {
		return Target{}, &DenyError{Target: hostport, Reason: err.Error()}
	}
	return Target{Host: host, Port: port}, nil
}

// normaliseHost lower-cases a host and strips one trailing dot. The trailing
// dot is a fully-qualified name that resolves identically, so leaving it on
// would let "api.anthropic.com." slip past an entry for "api.anthropic.com".
func normaliseHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	// Only ONE trailing dot is meaningful; "host.." is malformed and is left
	// alone so validHostname rejects it (it produces an empty final label).
	if strings.HasSuffix(host, ".") && !strings.HasSuffix(host, "..") {
		host = host[:len(host)-1]
	}
	return host
}

// validHostname enforces ASCII LDH syntax.
func validHostname(host string) error {
	if len(host) > maxHostLen {
		return fmt.Errorf("hostname longer than %d characters", maxHostLen)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return fmt.Errorf("hostname %q has an empty label", host)
		}
		if len(label) > maxLabelLen {
			return fmt.Errorf("hostname %q has a label longer than %d characters", host, maxLabelLen)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("hostname %q has a label starting or ending with '-'", host)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			default:
				return fmt.Errorf("hostname %q is not ASCII letters/digits/hyphen (IDN names are not supported)", host)
			}
		}
	}
	return nil
}

// Policy decides which targets the sandbox may reach.
type Policy struct {
	// Allow is the effective allowlist: the union of the capability defaults,
	// the profile's network_allow along the extends chain, and the CLI. See
	// network.md → Allowlist layers.
	Allow []string
	// Deny is subtracted from that union (network_deny), so a profile can drop
	// a domain a capability contributed without losing the rest. A bare host in
	// Deny denies EVERY port, not just the default ones: a subtraction being
	// broader than the corresponding addition is the safe direction.
	Deny []string

	// AllowPrivateDestinations disables the address checks in AllowsAddr.
	//
	// It exists so the test suite can point the proxy at a loopback listener:
	// every address a developer machine or a CI runner can bind is loopback or
	// RFC1918, i.e. exactly what those checks reject, so without this seam the
	// end-to-end test cannot exist at all.
	//
	// It is a struct field on purpose, and it MUST stay one: no TOML key, no
	// environment variable. An env var would let a compromised profile — or a
	// wrapper script around inner — turn off the one part of the policy that no
	// configuration is supposed to be able to weaken.
	// TestNoConfigSurfaceEnablesPrivateDestinations guards that invariant.
	AllowPrivateDestinations bool
}

// AllowsHost is step 2 of the decision chain: the allowlist gate, applied to
// the NAME, before anything is resolved.
func (p Policy) AllowsHost(t Target) error {
	for _, pattern := range p.Deny {
		if matches(pattern, t, true) {
			return &DenyError{Target: t.String(), Reason: fmt.Sprintf("denied by network_deny entry %q", pattern)}
		}
	}
	for _, pattern := range p.Allow {
		if matches(pattern, t, false) {
			return nil
		}
	}
	return &DenyError{Target: t.String(), Reason: "not in the allow list"}
}

// matches reports whether an allow/deny pattern covers a target.
//
// Pattern forms, all matched case-insensitively against the already-normalised
// target:
//
//	example.com          exactly that name, on the default ports
//	example.com:8443     exactly that name, on exactly that port
//	*.example.com        any subdomain at ANY depth, but NOT the apex
//	*.example.com:8443   the same, on exactly that port
//	10.0.0.7 / [::1]     an IP literal, compared as a parsed address
//
// The wildcard deliberately excludes the apex: a profile that wants both lists
// both. Explicit is worth the extra line here — the alternative silently widens
// every wildcard entry by one name.
//
// denyMode makes a portless pattern cover every port (see Policy.Deny).
func matches(pattern string, t Target, denyMode bool) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}

	patHost, patPort, hasPort := splitPatternPort(pattern)
	patHost = normaliseHost(patHost)
	if patHost == "" {
		return false
	}

	if hasPort {
		if patPort != t.Port {
			return false
		}
	} else if !denyMode && !portAllowedByDefault(t.Port) {
		return false
	}

	// IP-literal patterns are compared as parsed addresses, so neither
	// "127.1" nor "::ffff:127.0.0.1" can be smuggled past a textual entry.
	if patIP := net.ParseIP(patHost); patIP != nil {
		return t.IP != nil && t.IP.Equal(patIP)
	}
	// A name pattern never matches an IP-literal target.
	if t.IP != nil {
		return false
	}

	if suffix, ok := strings.CutPrefix(patHost, "*."); ok {
		return strings.HasSuffix(t.Host, "."+suffix) && len(t.Host) > len(suffix)+1
	}
	return t.Host == patHost
}

// splitPatternPort separates a trailing ":port" from a pattern, leaving
// bracketed IPv6 literals and bare IPv6 literals alone.
func splitPatternPort(pattern string) (host string, port int, hasPort bool) {
	if strings.HasPrefix(pattern, "[") {
		// [::1] or [::1]:443
		if h, p, err := net.SplitHostPort(pattern); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				return h, n, true
			}
			return pattern, 0, false
		}
		return strings.Trim(pattern, "[]"), 0, false
	}
	i := strings.LastIndex(pattern, ":")
	if i < 0 {
		return pattern, 0, false
	}
	// More than one colon and no brackets means a bare IPv6 literal, not a port.
	if strings.Count(pattern, ":") > 1 {
		return pattern, 0, false
	}
	n, err := strconv.Atoi(pattern[i+1:])
	if err != nil || n < 1 || n > 65535 {
		return pattern, 0, false
	}
	return pattern[:i], n, true
}

func portAllowedByDefault(port int) bool {
	for _, p := range defaultPorts {
		if p == port {
			return true
		}
	}
	return false
}

// cgnat is RFC 6598 shared address space (100.64.0.0/10): carrier-grade NAT,
// and the range Tailscale hands out. Go's net.IP.IsPrivate does not cover it.
var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// AllowsAddr is step 4 of the decision chain: the always-deny address list,
// applied to every resolved IP. It is independent of Allow and cannot be
// overridden by profile or capability config.
//
// This exists because the proxy runs on the HOST, in the host network
// namespace. Turning it on does not only narrow what the sandbox can reach — it
// re-attaches a previously fully-isolated namespace to the host's stack. Without
// these checks the sandbox would GAIN access to the Docker API on
// 127.0.0.1:2375, a local Ollama, the LAN router, the NAS, and the cloud
// metadata endpoint: a new capability handed out by a feature whose whole
// purpose is to take capabilities away.
//
// The test is positive — reject unless the address is ordinary global unicast —
// rather than an enumeration of CIDRs, so an address family or range nobody
// thought of fails closed instead of falling through.
func (p Policy) AllowsAddr(ip net.IP) error {
	if p.AllowPrivateDestinations {
		return nil
	}
	if ip == nil {
		return &DenyError{Target: "<nil>", Reason: "unresolvable address"}
	}
	// Normalise IPv4-mapped IPv6 (::ffff:169.254.169.254) to its 4-byte form
	// first. Without this an IPv4-only range test walks straight past it.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	switch {
	case ip.IsUnspecified():
		return denyAddr(ip, "unspecified address")
	case ip.IsLoopback():
		// Reached through the host's own stack, so this is every service the
		// user runs on localhost.
		return denyAddr(ip, "loopback address (host-local services)")
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// Covers 169.254.169.254, the AWS/GCP/Azure/DO metadata endpoint.
		return denyAddr(ip, "link-local address (cloud metadata endpoint)")
	case ip.IsPrivate():
		// RFC 1918 and RFC 4193 ULA — the latter covers fd00:ec2::254, the
		// AWS IMDSv6 endpoint.
		return denyAddr(ip, "private address (LAN or unique-local)")
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return denyAddr(ip, "multicast address")
	case len(ip) == net.IPv4len && ip[0] == 0:
		// 0.0.0.0/8: only 0.0.0.0 itself is "unspecified" to Go.
		return denyAddr(ip, "reserved 0.0.0.0/8 address")
	case len(ip) == net.IPv4len && cgnat.Contains(ip):
		return denyAddr(ip, "shared address space 100.64.0.0/10 (CGNAT, Tailscale)")
	case !ip.IsGlobalUnicast():
		return denyAddr(ip, "not a global unicast address")
	}
	return nil
}

func denyAddr(ip net.IP, reason string) error {
	return &DenyError{Target: ip.String(), Reason: reason}
}
