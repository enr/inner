package config

import (
	"path/filepath"
	"slices"
	"strings"
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
	// Flatpak/snap browsers and mail clients.
	".var/app/org.mozilla.firefox/.mozilla/firefox/p.default/cookies.sqlite",
	".var/app/com.google.Chrome/config/google-chrome/Default/Cookies",
	"snap/firefox/common/.mozilla/firefox/p.default/logins.json",
	".thunderbird/p.default/logins.json",
	// More credential files.
	".config/git/credentials",
	".config/containers/auth.json",
	".vault-token",
	".config/rclone/rclone.conf",
	".s3cfg",
	".config/sops/age/keys.txt",
	".pulumi/credentials.json",
	// The agents' own login tokens.
	".claude/.credentials.json",
	".gemini/oauth_creds.json",
	".local/share/opencode/auth.json",
	".config/cursor/auth.json",
	// Shell and REPL history: recorded secrets pasted on a command line.
	".bash_history",
	".zsh_history",
	".local/share/fish/fish_history",
	".psql_history",
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
// hatch. The history keys are the deliberate exception: nothing legitimately
// needs the host's shell or REPL history inside a sandbox.
func TestSensitiveResources_keysAreDeclassifiable(t *testing.T) {
	nonDeclassifiable := []string{"bash-history", "zsh-history", "shell-history"}
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
	notCredentials := append([]string{"bash-history", "zsh-history", "shell-history"}, HostPrivilegeAllowKeys...)
	for _, r := range SensitiveResources("/home/tester", "1000") {
		if slices.Contains(notCredentials, r.Key) {
			continue
		}
		if !slices.Contains(CredentialAllowKeys, r.Key) {
			t.Errorf("hide key %q is missing from CredentialAllowKeys: allowing it with network = true would not be reported", r.Key)
		}
	}
}

// wellKnownRuntimeSockets is the canary list for $XDG_RUNTIME_DIR: sockets a
// sandboxed process could otherwise connect to through the read-only root
// bind. Paths are relative to /run/user/<uid>.
var wellKnownRuntimeSockets = []string{
	"bus",                     // session D-Bus: systemd1, secrets
	"systemd/private",         // systemd user manager
	"ssh-agent.socket",        // systemd ssh-agent.service
	"openssh_agent",           // openssh-agent unit on some distributions
	"gcr/ssh",                 // GNOME gcr-ssh-agent
	"keyring/ssh",             // gnome-keyring ssh component
	"keyring/control",         // gnome-keyring control socket
	"gnupg/S.gpg-agent",       // gpg-agent, default homedir
	"gnupg/d.abc/S.gpg-agent", // gpg-agent, non-default homedir
}

func TestSensitiveResources_coverRuntimeSockets(t *testing.T) {
	resources := SensitiveResources("/home/tester", "1000")
	paths := make([]string, 0, len(resources))
	for _, r := range resources {
		paths = append(paths, r.Path)
	}
	for _, rel := range wellKnownRuntimeSockets {
		full := filepath.Join("/run/user/1000", rel)
		if !PathCoveredBy(paths, full) {
			t.Errorf("runtime socket %q is not hidden by default: add an entry to SensitiveResources", full)
		}
	}
}

// Every hide key is either a readable credential or a host privilege, so that
// both the "network + credentials" warning and the remote-profile hardening
// know about it; and every host-privilege key must be a valid allow key.
func TestHostPrivilegeAllowKeys(t *testing.T) {
	for _, key := range HostPrivilegeAllowKeys {
		if !slices.Contains(ValidAllowKeys, key) {
			t.Errorf("host-privilege key %q is not in ValidAllowKeys", key)
		}
		if slices.Contains(CredentialAllowKeys, key) {
			t.Errorf("key %q is both a credential and a host-privilege key", key)
		}
	}
}

func TestHidePlaceholder(t *testing.T) {
	for _, r := range SensitiveResources("/home/tester", "1000") {
		got := HidePlaceholder(r)
		want := ""
		if r.Path == "/home/tester/.m2/settings.xml" {
			want = "<settings/>\n"
		}
		if got != want {
			t.Errorf("HidePlaceholder(%s) = %q, want %q", r.Path, got, want)
		}
		if got != "" && r.Dir {
			t.Errorf("%s: a directory cannot have a file placeholder", r.Path)
		}
	}
}

func TestAllowKeyEnabled(t *testing.T) {
	cases := []struct {
		allow []string
		key   string
		want  bool
	}{
		{[]string{"ssh-agent"}, "ssh-agent", true},
		{[]string{"ssh-keys"}, "ssh-agent", true},
		{[]string{"gpg-keys"}, "gpg-agent", true},
		{[]string{"ssh-agent"}, "ssh-keys", false}, // not the other way round
		{[]string{"ssh-keys"}, "session-bus", false},
		{nil, "ssh-agent", false},
	}
	for _, tc := range cases {
		if got := AllowKeyEnabled(tc.allow, tc.key); got != tc.want {
			t.Errorf("AllowKeyEnabled(%v, %q) = %v, want %v", tc.allow, tc.key, got, tc.want)
		}
	}
}

// withEnv points the table at a fake host environment for one test.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := getenv
	getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { getenv = prev })
}

func resourceFor(resources []SensitiveResource, path string) (SensitiveResource, bool) {
	for _, r := range resources {
		if r.Path == path {
			return r, true
		}
	}
	return SensitiveResource{}, false
}

// ISS-34: a secret moved by the environment is hidden at its new place, under
// the key of the location it replaces.
func TestSensitiveResources_relocatedByEnv(t *testing.T) {
	const home = "/home/tester"
	withEnv(t, map[string]string{
		"KUBECONFIG":                  "/work/a.yaml:~/clusters/b.yaml",
		"GNUPGHOME":                   "/keys/gnupg",
		"XDG_CONFIG_HOME":             "/cfg",
		"XDG_DATA_HOME":               "~/data",
		"DOCKER_CONFIG":               "/opt/docker",
		"CARGO_HOME":                  "/opt/cargo",
		"AWS_SHARED_CREDENTIALS_FILE": "/secrets/aws",
		"SSH_AUTH_SOCK":               "/run/user/1000/custom-agent.sock",
	})
	t.Setenv("HOME", home) // ExpandPath resolves ~ against it
	resources := SensitiveResources(home, "1000")

	want := map[string]struct {
		key string
		dir bool
	}{
		"/work/a.yaml":                     {"kube-config", false},
		home + "/clusters/b.yaml":          {"kube-config", false},
		"/keys/gnupg":                      {"gpg-keys", true},
		"/cfg/gh":                          {"gh-config", true},
		"/cfg/git/credentials":             {"git-credentials", false},
		"/cfg/google-chrome":               {"browser-profiles", true},
		home + "/data/keyrings":            {"keyrings", true},
		home + "/data/opencode/auth.json":  {"agent-credentials", false},
		"/opt/docker/config.json":          {"docker-config", false},
		"/opt/cargo/credentials.toml":      {"cargo-credentials", false},
		"/secrets/aws":                     {"aws-credentials", false},
		"/run/user/1000/custom-agent.sock": {"ssh-agent", false},
	}
	for path, w := range want {
		r, ok := resourceFor(resources, path)
		if !ok {
			t.Errorf("%s is not hidden", path)
			continue
		}
		if r.Key != w.key || r.Dir != w.dir {
			t.Errorf("%s: got key %q dir %v, want %q %v", path, r.Key, r.Dir, w.key, w.dir)
		}
	}
	// The fixed locations stay hidden too.
	if _, ok := resourceFor(resources, home+"/.kube"); !ok {
		t.Error("~/.kube no longer hidden")
	}
}

// A variable pointing at the home itself (or at /) must not hide everything.
func TestSensitiveResources_relocationNeverCoversHome(t *testing.T) {
	const home = "/home/tester"
	withEnv(t, map[string]string{
		"GNUPGHOME":          home,
		"PASSWORD_STORE_DIR": "/",
		"XDG_CONFIG_HOME":    "/home",
		"SSH_AUTH_SOCK":      "/tmp/ssh-XYZ/agent.1",
		"KUBECONFIG":         "relative/config",
	})
	for _, r := range SensitiveResources(home, "1000") {
		if r.Dir && (r.Path == "/" || PathCoveredBy([]string{r.Path}, home)) {
			t.Errorf("%s (%s) would hide the whole home", r.Path, r.Key)
		}
		if strings.HasPrefix(r.Path, "/tmp/") {
			t.Errorf("%s: /tmp is a fresh tmpfs in the sandbox, nothing to hide", r.Path)
		}
		if !filepath.IsAbs(r.Path) {
			t.Errorf("relative path %q in the table", r.Path)
		}
	}
}

// Without relocation variables the table is exactly the fixed one: a default
// XDG_CONFIG_HOME adds no duplicate.
func TestSensitiveResources_defaultXDGAddsNothing(t *testing.T) {
	const home = "/home/tester"
	withEnv(t, map[string]string{"XDG_CONFIG_HOME": home + "/.config"})
	if got, want := len(SensitiveResources(home, "1000")), len(fixedSensitiveResources(home, "1000")); got != want {
		t.Errorf("%d entries, want %d (no duplicates)", got, want)
	}
}
