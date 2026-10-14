package main

import (
	"os"
	"slices"

	"github.com/enr/inner/internal/config"
)

// dryRunJSON is the output of `inner run --dry-run --json`: the same facts as
// the text dry-run, with snake_case keys that stay stable across releases.
type dryRunJSON struct {
	Profile      string           `json:"profile"`
	ProfilePath  string           `json:"profile_path"`
	GlobalConfig string           `json:"global_config"`
	LocalConfig  string           `json:"local_config,omitempty"`
	Entrypoint   dryRunEntrypoint `json:"entrypoint"`
	Workdir      string           `json:"workdir,omitempty"`
	Home         string           `json:"home"`
	HomeAllow    []string         `json:"home_allow"`
	Network      dryRunNetwork    `json:"network"`
	GitDir       string           `json:"git_dir"`
	PidNamespace bool             `json:"pid_namespace"`
	Clipboard    bool             `json:"clipboard"`
	Timeout      int              `json:"timeout_seconds"`
	Mounts       []dryRunMount    `json:"mounts"`
	Env          dryRunEnv        `json:"env"`
	Noop         dryRunNoop       `json:"noop"`
	Shims        []string         `json:"shims"` // added by capabilities
	Allow        []string         `json:"allow"`
	Capabilities []string         `json:"capabilities"`
	Limits       dryRunLimits     `json:"limits"`
	LogDir       string           `json:"log_dir,omitempty"`
	Command      []string         `json:"command"` // the full sandbox command line
}

type dryRunEntrypoint struct {
	Cmd         string   `json:"cmd"`
	Args        []string `json:"args"`
	Interactive bool     `json:"interactive"`
	TUI         bool     `json:"tui"`
}

type dryRunNetwork struct {
	Mode  string            `json:"mode"` // "off", "full" or "allowlist"
	Allow []dryRunAllowRule `json:"allow"`
	Deny  []string          `json:"deny"`
}

type dryRunAllowRule struct {
	Entry  string `json:"entry"`
	Origin string `json:"origin,omitempty"` // the layer that contributed it
}

type dryRunMount struct {
	Src  string `json:"src"`
	Dest string `json:"dest"`
	Mode string `json:"mode"`
}

type dryRunEnv struct {
	InheritAll bool              `json:"inherit_all"`
	Inherit    []string          `json:"inherit"`
	Set        map[string]string `json:"set"`
}

type dryRunNoop struct {
	Block   []string          `json:"block"`
	Rewrite map[string]string `json:"rewrite"`
}

type dryRunLimits struct {
	Memory string `json:"memory,omitempty"`
	CPU    string `json:"cpu,omitempty"`
	Pids   int    `json:"pids,omitempty"`
}

// orEmpty turns a nil slice into an empty one, so JSON lists are [] not null.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// orEmptyMap turns a nil map into an empty one, so JSON maps are {} not null.
func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// entrypoint and entrypointArgs are the command the user asked for: by the
// time of the dry-run rc.Entrypoint may already be wrapped in inner's own
// __scope / __net-relay, which the full command line still shows.
func newDryRunJSON(profilePath, globalConfigPath, localConfigPath string, rc *config.RunConfig, entrypoint string, entrypointArgs, cmdArgs []string) dryRunJSON {
	home := rc.HomeMode
	if home == "" {
		home = config.HomeHostRO
	}
	out := dryRunJSON{
		Profile:      rc.Name,
		ProfilePath:  profilePath,
		GlobalConfig: globalConfigPath,
		Entrypoint: dryRunEntrypoint{
			Cmd:         entrypoint,
			Args:        orEmpty(entrypointArgs),
			Interactive: rc.Entrypoint.Interactive,
			TUI:         rc.Entrypoint.TUI,
		},
		Workdir:      rc.Workdir,
		Home:         home,
		HomeAllow:    []string{},
		Network:      dryRunNetwork{Mode: rc.EffectiveNetworkMode(), Allow: []dryRunAllowRule{}, Deny: orEmpty(rc.NetworkDeny)},
		GitDir:       rc.EffectiveGitDirMode(),
		PidNamespace: rc.PidNamespace,
		Clipboard:    rc.Clipboard,
		Timeout:      rc.Timeout,
		Mounts:       []dryRunMount{},
		Env: dryRunEnv{
			InheritAll: rc.Env.InheritAll,
			Inherit:    orEmpty(rc.Env.Inherit),
			Set:        orEmptyMap(rc.Env.Set),
		},
		Noop:         dryRunNoop{Block: orEmpty(rc.Noop.Block), Rewrite: orEmptyMap(rc.Noop.Rewrite)},
		Shims:        []string{},
		Allow:        orEmpty(rc.Allow),
		Capabilities: orEmpty(rc.Capabilities),
		Limits:       dryRunLimits{Memory: rc.Limits.Memory, CPU: rc.Limits.CPU, Pids: rc.Limits.Pids},
		LogDir:       rc.LogDir,
		Command:      orEmpty(cmdArgs),
	}
	if localConfigPath != "" {
		if _, err := os.Stat(localConfigPath); err == nil {
			out.LocalConfig = localConfigPath
		}
	}
	if rc.HomeIsolated() {
		out.HomeAllow = orEmpty(rc.HomeAllow)
	}
	if out.Network.Mode == config.NetworkAllowlist {
		for _, entry := range rc.NetworkAllow {
			out.Network.Allow = append(out.Network.Allow, dryRunAllowRule{Entry: entry, Origin: rc.NetworkAllowOrigin[entry]})
		}
	}
	for _, m := range rc.Mounts {
		out.Mounts = append(out.Mounts, dryRunMount{Src: m.Src, Dest: m.Dest, Mode: m.Mode})
	}
	for name := range rc.Shims {
		out.Shims = append(out.Shims, name)
	}
	slices.Sort(out.Shims)
	return out
}
