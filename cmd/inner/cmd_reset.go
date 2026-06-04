package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/enr/inner/internal/setup"
	"github.com/spf13/cobra"
)

// reset_ overwrites the built-in default profiles in ~/.inner/profiles/ with
// the versions embedded in the binary. Only files whose name matches an
// embedded profile are touched; user-created profiles are left untouched.
func (a *App) reset_(w io.Writer, force bool) error {
	dir := a.loader.Dir

	// If the config directory does not exist at all, just init.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Fprintln(w, "config directory does not exist, running init...")
		return a.init_(w, false)
	}

	if !force {
		fmt.Fprintf(w, colorizeW(w, ansiBoldYellow, "warning")+": overwrites all built-in profiles in %s/profiles/\n", dir)
		fmt.Fprintln(w, "         user-created profiles are not affected.")
		fmt.Fprint(w, "continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(w, "aborted.")
			return nil
		}
	}

	names, err := setup.ResetProfiles(dir)
	if err != nil {
		return err
	}

	for _, name := range names {
		fmt.Fprintf(w, "profile %s: reset\n", name)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "reset complete.")
	return nil
}

func (a *App) newResetCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Restore built-in profiles to the versions embedded in the binary",
		Long: `Overwrites all built-in default profiles in ~/.config/inner/profiles/ with the
versions embedded in the current binary.

Only profiles whose filename matches a built-in profile are overwritten.
User-created profiles (any file not shipped by inner) are left untouched.
Use this command to pick up updated built-in profiles after an upgrade.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.reset_(cmd.OutOrStdout(), force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip the confirmation prompt")
	return cmd
}
