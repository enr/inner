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
