package setup

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed profiles/*.toml
var defaultProfiles embed.FS

// InitResult reports what Init created vs skipped.
type InitResult struct {
	DirsCreated       []string
	ProfilesInstalled []string
	ProfilesSkipped   []string
	ConfigCreated     bool
}

// Init creates the ~/.inner directory structure and installs the default
// profiles if they do not already exist. It is idempotent and safe to call
// on every startup.
func Init(dir string) error {
	_, err := InitVerbose(dir)
	return err
}

// InitVerbose is like Init but returns a detailed report of what was done.
func InitVerbose(dir string) (InitResult, error) {
	var r InitResult
	for _, subdir := range []string{"profiles", "logs", "directives"} {
		path := filepath.Join(dir, subdir)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return r, fmt.Errorf("creating %s directory: %w", subdir, err)
			}
			r.DirsCreated = append(r.DirsCreated, path)
		} else if err != nil {
			return r, fmt.Errorf("checking %s directory: %w", subdir, err)
		}
	}

	created, err := installConfig(dir)
	if err != nil {
		return r, err
	}
	r.ConfigCreated = created

	installed, skipped, err := installProfiles(dir)
	if err != nil {
		return r, err
	}
	r.ProfilesInstalled = installed
	r.ProfilesSkipped = skipped
	return r, nil
}

// InitLocal creates a .inner/ directory in workDir with a starter config.toml
// and an empty profiles/ directory. Idempotent: safe to run multiple times.
func InitLocal(workDir string) (InitResult, error) {
	var r InitResult
	innerDir := filepath.Join(workDir, ".inner")
	profilesDir := filepath.Join(innerDir, "profiles")

	if _, err := os.Stat(innerDir); os.IsNotExist(err) {
		if err := os.MkdirAll(innerDir, 0o755); err != nil {
			return r, fmt.Errorf("creating .inner directory: %w", err)
		}
	} else if err != nil {
		return r, fmt.Errorf("checking .inner directory: %w", err)
	}

	if _, err := os.Stat(profilesDir); os.IsNotExist(err) {
		if err := os.MkdirAll(profilesDir, 0o755); err != nil {
			return r, fmt.Errorf("creating .inner/profiles directory: %w", err)
		}
		r.DirsCreated = append(r.DirsCreated, profilesDir)
	} else if err != nil {
		return r, fmt.Errorf("checking .inner/profiles directory: %w", err)
	}

	cfgPath := filepath.Join(innerDir, "config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		const tmpl = "# inner local configuration (directory-level)\n# Settings here override ~/.inner/config.toml for this directory.\n\n# Profile used by default in this directory when -p is not specified.\n# default_profile = \"my-project-profile\"\n\n# Directory where run logs are stored (overrides global).\n# log_dir = \"~/.inner/logs/\"\n\n# Host directory for workspace mount-point pre-creation (overrides global).\n# Required only when a profile mount uses the ${workspaces_path} token.\n# workspaces_path = \"~/.inner/workspaces\"\n\n# Aliases expand a short name to a full inner command (local overrides global).\n# [aliases]\n# review = \"run --profile code-review\"\n"
		if err := os.WriteFile(cfgPath, []byte(tmpl), 0o644); err != nil {
			return r, fmt.Errorf("writing local config: %w", err)
		}
		r.ConfigCreated = true
	} else if err != nil {
		return r, fmt.Errorf("checking local config: %w", err)
	}

	return r, nil
}

// installConfig writes a default config.toml if one does not already exist.
// Returns true if the file was created.
func installConfig(dir string) (bool, error) {
	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); err == nil {
		return false, nil // already present
	}
	const tmpl = "# inner global configuration\n\n# Profile used by default when -p is not specified.\ndefault_profile = \"shell\"\n\n# Directory where run logs are stored.\n# log_dir = \"~/.inner/logs/\"\n\n# Host directory where workspace mount-point directories are pre-created\n# before running the sandbox. Required only when a profile mount uses the\n# ${workspaces_path} token in its dest field.\n# workspaces_path = \"~/.inner/workspaces\"\n\n# Aliases expand a short name to a full inner command.\n# [aliases]\n# c  = \"run -p claude-interactive\"\n# cs = \"run -p claude-one-shot\"\n"
	return true, os.WriteFile(cfgPath, []byte(tmpl), 0o644)
}

// DefaultProfilesFS returns an fs.FS rooted at the embedded profiles.
// Exposed for inspection and testing.
func DefaultProfilesFS() fs.FS {
	sub, _ := fs.Sub(defaultProfiles, "profiles")
	return sub
}

// installProfiles copies embedded profiles to dir/profiles/.
// Existing files are never overwritten so user customisations are preserved.
// Returns lists of installed and skipped profile names.
func installProfiles(dir string) (installed, skipped []string, err error) {
	entries, err := fs.ReadDir(defaultProfiles, "profiles")
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		dest := filepath.Join(dir, "profiles", e.Name())
		if _, err := os.Stat(dest); err == nil {
			skipped = append(skipped, name)
			continue
		}
		data, err := defaultProfiles.ReadFile("profiles/" + e.Name())
		if err != nil {
			return installed, skipped, err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return installed, skipped, fmt.Errorf("installing profile %s: %w", e.Name(), err)
		}
		installed = append(installed, name)
	}
	return installed, skipped, nil
}
