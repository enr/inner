package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultPublishHostIP is the host address a [sandbox] publish entry binds to
// when it names none. Loopback: a published port is for the user at this
// machine, not for the LAN, unless the entry explicitly says otherwise.
const DefaultPublishHostIP = "127.0.0.1"

// PortPublish is one parsed [sandbox] publish entry: connections to
// HostIP:HostPort on the host are forwarded to 127.0.0.1:SandboxPort inside the
// sandbox's network namespace.
type PortPublish struct {
	HostIP      string
	HostPort    int
	SandboxPort int
}

// HostAddr is the host address to listen on, in net.Listen form.
func (p PortPublish) HostAddr() string {
	return net.JoinHostPort(p.HostIP, strconv.Itoa(p.HostPort))
}

// SandboxAddr is the address inside the sandbox the connections go to.
func (p PortPublish) SandboxAddr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.SandboxPort))
}

// Loopback reports whether the host side is reachable from this machine only.
func (p PortPublish) Loopback() bool {
	ip := net.ParseIP(p.HostIP)
	return ip != nil && ip.IsLoopback()
}

// String renders the entry for --dry-run, the consent prompt and warnings.
func (p PortPublish) String() string {
	return fmt.Sprintf("%s -> sandbox %s", p.HostAddr(), p.SandboxAddr())
}

// ParsePublish parses a [sandbox] publish entry (or a --publish value):
//
//	"8080"                    127.0.0.1:8080 -> sandbox :8080
//	"8080:3000"               127.0.0.1:8080 -> sandbox :3000
//	"127.0.0.1:8080"          127.0.0.1:8080 -> sandbox :8080
//	"0.0.0.0:8080:3000"       every interface, port 8080 -> sandbox :3000
//	"[::1]:8080[:3000]"       an IPv6 host address goes in brackets
//
// The host address must be an IP literal: a name would be resolved at listen
// time, and "localhost" resolving to a non-loopback address is exactly the
// surprise this field must not have.
func ParsePublish(s string) (PortPublish, error) {
	bad := func(reason string) (PortPublish, error) {
		return PortPublish{}, fmt.Errorf("invalid publish entry %q: %s (expected [HOST_IP:]PORT[:SANDBOX_PORT])", s, reason)
	}
	rest := strings.TrimSpace(s)
	if rest == "" {
		return bad("empty")
	}

	var ip string
	var ports []string
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 || !strings.HasPrefix(rest[end+1:], ":") {
			return bad("an IPv6 address must be written [ADDR]:PORT")
		}
		ip = rest[1:end]
		ports = strings.Split(rest[end+2:], ":")
		if len(ports) > 2 {
			return bad("too many fields")
		}
	} else {
		parts := strings.Split(rest, ":")
		switch len(parts) {
		case 1:
			ports = parts
		case 2:
			// "IP:PORT" or "HOST_PORT:SANDBOX_PORT": a port never parses as an IP.
			if net.ParseIP(parts[0]) != nil {
				ip, ports = parts[0], parts[1:]
			} else {
				ports = parts
			}
		case 3:
			ip, ports = parts[0], parts[1:]
		default:
			return bad("too many fields")
		}
	}

	p := PortPublish{HostIP: DefaultPublishHostIP}
	if ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return bad(fmt.Sprintf("host address %q is not an IP literal", ip))
		}
		p.HostIP = parsed.String()
	}
	var err error
	if p.HostPort, err = parsePort(ports[0]); err != nil {
		return bad(err.Error())
	}
	p.SandboxPort = p.HostPort
	if len(ports) == 2 {
		if p.SandboxPort, err = parsePort(ports[1]); err != nil {
			return bad(err.Error())
		}
	}
	return p, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("port %q is not a number between 1 and 65535", s)
	}
	return n, nil
}

// ParsePublishList parses every entry. See AppendPublish.
func ParsePublishList(entries []string) ([]PortPublish, error) {
	return AppendPublish(nil, entries)
}

// AppendPublish parses entries and appends them to list. An entry identical to
// one already present is skipped (the same port in the profile and on the
// command line); two different entries binding the same host address are
// refused, since the second listener would fail at run time with a bare
// "address already in use" that names neither entry.
func AppendPublish(list []PortPublish, entries []string) ([]PortPublish, error) {
	for _, e := range entries {
		p, err := ParsePublish(e)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, prev := range list {
			if prev.HostAddr() != p.HostAddr() {
				continue
			}
			if prev != p {
				return nil, fmt.Errorf("publish entries %q and %q both bind %s", prev.String(), p.String(), p.HostAddr())
			}
			duplicate = true
		}
		if !duplicate {
			list = append(list, p)
		}
	}
	return list, nil
}
