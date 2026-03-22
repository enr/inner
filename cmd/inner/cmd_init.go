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
// If local is true, creates a .inner/ directory in the current working directory instead.
func (a *App) init_(w io.Writer, local bool) error {
	if local {
		if a.loader.WorkDir == "" {
			return fmt.Errorf("--local requires a working directory")
		}
		r, err := setup.InitLocal(a.loader.WorkDir)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "dir: %s\n", a.loader.WorkDir)
		if len(r.DirsCreated) > 0 {
			fmt.Fprintf(w, "created dirs: %s\n", strings.Join(r.DirsCreated, ", "))
		}
		if r.ConfigCreated {
			fmt.Fprintln(w, "config: created")
		} else {
			fmt.Fprintln(w, "config: already exists (skipped)")
		}
		return nil
	}

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
	var local bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize ~/.inner (profiles, config, directories)",
		Long: `Create the ~/.inner directory structure, install default profiles, and write a starter config.toml if none exists. Idempotent: safe to run multiple times.

With --local: create a .inner/ directory in the current directory with a starter config.toml and an empty profiles/ folder.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.init_(cmd.OutOrStdout(), local)
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "Initialize a local .inner/ directory in the current directory")
	return cmd
}
