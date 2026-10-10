package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	goruntime "runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/landlock"
	"github.com/enr/inner/internal/runtime"
)

// newScopeCmd builds the hidden `inner __scope` subcommand: the entrypoint
// wrapper that cuts a network_mode = "full" sandbox off the host's abstract
// unix sockets (see internal/landlock), then becomes the real entrypoint.
//
// Unlike __net-relay it has nothing to do once the entrypoint runs, so it
// execs rather than supervising a child: the entrypoint keeps the terminal,
// the PID and the exit status exactly as if it had been started directly.
func (a *App) newScopeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "__scope -- COMMAND [ARGS...]",
		Short:  "Internal: restrict abstract unix sockets, then exec the entrypoint",
		Hidden: true,
		Long: `__scope is an implementation detail of network_mode = "full".

inner re-invokes itself with this subcommand as the sandbox entrypoint. It
applies a Landlock scope that refuses connections to abstract unix sockets
created outside the sandbox (the host X server, among others), then execs the
real entrypoint.

Not meant to be run by hand.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScope(cmd.ErrOrStderr(), args)
		},
	}
	// `-- claude --verbose` must not have --verbose parsed here.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// runScope only returns on failure: on success the process image is replaced.
func runScope(stderr io.Writer, args []string) error {
	path, err := exec.LookPath(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "inner: %v\n", err)
		return exitCodeError{code: 127}
	}
	// Landlock restricts the calling thread; the exec below must happen on the
	// same one.
	goruntime.LockOSThread()
	if err := landlock.ScopeAbstractUnix(); err != nil {
		// Fail closed: the host decided this kernel supports the scope, so a
		// failure here is unexpected, and running unscoped would hand the
		// entrypoint the host's X server without anyone noticing.
		return fmt.Errorf("restricting abstract unix sockets: %w", err)
	}
	return syscall.Exec(path, args, os.Environ())
}

// landlockABI is the host kernel's Landlock ABI. Overridable in tests.
var landlockABI = landlock.ABI

// hostDisplay is the display server [sandbox] clipboard forwards. Overridable
// in tests.
var hostDisplay = runtime.DetectDisplay

// abstractScopeWanted reports whether the entrypoint must go through __scope.
//
// Only network_mode = "full" needs it: every other mode runs in a private
// network namespace, where the host's abstract sockets do not exist. It is
// skipped when the profile opts out (allow = ["abstract-sockets"]), when a
// step needs a host abstract socket itself (an unfiltered session bus at an
// abstract address), and on kernels without the scope — `inner doctor` and
// `inner verify` report that case.
//
// Also skipped for [sandbox] clipboard on X11. That profile already hands the
// sandbox the X server through /tmp/.X11-unix, and Xlib and xcb try the
// abstract socket first and fall back to the file only on ENOENT or
// ECONNREFUSED — the EPERM the scope returns makes every X client fail.
func abstractScopeWanted(rc *config.RunConfig) bool {
	return rc.EffectiveNetworkMode() == config.NetworkFull &&
		!config.AllowKeyEnabled(rc.Allow, "abstract-sockets") &&
		!rc.KeepHostAbstractSockets &&
		!(rc.Clipboard && hostDisplay() == runtime.DisplayX11) &&
		landlockABI() >= landlock.ScopeABI
}

// applyAbstractScope wraps the entrypoint in `inner __scope` when
// abstractScopeWanted. Like rewriteEntrypointThroughRelay, it must run after
// every step that inspects or mutates the entrypoint.
func applyAbstractScope(rc *config.RunConfig) error {
	if !abstractScopeWanted(rc) {
		return nil
	}
	innerBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("abstract socket scope: cannot determine the inner binary path: %w", err)
	}
	// Under an isolated home the empty tmpfs would erase the binary that is
	// about to become the entrypoint.
	appendHomeAllowIfHidden(rc, innerBin)

	cmd := rc.Entrypoint.Cmd
	if cmd == "" {
		// Mirrors the isolator's own fallback (see rewriteEntrypointThroughRelay).
		cmd = os.Getenv("SHELL")
		if cmd == "" {
			cmd = "/bin/sh"
		}
	}
	rc.Entrypoint.Args = append([]string{"__scope", "--", cmd}, rc.Entrypoint.Args...)
	rc.Entrypoint.Cmd = innerBin
	return nil
}
