package isolator

import (
	"fmt"
	"runtime"
)

// NewIsolator returns the appropriate Isolator implementation for the
// current operating system. The extension point for future backends is
// explicit here: adding macOS support means adding a case, nothing else.
func NewIsolator() (Isolator, error) {
	switch runtime.GOOS {
	case "linux":
		return NewBwrapIsolator()
	case "darwin":
		return nil, fmt.Errorf("macOS not yet supported (planned: sandbox-exec or Docker backend)")
	default:
		return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}
