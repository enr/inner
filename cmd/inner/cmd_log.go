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

// logEntry describes one run found in the logs directory: its record
// (<id>.json, written for every run since run IDs became addressable), its
// output log (<id>.log, non-interactive runs with logging on), or both.
type logEntry struct {
	RunID   string
	Path    string     // the output log, "" if the run has none
	ModTime time.Time  // when the run started (record), else the log's mtime
	Size    int64      // size of the output log
	Record  *runRecord // nil for runs older than run records
}

// listLogs returns every run in the logs directory, newest first.
func listLogs(logsDir string) ([]logEntry, error) {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	byID := map[string]*logEntry{}
	get := func(id string) *logEntry {
		if e, ok := byID[id]; ok {
			return e
		}
		e := &logEntry{RunID: id}
		byID[id] = e
		return e
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".log"):
			info, err := e.Info()
			if err != nil {
				continue
			}
			l := get(strings.TrimSuffix(name, ".log"))
			l.Path = filepath.Join(logsDir, name)
			l.Size = info.Size()
			if l.Record == nil {
				l.ModTime = info.ModTime()
			}
		case strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(name, ".json")
			rec, err := readRunRecord(logsDir, id)
			if err != nil {
				continue
			}
			l := get(id)
			l.Record = rec
			l.ModTime = rec.StartedAt
			// A profile's [output] log can send the output elsewhere.
			if l.Path == "" && rec.LogPath != "" {
				if info, err := os.Stat(rec.LogPath); err == nil {
					l.Path, l.Size = rec.LogPath, info.Size()
				}
			}
		}
	}

	logs := make([]logEntry, 0, len(byID))
	for _, l := range byID {
		logs = append(logs, *l)
	}
	sort.Slice(logs, func(i, j int) bool {
		if !logs[i].ModTime.Equal(logs[j].ModTime) {
			return logs[i].ModTime.After(logs[j].ModTime)
		}
		return logs[i].RunID > logs[j].RunID
	})
	return logs, nil
}

// logListItem is one row of `inner log list --json`.
type logListItem struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	Profile   string    `json:"profile,omitempty"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	LogPath   string    `json:"log_path,omitempty"`
	LogSize   int64     `json:"log_size"`
}

// (a *App) logList prints a table of all runs in the logs directory.
func (a *App) logList(w io.Writer, asJSON bool) error {
	logs, err := listLogs(a.logsDir())
	if err != nil {
		return fmt.Errorf("reading logs dir: %w", err)
	}

	if asJSON {
		items := make([]logListItem, 0, len(logs))
		for _, l := range logs {
			item := logListItem{RunID: l.RunID, StartedAt: l.ModTime, LogPath: l.Path, LogSize: l.Size}
			if l.Record != nil {
				item.Profile, item.ExitCode = l.Record.Profile, l.Record.ExitCode
			}
			items = append(items, item)
		}
		return writeJSON(w, items)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN_ID\tDATE\tPROFILE\tEXIT\tSIZE")
	for _, l := range logs {
		profile, exit, size := "-", "-", "-"
		if l.Record != nil {
			profile = l.Record.Profile
			switch {
			case l.Record.ExitCode != nil:
				exit = fmt.Sprint(*l.Record.ExitCode)
			case l.Record.Error != "":
				exit = "error"
			case l.Record.EndedAt == nil:
				exit = "running"
			}
		}
		if l.Path != "" {
			size = formatSize(l.Size)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			l.RunID,
			l.ModTime.Format("2006-01-02 15:04:05"),
			profile, exit, size,
		)
	}
	return tw.Flush()
}

// (a *App) logShow prints what inner knows about one run: the run record,
// then the output log (through $PAGER when writing to a terminal). With
// asJSON it prints the record alone, as JSON.
func (a *App) logShow(w io.Writer, runID string, asJSON bool) error {
	if err := checkRunID(runID); err != nil {
		return err
	}
	logsDir := a.logsDir()
	rec, recErr := readRunRecord(logsDir, runID)
	logPath := filepath.Join(logsDir, runID+".log")
	if rec != nil && rec.LogPath != "" {
		logPath = rec.LogPath
	}
	if _, err := os.Stat(logPath); err != nil {
		logPath = ""
	}
	if recErr != nil && logPath == "" {
		return fmt.Errorf("run not found: %s", runID)
	}

	if asJSON {
		if rec == nil {
			// A run older than run records: only the log is known.
			rec = &runRecord{RunID: runID}
		}
		rec.LogPath = logPath
		return writeJSON(w, rec)
	}

	if rec != nil {
		printRunRecord(w, rec)
	}
	if logPath == "" {
		if rec != nil {
			fmt.Fprintln(w, "log:         none (interactive run, or logging off)")
		}
		return nil
	}
	if rec != nil {
		fmt.Fprintf(w, "log:         %s\n\n", logPath)
	}

	if f, ok := w.(*os.File); ok && isTTY(f) {
		pager := os.Getenv("PAGER")
		if pager == "" {
			pager = "less"
		}
		if _, err := exec.LookPath(pager); err == nil {
			c := exec.Command(pager, logPath)
			c.Stdin = os.Stdin
			c.Stdout = f
			c.Stderr = os.Stderr
			return c.Run()
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// printRunRecord writes the human-readable header of `inner log show`.
func printRunRecord(w io.Writer, rec *runRecord) {
	fmt.Fprintf(w, "run:         %s\n", rec.RunID)
	fmt.Fprintf(w, "profile:     %s", rec.Profile)
	if rec.ProfilePath != "" {
		fmt.Fprintf(w, " (%s)", tildePath(rec.ProfilePath))
	}
	fmt.Fprintln(w)
	cmd := rec.Entrypoint
	if len(rec.Args) > 0 {
		cmd += " " + strings.Join(rec.Args, " ")
	}
	fmt.Fprintf(w, "command:     %s\n", cmd)
	if rec.Workdir != "" {
		fmt.Fprintf(w, "workdir:     %s\n", rec.Workdir)
	}
	fmt.Fprintf(w, "home:        %s\n", rec.Home)
	fmt.Fprintf(w, "network:     %s\n", rec.Network)
	fmt.Fprintf(w, "started:     %s\n", rec.StartedAt.Format("2006-01-02 15:04:05"))
	switch {
	case rec.ExitCode != nil:
		fmt.Fprintf(w, "exit code:   %d (after %s)\n", *rec.ExitCode, (time.Duration(rec.DurationMS) * time.Millisecond).String())
	case rec.Error != "":
		fmt.Fprintf(w, "error:       %s\n", rec.Error)
	default:
		fmt.Fprintln(w, "exit code:   - (running, or inner stopped before the sandbox exited)")
	}
}

// (a *App) logClean removes log files older than olderThan days.
// If dryRun is true, it only lists what would be deleted.
func (a *App) logClean(w io.Writer, olderThan int, dryRun bool) error {
	logsDir := a.logsDir()
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
		if l.Path != "" {
			if err := os.Remove(l.Path); err != nil {
				fmt.Fprintf(w, "error deleting %s: %v\n", l.RunID, err)
				continue
			}
		}
		// The record and a legacy summary file go with it (best-effort).
		_ = os.Remove(filepath.Join(logsDir, l.RunID+".json"))
		_ = os.Remove(filepath.Join(logsDir, l.RunID+".summary"))
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
	var asJSON bool
	logCmd := &cobra.Command{
		Use:   "log [RUN_ID]",
		Short: "Manage run logs (inner log RUN_ID is inner log show RUN_ID)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return a.logShow(cmd.OutOrStdout(), args[0], asJSON)
		},
	}
	logCmd.Flags().BoolVar(&asJSON, "json", false, "Print the run record as JSON")
	logCmd.AddCommand(a.newLogListCmd())
	logCmd.AddCommand(a.newLogShowCmd())
	logCmd.AddCommand(a.newLogCleanCmd())
	return logCmd
}

func (a *App) newLogListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all runs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.logList(cmd.OutOrStdout(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the runs as a JSON array")
	return cmd
}

func (a *App) newLogShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show RUN_ID",
		Short: "Show a run: its record, then its output log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.logShow(cmd.OutOrStdout(), args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the run record as JSON")
	return cmd
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
