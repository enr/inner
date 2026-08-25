package netproxy

import (
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// ── ParseTarget ───────────────────────────────────────────────────────────────

func TestParseTarget_normalises(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
	}{
		{"api.anthropic.com:443", "api.anthropic.com", 443},
		{"API.Anthropic.COM:443", "api.anthropic.com", 443},
		// A single trailing dot is a fully-qualified name that resolves
		// identically, so it must normalise to the same string the config uses.
		{"api.anthropic.com.:443", "api.anthropic.com", 443},
		{"1.2.3.4:443", "1.2.3.4", 443},
		{"[2606:4700::1111]:443", "2606:4700::1111", 443},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTarget(tt.in)
			if err != nil {
				t.Fatalf("ParseTarget(%q) = %v", tt.in, err)
			}
			if got.Host != tt.wantHost || got.Port != tt.wantPort {
				t.Errorf("got %q:%d, want %q:%d", got.Host, got.Port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestParseTarget_rejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"no port", "api.anthropic.com"},
		{"empty host", ":443"},
		{"port zero", "api.anthropic.com:0"},
		{"port out of range", "api.anthropic.com:70000"},
		{"non-numeric port", "api.anthropic.com:https"},
		{"empty label", "api..anthropic.com:443"},
		{"double trailing dot", "api.anthropic.com..:443"},
		{"leading hyphen label", "-evil.example.com:443"},
		{"trailing hyphen label", "evil-.example.com:443"},
		{"underscore", "api_internal.example.com:443"},
		// IDN is refused rather than converted: correct handling needs punycode
		// normalisation on both sides, i.e. an idna dependency on a
		// security-critical normaliser. See network.md.
		{"unicode host", "exämple.com:443"},
		{"raw punycode-looking unicode", "аpple.com:443"}, // Cyrillic 'а'
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ParseTarget(tt.in); err == nil {
				t.Errorf("ParseTarget(%q) unexpectedly accepted: %+v", tt.in, got)
			}
		})
	}
}

// ── Pattern matching ──────────────────────────────────────────────────────────

func TestAllowsHost_patterns(t *testing.T) {
	tests := []struct {
		name    string
		allow   []string
		target  string
		allowed bool
	}{
		{"exact match", []string{"github.com"}, "github.com:443", true},
		{"exact match is not a suffix match", []string{"github.com"}, "evilgithub.com:443", false},
		{"exact match does not cover subdomains", []string{"github.com"}, "api.github.com:443", false},
		{"case-insensitive", []string{"GitHub.com"}, "github.com:443", true},
		{"trailing dot in pattern", []string{"github.com."}, "github.com:443", true},

		{"wildcard one level", []string{"*.githubusercontent.com"}, "raw.githubusercontent.com:443", true},
		{"wildcard any depth", []string{"*.githubusercontent.com"}, "a.b.githubusercontent.com:443", true},
		// The apex is deliberately excluded: a profile wanting both lists both.
		{"wildcard excludes the apex", []string{"*.example.com"}, "example.com:443", false},
		{"wildcard is not a plain suffix", []string{"*.example.com"}, "notexample.com:443", false},
		{"wildcard needs a non-empty label", []string{"*.example.com"}, "example.com:443", false},

		{"empty allow list denies", nil, "github.com:443", false},
		{"empty pattern is ignored", []string{""}, "github.com:443", false},

		{"ip literal", []string{"1.2.3.4"}, "1.2.3.4:443", true},
		{"ip literal, different address", []string{"1.2.3.4"}, "1.2.3.5:443", false},
		// Go rejects zero-padded octets, so this parses as a NAME, not an
		// address — and a name never matches an IP-literal entry. Either way it
		// is denied; pinned so a future parser change cannot silently make an
		// alternate spelling of an allowed IP resolve through.
		{"zero-padded octets are not the allowed literal", []string{"1.2.3.4"}, "1.2.3.004:443", false},
		{"name pattern never matches an ip target", []string{"example.com"}, "1.2.3.4:443", false},
		{"ip pattern never matches a name target", []string{"1.2.3.4"}, "example.com:443", false},
		// Parsed-IP comparison, not string comparison: the IPv4-mapped form of
		// an allowed literal IS the same address and must match. (The reverse
		// direction — smuggling a denied address past a textual entry — is
		// handled by AllowsAddr, which normalises before every range test.)
		{"ipv4-mapped form of an allowed literal", []string{"1.2.3.4"}, "[::ffff:1.2.3.4]:443", true},

		// Ports: a bare entry means 443 and 80 only. Without this,
		// allow = ["github.com"] would also authorise an SSH push channel.
		{"default port 443", []string{"github.com"}, "github.com:443", true},
		{"default port 80", []string{"github.com"}, "github.com:80", true},
		{"ssh port is not implied", []string{"github.com"}, "github.com:22", false},
		{"explicit port", []string{"github.com:9418"}, "github.com:9418", true},
		{"explicit port excludes the defaults", []string{"github.com:9418"}, "github.com:443", false},
		{"explicit port on a wildcard", []string{"*.example.com:8443"}, "a.example.com:8443", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := ParseTarget(tt.target)
			if err != nil {
				t.Fatalf("ParseTarget(%q): %v", tt.target, err)
			}
			err = Policy{Allow: tt.allow}.AllowsHost(target)
			if allowed := err == nil; allowed != tt.allowed {
				t.Errorf("allow=%v target=%q → allowed=%v, want %v (err: %v)",
					tt.allow, tt.target, allowed, tt.allowed, err)
			}
		})
	}
}

// network_deny is the one valve for dropping something a capability contributed.
func TestAllowsHost_denyWins(t *testing.T) {
	p := Policy{
		Allow: []string{"sentry.io", "api.anthropic.com"},
		Deny:  []string{"sentry.io"},
	}
	for _, tc := range []struct {
		target  string
		allowed bool
	}{
		{"api.anthropic.com:443", true},
		{"sentry.io:443", false},
		// A bare deny entry covers EVERY port: a subtraction being broader than
		// the matching addition is the safe direction.
		{"sentry.io:8443", false},
	} {
		target, err := ParseTarget(tc.target)
		if err != nil {
			t.Fatalf("ParseTarget(%q): %v", tc.target, err)
		}
		if allowed := p.AllowsHost(target) == nil; allowed != tc.allowed {
			t.Errorf("%s → allowed=%v, want %v", tc.target, allowed, tc.allowed)
		}
	}
}

func TestAllowsHost_denyWildcard(t *testing.T) {
	p := Policy{Allow: []string{"*.example.com"}, Deny: []string{"*.internal.example.com"}}
	for _, tc := range []struct {
		target  string
		allowed bool
	}{
		{"public.example.com:443", true},
		{"db.internal.example.com:443", false},
	} {
		target, _ := ParseTarget(tc.target)
		if allowed := p.AllowsHost(target) == nil; allowed != tc.allowed {
			t.Errorf("%s → allowed=%v, want %v", tc.target, allowed, tc.allowed)
		}
	}
}

// ── Always-deny addresses ─────────────────────────────────────────────────────

func TestAllowsAddr_alwaysDenied(t *testing.T) {
	tests := []struct {
		name string
		ip   string
	}{
		{"loopback v4 (host-local services: docker, ollama, dev servers)", "127.0.0.1"},
		{"loopback v4, whole /8", "127.53.1.9"},
		{"loopback v6", "::1"},
		{"cloud metadata", "169.254.169.254"},
		// The bypass an IPv4-only range test walks straight past.
		{"cloud metadata, IPv4-mapped IPv6", "::ffff:169.254.169.254"},
		{"link-local v6", "fe80::1"},
		{"AWS IMDSv6 (ULA)", "fd00:ec2::254"},
		{"RFC1918 10/8", "10.1.2.3"},
		{"RFC1918 172.16/12", "172.20.0.5"},
		{"RFC1918 192.168/16", "192.168.1.1"},
		{"RFC1918, IPv4-mapped IPv6", "::ffff:192.168.1.1"},
		{"CGNAT / Tailscale", "100.100.100.100"},
		{"unspecified v4", "0.0.0.0"},
		{"reserved 0.0.0.0/8", "0.1.2.3"},
		{"unspecified v6", "::"},
		{"multicast", "224.0.0.1"},
	}
	var p Policy
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("bad test fixture %q", tt.ip)
			}
			if err := p.AllowsAddr(ip); err == nil {
				t.Errorf("AllowsAddr(%s) allowed an address that must always be denied", tt.ip)
			}
		})
	}
}

func TestAllowsAddr_ordinaryPublicAddressesPass(t *testing.T) {
	var p Policy
	for _, s := range []string{"1.1.1.1", "140.82.121.4", "2606:4700:4700::1111"} {
		if err := p.AllowsAddr(net.ParseIP(s)); err != nil {
			t.Errorf("AllowsAddr(%s) = %v, want allowed", s, err)
		}
	}
}

func TestAllowsAddr_nilIsDenied(t *testing.T) {
	if err := (Policy{}).AllowsAddr(nil); err == nil {
		t.Error("a nil address must be denied, not treated as absent")
	}
}

// ── The test seam ─────────────────────────────────────────────────────────────

// AllowPrivateDestinations exists only so the end-to-end test can point the
// proxy at a loopback listener: every address a dev box or CI runner can bind
// is loopback or RFC1918, i.e. exactly what AllowsAddr rejects.
func TestAllowPrivateDestinations_isWhatMakesTheE2EPossible(t *testing.T) {
	loopback := net.ParseIP("127.0.0.1")
	if err := (Policy{}).AllowsAddr(loopback); err == nil {
		t.Fatal("precondition failed: loopback must be denied by default")
	}
	if err := (Policy{AllowPrivateDestinations: true}).AllowsAddr(loopback); err != nil {
		t.Errorf("test seam did not allow loopback: %v", err)
	}
}

// The seam must never become reachable from configuration. An env var or a TOML
// key would let a compromised profile — or a wrapper script around inner — turn
// off the one part of the policy no configuration is supposed to weaken.
//
// This walks the actual config struct rather than trusting a comment, so it
// fails the day someone adds such a key.
func TestNoConfigSurfaceEnablesPrivateDestinations(t *testing.T) {
	forbidden := []string{"private", "allow_private", "insecure", "allow_all", "no_deny"}

	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		if rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("toml")
			if tag == "" {
				continue
			}
			for _, bad := range forbidden {
				if strings.Contains(strings.ToLower(tag), bad) {
					t.Errorf("%s.%s exposes TOML key %q: the always-deny address list must not be configurable (see Policy.AllowPrivateDestinations)",
						path, f.Name, tag)
				}
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	var seen []string
	walk(reflect.TypeOf(config.Profile{}), "Profile")

	// Guard against a vacuous pass: if the walker ever stops descending into
	// [sandbox], this test would report success while inspecting nothing.
	collect := func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			seen = append(seen, rt.Field(i).Tag.Get("toml"))
		}
	}
	collect(reflect.TypeOf(config.SandboxConfig{}))
	if !slices.Contains(seen, "network_mode") {
		t.Fatal("the walker no longer reaches [sandbox]; this test is not checking anything")
	}
}
