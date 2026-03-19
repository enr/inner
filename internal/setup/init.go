package setup

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed profiles/*.toml
var defaultProfiles embed.FS

// Init creates the ~/.inner directory structure and installs the default
// profiles if they do not already exist. It is idempotent and safe to call
// on every startup.
func Init(dir string) error {
	for _, subdir := range []string{"profiles", "logs", "directives"} {
		if err := os.MkdirAll(filepath.Join(dir, subdir), 0o755); err != nil {
			return fmt.Errorf("creating %s directory: %w", subdir, err)
		}
	}
	return installProfiles(dir)
}

// DefaultProfilesFS returns an fs.FS rooted at the embedded profiles.
// Exposed for inspection and testing.
func DefaultProfilesFS() fs.FS {
	sub, _ := fs.Sub(defaultProfiles, "profiles")
	return sub
}

// installProfiles copies embedded profiles to dir/profiles/.
// Existing files are never overwritten so user customisations are preserved.
func installProfiles(dir string) error {
	entries, err := fs.ReadDir(defaultProfiles, "profiles")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dest := filepath.Join(dir, "profiles", e.Name())
		if _, err := os.Stat(dest); err == nil {
			continue // already present — do not overwrite
		}
		data, err := defaultProfiles.ReadFile("profiles/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("installing profile %s: %w", e.Name(), err)
		}
	}
	return nil
}
