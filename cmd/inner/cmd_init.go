package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/enr/inner/internal/setup"
	"github.com/spf13/cobra"
)

// init_ creates the ~/.inner directory structure, installs default profiles,
// and writes a starter config.toml if none exists.
func (a *App) init_(w io.Writer) error {
	r, err := setup.InitVerbose(a.loader.Dir)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "dir: %s\n", a.loader.Dir)

	if len(r.DirsCreated) > 0 {
		fmt.Fprintf(w, "created dirs: %s\n", strings.Join(r.DirsCreated, ", "))
	}

	if r.ConfigCreated {
		fmt.Fprintln(w, "config: created")
	} else {
		fmt.Fprintln(w, "config: already exists (skipped)")
	}

	for _, name := range r.ProfilesInstalled {
		fmt.Fprintf(w, "profile %s: installed\n", name)
	}
	for _, name := range r.ProfilesSkipped {
		fmt.Fprintf(w, "profile %s: already exists (skipped)\n", name)
	}

	return nil
}

func (a *App) newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize ~/.inner (profiles, config, directories)",
		Long:  "Create the ~/.inner directory structure, install default profiles, and write a starter config.toml if none exists. Idempotent: safe to run multiple times.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.init_(cmd.OutOrStdout())
		},
	}
}
