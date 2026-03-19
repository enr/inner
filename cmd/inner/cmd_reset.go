package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/enr/inner/internal/setup"
	"github.com/spf13/cobra"
)

// reset_ backs up the current ~/.inner contents into ~/.inner/backups/<datetime>/
// and re-initializes ~/.inner with default profiles and config.
func (a *App) reset_(w io.Writer, force bool) error {
	dir := a.loader.Dir

	// If ~/.inner does not exist at all, just init.
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Fprintln(w, "~/.inner non esiste, eseguo init...")
		return a.init_(w)
	}

	if !force {
		fmt.Fprintf(w, "questo comando archivia il contenuto di %s e ricrea la configurazione di default.\n", dir)
		fmt.Fprint(w, "continuare? [s/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "s" {
			fmt.Fprintln(w, "annullato.")
			return nil
		}
	}

	// Collect entries to back up (everything except backups/ itself).
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("leggendo %s: %w", dir, err)
	}

	var toMove []string
	for _, e := range entries {
		if e.Name() == "backups" {
			continue
		}
		toMove = append(toMove, e.Name())
	}

	// Create backup dir (even if there is nothing to move, record the attempt).
	timestamp := time.Now().Format("20060102-150405")
	backupDir := filepath.Join(dir, "backups", timestamp)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("creando backup dir: %w", err)
	}

	// Move each entry into the backup.
	for _, name := range toMove {
		src := filepath.Join(dir, name)
		dst := filepath.Join(backupDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("spostando %s: %w", name, err)
		}
	}

	if len(toMove) > 0 {
		fmt.Fprintf(w, "backup salvato in: %s\n", backupDir)
	}

	// Re-initialize.
	r, err := setup.InitVerbose(dir)
	if err != nil {
		return err
	}

	for _, name := range r.ProfilesInstalled {
		fmt.Fprintf(w, "profile %s: installato\n", name)
	}
	if r.ConfigCreated {
		fmt.Fprintln(w, "config: creata")
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "reset completato.")
	if len(toMove) > 0 {
		fmt.Fprintf(w, "per annullare: mv %s/* %s/\n", backupDir, dir)
	}
	return nil
}

func (a *App) newResetCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Archivia ~/.inner e ripristina la configurazione di default",
		Long: `Sposta il contenuto di ~/.inner in ~/.inner/backups/<datetime>/ e
ricrea profili e configurazione di default tramite init.

Utile per aggiornare i profili built-in dopo un upgrade o per ripartire
da zero senza perdere la storia (il backup resta in ~/.inner/backups/).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.reset_(cmd.OutOrStdout(), force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Salta la richiesta di conferma")
	return cmd
}
