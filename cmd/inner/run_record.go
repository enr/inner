package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/enr/inner/internal/config"
)

// runRecord is the metadata inner keeps about every run, interactive or not,
// as <logs dir>/<run id>.json. It is what makes a run ID addressable: the
// output log exists only for non-interactive runs with logging on, the record
// always exists. Its JSON form is the stable output of `inner log show --json`.
//
// The entrypoint arguments are stored as given (a prompt is an argument); the
// file is 0600 like the logs. Redacting secrets out of it is ISS-14.
type runRecord struct {
	RunID       string     `json:"run_id"`
	Profile     string     `json:"profile"`
	ProfilePath string     `json:"profile_path,omitempty"`
	Entrypoint  string     `json:"entrypoint"`
	Args        []string   `json:"args,omitempty"`
	Workdir     string     `json:"workdir,omitempty"`
	Interactive bool       `json:"interactive"`
	Home        string     `json:"home"`
	Network     string     `json:"network"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	DurationMS  int64      `json:"duration_ms,omitempty"`
	// ExitCode is absent while the run is in progress (or if inner itself
	// died before the sandbox exited).
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"` // a launch failure, as opposed to a non-zero exit
	LogPath  string `json:"log_path,omitempty"`
}

// runIDPattern accepts the IDs inner generates (YYYYMMDD-HHMMSS-xxxx) and any
// other plain file stem, never a path.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// checkRunID refuses a run ID that could address a file outside the logs dir.
func checkRunID(id string) error {
	if !runIDPattern.MatchString(id) || filepath.Base(id) != id {
		return fmt.Errorf("invalid run ID %q", id)
	}
	return nil
}

// logsDir is where inner keeps run records, and output logs unless a profile
// sends them elsewhere ([output] log): log_dir from the effective config, or
// ~/.config/inner/logs.
func (a *App) logsDir() string {
	if g, err := a.loader.LoadEffectiveGlobal(); err == nil && g.LogDir != "" {
		return config.ExpandPath(g.LogDir)
	}
	return filepath.Join(a.loader.Dir, "logs")
}

// writeRunRecord writes rec atomically (temp file + rename) so a reader never
// sees half a record.
func writeRunRecord(dir string, rec *runRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".run-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name()) //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name()) //nolint:errcheck
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, rec.RunID+".json")); err != nil {
		os.Remove(tmp.Name()) //nolint:errcheck
		return err
	}
	return nil
}

// readRunRecord reads the record of runID from dir.
func readRunRecord(dir, runID string) (*runRecord, error) {
	data, err := os.ReadFile(filepath.Join(dir, runID+".json"))
	if err != nil {
		return nil, err
	}
	var rec runRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("run record %s: %w", runID, err)
	}
	return &rec, nil
}

// finish completes rec with the outcome of the run.
func (rec *runRecord) finish(exitCode int, runErr error, logPath string) {
	now := time.Now()
	rec.EndedAt = &now
	rec.DurationMS = now.Sub(rec.StartedAt).Milliseconds()
	if runErr != nil {
		rec.Error = runErr.Error()
	} else {
		rec.ExitCode = &exitCode
	}
	rec.LogPath = logPath
}

// writeJSON writes v as indented JSON followed by a newline: the one encoding
// every --json output uses.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
