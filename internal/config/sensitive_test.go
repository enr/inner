package config

import (
	"path/filepath"
	"slices"
	"testing"
)

// wellKnownSecretPaths is the canary list for the default hide list: paths a
// reasonable user expects a sandboxed agent NOT to be able to read under
// home = "host-ro". Each entry must be the path of a SensitiveResource or live
// underneath one.
//
// This test is the guard the review asked for: when a new tool invents a token
// path, add it here and the test fails until SensitiveResources covers it.
// Paths are relative to the home directory.
var wellKnownSecretPaths = []string{
	// Keys and git.
	".ssh/id_ed25519",
	".gnupg/private-keys-v1.d/key.key",
	".git-credentials",
	".netrc",
	// Cloud providers.
	".aws/credentials",
	".config/gcloud/credentials.db",
	".kube/config",
	".azure/msal_token_cache.json",
	// Registries and build tools.
	".docker/config.json",
	".npmrc",
	".pypirc",
	".cargo/credentials",
	".cargo/credentials.toml",
	".config/gh/hosts.yml",
	".terraform.d/credentials.tfrc.json",
	".m2/settings.xml",
	".m2/settings-security.xml",
	".gradle/gradle.properties",
	".config/helm/repositories.yaml",
	// Databases.
	".pgpass",
	".my.cnf",
	// Secret stores.
	".password-store/personal/github.gpg",
	".local/share/keyrings/login.keyring",
	".config/op/config",
	// Browser cookie jars and password databases.
	".mozilla/firefox/profile.default/cookies.sqlite",
	".config/google-chrome/Default/Cookies",
	".config/chromium/Default/Login Data",
	".config/BraveSoftware/Brave-Browser/Default/Cookies",
	".config/microsoft-edge/Default/Cookies",
	".config/vivaldi/Default/Cookies",
	".config/opera/Cookies",
	// Shell history: recorded secrets pasted on a command line.
	".bash_history",
	".zsh_history",
}

func TestSensitiveResources_coverWellKnownSecrets(t *testing.T) {
	const home = "/home/tester"
	resources := SensitiveResources(home, "1000")

	paths := make([]string, 0, len(resources))
	for _, r := range resources {
		paths = append(paths, r.Path)
	}

	for _, rel := range wellKnownSecretPaths {
		full := filepath.Join(home, rel)
		if !PathCoveredBy(paths, full) {
			t.Errorf("well-known secret %q is not hidden by default: add an entry to SensitiveResources", full)
		}
	}
}

// Every key used by a hide entry must be declassifiable, otherwise a user hit
// by a false positive (a tool that genuinely needs the path) has no escape
// hatch. The two shell-history keys are the deliberate exception: nothing
// legitimately needs the host's shell history inside a sandbox.
func TestSensitiveResources_keysAreDeclassifiable(t *testing.T) {
	nonDeclassifiable := []string{"bash-history", "zsh-history"}
	for _, r := range SensitiveResources("/home/tester", "1000") {
		if slices.Contains(nonDeclassifiable, r.Key) {
			continue
		}
		if !slices.Contains(ValidAllowKeys, r.Key) {
			t.Errorf("hide key %q is missing from ValidAllowKeys: [sandbox] allow could never un-hide %s", r.Key, r.Path)
		}
	}
}

// A key that un-hides a readable secret must be listed in CredentialAllowKeys,
// which is what makes "network + credentials" reportable by the validator.
func TestSensitiveResources_credentialKeysListed(t *testing.T) {
	// Keys whose resource is not a readable secret file/dir on its own.
	notCredentials := []string{
		"docker-socket", "podman-socket", "bash-history", "zsh-history",
	}
	for _, r := range SensitiveResources("/home/tester", "1000") {
		if slices.Contains(notCredentials, r.Key) {
			continue
		}
		if !slices.Contains(CredentialAllowKeys, r.Key) {
			t.Errorf("hide key %q is missing from CredentialAllowKeys: allowing it with network = true would not be reported", r.Key)
		}
	}
}
