package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/gitguard"
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
	// loadNotes are the changes config.Loader.BuildUntrusted made while
	// loading the profile, reported by the consent gate with the rest.
	loadNotes []string
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
	"PRIVATE", "COOKIE", "DSN", "DATABASE_URL", "CONNECTION_STRING",
}

// secretishEnvWords are matched as whole "_"-separated words only: as a
// substring "PAT" would also match PATH.
var secretishEnvWords = []string{"PAT", "PASS"}

func looksLikeSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, pat := range secretishEnvNames {
		if strings.Contains(upper, pat) {
			return true
		}
	}
	for _, word := range strings.Split(upper, "_") {
		if slices.Contains(secretishEnvWords, word) {
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

	if rc.EffectiveGitDirMode() == gitguard.ModeRW {
		rc.GitDirMode = gitguard.ModeProtected
		applied = append(applied, `[sandbox] git_dir = "rw" ignored — git hooks and config stay read-only`)
	}

	var keptMounts []config.Mount
	for _, m := range rc.Mounts {
		if hidden := relocatedSensitivePath(m); hidden != "" {
			applied = append(applied, fmt.Sprintf("[mounts] %s -> %s dropped — it would carry %s, which the sandbox hides, to another path", m.Src, m.Dest, hidden))
			continue
		}
		keptMounts = append(keptMounts, m)
	}
	rc.Mounts = keptMounts

	return applied
}

// relocatedSensitivePath reports the hidden resource a mount would expose, or
// "" when it exposes none.
//
// The hide rules act on the host path itself: a mount whose destination is its
// own source gets them on top (they are emitted after every mount). A mount
// that puts the source somewhere else escapes them — "~/.ssh" at /tmp/k, or
// "/" at /tmp/root. The source is resolved first, so a symlink cannot disguise
// either the source or the relocation.
func relocatedSensitivePath(m config.Mount) string {
	if m.Mode == "tmpfs" {
		return "" // an empty overlay carries nothing from the host
	}
	src := filepath.Clean(m.Src)
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	if src == filepath.Clean(m.Dest) {
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, r := range config.SensitiveResources(home, strconv.Itoa(os.Getuid())) {
		// The resource is hidden where the isolator resolves it to, which is
		// not the listed path when a component is a symlink (~/.m2 -> /data/m2).
		paths := []string{r.Path}
		if resolved, err := filepath.EvalSymlinks(r.Path); err == nil && resolved != r.Path {
			paths = append(paths, resolved)
		}
		for _, p := range paths {
			// The mount carries the resource when its source covers it (a
			// parent directory) or lies inside it (a file under ~/.ssh).
			if config.PathCoveredBy([]string{src}, p) || config.PathCoveredBy([]string{p}, src) {
				return r.Path
			}
		}
	}
	return ""
}

// printable escapes control characters in text taken from a downloaded
// profile, so that a crafted value cannot move the cursor or erase lines and
// rewrite the consent prompt the user is reading.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, "\\x%02x", r)
	}
	return b.String()
}

// isPrivilegedAllowKey reports whether an allow key gives the sandbox something
// it could use against the host: a readable credential, or a channel to act
// with the user's authority (container sockets, nested-user-ns, the session
// bus, the systemd user manager, the ssh/gpg agents). The remaining keys only
// downgrade an `inner verify` check and are harmless from a remote profile.
func isPrivilegedAllowKey(key string) bool {
	return slices.Contains(config.CredentialAllowKeys, key) ||
		slices.Contains(config.HostPrivilegeAllowKeys, key)
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
	case config.NetworkAllowlist:
		// The destinations, not just the mode, and expanded: a capability
		// keyword and a "@group" reference each stand for several hosts, and a
		// remote profile must not be able to smuggle one past this prompt
		// behind a name the user has no way to look up here. The provenance
		// comes along because "the profile asked for it" and "the tool needs
		// it" are different things to consent to.
		out = append(out, "network: allowlist — the sandboxed process can reach only:")
		if len(rc.NetworkAllow) == 0 {
			out = append(out, "  (nothing)")
		}
		for _, entry := range rc.NetworkAllow {
			if origin := rc.NetworkAllowOrigin[entry]; origin != "" {
				out = append(out, fmt.Sprintf("  %s [%s]", entry, origin))
			} else {
				out = append(out, "  "+entry)
			}
		}
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

	switch rc.EffectiveGitDirMode() {
	case gitguard.ModeRW:
		out = append(out, "git_dir: rw — the sandbox can plant git hooks your next git command runs on the host")
	case gitguard.ModeRO:
		out = append(out, "git_dir: ro — repositories are read-only")
	}

	if rc.HomeIsolated() {
		for _, p := range rc.HomeAllow {
			out = append(out, "home_allow: "+p+" (read-only)")
		}
	}

	if len(rc.Capabilities) > 0 {
		out = append(out, fmt.Sprintf("capabilities: %s — the matching host tool config and credentials are copied into the sandbox", strings.Join(rc.Capabilities, ", ")))
	}
	if len(rc.Allow) > 0 {
		out = append(out, "allow: "+strings.Join(rc.Allow, ", "))
	}
	if len(rc.Env.Inherit) > 0 {
		out = append(out, "env inherit: "+strings.Join(rc.Env.Inherit, ", "))
	}
	if rc.Env.InheritAll {
		out = append(out, "env inherit_all: the whole host environment, every exported secret included")
	}
	keys := make([]string, 0, len(rc.Env.Set))
	for k := range rc.Env.Set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, fmt.Sprintf("env set: %s=%s", k, rc.Env.Set[k]))
	}
	for _, p := range rc.Env.PathPrepend {
		out = append(out, "PATH prepend: "+p)
	}

	// Every mount, read-only ones included: a read-only mount of a secret is
	// still a leak, and the user can only judge what is listed.
	for _, m := range rc.Mounts {
		mode := m.Mode
		if mode == "" {
			mode = "ro"
		}
		if mode == "tmpfs" {
			out = append(out, fmt.Sprintf("mount: empty tmpfs at %s", m.Dest))
			continue
		}
		out = append(out, fmt.Sprintf("mount: %s -> %s (%s)", m.Src, m.Dest, mode))
	}

	// Only reachable with --trust-remote: BuildUntrusted drops them otherwise.
	if rc.Workdir != "" {
		out = append(out, "workdir: "+rc.Workdir+" (mounted read-write)")
	}
	if rc.LogDir != "" {
		out = append(out, "log dir: "+rc.LogDir+" (inner writes the sandbox output there, on the host)")
	}
	if rc.Clipboard {
		out = append(out, "clipboard: the display socket — a client can type into your session")
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
		for _, line := range src.loadNotes {
			fmt.Fprintf(w, "  hardened: %s\n", printable(line))
		}
		for _, line := range hardenRemoteProfile(rc) {
			fmt.Fprintf(w, "  hardened: %s\n", printable(line))
		}
	}

	fmt.Fprintln(w, "  the profile asks to:")
	for _, line := range remoteProfileRequests(rc) {
		fmt.Fprintf(w, "    - %s\n", printable(line))
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
