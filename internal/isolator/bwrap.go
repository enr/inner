package isolator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// BwrapIsolator implements Isolator using bubblewrap (bwrap) on Linux.
type BwrapIsolator struct {
	bwrapPath string
	info      runtime.RuntimeInfo
	// statFn checks whether a path exists. Defaults to os.Lstat.
	// Overridable in tests to avoid filesystem dependencies.
	statFn func(string) (os.FileInfo, error)
	// evalSymlinksFn resolves symlinks to a canonical path. Defaults to
	// filepath.EvalSymlinks. Overridable in tests.
	evalSymlinksFn func(string) (string, error)
}

// pathExists reports whether the path exists on the host.
func (b *BwrapIsolator) pathExists(path string) bool {
	stat := b.statFn
	if stat == nil {
		stat = os.Lstat
	}
	_, err := stat(path)
	return err == nil
}

// isAllowed reports whether key is present in the allow list.
func isAllowed(allow []string, key string) bool {
	return slices.Contains(allow, key)
}

// isUnderTmpfs reports whether path falls inside any tmpfs mount in mounts.
// Used to skip redundant sensitive-resource hiding for paths already erased
// by a profile-level tmpfs (a bind inside an empty tmpfs would fail).
func isUnderTmpfs(mounts []config.Mount, path string) bool {
	for _, m := range mounts {
		if m.Mode == "tmpfs" && strings.HasPrefix(path, m.Dest+"/") {
			return true
		}
	}
	return false
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
	// Bind the host root read-only. This is the "deny by default, allow by
	// exception" security model: the entire host filesystem is visible but
	// immutable inside the sandbox; explicit mounts in the profile open only
	// what is needed. Mount destinations must exist on the host; the workspace
	// manager pre-creates them under workspaces_path before bwrap starts.
	args = append(args, "--ro-bind", "/", "/")
	// Re-mount /proc, /dev, /tmp so the sandbox is functional.
	args = append(args, "--proc", "/proc")
	args = append(args, "--dev", "/dev")
	// Expose the host's /dev/pts entries inside the sandbox so that interactive
	// TUI apps can resolve the controlling-terminal path returned by ttyname_r().
	args = append(args, "--bind", "/dev/pts", "/dev/pts")
	// Ensure /dev/ptmx is accessible. bwrap --dev creates /dev/ptmx as a symlink
	// to pts/ptmx. Since we bound the host's /dev/pts, /dev/pts/ptmx is the host's
	// one. If the host has ptmxmode=000 (common on Debian/Ubuntu), opening it
	// directly via the symlink fails. Binding the host's /dev/ptmx ensures we
	// use the global ptmx node which the kernel handles correctly.
	if b.pathExists("/dev/ptmx") {
		args = append(args, "--dev-bind", "/dev/ptmx", "/dev/ptmx")
	}
	args = append(args, "--tmpfs", "/tmp")

	// ── Additional mounts ────────────────────────────────────────────────────
	// Emission order matters: bwrap processes args left-to-right and later
	// mounts shadow earlier ones at the same path.
	//
	// 1. tmpfs mounts first — so subsequent bind mounts can land inside them.
	// 2. Non-workspace bind mounts (including any workdir rw bind that covers
	//    the workspace parent directory).
	// 3. Workspace bind mounts LAST — so they shadow any earlier recursive
	//    bind of a parent directory (e.g. the workdir --bind ~/Projects ~/Projects
	//    is MS_BIND|MS_REC and would overwrite workspace sub-paths if emitted after).
	workspaceDests := make(map[string]bool, len(cfg.WorkspaceDests))
	for _, d := range cfg.WorkspaceDests {
		workspaceDests[d] = true
	}

	for _, m := range cfg.Mounts {
		if m.Mode == "tmpfs" {
			args = append(args, "--tmpfs", m.Dest)
		}
	}
	for _, m := range cfg.Mounts {
		if workspaceDests[m.Dest] {
			continue // deferred to the workspace pass below
		}
		if m.Mode == "rw" {
			args = append(args, "--bind", m.Src, m.Dest)
		} else if m.Mode != "tmpfs" {
			args = append(args, "--ro-bind", m.Src, m.Dest)
		}
	}
	// Workspace mounts come last so they shadow parent-directory rbinds.
	for _, m := range cfg.Mounts {
		if !workspaceDests[m.Dest] {
			continue
		}
		if m.Mode == "rw" {
			args = append(args, "--bind", m.Src, m.Dest)
		} else if m.Mode != "tmpfs" {
			args = append(args, "--ro-bind", m.Src, m.Dest)
		}
	}

	// ── Process lifecycle ────────────────────────────────────────────────────
	// --die-with-parent: sandbox is killed if the inner process crashes.
	//
	// --unshare-pid is applied only for non-interactive runs. When enabled,
	// bwrap forks internally and the child calls setsid(), creating a new
	// session with no controlling terminal. Interactive TUI apps (Node.js /
	// claude, gemini) call open("/dev/tty") at startup to initialise raw mode;
	// without a controlling terminal they receive ENXIO and hang with no output.
	args = append(args, "--die-with-parent")
	if !cfg.Entrypoint.Interactive {
		args = append(args, "--unshare-pid")
	}

	// ── Network ──────────────────────────────────────────────────────────────
	if !cfg.Network {
		args = append(args, "--unshare-net")
	}

	// ── Nested user namespaces ────────────────────────────────────────────────
	// Required for rootless container runtimes (podman, docker rootless) inside
	// the sandbox. We explicitly unshare the user namespace so that bwrap's
	// --userns-block-fd mechanism works (it requires --unshare-user). The
	// runSandbox pipeline then calls newuidmap/newgidmap from the host (where
	// the setuid bit is fully effective) to set up a uid mapping that includes
	// the full subuid range, enabling nested rootless containers.
	// CAP_SETUID/CAP_SETGID are added so that newuidmap called from *inside*
	// the sandbox (by podman creating nested containers) can raise its caps.
	if isAllowed(cfg.Allow, "nested-user-ns") {
		args = append(args, "--unshare-user")
		// Keep the real uid/gid inside the sandbox so rootless tools (podman)
		// continue to use user-mode storage paths (~/.local/share/containers)
		// instead of system-wide root paths (/var/lib/containers).
		args = append(args, "--uid", strconv.Itoa(os.Getuid()), "--gid", strconv.Itoa(os.Getgid()))
		args = append(args, "--cap-add", "cap_setuid", "--cap-add", "cap_setgid")
		// /var/tmp is used by podman as scratch space during image pulls and
		// container setup. The root is bound read-only so we overlay a tmpfs.
		args = append(args, "--tmpfs", "/var/tmp")
		// /dev/net/tun is required by pasta (podman's rootless network backend)
		// to create tap devices for container networking. bwrap's --dev /dev
		// creates a minimal devtmpfs without it, so we bind it explicitly.
		if b.pathExists("/dev/net/tun") {
			args = append(args, "--dir", "/dev/net")
			args = append(args, "--dev-bind", "/dev/net/tun", "/dev/net/tun")
		}
	}

	// ── Environment ──────────────────────────────────────────────────────────
	// Clear by default; only skip when InheritAll explicitly opts into full
	// host env inheritance (leaks secrets — use sparingly).
	if !cfg.Env.InheritAll {
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

	// ── Sensitive resource hiding ────────────────────────────────────────────
	// These resources are hidden by default using tmpfs overlays (directories)
	// or /dev/null binds (files and sockets). Hiding is skipped when:
	//   a) the key is listed in cfg.Allow, or
	//   b) the path does not exist on the host (nothing to hide).
	// "nested-user-ns" is a valid allow key but has no hide action (caps are added above).
	{
		home, _ := os.UserHomeDir()
		uid := strconv.Itoa(os.Getuid())

		type sensitiveEntry struct {
			key  string
			path string
			dir  bool // true → --tmpfs; false → --bind /dev/null
		}
		sensitive := []sensitiveEntry{
			{"ssh-keys", filepath.Join(home, ".ssh"), true},
			{"gpg-keys", filepath.Join(home, ".gnupg"), true},
			{"git-credentials", filepath.Join(home, ".git-credentials"), false},
			{"netrc", filepath.Join(home, ".netrc"), false},
			{"docker-socket", "/var/run/docker.sock", false},
			{"podman-socket", "/run/user/" + uid + "/podman/podman.sock", false},
			{"bash-history", filepath.Join(home, ".bash_history"), false},
			{"zsh-history", filepath.Join(home, ".zsh_history"), false},
			// Cloud provider credentials.
			{"aws-credentials", filepath.Join(home, ".aws"), true},
			{"gcloud-credentials", filepath.Join(home, ".config", "gcloud"), true},
			{"kube-config", filepath.Join(home, ".kube"), true},
			{"azure-credentials", filepath.Join(home, ".azure"), true},
			// Package manager / registry tokens.
			{"docker-config", filepath.Join(home, ".docker", "config.json"), false},
			{"npmrc", filepath.Join(home, ".npmrc"), false},
			{"pypirc", filepath.Join(home, ".pypirc"), false},
			{"cargo-credentials", filepath.Join(home, ".cargo", "credentials"), false},
			// Password managers.
			{"password-store", filepath.Join(home, ".password-store"), true},
		}

		for _, r := range sensitive {
			if isAllowed(cfg.Allow, r.key) {
				continue
			}
			if !b.pathExists(r.path) {
				continue
			}
			// Skip if a profile tmpfs already covers this path: the tmpfs
			// replaces the entire directory with an empty tree so there is
			// nothing to hide, and a bind inside the empty tmpfs would fail.
			if isUnderTmpfs(cfg.Mounts, r.path) {
				continue
			}
			// Resolve symlinks so bwrap receives the canonical path. On
			// systems where /var/run is a symlink to /run, passing the
			// unresolved path as a bind destination fails because bwrap
			// cannot create a mount point through a symlink in the sandbox.
			evalSymlinks := b.evalSymlinksFn
			if evalSymlinks == nil {
				evalSymlinks = filepath.EvalSymlinks
			}
			bindPath := r.path
			if resolved, err := evalSymlinks(r.path); err == nil {
				bindPath = resolved
			}
			if r.dir {
				args = append(args, "--tmpfs", bindPath)
			} else {
				args = append(args, "--bind", "/dev/null", bindPath)
			}
		}
	}

	// ── Shim dir ─────────────────────────────────────────────────────────────
	// Mount the shim directory read-only at /tmp/inner-shims inside the sandbox,
	// then prepend it to PATH so shims take precedence over real binaries.
	// We use /tmp/inner-shims (not /run/inner-shims) because /tmp is already a
	// writable tmpfs at this point: --dir creates the subdirectory there without
	// touching the read-only root.
	if cfg.ShimDir != "" {
		const shimMount = "/tmp/inner-shims"
		args = append(args, "--dir", shimMount)
		args = append(args, "--ro-bind", cfg.ShimDir, shimMount)

		// Determine the PATH that will be active inside the sandbox.
		// Priority: explicitly set in profile → inherited from host → hard default.
		sandboxPath := cfg.Env.Set["PATH"]
		if sandboxPath == "" {
			sandboxPath = os.Getenv("PATH")
		}
		if sandboxPath == "" {
			sandboxPath = "/usr/local/bin:/usr/bin:/bin"
		}
		args = append(args, "--setenv", "PATH", shimMount+":"+sandboxPath)
	}

	// ── Git config injection ─────────────────────────────────────────────────
	if cfg.GitConfigPath != "" {
		args = append(args, "--ro-bind", cfg.GitConfigPath, cfg.GitConfigPath)
		args = append(args, "--setenv", "GIT_CONFIG_GLOBAL", cfg.GitConfigPath)
	}

	// ── Working directory ────────────────────────────────────────────────────
	if cfg.Workdir != "" {
		args = append(args, "--chdir", cfg.Workdir)
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
