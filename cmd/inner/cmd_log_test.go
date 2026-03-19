package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLogFile creates a log file in dir/logs/ with the given modtime.
func writeLogFile(t *testing.T, logsDir, runID string, modTime time.Time, content string) {
	t.Helper()
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	path := filepath.Join(logsDir, runID+".log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// ── listLogs ──────────────────────────────────────────────────────────────────

func TestListLogs_empty(t *testing.T) {
	dir := t.TempDir()
	logs, err := listLogs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 logs, got %d", len(logs))
	}
}

func TestListLogs_nonExistentDir(t *testing.T) {
	logs, err := listLogs("/nonexistent/path/logs")
	if err != nil {
		t.Fatalf("expected no error for missing dir, got: %v", err)
	}
	if logs != nil {
		t.Errorf("expected nil, got %v", logs)
	}
}

func TestListLogs_newestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLogFile(t, dir, "20240101-120000-aaaa", now.Add(-2*time.Hour), "old")
	writeLogFile(t, dir, "20240101-140000-bbbb", now.Add(-1*time.Hour), "newer")
	writeLogFile(t, dir, "20240101-150000-cccc", now, "newest")

	logs, err := listLogs(dir)
	if err != nil {
		t.Fatalf("listLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}
	if logs[0].RunID != "20240101-150000-cccc" {
		t.Errorf("first entry should be newest, got %s", logs[0].RunID)
	}
}

func TestListLogs_skipsNonLogFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLogFile(t, dir, "run-1", now, "content")
	// Write a non-.log file.
	os.WriteFile(filepath.Join(dir, "run-1.summary"), []byte("summary"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes"), 0o644)

	logs, err := listLogs(dir)
	if err != nil {
		t.Fatalf("listLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(logs))
	}
}

// ── formatSize ────────────────────────────────────────────────────────────────

func TestFormatSize_bytes(t *testing.T) {
	if s := formatSize(512); s != "512 B" {
		t.Errorf("got %q", s)
	}
}

func TestFormatSize_kilobytes(t *testing.T) {
	s := formatSize(2048)
	if !strings.Contains(s, "KB") {
		t.Errorf("got %q, want KB", s)
	}
}

func TestFormatSize_megabytes(t *testing.T) {
	s := formatSize(2 * 1024 * 1024)
	if !strings.Contains(s, "MB") {
		t.Errorf("got %q, want MB", s)
	}
}

// ── logList ───────────────────────────────────────────────────────────────────

func TestLogList_noLogsDir(t *testing.T) {
	app, _ := newTestApp(t)
	var buf bytes.Buffer
	if err := app.logList(&buf); err != nil {
		t.Fatalf("logList: %v", err)
	}
	// Should print header only.
	if !strings.Contains(buf.String(), "RUN_ID") {
		t.Errorf("expected header, got: %s", buf.String())
	}
}

func TestLogList_withLogs(t *testing.T) {
	app, dir := newTestApp(t)
	logsDir := filepath.Join(dir, "logs")
	writeLogFile(t, logsDir, "20240101-120000-aaaa", time.Now(), "content")

	var buf bytes.Buffer
	if err := app.logList(&buf); err != nil {
		t.Fatalf("logList: %v", err)
	}
	if !strings.Contains(buf.String(), "20240101-120000-aaaa") {
		t.Errorf("expected run ID in output, got: %s", buf.String())
	}
}

// ── logClean ──────────────────────────────────────────────────────────────────

func TestLogClean_dryRun_listsOldLogs(t *testing.T) {
	app, dir := newTestApp(t)
	logsDir := filepath.Join(dir, "logs")
	old := time.Now().AddDate(0, 0, -60)
	writeLogFile(t, logsDir, "20230101-000000-old", old, "old log")

	var buf bytes.Buffer
	if err := app.logClean(&buf, 30, true); err != nil {
		t.Fatalf("logClean: %v", err)
	}
	if !strings.Contains(buf.String(), "would delete") {
		t.Errorf("expected 'would delete' in output, got: %s", buf.String())
	}
	// File should still exist.
	if _, err := os.Stat(filepath.Join(logsDir, "20230101-000000-old.log")); err != nil {
		t.Error("file should still exist after dry-run")
	}
}

func TestLogClean_deletesOldLogs(t *testing.T) {
	app, dir := newTestApp(t)
	logsDir := filepath.Join(dir, "logs")
	old := time.Now().AddDate(0, 0, -60)
	recent := time.Now().AddDate(0, 0, -1)
	writeLogFile(t, logsDir, "20230101-000000-old", old, "old log")
	writeLogFile(t, logsDir, "20240101-000000-new", recent, "new log")

	var buf bytes.Buffer
	if err := app.logClean(&buf, 30, false); err != nil {
		t.Fatalf("logClean: %v", err)
	}

	if _, err := os.Stat(filepath.Join(logsDir, "20230101-000000-old.log")); err == nil {
		t.Error("old log should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(logsDir, "20240101-000000-new.log")); err != nil {
		t.Error("recent log should still exist")
	}
}

func TestLogClean_deletesSummaryToo(t *testing.T) {
	app, dir := newTestApp(t)
	logsDir := filepath.Join(dir, "logs")
	old := time.Now().AddDate(0, 0, -60)
	writeLogFile(t, logsDir, "20230101-000000-old", old, "old log")
	// Write a matching summary file.
	summaryPath := filepath.Join(logsDir, "20230101-000000-old.summary")
	os.WriteFile(summaryPath, []byte("summary"), 0o644)

	var buf bytes.Buffer
	if err := app.logClean(&buf, 30, false); err != nil {
		t.Fatalf("logClean: %v", err)
	}

	if _, err := os.Stat(summaryPath); err == nil {
		t.Error("summary file should have been deleted")
	}
}

func TestLogClean_noLogs_printsMessage(t *testing.T) {
	app, dir := newTestApp(t)
	logsDir := filepath.Join(dir, "logs")
	recent := time.Now()
	writeLogFile(t, logsDir, "20240101-000000-new", recent, "new log")

	var buf bytes.Buffer
	if err := app.logClean(&buf, 30, false); err != nil {
		t.Fatalf("logClean: %v", err)
	}
	if !strings.Contains(buf.String(), "no logs to clean") {
		t.Errorf("expected 'no logs to clean', got: %s", buf.String())
	}
}
