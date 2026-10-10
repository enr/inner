// Package landlock applies the one Landlock restriction inner uses: scoping
// abstract unix sockets to the sandbox.
//
// Abstract unix sockets (the "@name" ones in /proc/net/unix) live in the
// network namespace, not in the filesystem, so no mount hides them. A sandbox
// that keeps the host network namespace (network_mode = "full") can connect to
// every abstract socket of the host: the X server (@/tmp/.X11-unix/X0), which
// lets it type into any window of the user's session, and whatever else
// listens there. LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET (Landlock ABI 6, Linux
// 6.12) refuses a connect(2) to an abstract socket created outside the
// caller's Landlock domain, while TCP, filesystem sockets and the abstract
// sockets the sandbox creates for itself keep working.
package landlock

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ScopeABI is the first Landlock ABI that can scope abstract unix sockets.
const ScopeABI = 6

// ABI returns the Landlock ABI version of the running kernel, or 0 when
// Landlock is not available (not built in, or not enabled among the LSMs).
func ABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// ScopeAbstractUnix restricts the CALLING THREAD so that it can no longer
// connect to abstract unix sockets created outside its Landlock domain. The
// restriction is inherited across fork and execve and cannot be lifted.
//
// Landlock acts on the thread's credentials, and the Go runtime runs
// goroutines on several threads: the caller must hold runtime.LockOSThread and
// execve from that same thread, which then carries the domain into the new
// program.
func ScopeAbstractUnix() error {
	if abi := ABI(); abi < ScopeABI {
		return fmt.Errorf("landlock ABI %d, abstract unix socket scoping needs %d (Linux 6.12)", abi, ScopeABI)
	}
	attr := unix.LandlockRulesetAttr{Scoped: unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer unix.Close(int(fd))
	// Required for an unprivileged landlock_restrict_self. bwrap has already
	// set it for every sandboxed process, so this changes nothing there.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}
