package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// RunOptions configures a single sandbox execution.
type RunOptions struct {
	Interactive bool
	Timeout     int    // seconds; 0 = no timeout
	LogDir      string // empty = no log file
	RunID       string // if empty, auto-generated via GenerateRunID()
	// Cleanups are called in order after the process exits, regardless of exit code.
	// Errors are collected but do not affect the RunResult.
	Cleanups []func() error
}

// RunResult holds the outcome of a completed run.
type RunResult struct {
	ExitCode int
	RunID    string
	LogPath  string // empty if logging was not configured
}

// Launcher executes a *exec.Cmd produced by an Isolator.
// It has no knowledge of which backend built the command.
//
// Stdin/Stdout/Stderr are injectable for tests; they default to the
// real os.Stdin/Stdout/Stderr when constructed with New().
type Launcher struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// New returns a Launcher wired to the real terminal.
func New() *Launcher {
	return &Launcher{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

// Run executes cmd according to opts and returns the result.
// A non-zero exit code is not treated as an error; only launch failures are.
func (l *Launcher) Run(cmd *exec.Cmd, opts RunOptions) (RunResult, error) {
	runID := opts.RunID
	if runID == "" {
		runID = GenerateRunID()
	}

	var logFile *os.File
	logPath := ""
	if opts.LogDir != "" {
		if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
			return RunResult{}, fmt.Errorf("creating log dir: %w", err)
		}
		logPath = filepath.Join(opts.LogDir, runID+".log")
		var err error
		logFile, err = os.Create(logPath)
		if err != nil {
			return RunResult{}, fmt.Errorf("creating log file: %w", err)
		}
		defer logFile.Close()
	}

	var exitCode int
	var err error
	if opts.Interactive {
		exitCode, err = l.runInteractive(cmd, opts.Timeout, logFile)
	} else {
		exitCode, err = l.runNonInteractive(cmd, opts.Timeout, logFile)
	}

	var cleanupErrs []error
	for _, fn := range opts.Cleanups {
		if e := fn(); e != nil {
			cleanupErrs = append(cleanupErrs, e)
		}
	}
	if err == nil && len(cleanupErrs) > 0 {
		err = fmt.Errorf("cleanup errors: %v", cleanupErrs)
	}

	return RunResult{ExitCode: exitCode, RunID: runID, LogPath: logPath}, err
}

// ── Non-interactive ───────────────────────────────────────────────────────────

func (l *Launcher) runNonInteractive(cmd *exec.Cmd, timeoutSec int, log *os.File) (int, error) {
	stdout := l.Stdout
	stderr := l.Stderr
	if log != nil {
		stdout = io.MultiWriter(l.Stdout, log)
		stderr = io.MultiWriter(l.Stderr, log)
	}
	cmd.Stdin = l.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting process: %w", err)
	}

	done := make(chan struct{})
	defer close(done)

	forwardSignals(cmd, done)
	applyTimeout(cmd, timeoutSec, done)

	return waitExitCode(cmd)
}

// ── Interactive ───────────────────────────────────────────────────────────────

// runInteractive connects the process directly to the host terminal (no
// intermediate PTY). This lets TUI applications (claude, gemini, bash) take
// full control of the terminal via tcsetattr/ioctl without going through an
// additional PTY layer that would cause session-leader / ttyname issues when
// combined with bwrap's --unshare-pid internal fork.
//
// Consequence: output cannot be tee'd to a log file during an interactive
// session (piping stdout would turn it into a non-TTY from the app's
// perspective and break TUI rendering). The log parameter is ignored.
func (l *Launcher) runInteractive(cmd *exec.Cmd, timeoutSec int, _ *os.File) (int, error) {
	cmd.Stdin = l.Stdin
	cmd.Stdout = l.Stdout
	cmd.Stderr = l.Stderr

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting process: %w", err)
	}

	done := make(chan struct{})
	defer close(done)

	forwardSignals(cmd, done)
	applyTimeout(cmd, timeoutSec, done)

	return waitExitCode(cmd)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// forwardSignals relays SIGINT and SIGTERM to the child process until done is closed.
func forwardSignals(cmd *exec.Cmd, done <-chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		defer signal.Stop(sigCh)
		for {
			select {
			case sig := <-sigCh:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()
}

// applyTimeout sends SIGTERM after timeoutSec seconds, then SIGKILL after a
// 5-second grace period if the process has not yet exited.
// Does nothing if timeoutSec <= 0.
func applyTimeout(cmd *exec.Cmd, timeoutSec int, done <-chan struct{}) {
	if timeoutSec <= 0 {
		return
	}
	go func() {
		select {
		case <-time.After(time.Duration(timeoutSec) * time.Second):
			if cmd.Process == nil {
				return
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
			case <-done:
			}
		case <-done:
		}
	}()
}

// waitExitCode waits for cmd to finish and returns its exit code.
// A non-zero exit is not treated as an error.
func waitExitCode(cmd *exec.Cmd) (int, error) {
	err := cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}
