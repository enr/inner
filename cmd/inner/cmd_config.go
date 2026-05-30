package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func (a *App) newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}
	cmd.AddCommand(
		a.configShowCmd(),
		a.configEditCmd(),
	)
	return cmd
}

// ── Business logic (testable) ─────────────────────────────────────────────────

// configShow writes all config sections (user + project) to w.
func (a *App) configShow(w io.Writer) error {
	globalPath := a.loader.GlobalConfigPath()
	if err := showConfigSection(w, "Global", globalPath); err != nil {
		return err
	}

	if a.loader.WorkDir == "" {
		return nil
	}

	projectFiles := a.loader.ProjectConfigFiles()
	if len(projectFiles) == 0 {
		localPath := a.loader.LocalConfigPath()
		if localPath != "" {
			fmt.Fprintln(w)
			if err := showConfigSection(w, "Local", localPath); err != nil {
				return err
			}
		}
		return nil
	}

	for _, path := range projectFiles {
		fmt.Fprintln(w)
		label := "Local"
		if rel, err := filepath.Rel(a.loader.WorkDir, path); err == nil {
			label = "Local: " + rel
		}
		if err := showConfigSection(w, label, path); err != nil {
			return err
		}
	}
	return nil
}

// showConfigSection prints a labelled config file section to w.
// If the file does not exist a friendly placeholder is printed instead.
func showConfigSection(w io.Writer, label, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "# %s: %s\n", label, path)
			fmt.Fprintln(w, "# (no file — using defaults)")
			return nil
		}
		return err
	}
	content := string(data)
	if f, ok := w.(*os.File); ok && isTTY(f) {
		content = highlightTOML(content)
	}
	fmt.Fprintf(w, "# %s: %s\n", label, path)
	_, err = fmt.Fprint(w, content)
	return err
}

// configEdit ensures the target config file exists, then opens it in the editor.
// useLocal=true edits the local (directory-level) config; false edits the global config.
func (a *App) configEdit(_ io.Writer, useLocal bool) error {
	var path string
	var tmpl string
	if useLocal {
		path = a.loader.LocalConfigPath()
		if path == "" {
			return fmt.Errorf("local config requires a working directory (WorkDir not set)")
		}
		tmpl = localConfigTemplate()
	} else {
		path = a.loader.GlobalConfigPath()
		tmpl = globalConfigTemplate()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating config directory: %w", err)
		}
		if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
			return fmt.Errorf("writing default config: %w", err)
		}
	}
	return a.editorFn(path)
}

// globalConfigTemplate returns a starter global config.toml with commented defaults.
func globalConfigTemplate() string {
	return `# inner global configuration (~/.config/inner/config.toml)

# Profile used by default when -p is not specified.
# default_profile = "shell"

# Directory where run logs are stored.
# log_dir = "~/.config/inner/logs/"

# Aliases expand a short name to a full inner command.
# Example: "inner review" → "inner run --profile code-review"
# [aliases]
# review = "run --profile code-review"
# chat   = "run --profile claude-interactive"
`
}

// localConfigTemplate returns a starter local config.toml with commented defaults.
func localConfigTemplate() string {
	return `# inner project configuration (.config/inner.toml)
# Settings here override the user config for this project.
# Commit this file; use inner.local.toml for secrets and personal overrides.

# Profile used by default in this project when -p is not specified.
# default_profile = "my-project-profile"

# Aliases expand a short name to a full inner command (project overrides global).
# [aliases]
# review = "run --profile code-review"
`
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print global and local configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.configShow(cmd.OutOrStdout())
		},
	}
}

func (a *App) configEditCmd() *cobra.Command {
	var useLocal bool
	var useGlobal bool

	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Open a config file in $EDITOR",
		Long: `Open a configuration file in $EDITOR.

  --global (default): edit ~/.config/inner/config.toml
  --local:            edit .config/inner.toml in the current directory`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if useLocal && useGlobal {
				return fmt.Errorf("--local and --global are mutually exclusive")
			}
			return a.configEdit(cmd.OutOrStdout(), useLocal)
		},
	}
	cmd.Flags().BoolVar(&useGlobal, "global", false, "Edit the global config (default)")
	cmd.Flags().BoolVar(&useLocal, "local", false, "Edit the local (directory-level) config")
	return cmd
}
