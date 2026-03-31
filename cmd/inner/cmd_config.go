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

// configShow writes global and local config sections to w.
func (a *App) configShow(w io.Writer) error {
	globalPath := a.loader.GlobalConfigPath()
	if err := showConfigSection(w, "Global", globalPath); err != nil {
		return err
	}
	localPath := a.loader.LocalConfigPath()
	if localPath == "" || localPath == globalPath {
		return nil
	}
	fmt.Fprintln(w)
	return showConfigSection(w, "Local", localPath)
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
	return `# inner global configuration

# Profile used by default when -p is not specified.
# default_profile = "shell"

# Directory where run logs are stored.
# log_dir = "~/.inner/logs/"

# Aliases expand a short name to a full inner command.
# Example: "inner review" → "inner run --profile code-review"
# [aliases]
# review = "run --profile code-review"
# chat   = "run --profile claude-interactive"
`
}

// localConfigTemplate returns a starter local config.toml with commented defaults.
func localConfigTemplate() string {
	return `# inner local configuration (directory-level)
# Settings here override ~/.inner/config.toml for this directory.

# Profile used by default in this directory when -p is not specified.
# default_profile = "my-project-profile"

# Directory where run logs are stored.
# log_dir = "~/.inner/logs/"

# Aliases expand a short name to a full inner command (local overrides global).
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

  --global (default): edit ~/.inner/config.toml
  --local:            edit .inner/config.toml in the current directory`,
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
