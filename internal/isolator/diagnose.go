package isolator

import (
	"strings"

	"github.com/enr/inner/internal/runtime"
)

// FailureKind classifies why a sandbox did not start.
type FailureKind string

const (
	FailureUserNS      FailureKind = "userns"       // the kernel refused the user namespace
	FailureMountSource FailureKind = "mount-source" // a bind-mount source does not exist on the host
	FailureMountDest   FailureKind = "mount-dest"   // a mount destination cannot be created in the sandbox
	FailurePty         FailureKind = "pty"          // /dev/pts or /dev/ptmx could not be set up
	FailureEntrypoint  FailureKind = "entrypoint"   // the entrypoint is not found inside the sandbox
	FailureOther       FailureKind = "other"        // any other bwrap error
)

// Diagnosis is the actionable reading of a sandbox start failure.
type Diagnosis struct {
	Kind    FailureKind
	Summary string // what went wrong, one line
	Hint    string // what to do about it, one line
	Detail  string // the bwrap error line it was derived from, if any
}

// DiagnoseBwrapError reads the stderr of a bwrap that failed to set up the
// sandbox and maps its error to an actionable diagnosis. restrictions are the
// host's user-namespace restrictions (runtime.UserNSRestrictions), used to name
// the setting at fault. Returns nil when stderr carries no bwrap error.
//
// bwrap dies on its first error, so only the first "bwrap: " line counts.
func DiagnoseBwrapError(stderr string, restrictions []runtime.UserNSRestriction) *Diagnosis {
	var line string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "bwrap: ") {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		return nil
	}
	msg := strings.TrimPrefix(line, "bwrap: ")
	d := &Diagnosis{Detail: line}

	switch {
	case containsAny(msg,
		"No permissions to creat", // "…creating new namespace" (bwrap < 0.10) / "…create a new namespace"
		"Creating new namespace failed",
		"setting up uid map", "setting up gid map", "setgroups",
		"loopback: Failed RTM_NEWADDR"):
		d.Kind = FailureUserNS
		d.Summary = "the kernel refused to create the sandbox's user namespace"
		d.Hint = userNSHint(restrictions)

	case strings.HasPrefix(msg, "Can't find source path "):
		d.Kind = FailureMountSource
		path, _, _ := strings.Cut(strings.TrimPrefix(msg, "Can't find source path "), ": ")
		d.Summary = "mount source " + path + " does not exist on the host"
		d.Hint = "create it, or remove the mount from the profile's [mounts] / the -m flag"

	case containsAny(msg, "ptmx", "devpts", "/dev/pts"):
		d.Kind = FailurePty
		d.Summary = "the sandbox could not set up its pseudo-terminals (/dev/pts, /dev/ptmx)"
		d.Hint = "do not mount /dev/ptmx or /dev/pts from the profile: inner sets them up itself; run 'inner doctor'"

	case strings.HasPrefix(msg, "Can't mkdir "), strings.HasPrefix(msg, "Can't create file at "):
		d.Kind = FailureMountDest
		path := mountDestPath(msg)
		d.Summary = "cannot create mount destination " + path + " inside the sandbox"
		d.Hint = "the host root is read-only in the sandbox, so a destination must already exist on the host " +
			"or sit under a writable mount (/tmp, the workdir, a workspace)"

	case strings.HasPrefix(msg, "execvp "):
		cmd, _, _ := strings.Cut(strings.TrimPrefix(msg, "execvp "), ": ")
		return EntrypointNotFound(cmd, line)

	default:
		d.Kind = FailureOther
		d.Summary = "bwrap could not set up the sandbox"
		d.Hint = "run 'inner doctor' and 'inner run --dry-run' to check the environment and the bwrap command"
	}
	return d
}

// EntrypointNotFound is the diagnosis for an entrypoint that does not resolve
// inside the sandbox. detail is the error line it comes from, if any.
func EntrypointNotFound(cmd, detail string) *Diagnosis {
	return &Diagnosis{
		Kind:    FailureEntrypoint,
		Summary: "entrypoint " + cmd + " was not found inside the sandbox",
		Hint: "check that it is installed and on the sandbox PATH ([env] path_prepend); " +
			"with home = \"isolated\", list its directory in [sandbox] home_allow",
		Detail: detail,
	}
}

// userNSHint names the host setting that blocks user namespaces, or points to
// inner doctor when none is detected.
func userNSHint(restrictions []runtime.UserNSRestriction) string {
	var parts []string
	for _, r := range restrictions {
		parts = append(parts, r.Setting+": "+r.Fix)
	}
	if len(parts) == 0 {
		return "unprivileged user namespaces are disabled or restricted on this host; run 'inner doctor'"
	}
	return strings.Join(parts, "; ")
}

// mountDestPath extracts the path from "Can't mkdir parents for P: …",
// "Can't mkdir P: …" and "Can't create file at P: …".
func mountDestPath(msg string) string {
	for _, prefix := range []string{"Can't mkdir parents for ", "Can't mkdir ", "Can't create file at "} {
		if rest, ok := strings.CutPrefix(msg, prefix); ok {
			path, _, _ := strings.Cut(rest, ": ")
			return path
		}
	}
	return msg
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
