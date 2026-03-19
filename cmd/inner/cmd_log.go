package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// ── Business logic ────────────────────────────────────────────────────────────

// logEntry holds metadata for a single log file.
type logEntry struct {
	RunID   string
	Path    string
	ModTime time.Time
	Size    int64
}

// listLogs returns all log entries from the logs directory, newest first.
func listLogs(logsDir string) ([]logEntry, error) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var logs []logEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		runID := strings.TrimSuffix(e.Name(), ".log")
		logs = append(logs, logEntry{
			RunID:   runID,
			Path:    filepath.Join(logsDir, e.Name()),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
	}

	sort.Slice(logs, func(i, j int) bool {
		return logs[i].ModTime.After(logs[j].ModTime)
	})
	return logs, nil
}

// (a *App) logList prints a table of all runs in the logs directory.
func (a *App) logList(w io.Writer) error {
	logsDir := filepath.Join(a.loader.Dir, "logs")
	logs, err := listLogs(logsDir)
	if err != nil {
		return fmt.Errorf("reading logs dir: %w", err)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN_ID\tDATE\tSIZE")
	for _, l := range logs {
		fmt.Fprintf(tw, "%s\t%s\t%s\n",
			l.RunID,
			l.ModTime.Format("2006-01-02 15:04:05"),
			formatSize(l.Size),
		)
	}
	return tw.Flush()
}

// (a *App) logShow prints the content of a single run log.
func (a *App) logShow(runID string) error {
	logsDir := filepath.Join(a.loader.Dir, "logs")
	logPath := filepath.Join(logsDir, runID+".log")

	if _, err := os.Stat(logPath); err != nil {
		return fmt.Errorf("log not found: %s", runID)
	}

	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less"
	}
	// If pager is not available, fall back to cat.
	if _, err := exec.LookPath(pager); err != nil {
		pager = "cat"
	}

	c := exec.Command(pager, logPath)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// (a *App) logClean removes log files older than olderThan days.
// If dryRun is true, it only lists what would be deleted.
func (a *App) logClean(w io.Writer, olderThan int, dryRun bool) error {
	logsDir := filepath.Join(a.loader.Dir, "logs")
	logs, err := listLogs(logsDir)
	if err != nil {
		return fmt.Errorf("reading logs dir: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -olderThan)
	var candidates []logEntry
	for _, l := range logs {
		if l.ModTime.Before(cutoff) {
			candidates = append(candidates, l)
		}
	}

	if len(candidates) == 0 {
		fmt.Fprintln(w, "no logs to clean")
		return nil
	}

	for _, l := range candidates {
		if dryRun {
			fmt.Fprintf(w, "would delete: %s (%s)\n", l.RunID, l.ModTime.Format("2006-01-02"))
			continue
		}
		if err := os.Remove(l.Path); err != nil {
			fmt.Fprintf(w, "error deleting %s: %v\n", l.RunID, err)
			continue
		}
		// Also remove a matching summary file if present.
		summaryPath := filepath.Join(logsDir, l.RunID+".summary")
		_ = os.Remove(summaryPath) // best-effort
		fmt.Fprintf(w, "deleted: %s\n", l.RunID)
	}
	return nil
}

// formatSize returns a human-readable file size.
func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ── Cobra wiring (thin) ───────────────────────────────────────────────────────

func (a *App) newLogCmd() *cobra.Command {
	logCmd := &cobra.Command{
		Use:   "log",
		Short: "Manage run logs",
	}
	logCmd.AddCommand(a.newLogListCmd())
	logCmd.AddCommand(a.newLogShowCmd())
	logCmd.AddCommand(a.newLogCleanCmd())
	return logCmd
}

func (a *App) newLogListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all run logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.logList(cmd.OutOrStdout())
		},
	}
}

func (a *App) newLogShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show RUN_ID",
		Short: "Show the content of a run log",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return a.logShow(args[0])
		},
	}
}

func (a *App) newLogCleanCmd() *cobra.Command {
	var olderThan int
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Delete old run logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.logClean(cmd.OutOrStdout(), olderThan, dryRun)
		},
	}
	cmd.Flags().IntVar(&olderThan, "older-than", 30, "Delete logs older than N days")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List logs that would be deleted without deleting them")
	return cmd
}
