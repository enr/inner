package config

import (
	"os"
	"path/filepath"
	"strings"
)

// getenv reads the host environment for relocatedSensitiveResources. A
// package variable so tests can supply their own.
var getenv = os.Getenv

// relocatedDirVars are the variables that name a directory: every entry under
// its default location (relative to the home) moves with it.
var relocatedDirVars = []struct{ env, defaultDir string }{
	{"XDG_CONFIG_HOME", ".config"},
	{"XDG_DATA_HOME", filepath.Join(".local", "share")},
	{"GNUPGHOME", ".gnupg"},
	{"DOCKER_CONFIG", ".docker"},
	{"CARGO_HOME", ".cargo"},
	{"GRADLE_USER_HOME", ".gradle"},
	{"PASSWORD_STORE_DIR", ".password-store"},
	{"GH_CONFIG_DIR", filepath.Join(".config", "gh")},
	{"CLOUDSDK_CONFIG", filepath.Join(".config", "gcloud")},
	{"AZURE_CONFIG_DIR", ".azure"},
	{"HELM_CONFIG_HOME", filepath.Join(".config", "helm")},
	{"PULUMI_HOME", ".pulumi"},
}

// relocatedFileVars are the variables that name a file, with the hide key the
// file belongs to.
var relocatedFileVars = []struct{ env, key string }{
	{"AWS_SHARED_CREDENTIALS_FILE", "aws-credentials"},
	{"AWS_CONFIG_FILE", "aws-credentials"}, // SSO tokens and credential_process live here too
	{"NPM_CONFIG_USERCONFIG", "npmrc"},
	{"PGPASSFILE", "pgpass"},
	{"VAULT_CONFIG_PATH", "vault-token"},
	{"RCLONE_CONFIG", "rclone-config"},
	{"SOPS_AGE_KEY_FILE", "age-keys"},
	{"SSH_AUTH_SOCK", "ssh-agent"},
}

// RelocationEnvVars returns the names of every host variable that
// SensitiveResources consults to find relocated files. Tests outside this
// package clear them so the host session (an ssh-agent socket from
// gnome-keyring, a custom GNUPGHOME, …) does not leak into their results.
func RelocationEnvVars() []string {
	names := make([]string, 0, len(relocatedDirVars)+len(relocatedFileVars)+1)
	for _, v := range relocatedDirVars {
		names = append(names, v.env)
	}
	for _, v := range relocatedFileVars {
		names = append(names, v.env)
	}
	return append(names, "KUBECONFIG")
}

// relocatedSensitiveResources returns the hide entries for tools whose files
// the environment moves away from the fixed locations of base:
//
//   - a variable naming the directory or file itself (KUBECONFIG, GNUPGHOME,
//     AWS_SHARED_CREDENTIALS_FILE, …), hidden under the key of the location it
//     replaces, so the same [sandbox] allow key un-hides it;
//   - XDG_CONFIG_HOME and XDG_DATA_HOME, which move every entry under
//     ~/.config and ~/.local/share at once;
//   - SSH_AUTH_SOCK, wherever the agent socket lives (/tmp is already a fresh
//     tmpfs in the sandbox).
//
// Entries already in base are skipped, and so is a directory entry that would
// cover / or the home itself (GNUPGHOME=$HOME): hiding it would hide
// everything.
func relocatedSensitiveResources(home string, base []SensitiveResource) []SensitiveResource {
	var out []SensitiveResource
	seen := make(map[string]bool, len(base))
	for _, r := range base {
		seen[r.Path] = true
	}
	add := func(key, path string, dir bool) {
		path = ExpandPath(path)
		if !filepath.IsAbs(path) {
			return
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		if dir && (path == "/" || PathCoveredBy([]string{path}, home)) {
			return
		}
		seen[path] = true
		out = append(out, SensitiveResource{Key: key, Path: path, Dir: dir})
	}

	// Variables that name a directory: every entry under its default location
	// moves with it.
	for _, v := range relocatedDirVars {
		dir := getenv(v.env)
		if dir == "" {
			continue
		}
		dir = ExpandPath(dir)
		defaultDir := filepath.Join(home, v.defaultDir)
		for _, r := range base {
			rel, err := filepath.Rel(defaultDir, r.Path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				continue
			}
			add(r.Key, filepath.Join(dir, rel), r.Dir)
		}
	}

	// Variables that name a file.
	for _, v := range relocatedFileVars {
		if path := getenv(v.env); path != "" {
			if v.env == "SSH_AUTH_SOCK" && PathCoveredBy([]string{"/tmp"}, ExpandPath(path)) {
				continue // /tmp is a fresh tmpfs inside the sandbox
			}
			add(v.key, path, false)
		}
	}
	// KUBECONFIG is a list.
	for _, path := range filepath.SplitList(getenv("KUBECONFIG")) {
		if path != "" {
			add("kube-config", path, false)
		}
	}
	return out
}
