package runtime

// DisplayServer identifies which display server is active on the host.
type DisplayServer string

const (
	DisplayNone    DisplayServer = ""
	DisplayWayland DisplayServer = "wayland"
	DisplayX11     DisplayServer = "x11"
)

// RuntimeInfo describes the capabilities of the host environment
// detected at startup. It is read-only data: the Isolator reads it,
// nothing modifies it after Detect() returns.
type RuntimeInfo struct {
	// Bubblewrap backend.
	BwrapPath      string
	BwrapVersion   string
	BwrapAvailable bool

	// Linux kernel capabilities.
	UserNSEnabled bool

	// Display server (for clipboard/Wayland/X11 forwarding).
	Display       DisplayServer
	WaylandSocket string // absolute path to the Wayland socket
	X11Display    string // $DISPLAY value (e.g. ":0")
}
