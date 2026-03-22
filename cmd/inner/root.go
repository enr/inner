package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/enr/inner/internal/version"
	"github.com/spf13/cobra"
)

// buildRootCmd assembles the full cobra command tree for the given App.
// Keeping this as a constructor (not a package-level var) means tests can
// instantiate independent command trees without shared state.
func buildRootCmd(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "inner",
		Short:         "Run agentic tools in isolated, reproducible environments",
		Long:          `inner launches agentic tools (Claude Code, Aider, interactive shell) in isolated sandboxes backed by bubblewrap on Linux.`,
		Version:       version.Version,
		SilenceUsage:  true, // don't dump usage on every operational error
		SilenceErrors: true, // we print the error ourselves in execute()
	}
	root.SetVersionTemplate(fmt.Sprintf("inner %s (git: %s, built: %s)\n",
		version.Version, version.GitCommit, version.BuildTime))
	root.AddCommand(newVersionCmd())
	root.AddCommand(app.newInitCmd())
	root.AddCommand(app.newResetCmd())
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
	root := buildRootCmd(app)
	expandAliases(root, app)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// expandAliases checks whether os.Args[1] matches a configured alias and, if
// so, rewrites the cobra arg list by prepending the alias expansion.
// Built-in commands are never shadowed: if the alias name collides with an
// existing subcommand the alias is silently skipped.
func expandAliases(root *cobra.Command, app *App) {
	if len(os.Args) < 2 {
		return
	}
	name := os.Args[1]
	// Skip flags and built-in commands.
	if strings.HasPrefix(name, "-") {
		return
	}
	if cmd, _, err := root.Find([]string{name}); err == nil && cmd != root {
		return // built-in takes precedence
	}
	aliases, err := app.loader.Aliases()
	if err != nil || len(aliases) == 0 {
		return
	}
	expansion, ok := aliases[name]
	if !ok || strings.TrimSpace(expansion) == "" {
		return
	}
	expanded := strings.Fields(expansion)
	root.SetArgs(append(expanded, os.Args[2:]...))
}
