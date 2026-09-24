package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Detect inspects the current host and returns a RuntimeInfo.
// It never returns an error: missing capabilities are represented
// as zero values or false fields in RuntimeInfo.
func Detect() RuntimeInfo {
	bwrapPath, bwrapVersion, bwrapOK := detectBwrap()
	display, waylandSocket, x11Display := detectDisplay()

	return RuntimeInfo{
		BwrapPath:      bwrapPath,
		BwrapVersion:   bwrapVersion,
		BwrapAvailable: bwrapOK,
		UserNSEnabled:  checkUserNamespaces(),
		Display:        display,
		WaylandSocket:  waylandSocket,
		X11Display:     x11Display,
	}
}

// ResolveEntrypoint returns the absolute path of cmd via PATH lookup,
// or cmd unchanged if not found.
func ResolveEntrypoint(cmd string) string {
	if cmd == "" {
		return cmd
	}
	if path, err := exec.LookPath(cmd); err == nil {
		return path
	}
	return cmd
}

// detectBwrap checks whether bwrap is available and returns its path and version.
func detectBwrap() (path, version string, ok bool) {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return "", "", false
	}

	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		// Found but can't query version — still usable.
		return path, "", true
	}

	// Output format: "bubblewrap 0.8.0"
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) >= 2 {
		version = fields[1]
	}
	return path, version, true
}

// detectDisplay returns the active display server and relevant socket paths.
// Priority: Wayland > X11 > none.
func detectDisplay() (DisplayServer, string, string) {
	waylandDisplay := os.Getenv("WAYLAND_DISPLAY")
	if waylandDisplay != "" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir != "" {
			socketPath := filepath.Join(runtimeDir, waylandDisplay)
			if _, err := os.Stat(socketPath); err == nil {
				return DisplayWayland, socketPath, ""
			}
		}
	}

	if display := os.Getenv("DISPLAY"); display != "" {
		return DisplayX11, "", display
	}

	return DisplayNone, "", ""
}

// checkUserNamespaces returns true if unprivileged user namespaces are enabled.
// On kernels that do not expose the sysctl, we assume they are enabled.
func checkUserNamespaces() bool {
	data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone")
	if err != nil {
		// Sysctl not present — most distros default to enabled.
		return true
	}
	return strings.TrimSpace(string(data)) == "1"
}

// TIOCSTIStatus says whether a process that shares the user's terminal can
// push characters into its input queue with ioctl(TIOCSTI) — typing commands
// the user's shell runs once the sandbox exits.
type TIOCSTIStatus int

const (
	// TIOCSTIRestricted: dev.tty.legacy_tiocsti = 0, the kernel refuses
	// TIOCSTI to processes without CAP_SYS_ADMIN (a sandbox has none).
	TIOCSTIRestricted TIOCSTIStatus = iota
	// TIOCSTIAllowed: dev.tty.legacy_tiocsti = 1, the restriction exists but
	// is switched off.
	TIOCSTIAllowed
	// TIOCSTINoSysctl: the sysctl does not exist, i.e. a kernel older than
	// 6.2, which has no restriction at all.
	TIOCSTINoSysctl
)

// legacyTIOCSTIPath is the sysctl introduced in Linux 6.2. A package variable
// so tests can point it at a fixture.
var legacyTIOCSTIPath = "/proc/sys/dev/tty/legacy_tiocsti"

// DetectTIOCSTI reports whether the kernel restricts TIOCSTI.
func DetectTIOCSTI() TIOCSTIStatus {
	data, err := os.ReadFile(legacyTIOCSTIPath)
	if err != nil {
		return TIOCSTINoSysctl
	}
	if strings.TrimSpace(string(data)) == "0" {
		return TIOCSTIRestricted
	}
	return TIOCSTIAllowed
}
