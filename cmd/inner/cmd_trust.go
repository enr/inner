package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/enr/inner/internal/config"
)

// trust_ records the current content of project files as approved by the user
// (see internal/config/trust.go). With no paths it approves every project
// config file found from the working directory and every profile in
// .config/inner/profiles; each is listed so the user sees what they approve.
func (a *App) trust_(w io.Writer, paths []string) error {
	if len(paths) == 0 {
		paths = a.projectTrustCandidates()
		if len(paths) == 0 {
			fmt.Fprintln(w, "no project config files or project profiles found")
			return nil
		}
	}
	results, err := a.loader.Trust(paths...)
	if err != nil {
		return err
	}
	// Changed files first and loudest: an approval that existed for other
	// content is exactly what a sandbox rewriting a project file looks like,
	// and must not blend in with files the user already reviewed.
	for _, state := range []config.TrustState{config.TrustChanged, config.TrustNew, config.TrustUnchanged} {
		for _, r := range results {
			if r.State != state {
				continue
			}
			switch state {
			case config.TrustChanged:
				fmt.Fprintf(w, "trusted %s (sha256:%s) — CHANGED since last approval\n", r.Path, r.Digest)
			case config.TrustNew:
				fmt.Fprintf(w, "trusted %s (sha256:%s) — new\n", r.Path, r.Digest)
			case config.TrustUnchanged:
				fmt.Fprintf(w, "already trusted %s (unchanged)\n", r.Path)
			}
		}
	}
	return nil
}

// projectTrustCandidates lists the project files subject to trust.
func (a *App) projectTrustCandidates() []string {
	files := a.loader.ProjectConfigFiles()
	if dir := a.loader.LocalProfilesDir(); dir != "" {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	return files
}

// trustQuietly approves a file the user has just created or edited through
// inner itself. Failure only costs a later warning, so it is not an error.
func (a *App) trustQuietly(path string) {
	if _, err := a.loader.Trust(path); err != nil {
		fmt.Fprintf(a.errW(), "inner: warning: cannot record %s as trusted: %v\n", path, err)
	}
}

func (a *App) newTrustCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "trust [FILE...]",
		Short: "Approve the project config files and project profiles in this directory",
		Long: `inner honors a project config file (inner.toml, .config/inner.toml, …) or a
project profile (.config/inner/profiles/*.toml, or a profile path under the
working directory) only after you approve its exact content with this command.

The working directory is what a sandbox can write: without approval, an agent
could drop an inner.toml or rewrite a project profile and so choose the policy
of your next run. A freshly cloned repository could do the same. Any change to
an approved file withdraws the approval: review the change, then trust it again.

With no arguments, every project config file found from the current directory
and its ancestors, and every profile in .config/inner/profiles, is approved and
listed. A file whose approval existed for different content is flagged as
CHANGED: someone — possibly a sandbox — modified it since you last reviewed it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.trust_(cmd.OutOrStdout(), args)
		},
	}
}
