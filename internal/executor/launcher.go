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

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// RunOptions configures a single sandbox execution.
type RunOptions struct {
	Interactive bool
	Timeout     int    // seconds; 0 = no timeout
	LogDir      string // empty = no log file
	RunID       string // if empty, auto-generated via GenerateRunID()
	// ForceRawMode puts the host terminal in raw mode before the child starts.
	// Required for TUI apps (claude, gemini) that use Node.js/libuv: they probe
	// terminal capabilities during module init, before calling setRawMode, and
	// in cooked mode the capability response is buffered until a newline arrives;
	// the subsequent TCSAFLUSH then discards it, causing the app to hang.
	// For plain shells (bash, sh, zsh) this must be false: bash configures the
	// terminal itself via readline, and pre-raw mode breaks paste echo.
	ForceRawMode bool
	// CursorFix selects a cursor-repair strategy ("", "newlines", "shell").
	// Any non-empty value causes \r\n to be printed after the child exits.
	// Implied when ForceRawMode is true.
	CursorFix string
	// PostStart, if non-nil, is called in a goroutine immediately after the
	// process is started. It runs concurrently with the process and must not
	// block indefinitely. Errors are logged but do not abort the run.
	PostStart func() error
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
		exitCode, err = l.runInteractive(cmd, opts.Timeout, opts.ForceRawMode, opts.ForceRawMode || opts.CursorFix != "", opts.PostStart, logFile)
	} else {
		exitCode, err = l.runNonInteractive(cmd, opts.Timeout, opts.PostStart, logFile)
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

func (l *Launcher) runNonInteractive(cmd *exec.Cmd, timeoutSec int, postStart func() error, log *os.File) (int, error) {
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

	if postStart != nil {
		go postStart() //nolint:errcheck
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
func (l *Launcher) runInteractive(cmd *exec.Cmd, timeoutSec int, forceRawMode bool, cursorFix bool, postStart func() error, _ *os.File) (int, error) {
	// forceRawMode: put the terminal in raw mode before the child starts so
	// that terminal capability responses (DA, XTVERSION, etc.) are available
	// immediately, without cooked-mode line-discipline buffering.
	//
	// Required for TUI apps (claude, gemini): Node.js/libuv sends terminal
	// queries during module init, before calling setRawMode. In cooked mode
	// the response is held in the kernel TTY buffer until a newline; the
	// subsequent TCSAFLUSH discards it and the app hangs.
	//
	// Must NOT be set for plain shells (bash, sh, zsh): bash configures the
	// terminal via readline and re-enables echo itself. Pre-raw mode interferes
	// with readline's bracketed-paste echo, making pasted text invisible.
	if forceRawMode {
		if f, ok := l.Stdin.(*os.File); ok {
			fd := int(f.Fd())
			if term.IsTerminal(fd) {
				if oldState, err := term.MakeRaw(fd); err == nil {
					defer term.Restore(fd, oldState)
					// MakeRaw clears OPOST, disabling \n → \r\n translation in
					// output. Re-enable it: we only need raw input mode (so that
					// terminal capability responses aren't buffered by the line
					// discipline); output post-processing must stay on or every
					// bare \n moves the cursor down without returning to column 0,
					// producing a staircase/slanted display.
					if t, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
						t.Oflag |= unix.OPOST
						_ = unix.IoctlSetTermios(fd, unix.TCSETS, t)
					}
				}
			}
		}
	}

	cmd.Stdin = l.Stdin
	cmd.Stdout = l.Stdout
	cmd.Stderr = l.Stderr

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting process: %w", err)
	}

	if postStart != nil {
		go postStart() //nolint:errcheck
	}

	done := make(chan struct{})
	defer close(done)

	forwardSignals(cmd, done)
	applyTimeout(cmd, timeoutSec, done)

	code, err := waitExitCode(cmd)

	// After a TUI child exits (e.g. on SIGINT) the cursor may be left anywhere
	// inside the TUI area. Print \r\n so the host shell prompt always appears
	// on a fresh line below whatever was drawn last.
	if cursorFix {
		_, _ = fmt.Fprint(l.Stdout, "\r\n\r\n\r\n")
	}

	return code, err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// forwardSignals relays SIGINT and SIGTERM to the child process until done is closed.
func forwardSignals(cmd *exec.Cmd, done <-chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)
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
