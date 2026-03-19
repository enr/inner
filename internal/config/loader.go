package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Loader loads and merges configuration from ~/.inner/.
type Loader struct {
	Dir string // root config directory, e.g. ~/.inner
}

// DefaultLoader returns a Loader pointing at the default ~/.inner directory.
func DefaultLoader() (*Loader, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &Loader{Dir: filepath.Join(home, ".inner")}, nil
}

// NewLoader returns a Loader with an explicit config directory (useful in tests).
func NewLoader(dir string) *Loader {
	return &Loader{Dir: dir}
}

// GlobalConfigPath returns the path to the global config file.
func (l *Loader) GlobalConfigPath() string {
	return filepath.Join(l.Dir, "config.toml")
}

// ProfilePath returns the path to a named profile file.
func (l *Loader) ProfilePath(name string) string {
	return filepath.Join(l.Dir, "profiles", name+".toml")
}

// ProfilesDir returns the path to the profiles directory.
func (l *Loader) ProfilesDir() string {
	return filepath.Join(l.Dir, "profiles")
}

// LoadGlobal reads the global config file.
// Returns an empty GlobalConfig (not an error) if the file does not exist.
func (l *Loader) LoadGlobal() (*GlobalConfig, error) {
	cfg := &GlobalConfig{}
	path := l.GlobalConfigPath()
	_, err := toml.DecodeFile(path, cfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading global config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadProfile reads a named profile file.
// Returns an error if the file does not exist.
func (l *Loader) LoadProfile(name string) (*Profile, error) {
	path := l.ProfilePath(name)
	p := &Profile{}
	_, err := toml.DecodeFile(path, p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("profile %q not found at %s", name, path)
		}
		return nil, fmt.Errorf("reading profile %q: %w", name, err)
	}
	// Back-fill name from filename if not set in the file.
	if p.Name == "" {
		p.Name = name
	}
	return p, nil
}

// Build loads global config and the named profile, then produces a RunConfig.
// profileName defaults to "default" if empty.
func (l *Loader) Build(profileName string) (*RunConfig, error) {
	if profileName == "" {
		profileName = "default"
	}
	global, err := l.LoadGlobal()
	if err != nil {
		return nil, err
	}
	profile, err := l.LoadProfile(profileName)
	if err != nil {
		return nil, err
	}
	return toRunConfig(global, profile), nil
}

// toRunConfig converts a loaded Profile (and GlobalConfig) into a RunConfig,
// applying path expansion.
func toRunConfig(global *GlobalConfig, p *Profile) *RunConfig {
	cfg := &RunConfig{
		Network:            p.Sandbox.Network,
		Clipboard:          p.Sandbox.Clipboard,
		Env:                p.Env,
		Git:                p.Git,
		LogSummary:         p.Output.Summary,
		Timeout:            p.Output.TimeoutSeconds,
		Noop:               p.Noop,
		Allow:              p.Sandbox.Allow,
		VerifyCustomChecks: p.Verify.Custom.Checks,
	}

	// Mounts: expand paths, default mode to "ro".
	for src, entry := range p.Mounts {
		mode := entry.Mode
		if mode == "" {
			mode = "ro"
		}
		cfg.Mounts = append(cfg.Mounts, Mount{
			Src:  ExpandPath(src),
			Dest: ExpandPath(entry.Dest),
			Mode: mode,
		})
	}

	// Entrypoint.
	cfg.Entrypoint = Entrypoint{
		Cmd:         p.Entrypoint.Cmd,
		Args:        p.Entrypoint.Args,
		Interactive: p.Entrypoint.Interactive,
	}

	// Log directory: profile takes precedence over global.
	logDir := p.Output.Log
	if logDir == "" {
		logDir = global.LogDir
	}
	cfg.LogDir = ExpandPath(logDir)

	return cfg
}
