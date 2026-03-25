package config

import "github.com/BurntSushi/toml"

// mergeProfiles applies overlay on top of base, producing a new Profile.
//
// Rules:
//   - Scalars (bool, string, int): overlay wins only when IsDefined in meta.
//   - Slices (allow, inherit, noop.block, git.strip_sections): union, preserving order, no duplicates.
//   - Maps (mounts, env.set, noop.rewrite, git.overrides): merged; overlay keys win.
//   - verify.custom.checks: base checks first, overlay checks appended.
//   - git: merged field-by-field when overlay declares a [git] section.
func mergeProfiles(base, overlay *Profile, meta toml.MetaData) *Profile {
	result := *base // shallow copy — maps/slices are replaced below, never mutated in place

	// --- top-level scalars ---
	if meta.IsDefined("schema_version") {
		result.SchemaVersion = overlay.SchemaVersion
	}
	if meta.IsDefined("name") {
		result.Name = overlay.Name
	}
	if meta.IsDefined("description") {
		result.Description = overlay.Description
	}
	if meta.IsDefined("workspaces_path") {
		result.WorkspacesPath = overlay.WorkspacesPath
	}
	if meta.IsDefined("experimental") {
		result.Experimental = overlay.Experimental
	}

	// --- [sandbox] ---
	if meta.IsDefined("sandbox", "network") {
		result.Sandbox.Network = overlay.Sandbox.Network
	}
	if meta.IsDefined("sandbox", "clipboard") {
		result.Sandbox.Clipboard = overlay.Sandbox.Clipboard
	}
	if meta.IsDefined("sandbox", "allow") {
		result.Sandbox.Allow = mergeUnique(base.Sandbox.Allow, overlay.Sandbox.Allow)
	}

	// --- capabilities ---
	if meta.IsDefined("capabilities") {
		result.Capabilities = mergeUnique(base.Capabilities, overlay.Capabilities)
	}

	// --- [mounts] ---
	if len(overlay.Mounts) > 0 {
		merged := make(map[string]MountEntry, len(base.Mounts)+len(overlay.Mounts))
		for k, v := range base.Mounts {
			merged[k] = v
		}
		for k, v := range overlay.Mounts {
			merged[k] = v
		}
		result.Mounts = merged
	}

	// --- [env] ---
	if meta.IsDefined("env", "clearenv") {
		result.Env.Clear = overlay.Env.Clear
	}
	if meta.IsDefined("env", "inherit") {
		result.Env.Inherit = mergeUnique(base.Env.Inherit, overlay.Env.Inherit)
	}
	if len(overlay.Env.Set) > 0 {
		merged := make(map[string]string, len(base.Env.Set)+len(overlay.Env.Set))
		for k, v := range base.Env.Set {
			merged[k] = v
		}
		for k, v := range overlay.Env.Set {
			merged[k] = v
		}
		result.Env.Set = merged
	}

	// --- [git] ---
	if overlay.Git != nil {
		g := GitConfig{}
		if base.Git != nil {
			g = *base.Git
		}
		if meta.IsDefined("git", "strip_sections") {
			g.StripSections = mergeUnique(g.StripSections, overlay.Git.StripSections)
		}
		if len(overlay.Git.Overrides) > 0 {
			ovr := make(map[string]string, len(g.Overrides)+len(overlay.Git.Overrides))
			for k, v := range g.Overrides {
				ovr[k] = v
			}
			for k, v := range overlay.Git.Overrides {
				ovr[k] = v
			}
			g.Overrides = ovr
		}
		result.Git = &g
	}

	// --- [entrypoint] ---
	if meta.IsDefined("entrypoint", "cmd") {
		result.Entrypoint.Cmd = overlay.Entrypoint.Cmd
	}
	if meta.IsDefined("entrypoint", "args") {
		result.Entrypoint.Args = overlay.Entrypoint.Args
	}
	if meta.IsDefined("entrypoint", "interactive") {
		result.Entrypoint.Interactive = overlay.Entrypoint.Interactive
	}
	if meta.IsDefined("entrypoint", "tui") {
		result.Entrypoint.TUI = overlay.Entrypoint.TUI
	}
	if meta.IsDefined("entrypoint", "workdir") {
		result.Entrypoint.Workdir = overlay.Entrypoint.Workdir
	}
	if meta.IsDefined("entrypoint", "history") {
		result.Entrypoint.History = mergeUnique(base.Entrypoint.History, overlay.Entrypoint.History)
	}

	// --- [output] ---
	if meta.IsDefined("output", "summary") {
		result.Output.Summary = overlay.Output.Summary
	}
	if meta.IsDefined("output", "log") {
		result.Output.Log = overlay.Output.Log
	}
	if meta.IsDefined("output", "timeout_seconds") {
		result.Output.TimeoutSeconds = overlay.Output.TimeoutSeconds
	}

	// --- [noop] ---
	if meta.IsDefined("noop", "block") {
		result.Noop.Block = mergeUnique(base.Noop.Block, overlay.Noop.Block)
	}
	if len(overlay.Noop.Rewrite) > 0 {
		merged := make(map[string]string, len(base.Noop.Rewrite)+len(overlay.Noop.Rewrite))
		for k, v := range base.Noop.Rewrite {
			merged[k] = v
		}
		for k, v := range overlay.Noop.Rewrite {
			merged[k] = v
		}
		result.Noop.Rewrite = merged
	}

	// --- [verify.custom] ---
	if meta.IsDefined("verify", "custom", "checks") {
		result.Verify.Custom.Checks = append(
			append([]CustomCheck(nil), base.Verify.Custom.Checks...),
			overlay.Verify.Custom.Checks...,
		)
	}

	return &result
}

// mergeGlobalConfig applies local on top of base GlobalConfig.
// Non-zero fields in local override base.
// Aliases are merged: local keys win on conflict, base keys are preserved.
func mergeGlobalConfig(base, local *GlobalConfig) *GlobalConfig {
	result := *base
	if local.DefaultProfile != "" {
		result.DefaultProfile = local.DefaultProfile
	}
	if local.LogDir != "" {
		result.LogDir = local.LogDir
	}
	if local.WorkspacesPath != "" {
		result.WorkspacesPath = local.WorkspacesPath
	}
	if len(local.Aliases) > 0 {
		merged := make(map[string]string, len(base.Aliases)+len(local.Aliases))
		for k, v := range base.Aliases {
			merged[k] = v
		}
		for k, v := range local.Aliases {
			merged[k] = v
		}
		result.Aliases = merged
	}
	return &result
}

// mergeUnique appends items from b to a, skipping duplicates. Order is preserved.
func mergeUnique(a, b []string) []string {
	if len(b) == 0 {
		return append([]string(nil), a...)
	}
	seen := make(map[string]bool, len(a))
	for _, v := range a {
		seen[v] = true
	}
	result := append([]string(nil), a...)
	for _, v := range b {
		if !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	return result
}
