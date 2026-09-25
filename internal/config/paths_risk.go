package config

import (
	"path/filepath"
	"slices"
	"strings"
)

// PathCoveredBy reports whether path is one of prefixes or lives underneath
// one of them. Prefixes and path must be absolute and are compared after
// cleaning, so "~/.local/bin" (expanded) covers "~/.local/bin/claude" but not
// "~/.local/bindir".
//
// It is the single definition of "this path is inside that subtree" shared by
// the isolator (does a mount carry a hidden secret back into an isolated
// home?), the profile validator, and `inner run`'s pre-flight checks.
func PathCoveredBy(prefixes []string, path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	for _, prefix := range prefixes {
		if prefix == "" {
			continue
		}
		p := filepath.Clean(prefix)
		if clean == p || strings.HasPrefix(clean, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// IsFilesystemRoot reports whether path is "/" after cleaning.
//
// A read-write bind of the filesystem root removes the one guarantee the
// sandbox always gives — that the host filesystem is immutable — so callers
// treat it as a hard error rather than a warning.
func IsFilesystemRoot(path string) bool {
	return path != "" && filepath.Clean(path) == "/"
}

// SystemDirs are the top-level directories whose content defines the host
// system: binaries on PATH, system configuration, boot files, shared state.
// A read-write bind of any of them lets a sandboxed process rewrite the host
// (a persistence vector that survives the sandbox), which is why mounting one
// rw is reported to the user even though it is technically allowed.
//
// /home and /root are included: they are other people's — or root's — home
// directories, not the caller's workspace.
var SystemDirs = []string{
	"/usr", "/etc", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/boot", "/var", "/opt", "/srv", "/home", "/root",
}

// IsSystemDir reports whether path is exactly one of SystemDirs. Sub-paths are
// not matched: mounting /opt/my-toolchain read-write is ordinary, mounting
// /opt read-write is not.
func IsSystemDir(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	for _, dir := range SystemDirs {
		if clean == dir {
			return true
		}
	}
	return false
}

// CredentialAllowKeys are the [sandbox] allow keys that un-hide a *readable
// secret* (as opposed to keys that grant a capability, like nested-user-ns, or
// that only downgrade a verify check). Combining one of these with an open
// network is the classic exfiltration setup, so callers warn about it.
var CredentialAllowKeys = []string{
	"ssh-keys", "git-credentials", "gpg-keys", "netrc",
	"aws-credentials", "gcloud-credentials", "kube-config", "azure-credentials",
	"docker-config", "npmrc", "pypirc", "cargo-credentials", "gh-config",
	"terraform-credentials", "maven-settings", "gradle-properties",
	"helm-config", "pgpass", "mysql-config",
	"password-store", "keyrings", "onepassword-config", "browser-profiles",
}

// HostPrivilegeAllowKeys are the [sandbox] allow keys that hand the sandbox a
// channel to act on the host with the user's authority, rather than a secret
// to read: container sockets, the nested user namespace, the session bus and
// systemd user manager (both can start processes outside the sandbox), and
// the ssh/gpg agents (they sign with keys the sandbox cannot otherwise see).
// A profile downloaded from a URL never gets to choose them.
var HostPrivilegeAllowKeys = []string{
	"docker-socket", "podman-socket", "nested-user-ns",
	"session-bus", "systemd-user", "ssh-agent", "gpg-agent",
}

// allowImplies lists allow keys that also enable others. Listing ssh-keys or
// gpg-keys already hands the sandbox the private keys; the agent sockets were
// reachable alongside them before the runtime sockets were hidden, and a
// profile that allowed the keys to sign commits or push over ssh relied on
// the agent too. Keeping that working is worth more than the marginal
// difference between "the key files" and "an agent holding them unlocked".
var allowImplies = map[string][]string{
	"ssh-agent": {"ssh-keys"},
	"gpg-agent": {"gpg-keys"},
}

// AllowKeyEnabled reports whether key is enabled by the allow list, either
// directly or through a key that implies it (see allowImplies).
func AllowKeyEnabled(allow []string, key string) bool {
	if slices.Contains(allow, key) {
		return true
	}
	for _, by := range allowImplies[key] {
		if slices.Contains(allow, by) {
			return true
		}
	}
	return false
}
