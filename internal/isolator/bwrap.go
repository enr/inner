package isolator

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// BwrapIsolator implements Isolator using bubblewrap (bwrap) on Linux.
type BwrapIsolator struct {
	bwrapPath string
	info      runtime.RuntimeInfo
}

// NewBwrapIsolator creates a BwrapIsolator after verifying that bwrap is
// available on the host.
func NewBwrapIsolator() (*BwrapIsolator, error) {
	info := runtime.Detect()
	if !info.BwrapAvailable {
		return nil, fmt.Errorf("bwrap not found in PATH")
	}
	return &BwrapIsolator{bwrapPath: info.BwrapPath, info: info}, nil
}

// newBwrapIsolatorWithInfo creates a BwrapIsolator with explicit values.
// Intended for unit tests that do not require bwrap to be installed.
func newBwrapIsolatorWithInfo(bwrapPath string, info runtime.RuntimeInfo) *BwrapIsolator {
	return &BwrapIsolator{bwrapPath: bwrapPath, info: info}
}

// Available implements Isolator.
func (b *BwrapIsolator) Available() (bool, string) {
	if !b.info.BwrapAvailable {
		return false, "bwrap not found in PATH"
	}
	msg := fmt.Sprintf("bwrap %s at %s", b.info.BwrapVersion, b.info.BwrapPath)
	return true, msg
}

// Build implements Isolator.
// It assembles the bwrap argument list from cfg and returns an *exec.Cmd
// that, when Run(), will execute the entrypoint inside the sandbox.
// Build itself has no side effects.
func (b *BwrapIsolator) Build(cfg config.RunConfig) (*exec.Cmd, error) {
	var args []string

	// ── Base filesystem ──────────────────────────────────────────────────────
	// Bind the host root read-only as the sandbox root.
	args = append(args, "--ro-bind", "/", "/")
	// Re-mount /proc, /dev, /tmp so the sandbox is functional.
	args = append(args, "--proc", "/proc")
	args = append(args, "--dev", "/dev")
	args = append(args, "--tmpfs", "/tmp")

	// ── Additional mounts ────────────────────────────────────────────────────
	for _, m := range cfg.Mounts {
		if m.Mode == "rw" {
			args = append(args, "--bind", m.Src, m.Dest)
		} else {
			args = append(args, "--ro-bind", m.Src, m.Dest)
		}
	}

	// ── PID isolation ────────────────────────────────────────────────────────
	args = append(args, "--unshare-pid")
	args = append(args, "--die-with-parent")

	// ── Network ──────────────────────────────────────────────────────────────
	if !cfg.Network {
		args = append(args, "--unshare-net")
	}

	// ── Environment ──────────────────────────────────────────────────────────
	if cfg.Env.Clear {
		args = append(args, "--clearenv")
		// Explicitly forward each whitelisted variable from the host.
		for _, key := range cfg.Env.Inherit {
			args = append(args, "--setenv", key, os.Getenv(key))
		}
	}
	// Explicitly set variables override whatever was inherited.
	for key, val := range cfg.Env.Set {
		args = append(args, "--setenv", key, val)
	}

	// ── Clipboard / display server ───────────────────────────────────────────
	if cfg.Clipboard {
		switch b.info.Display {
		case runtime.DisplayWayland:
			args = append(args, "--ro-bind", b.info.WaylandSocket, b.info.WaylandSocket)
			args = append(args, "--setenv", "WAYLAND_DISPLAY", os.Getenv("WAYLAND_DISPLAY"))
			if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
				args = append(args, "--setenv", "XDG_RUNTIME_DIR", xdg)
			}
		case runtime.DisplayX11:
			args = append(args, "--ro-bind", "/tmp/.X11-unix", "/tmp/.X11-unix")
			args = append(args, "--setenv", "DISPLAY", b.info.X11Display)
		}
	}

	// ── Git config injection ─────────────────────────────────────────────────
	if cfg.GitConfigPath != "" {
		args = append(args, "--ro-bind", cfg.GitConfigPath, cfg.GitConfigPath)
		args = append(args, "--setenv", "GIT_CONFIG_GLOBAL", cfg.GitConfigPath)
	}

	// ── Entrypoint ───────────────────────────────────────────────────────────
	args = append(args, "--")

	cmd := cfg.Entrypoint.Cmd
	if cmd == "" {
		cmd = os.Getenv("SHELL")
		if cmd == "" {
			cmd = "/bin/sh"
		}
	}
	args = append(args, cmd)
	args = append(args, cfg.Entrypoint.Args...)

	return exec.Command(b.bwrapPath, args...), nil
}
