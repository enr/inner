package main

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/profile"
	"github.com/spf13/cobra"
)

func (a *App) newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage profiles",
	}
	cmd.AddCommand(
		a.profileListCmd(),
		a.profileShowCmd(),
		a.profileValidateCmd(),
		a.profileNewCmd(),
		a.profileEditCmd(),
		a.profileCloneCmd(),
		a.profileInstallCmd(),
	)
	return cmd
}

// ── Business logic (testable) ─────────────────────────────────────────────────

// profileEntry holds display data for a single profile row.
type profileEntry struct {
	name     string
	desc     string
	path     string
	isLocal  bool
	shadowed bool // global profile hidden by a local profile of the same name
}

// profileList writes a table of available profiles to w.
// When wide is true, a wider format is used with SCOPE and PATH columns;
// shadowed global profiles (overridden by a local profile with the same name)
// are also shown in wide mode but hidden in normal mode.
func (a *App) profileList(w io.Writer, wide bool) error {
	defaultName := a.loader.DefaultProfileName()
	// default_profile may be a file path (e.g. ".config/inner/profiles/foo.toml");
	// normalise to the bare stem so it matches the name column.
	if strings.HasSuffix(defaultName, ".toml") {
		defaultName = strings.TrimSuffix(filepath.Base(defaultName), ".toml")
	}

	globalEntries, globalErr := os.ReadDir(a.loader.ProfilesDir())
	if globalErr != nil && !os.IsNotExist(globalErr) {
		return fmt.Errorf("reading profiles directory: %w", globalErr)
	}

	localDir := a.loader.LocalProfilesDir()
	var localEntries []os.DirEntry
	if localDir != "" {
		var localErr error
		localEntries, localErr = os.ReadDir(localDir)
		if localErr != nil && !os.IsNotExist(localErr) {
			return fmt.Errorf("reading local profiles directory: %w", localErr)
		}
	}

	if os.IsNotExist(globalErr) && len(localEntries) == 0 {
		fmt.Fprintln(w, "No profiles directory found. Run 'inner doctor' for setup help.")
		return nil
	}

	// Build a set of local profile names to detect shadowed globals.
	localNames := make(map[string]bool)
	for _, e := range localEntries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			localNames[strings.TrimSuffix(e.Name(), ".toml")] = true
		}
	}

	var entries []profileEntry

	for _, e := range globalEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		path := filepath.Join(a.loader.ProfilesDir(), e.Name())
		desc := ""
		if p, err := a.loader.LoadProfileFromPath(path); err == nil {
			desc = p.Description
			if p.Experimental {
				desc = "[experimental] " + desc
			}
		}
		entries = append(entries, profileEntry{
			name:     name,
			desc:     desc,
			path:     path,
			isLocal:  false,
			shadowed: localNames[name],
		})
	}

	for _, e := range localEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		path := filepath.Join(localDir, e.Name())
		desc := ""
		if p, err := a.loader.LoadProfileFromPath(path); err == nil {
			desc = p.Description
			if p.Experimental {
				desc = "[experimental] " + desc
			}
		}
		entries = append(entries, profileEntry{
			name:    name,
			desc:    desc,
			path:    path,
			isLocal: true,
		})
	}

	home, _ := os.UserHomeDir()
	abbreviate := func(path string) string {
		if home != "" {
			return strings.Replace(path, home, "~", 1)
		}
		return path
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	if wide {
		fmt.Fprintln(tw, "\tNAME\tSCOPE\tDESCRIPTION\tPATH")
		for _, en := range entries {
			marker := ""
			if en.name == defaultName {
				marker = "*"
			}
			scope := "global"
			if en.isLocal {
				scope = "local"
			}
			desc := en.desc
			if en.shadowed {
				if desc != "" {
					desc += "  [shadowed]"
				} else {
					desc = "[shadowed]"
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", marker, en.name, scope, desc, abbreviate(en.path))
		}
	} else {
		fmt.Fprintln(tw, "\tNAME\tDESCRIPTION")
		for _, en := range entries {
			if en.shadowed {
				continue
			}
			marker := ""
			if en.name == defaultName {
				marker = "*"
			}
			desc := en.desc
			if en.isLocal {
				if desc != "" {
					desc += "  [local]"
				} else {
					desc = "[local]"
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", marker, en.name, desc)
		}
	}

	return tw.Flush()
}

// profileShow writes the raw TOML content of a named profile to w.
// nameOrPath may be a profile name or an explicit file path.
func (a *App) profileShow(w io.Writer, nameOrPath string) error {
	path := a.loader.ResolveProfilePath(nameOrPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("profile %q not found", nameOrPath)
		}
		return err
	}
	content := string(data)
	if f, ok := w.(*os.File); ok && isTTY(f) {
		content = highlightTOML(content)
	}
	_, err = fmt.Fprint(w, content)
	return err
}

// profileShowResolved writes the effective (fully merged) profile to w as TOML.
// All extends chains and capabilities are resolved before serialisation.
func (a *App) profileShowResolved(w io.Writer, nameOrPath string) error {
	p, err := a.loader.LoadProfileAuto(nameOrPath)
	if err != nil {
		return fmt.Errorf("resolving profile %q: %w", nameOrPath, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(p); err != nil {
		return fmt.Errorf("encoding resolved profile: %w", err)
	}
	content := buf.String()
	if f, ok := w.(*os.File); ok && isTTY(f) {
		content = highlightTOML(content)
	}
	_, err = fmt.Fprint(w, content)
	return err
}

// profileShowExplain writes the raw TOML content of a profile to w, followed
// by a human-readable explanation of each capability it declares (including
// capabilities inherited via extends). If the merged profile cannot be loaded,
// the capability section is silently omitted so the raw TOML is always shown.
func (a *App) profileShowExplain(w io.Writer, nameOrPath string) error {
	// 1. Print the raw TOML file (same as profileShow).
	if err := a.profileShow(w, nameOrPath); err != nil {
		return err
	}

	// 2. Load the merged profile to resolve inherited capabilities.
	p, err := a.loader.LoadProfileAuto(nameOrPath)
	if err != nil || len(p.Capabilities) == 0 {
		return nil // graceful degradation: raw TOML already shown
	}

	fmt.Fprintln(w)
	for _, name := range p.Capabilities {
		cap, ok := capabilityRegistry[name]
		if !ok {
			continue
		}
		printCapabilityExplain(w, name, cap.Explain())
	}
	return nil
}

// printCapabilityExplain writes a formatted capability section to w.
func printCapabilityExplain(w io.Writer, name string, e CapabilityExplain) {
	const lineWidth = 70
	header := "── capability: " + name + " "
	dashes := lineWidth - len(header)
	if dashes < 4 {
		dashes = 4
	}
	fmt.Fprintf(w, "%s%s\n", header, strings.Repeat("─", dashes))

	if len(e.Mounts) > 0 {
		fmt.Fprintln(w, "  mounts injected at runtime:")
		for _, m := range e.Mounts {
			fmt.Fprintf(w, "    %-22s → %-22s [safe copy]\n", m.Src, m.Dest)
			if m.Detail != "" {
				fmt.Fprintf(w, "    %-22s   %s\n", "", m.Detail)
			}
		}
	}
	if len(e.PreRun) > 0 {
		fmt.Fprintln(w, "  pre-run:")
		for _, action := range e.PreRun {
			fmt.Fprintf(w, "    %s\n", action)
		}
	}
	if len(e.Notes) > 0 {
		fmt.Fprintln(w, "  notes:")
		for _, note := range e.Notes {
			fmt.Fprintf(w, "    %s\n", note)
		}
	}
}

// resolveValidateNames returns the list of profile names to validate.
// If all is true it reads the profiles directory; otherwise it uses args.
func (a *App) resolveValidateNames(args []string, all bool) ([]string, error) {
	if all {
		entries, err := os.ReadDir(a.loader.ProfilesDir())
		if err != nil {
			return nil, fmt.Errorf("reading profiles directory: %w", err)
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
			}
		}
		return names, nil
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("provide a profile name or use --all")
	}
	return args, nil
}

// profileValidate validates the named profiles, writes results to w,
// and returns true if any fatal errors were found.
func (a *App) profileValidate(w io.Writer, names []string) (anyError bool, err error) {
	for _, name := range names {
		p, loadErr := a.loader.LoadProfile(name)
		if loadErr != nil {
			fmt.Fprintf(w, "%s: load error: %v\n", name, loadErr)
			anyError = true
			continue
		}
		result := profile.Validate(p, a.loader.WorkDir)
		if len(result.Issues) == 0 {
			fmt.Fprintf(w, "%s: ok\n", name)
			continue
		}
		for _, issue := range result.Issues {
			fmt.Fprintf(w, "%s: %s\n", name, issue)
		}
		if result.HasErrors() {
			anyError = true
		}
	}
	return anyError, nil
}

// profileNew creates a new profile from the default template and opens it.
func (a *App) profileNew(w io.Writer, name string) error {
	if err := config.ValidateProfileName(name); err != nil {
		return err
	}
	path := a.loader.ProfilePath(name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("profile %q already exists at %s", name, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating profiles directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(profileTemplate(name)), 0o644); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	fmt.Fprintf(w, "created %s\n", path)
	return a.editorFn(path)
}

// profileEdit opens an existing profile in the editor.
func (a *App) profileEdit(_ io.Writer, name string) error {
	path := a.loader.ResolveProfilePath(name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("profile %q not found", name)
		}
		return err
	}
	return a.editorFn(path)
}

// profileClone copies a profile to a new name.
func (a *App) profileClone(w io.Writer, src, dst string) error {
	if err := config.ValidateProfileName(src); err != nil {
		return err
	}
	if err := config.ValidateProfileName(dst); err != nil {
		return err
	}
	srcPath := a.loader.ProfilePath(src)
	dstPath := a.loader.ProfilePath(dst)

	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("destination profile %q already exists", dst)
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("source profile %q not found", src)
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("creating profiles directory: %w", err)
	}
	if err := os.WriteFile(dstPath, data, 0o644); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}
	fmt.Fprintf(w, "cloned %q -> %q\n", src, dst)
	return nil
}

var fetchProfileURL = config.FetchURL

// profileInstallFromURL downloads a profile TOML from rawURL and installs it
// in the global profiles directory (~/.config/inner/profiles/).
// name overrides the filename derived from the URL; if empty, the last path
// segment (without .toml extension) is used.
// If force is false and the destination file already exists, an error is returned.
func (a *App) profileInstallFromURL(w io.Writer, rawURL, name string, force bool) error {
	if !config.IsURL(rawURL) {
		return fmt.Errorf("not a URL: %s", rawURL)
	}

	// Derive the profile name from the URL path if not provided.
	if name == "" {
		u, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("invalid URL: %w", err)
		}
		segment := filepath.Base(u.Path)
		if segment == "." || segment == "/" || segment == "" {
			return fmt.Errorf("cannot derive profile name from URL %q — use --name", rawURL)
		}
		name = strings.TrimSuffix(segment, ".toml")
		if name == "" {
			return fmt.Errorf("cannot derive profile name from URL %q — use --name", rawURL)
		}
	}

	data, err := fetchProfileURL(rawURL)
	if err != nil {
		return fmt.Errorf("downloading profile: %w", err)
	}

	// Validate the TOML before writing.
	var p config.Profile
	if err := toml.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("invalid TOML from %s: %w", rawURL, err)
	}

	destPath := a.loader.ProfilePath(name)
	if _, err := os.Stat(destPath); err == nil && !force {
		return fmt.Errorf("profile %q already exists at %s (use --force to overwrite)", name, destPath)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating profiles directory: %w", err)
	}
	// Profile files are user-readable config, not credentials; 0644 is intentional.
	if err := os.WriteFile(destPath, data, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing profile: %w", err)
	}
	fmt.Fprintf(w, "installed profile %q to %s\n", name, destPath)
	return nil
}

// profileTemplate returns a starter TOML template for a new profile.
func profileTemplate(name string) string {
	return fmt.Sprintf(`schema_version = "1"
name = "%s"
description = ""

[sandbox]
network   = false
clipboard = false

[mounts]
# "~/projects/foo" = { dest = "/workspace", mode = "rw" }

[env]
clearenv = true
inherit  = ["TERM", "LANG", "HOME"]
# set = { "CI" = "true" }

# [git]
# strip_sections = ["credential", "core.hooksPath"]
# overrides      = { "push.default" = "nothing" }

[entrypoint]
cmd         = ""   # empty = $SHELL
args        = []
interactive = true

[output]
summary         = false
log             = "~/.config/inner/logs/"
timeout_seconds = 0
`, name)
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) profileListCmd() *cobra.Command {
	var wide bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List available profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.profileList(cmd.OutOrStdout(), wide)
		},
	}
	cmd.Flags().BoolVarP(&wide, "wide", "w", false, "Show scope, path, and shadowed profiles")
	return cmd
}

func (a *App) profileShowCmd() *cobra.Command {
	var explain, resolved bool
	cmd := &cobra.Command{
		Use:   "show NAME|PATH",
		Short: "Print the contents of a profile (name or file path)",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("requires a profile name or path\n\nUsage: %s", cmd.UseLine())
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case resolved:
				return a.profileShowResolved(cmd.OutOrStdout(), args[0])
			case explain:
				return a.profileShowExplain(cmd.OutOrStdout(), args[0])
			default:
				return a.profileShow(cmd.OutOrStdout(), args[0])
			}
		},
	}
	cmd.Flags().BoolVar(&explain, "explain", false, "Append human-readable capability details after the raw TOML")
	cmd.Flags().BoolVar(&resolved, "resolved", false, "Show the effective profile after resolving extends and capabilities")
	cmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return a.loader.ProfileNames(), cobra.ShellCompDirectiveDefault
	}
	return cmd
}

func (a *App) profileValidateCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "validate [NAME]",
		Short: "Validate a profile (or all profiles with --all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := a.resolveValidateNames(args, all)
			if err != nil {
				return err
			}
			hasErrors, err := a.profileValidate(cmd.OutOrStdout(), names)
			if err != nil {
				return err
			}
			if hasErrors {
				return fmt.Errorf("validation failed")
			}
			return nil
		},
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return a.loader.ProfileNames(), cobra.ShellCompDirectiveDefault
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Validate all profiles")
	return cmd
}

func (a *App) profileNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new NAME",
		Short: "Create a new profile and open it in $EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileNew(cmd.OutOrStdout(), args[0])
		},
	}
}

func (a *App) profileEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: "Open a profile in $EDITOR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileEdit(cmd.OutOrStdout(), args[0])
		},
	}
	cmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return a.loader.ProfileNames(), cobra.ShellCompDirectiveDefault
	}
	return cmd
}

func (a *App) profileCloneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clone SRC DST",
		Short: "Clone a profile",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileClone(cmd.OutOrStdout(), args[0], args[1])
		},
	}
	// Complete the first arg (SRC) with existing profile names; DST is a new name, no completion.
	cmd.ValidArgsFunction = func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return a.loader.ProfileNames(), cobra.ShellCompDirectiveDefault
		}
		return nil, cobra.ShellCompDirectiveDefault
	}
	return cmd
}

func (a *App) profileInstallCmd() *cobra.Command {
	var name string
	var force bool
	cmd := &cobra.Command{
		Use:   "install URL",
		Short: "Download and install a profile from a URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileInstallFromURL(cmd.OutOrStdout(), args[0], name, force)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Override the profile name (default: derived from URL)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing profile")
	return cmd
}
