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
		Short: "Manage global configuration",
	}
	cmd.AddCommand(
		a.configShowCmd(),
		a.configEditCmd(),
	)
	return cmd
}

// ── Business logic (testable) ─────────────────────────────────────────────────

// configShow writes the global config file contents to w.
func (a *App) configShow(w io.Writer) error {
	path := a.loader.GlobalConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(w, "No global config found at %s\n", path)
			fmt.Fprintln(w, "(using built-in defaults)")
			return nil
		}
		return err
	}
	content := string(data)
	if f, ok := w.(*os.File); ok && isTTY(f) {
		content = highlightTOML(content)
	}
	fmt.Fprintf(w, "# %s\n", path)
	_, err = fmt.Fprint(w, content)
	return err
}

// configEdit ensures the global config file exists, then opens it in the editor.
func (a *App) configEdit(_ io.Writer) error {
	path := a.loader.GlobalConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("creating config directory: %w", err)
		}
		if err := os.WriteFile(path, []byte(globalConfigTemplate()), 0o644); err != nil {
			return fmt.Errorf("writing default config: %w", err)
		}
	}
	return a.editorFn(path)
}

// globalConfigTemplate returns a starter config.toml with commented defaults.
func globalConfigTemplate() string {
	return `# inner global configuration

# Directory where run logs are stored.
# log_dir = "~/.inner/logs/"
`
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the global configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.configShow(cmd.OutOrStdout())
		},
	}
}

func (a *App) configEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the global config in $EDITOR",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.configEdit(cmd.OutOrStdout())
		},
	}
}
