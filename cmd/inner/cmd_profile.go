package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

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
	// default_profile may be a file path (e.g. ".inner/profiles/foo.toml");
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
	path := a.loader.ProfilePath(name)
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
log             = "~/.inner/logs/"
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
	cmd := &cobra.Command{
		Use:   "show NAME|PATH",
		Short: "Print the contents of a profile (name or file path)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.profileShow(cmd.OutOrStdout(), args[0])
		},
	}
	cmd.ValidArgsFunction = func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return a.loader.ProfileNames(), cobra.ShellCompDirectiveNoFileComp
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
			return a.loader.ProfileNames(), cobra.ShellCompDirectiveNoFileComp
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
		return a.loader.ProfileNames(), cobra.ShellCompDirectiveNoFileComp
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
			return a.loader.ProfileNames(), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}
