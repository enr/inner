package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"time"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/isolator"
	"github.com/enr/inner/internal/runtime"
)

// probeScript runs inside the probe sandbox in place of the entrypoint. It
// checks that the real entrypoint ($0) resolves on the sandbox PATH, so the
// probe tells "bwrap could not build the sandbox" (bwrap's own error, exit 1)
// apart from "the sandbox is fine but the entrypoint is missing" (exit 127).
const probeScript = `command -v -- "$0" >/dev/null 2>&1 || exit 127`

// probeTimeout bounds the diagnostic probe: a sandbox that starts at all runs
// the script in milliseconds.
const probeTimeout = 10 * time.Second

// startFailureCodes are the exit codes a sandbox that never reached its
// entrypoint can return: 1 is every bwrap setup error (including execvp), 126
// and 127 are a wrapper entrypoint (__scope, __net-relay) or a shell that
// could not execute the command.
var startFailureCodes = []int{1, 126, 127}

// diagnoseStartFailure runs after a sandbox exited with a code that a start
// failure could produce. The interactive terminal is handed to the sandbox
// unfiltered, so bwrap's error cannot be read from the run itself: instead it
// rebuilds the same sandbox with probeScript as the entrypoint, captures its
// stderr, and prints an actionable one-liner when the probe fails too. A probe
// that succeeds means the sandbox did start and the exit code is the
// entrypoint's own: nothing is printed.
//
// rc is the run configuration the sandbox was built from; entrypoint is the
// command the user asked for, before any wrapper (__scope, __net-relay)
// replaced rc.Entrypoint.Cmd.
func diagnoseStartFailure(w io.Writer, iso isolator.Isolator, rc config.RunConfig, entrypoint string, exitCode int) {
	if !slices.Contains(startFailureCodes, exitCode) {
		return
	}
	probe := rc
	probe.Entrypoint.Cmd = "/bin/sh"
	probe.Entrypoint.Args = []string{"-c", probeScript, entrypoint}
	cmd, err := iso.Build(probe)
	if err != nil {
		return
	}
	code, stderr := runProbe(cmd)
	if code == 0 {
		return
	}
	d := isolator.DiagnoseBwrapError(stderr, runtime.UserNSRestrictions())
	if d == nil && code == 127 {
		d = isolator.EntrypointNotFound(entrypoint, "")
	}
	if d == nil {
		return
	}
	fmt.Fprintf(w, "%s: sandbox did not start: %s\n", colorizeW(w, ansiBoldRed, "error"), d.Summary)
	fmt.Fprintf(w, "  hint: %s\n", d.Hint)
}

// runProbe runs a probe sandbox detached from the terminal and returns its
// exit code and stderr.
func runProbe(cmd *exec.Cmd) (int, string) {
	var stderr bytes.Buffer
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return -1, ""
	}
	timer := time.AfterFunc(probeTimeout, func() { _ = cmd.Process.Kill() })
	err := cmd.Wait()
	timer.Stop()
	if err == nil {
		return 0, stderr.String()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stderr.String()
	}
	return -1, stderr.String()
}
