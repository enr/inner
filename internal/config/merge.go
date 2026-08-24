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
	// pid_namespace is a *bool: nil means "not set" so a child profile that
	// does not mention it inherits the base value; an explicit true/false in
	// the child always wins.
	if meta.IsDefined("sandbox", "pid_namespace") {
		result.Sandbox.PidNamespace = overlay.Sandbox.PidNamespace
	}
	if meta.IsDefined("sandbox", "allow") {
		result.Sandbox.Allow = mergeUnique(base.Sandbox.Allow, overlay.Sandbox.Allow)
	}
	// home is a scalar: a child profile switching to "isolated" (or back to
	// "host-ro") always wins over the base.
	if meta.IsDefined("sandbox", "home") {
		result.Sandbox.Home = overlay.Sandbox.Home
	}
	// home_allow is unioned like the other path lists: a child profile adds the
	// toolchain paths it needs without having to repeat the base's.
	if meta.IsDefined("sandbox", "home_allow") {
		result.Sandbox.HomeAllow = mergeUnique(base.Sandbox.HomeAllow, overlay.Sandbox.HomeAllow)
	}
	if meta.IsDefined("sandbox", "cgroup_manager") {
		result.Sandbox.CgroupManager = overlay.Sandbox.CgroupManager
	}
	// [sandbox.limits]: merge field-by-field so a child profile can override
	// individual sub-fields without resetting the ones it does not mention.
	if meta.IsDefined("sandbox", "limits") {
		result.Sandbox.Limits = mergeResourceLimits(base.Sandbox.Limits, overlay.Sandbox.Limits, meta)
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
	if meta.IsDefined("env", "inherit_all") {
		result.Env.InheritAll = overlay.Env.InheritAll
	}
	if meta.IsDefined("env", "inherit") {
		result.Env.Inherit = mergeUnique(base.Env.Inherit, overlay.Env.Inherit)
	}
	if meta.IsDefined("env", "path_prepend") {
		// Unlike other unioned slices (base first, overlay appended), the
		// overlay's entries go first: path_prepend controls PATH priority,
		// and a child profile pinning a toolchain must win PATH resolution
		// over whatever the base profile already prepended.
		result.Env.PathPrepend = mergeUnique(overlay.Env.PathPrepend, base.Env.PathPrepend)
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
	if meta.IsDefined("entrypoint", "cursor_fix") {
		result.Entrypoint.CursorFix = overlay.Entrypoint.CursorFix
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
	// DefaultLimits: merge field-by-field so a project config can override
	// individual sub-fields without wiping limits set in the user config.
	if local.DefaultLimits != nil {
		if result.DefaultLimits == nil {
			result.DefaultLimits = &ResourceLimits{}
		}
		if local.DefaultLimits.Memory != "" {
			result.DefaultLimits.Memory = local.DefaultLimits.Memory
		}
		if local.DefaultLimits.CPU != "" {
			result.DefaultLimits.CPU = local.DefaultLimits.CPU
		}
		if local.DefaultLimits.Pids > 0 {
			result.DefaultLimits.Pids = local.DefaultLimits.Pids
		}
	}
	return &result
}

// mergeResourceLimits merges overlay [sandbox.limits] sub-fields on top of
// base, respecting TOML metadata so that only explicitly-defined keys win.
func mergeResourceLimits(base, overlay *ResourceLimits, meta toml.MetaData) *ResourceLimits {
	result := &ResourceLimits{}
	if base != nil {
		*result = *base
	}
	if overlay == nil {
		return result
	}
	if meta.IsDefined("sandbox", "limits", "memory") {
		result.Memory = overlay.Memory
	}
	if meta.IsDefined("sandbox", "limits", "cpu") {
		result.CPU = overlay.CPU
	}
	if meta.IsDefined("sandbox", "limits", "pids") {
		result.Pids = overlay.Pids
	}
	return result
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
