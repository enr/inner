package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// loadEmbeddedProfiles installs the built-in profiles into a temp directory and
// returns them keyed by name, as the loader sees them.
func loadEmbeddedProfiles(t *testing.T) map[string]*config.Profile {
	t.Helper()
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	entries, err := fs.ReadDir(DefaultProfilesFS(), ".")
	if err != nil {
		t.Fatalf("reading embedded profiles: %v", err)
	}
	loader := config.NewLoader(dir)
	profiles := make(map[string]*config.Profile, len(entries))
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".toml")
		p, err := loader.LoadProfile(name)
		if err != nil {
			t.Fatalf("LoadProfile(%s): %v", name, err)
		}
		profiles[name] = p
	}
	return profiles
}

// TestDefaultProfiles_agentProfilesIsolateHome is the regression guard for the
// allowlist filesystem model: every built-in profile that drives a coding agent
// must run on an isolated home. Losing this silently would put the agent back on
// the denylist model, where everything under $HOME that the hide list does not
// know about stays readable.
func TestDefaultProfiles_agentProfilesIsolateHome(t *testing.T) {
	for name, p := range loadEmbeddedProfiles(t) {
		if len(p.Capabilities) == 0 {
			continue // not an agent profile
		}
		if p.Sandbox.Home != config.HomeIsolated {
			t.Errorf("profile %q drives %v but sets home = %q, want %q",
				name, p.Capabilities, p.Sandbox.Home, config.HomeIsolated)
		}
		// An isolated home with no allowlist means the agent CLI is only found
		// when it is installed outside the home directory.
		if len(p.Sandbox.HomeAllow) == 0 {
			t.Errorf("profile %q isolates the home but lists no home_allow paths", name)
		}
		// The sanitized gitconfig replaces the ~/.gitconfig the tmpfs hides.
		if p.Git == nil {
			t.Errorf("profile %q isolates the home but declares no [git] section: git would lose the user identity", name)
		}
	}
}

func TestDefaultProfiles_homeModesAreValid(t *testing.T) {
	for name, p := range loadEmbeddedProfiles(t) {
		if p.Sandbox.Home == "" {
			continue
		}
		if !slices.Contains(config.ValidHomeModes, p.Sandbox.Home) {
			t.Errorf("profile %q: invalid home mode %q", name, p.Sandbox.Home)
		}
	}
}

func TestDefaultProfiles_homeAllowStaysInsideHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	home = filepath.Clean(home)
	for name, p := range loadEmbeddedProfiles(t) {
		for _, entry := range p.Sandbox.HomeAllow {
			// The shipped lists must use ~ so they follow the user's home,
			// and must not hand back the whole home directory.
			if !strings.HasPrefix(entry, "~/") {
				t.Errorf("profile %q: home_allow entry %q should be written relative to ~", name, entry)
				continue
			}
			expanded := filepath.Clean(config.ExpandPath(entry))
			if expanded == home {
				t.Errorf("profile %q: home_allow entry %q re-exposes the whole home", name, entry)
			}
		}
	}
}
