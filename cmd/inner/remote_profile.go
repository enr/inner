package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/enr/inner/internal/config"
)

// A profile downloaded from a URL configures the whole sandbox: the entrypoint
// that runs, whether the network is open, whether the host environment is
// forwarded, which credentials are un-hidden. Trusting it the way a local
// profile is trusted turns one URL into "run this command on my machine with my
// secrets", which is exactly what the sandbox exists to prevent.
//
// inner therefore treats a remote profile as untrusted input:
//
//  1. it is hardened before it is used — the settings that hand the sandbox
//     host privileges are stripped (see hardenRemoteProfile);
//  2. what it still asks for is summarized and the run needs explicit consent —
//     a --allow-remote flag or an interactive y/N. --yes does *not* answer this
//     prompt: it exists to skip routine confirmations, not to accept code from
//     a URL;
//  3. the fetched bytes can be pinned with --sha256, so a URL that serves
//     something different tomorrow fails instead of running.
//
// --trust-remote is the opt-out for the hardening in (1); it also counts as
// consent for (2).

// remoteSource describes a profile that was downloaded for this run.
// A zero value means the profile came from the local filesystem.
type remoteSource struct {
	url    string // URL it was fetched from
	digest string // sha256 of the exact bytes fetched, hex
}

// isRemote reports whether the profile of this run came from a URL.
func (s remoteSource) isRemote() bool { return s.url != "" }

// sha256Hex returns the lowercase hex sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// checkProfileDigest compares the digest of the fetched profile against the
// pin the user passed on the command line. An empty pin means "not pinned".
// The pin may be written bare or with a "sha256:" prefix, in any case.
func checkProfileDigest(pin, digest, rawURL string) error {
	if pin == "" {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(pin))
	want = strings.TrimPrefix(want, "sha256:")
	if len(want) != 64 {
		return fmt.Errorf("invalid --sha256 %q: expected a 64-character hex sha256 digest", pin)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("invalid --sha256 %q: not hexadecimal", pin)
	}
	if want != digest {
		return fmt.Errorf("profile at %s does not match --sha256:\n  expected %s\n  actual   %s\n"+
			"The content served by that URL changed since the digest was taken — inspect it before running it "+
			"(inner profile show <url>)", rawURL, want, digest)
	}
	return nil
}

// secretishEnvNames are the substrings that make an environment variable name
// look like a secret. A remote profile that asks to inherit a matching name is
// asking for a host credential by name, so the hardening drops it.
var secretishEnvNames = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "KEY", "AUTH", "SESSION",
}

func looksLikeSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, pat := range secretishEnvNames {
		if strings.Contains(upper, pat) {
			return true
		}
	}
	return false
}

// hardenRemoteProfile strips from rc the settings a downloaded profile must not
// be able to choose on its own, and returns one line per change for the user.
//
// What is removed is what turns the sandbox into a host-secret pipe: full
// environment inheritance, inheritance of secret-looking variables by name, the
// allow keys that un-hide credentials or hand over a container socket, and the
// PID-namespace opt-out. What is *kept* (network, entrypoint, mounts,
// capabilities) is what a profile legitimately needs to describe — it is
// reported by remoteProfileRequests instead, and the consent prompt is what
// gates it.
func hardenRemoteProfile(rc *config.RunConfig) []string {
	var applied []string

	if rc.Env.InheritAll {
		rc.Env.InheritAll = false
		applied = append(applied, "[env] inherit_all = true ignored — the host environment stays cleared")
	}

	var keptEnv, droppedEnv []string
	for _, name := range rc.Env.Inherit {
		if looksLikeSecretEnvName(name) {
			droppedEnv = append(droppedEnv, name)
			continue
		}
		keptEnv = append(keptEnv, name)
	}
	if len(droppedEnv) > 0 {
		rc.Env.Inherit = keptEnv
		applied = append(applied, fmt.Sprintf("[env] inherit: dropped %s — a remote profile does not get to name host secrets", strings.Join(droppedEnv, ", ")))
	}

	var keptAllow, droppedAllow []string
	for _, key := range rc.Allow {
		if isPrivilegedAllowKey(key) {
			droppedAllow = append(droppedAllow, key)
			continue
		}
		keptAllow = append(keptAllow, key)
	}
	if len(droppedAllow) > 0 {
		rc.Allow = keptAllow
		applied = append(applied, fmt.Sprintf("[sandbox] allow: dropped %s — those keys un-hide host credentials or grant host privileges", strings.Join(droppedAllow, ", ")))
	}

	if !rc.PidNamespace {
		rc.PidNamespace = true
		applied = append(applied, "[sandbox] pid_namespace = false ignored — the sandbox keeps its own PID namespace")
	}

	return applied
}

// isPrivilegedAllowKey reports whether an allow key gives the sandbox something
// it could use against the host: a readable credential, a container socket, or
// the nested-user-ns capability. The remaining keys only downgrade an
// `inner verify` check and are harmless coming from a remote profile.
func isPrivilegedAllowKey(key string) bool {
	if slices.Contains(config.CredentialAllowKeys, key) {
		return true
	}
	switch key {
	case "docker-socket", "podman-socket", "nested-user-ns":
		return true
	}
	return false
}

// remoteProfileRequests summarizes, for the consent prompt, what the downloaded
// profile still gets to decide after hardening. Every line is something the
// user is being asked to accept.
func remoteProfileRequests(rc *config.RunConfig) []string {
	var out []string

	cmd := rc.Entrypoint.Cmd
	if cmd == "" {
		cmd = "$SHELL"
	}
	if len(rc.Entrypoint.Args) > 0 {
		cmd += " " + strings.Join(rc.Entrypoint.Args, " ")
	}
	out = append(out, "runs: "+cmd)

	// Named by mode, not by the on/off bool: the consent prompt is the one
	// place a user decides whether to trust an untrusted profile, so it must
	// not collapse two different network models into the same sentence.
	switch mode := rc.EffectiveNetworkMode(); mode {
	case config.NetworkOff:
		out = append(out, "network: disabled")
	case config.NetworkFull:
		out = append(out, "network: full — the sandboxed process can reach the internet")
	default:
		out = append(out, "network: "+mode)
	}

	home := rc.HomeMode
	if home == "" {
		home = config.HomeHostRO
	}
	if rc.HomeIsolated() {
		out = append(out, "home: isolated (tmpfs)")
	} else {
		out = append(out, fmt.Sprintf("home: %s — your home directory is readable inside the sandbox", home))
	}

	if len(rc.Capabilities) > 0 {
		out = append(out, fmt.Sprintf("capabilities: %s — the matching host tool config is copied into the sandbox", strings.Join(rc.Capabilities, ", ")))
	}
	if len(rc.Allow) > 0 {
		out = append(out, "allow: "+strings.Join(rc.Allow, ", "))
	}
	if len(rc.Env.Inherit) > 0 {
		out = append(out, "env inherit: "+strings.Join(rc.Env.Inherit, ", "))
	}

	for _, m := range rc.Mounts {
		if m.Mode == "rw" || m.Mode == "safe-rw" {
			out = append(out, fmt.Sprintf("mount: %s -> %s (%s)", m.Src, m.Dest, m.Mode))
		}
	}
	if len(rc.Noop.Block) > 0 || len(rc.Noop.Rewrite) > 0 {
		out = append(out, "noop: rewrites or blocks commands inside the sandbox")
	}
	return out
}

// gateRemoteProfile is the consent gate for a profile downloaded from a URL.
// It hardens rc (unless the user opted out with --trust-remote), prints what
// the profile asks for, and reports whether the run may proceed.
//
// Consent is explicit by construction: --allow-remote (or --trust-remote) on
// the command line, or a y/N answer read from in. flags.yes deliberately does
// not answer it — a flag meant to skip routine prompts must not double as
// blanket trust for downloaded code. A non-interactive run without the flag is
// refused rather than silently accepted.
func gateRemoteProfile(w io.Writer, in io.Reader, rc *config.RunConfig, src remoteSource, flags runCLIFlags, stdinIsTerminal bool) (proceed bool, err error) {
	warn := colorizeW(w, ansiBoldYellow, "warning")

	fmt.Fprintf(w, "%s: profile downloaded from %s\n", warn, src.url)
	fmt.Fprintf(w, "         sha256: %s\n", src.digest)
	if flags.sha256 == "" {
		fmt.Fprintf(w, "         (pin it with --sha256 %s to detect a change of content)\n", src.digest)
	}

	if flags.trustRemote {
		fmt.Fprintf(w, "%s: --trust-remote: the profile configures the sandbox with no restriction\n", warn)
	} else {
		for _, line := range hardenRemoteProfile(rc) {
			fmt.Fprintf(w, "  hardened: %s\n", line)
		}
	}

	fmt.Fprintln(w, "  the profile asks to:")
	for _, line := range remoteProfileRequests(rc) {
		fmt.Fprintf(w, "    - %s\n", line)
	}

	switch {
	case flags.dryRun:
		return true, nil
	case flags.allowRemote || flags.trustRemote:
		return true, nil
	case !stdinIsTerminal:
		return false, fmt.Errorf("refusing to run the profile downloaded from %s without consent: "+
			"no terminal to ask on. Pass --allow-remote to accept it (and --sha256 %s to pin it)", src.url, src.digest)
	}

	fmt.Fprintf(w, "  (--allow-remote accepts this without the prompt; --yes does not)\n")
	fmt.Fprint(w, "run this downloaded profile? [y/N] ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y"), nil
}

// stdinIsTerminal reports whether there is a user to ask. Overridable in tests.
var stdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
