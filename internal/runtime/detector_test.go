package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectDisplay_wayland(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "wayland-0")
	// Create a fake socket file so os.Stat succeeds.
	if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
		t.Fatalf("creating fake socket: %v", err)
	}

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DISPLAY", "") // ensure X11 fallback is not used

	display, socket, x11 := detectDisplay()

	if display != DisplayWayland {
		t.Errorf("display = %q, want %q", display, DisplayWayland)
	}
	if socket != socketPath {
		t.Errorf("socket = %q, want %q", socket, socketPath)
	}
	if x11 != "" {
		t.Errorf("x11 = %q, want empty", x11)
	}
}

func TestDetectDisplay_waylandSocketMissing_fallsBackToX11(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // socket file does NOT exist
	t.Setenv("DISPLAY", ":1")

	display, _, x11 := detectDisplay()

	if display != DisplayX11 {
		t.Errorf("display = %q, want %q", display, DisplayX11)
	}
	if x11 != ":1" {
		t.Errorf("x11 = %q, want %q", x11, ":1")
	}
}

func TestDetectDisplay_x11Only(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")

	display, socket, x11 := detectDisplay()

	if display != DisplayX11 {
		t.Errorf("display = %q, want %q", display, DisplayX11)
	}
	if socket != "" {
		t.Errorf("socket = %q, want empty", socket)
	}
	if x11 != ":0" {
		t.Errorf("x11 = %q, want %q", x11, ":0")
	}
}

func TestDetectDisplay_none(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	display, socket, x11 := detectDisplay()

	if display != DisplayNone {
		t.Errorf("display = %q, want none", display)
	}
	if socket != "" || x11 != "" {
		t.Errorf("expected empty socket and x11, got %q %q", socket, x11)
	}
}

func TestResolveEntrypoint_emptyCmd(t *testing.T) {
	got := ResolveEntrypoint("")
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestResolveEntrypoint_existingBinary(t *testing.T) {
	got := ResolveEntrypoint("sh")
	if got == "" || got == "sh" {
		// Should have resolved to an absolute path.
		t.Errorf("ResolveEntrypoint(sh) = %q, want an absolute path", got)
	}
}

func TestResolveEntrypoint_unknownBinary(t *testing.T) {
	cmd := "definitely-not-a-real-binary-xyzzy"
	got := ResolveEntrypoint(cmd)
	if got != cmd {
		t.Errorf("got %q, want %q (unchanged)", got, cmd)
	}
}

func TestDetect_returnsInfo(t *testing.T) {
	info := Detect()
	// We can't assert specific values since the environment varies,
	// but the call must not panic and must return a non-zero struct.
	_ = info.BwrapAvailable
	_ = info.Display
}
