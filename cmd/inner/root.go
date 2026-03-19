package main

import (
	"fmt"
	"os"

	"github.com/enr/inner/internal/version"
	"github.com/spf13/cobra"
)

// buildRootCmd assembles the full cobra command tree for the given App.
// Keeping this as a constructor (not a package-level var) means tests can
// instantiate independent command trees without shared state.
func buildRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:     "inner",
		Short:   "Run agentic tools in isolated, reproducible environments",
		Long:    `inner launches agentic tools (Claude Code, Aider, interactive shell) in isolated sandboxes backed by bubblewrap on Linux.`,
		Version: version.Version,
	}
	root.SetVersionTemplate(fmt.Sprintf("inner %s (git: %s, built: %s)\n",
		version.Version, version.GitCommit, version.BuildTime))
	root.AddCommand(newVersionCmd())
	root.AddCommand(app.newRunCmd())
	root.AddCommand(app.newProfileCmd())
	root.AddCommand(app.newConfigCmd())
	root.AddCommand(app.newDoctorCmd())
	root.AddCommand(app.newLogCmd())
	root.AddCommand(app.newVerifyCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "inner %s (git: %s, built: %s)\n",
				version.Version, version.GitCommit, version.BuildTime)
		},
	}
}

func execute() {
	app, err := newApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := buildRootCmd(app).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
