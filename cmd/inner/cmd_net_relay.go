package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/enr/inner/internal/netproxy"
	"github.com/spf13/cobra"
)

// forwardedSignals are the signals the relay passes on to the child.
//
// Deliberately NOT "everything". The relay and the child run in the same
// foreground process group, so the tty driver already delivers SIGINT, SIGQUIT,
// SIGTSTP and SIGWINCH to BOTH of them. Forwarding those would hand the child a
// SECOND copy of every Ctrl-C, which breaks a TUI whose quit gesture is "press
// Ctrl-C twice" (claude does exactly that): one keypress would look like two.
//
// What is left is what the tty does not broadcast — a SIGTERM or SIGHUP aimed at
// the relay by whoever is above it (bwrap's --die-with-parent path, a timeout,
// the user's window closing). Those must reach the child or it would keep
// running after its wrapper was told to stop.
//
// SIGURG is excluded from any blanket registration on purpose: the Go runtime
// uses it for asynchronous preemption, and forwarding it would be constant
// meaningless noise aimed at the child.
var forwardedSignals = []os.Signal{syscall.SIGTERM, syscall.SIGHUP}

// netRelayOptions is the parsed form of the __net-relay invocation.
type netRelayOptions struct {
	// ListenAddr is the loopback address to listen on inside the sandbox.
	ListenAddr string
	// UnixPath is the bind-mounted host proxy socket to forward to.
	UnixPath string
	// Cmd and Args are the real entrypoint this relay wraps.
	Cmd  string
	Args []string
}

// newNetRelayCmd builds the hidden `inner __net-relay` subcommand.
//
// It exists because HTTP_PROXY has to name a TCP address — no HTTP client
// speaks to a unix socket through that variable — while the only thing crossing
// into the sandbox is a bind-mounted unix socket. The relay bridges the two
// from inside: it listens on loopback in the sandbox's own network namespace
// and forwards every connection to the socket.
//
// It also wraps the real entrypoint rather than running beside it, so that when
// the entrypoint exits the relay goes with it, and the sandbox has exactly one
// process to wait for.
func (a *App) newNetRelayCmd() *cobra.Command {
	var opts netRelayOptions

	cmd := &cobra.Command{
		Use:    "__net-relay --listen ADDR --unix PATH -- COMMAND [ARGS...]",
		Short:  "Internal: bridge a loopback port to the sandbox network proxy socket",
		Hidden: true,
		Long: `__net-relay is an implementation detail of network_mode = "allowlist".

inner re-invokes itself with this subcommand as the sandbox entrypoint. The
relay listens on a loopback TCP port (what HTTP_PROXY must point at), forwards
each connection to the bind-mounted unix socket of the host-side proxy, then
runs the real entrypoint as a child and exits with its status.

Not meant to be run by hand.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Cmd, opts.Args = args[0], args[1:]
			code, err := runNetRelay(cmd.ErrOrStderr(), opts)
			if err != nil {
				return err
			}
			if code != 0 {
				return exitCodeError{code: code}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.ListenAddr, "listen", netproxy.RelayListenAddr, "Loopback address to listen on")
	cmd.Flags().StringVar(&opts.UnixPath, "unix", netproxy.SandboxSocketPath, "Path of the proxy unix socket")
	// Everything after the command name belongs to the child, including things
	// that look like our own flags: `-- claude --verbose` must not have
	// --verbose parsed here.
	cmd.Flags().SetInterspersed(false)

	return cmd
}

// runNetRelay starts the forwarder, runs the child, and returns the exit code
// the sandbox should report.
func runNetRelay(stderr io.Writer, opts netRelayOptions) (int, error) {
	// Listen BEFORE starting the child. If the child came up first it could
	// make its first request against a port nobody is listening on yet, and a
	// connection refused at startup is exactly the kind of failure that gets
	// misread as "the allow list is wrong".
	listener, err := net.Listen("tcp", opts.ListenAddr)
	if err != nil {
		// Fatal, not best-effort: continuing would start the entrypoint with
		// HTTP_PROXY pointing at nothing, and every request would fail with a
		// message that says nothing about the real cause.
		return 0, fmt.Errorf("network relay: cannot listen on %s: %w", opts.ListenAddr, err)
	}
	defer listener.Close()

	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // listener closed: the run is over
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				relayConn(stderr, conn, opts.UnixPath)
			}()
		}
	}()

	child := exec.Command(opts.Cmd, opts.Args...)
	// Inherited, not piped. This is what keeps an interactive TUI working: the
	// child gets the real terminal, so raw mode, resize and the controlling
	// terminal all behave exactly as they would without the relay in between —
	// the same way env(1) or nice(1) wrapping a process does not touch termios.
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	// No SysProcAttr.Setpgid: the child MUST stay in the relay's process group,
	// or the tty would stop delivering Ctrl-C and SIGWINCH to it.

	if err := child.Start(); err != nil {
		// 127 is the shell's "command not found", which is what this almost
		// always is: an entrypoint that is not in the sandbox's PATH.
		fmt.Fprintf(stderr, "inner: network relay: cannot start %s: %v\n", opts.Cmd, err)
		return 127, nil
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, forwardedSignals...)
	defer signal.Stop(sigCh)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				if p := child.Process; p != nil {
					_ = p.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()

	waitErr := child.Wait()
	close(done)
	_ = listener.Close()
	wg.Wait()

	return exitCodeOf(waitErr), nil
}

// exitCodeOf turns the result of (*exec.Cmd).Wait into the code the sandbox
// should exit with.
//
// A child killed by a signal reports 128+signum, the shell convention. We do
// not reset the handler and re-raise the signal on ourselves to die the same
// way: the only observable difference is to a parent that inspects
// WaitStatus.Signaled(), and bwrap's reaper already collapses that into an
// exit status before inner's launcher ever sees it.
func exitCodeOf(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		return 1
	}
	if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return ee.ExitCode()
}

// relayConn pipes one accepted TCP connection to the proxy's unix socket.
//
// Best-effort by design: a connection that cannot reach the socket is dropped
// with a message, and never takes the accept loop or any other connection with
// it. The client sees a closed connection, which is what it would see from any
// proxy that refused it.
func relayConn(stderr io.Writer, client net.Conn, unixPath string) {
	defer client.Close()

	upstream, err := net.Dial("unix", unixPath)
	if err != nil {
		fmt.Fprintf(stderr, "inner: network relay: cannot reach the proxy socket %s: %v\n", unixPath, err)
		return
	}
	defer upstream.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		// Half-close so the proxy sees EOF and can finish its own side, instead
		// of both directions waiting on each other forever.
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		if cw, ok := client.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}
