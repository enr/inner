package isolator

import (
	"strings"
	"testing"

	"github.com/enr/inner/internal/runtime"
)

func TestDiagnoseBwrapError(t *testing.T) {
	cases := []struct {
		name        string
		stderr      string
		wantKind    FailureKind
		wantSummary string // substring
	}{
		// Messages as printed by bwrap 0.9 (the mount and execvp ones captured
		// from a real run).
		{"userns disabled (debian)",
			"bwrap: No permissions to creating new namespace, likely because the kernel does not allow non-privileged user namespaces. On e.g. debian this can be enabled with 'sysctl kernel.unprivileged_userns_clone=1'.\n",
			FailureUserNS, "user namespace"},
		{"userns apparmor uid map",
			"bwrap: setting up uid map: Permission denied\n",
			FailureUserNS, "user namespace"},
		{"userns apparmor loopback",
			"bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted\n",
			FailureUserNS, "user namespace"},
		{"userns unsupported",
			"bwrap: Creating new namespace failed, likely because the kernel does not support user namespaces.  bwrap must be installed setuid on such systems.\n",
			FailureUserNS, "user namespace"},
		{"missing mount source",
			"bwrap: Can't find source path /nonexistent: No such file or directory\n",
			FailureMountSource, "/nonexistent does not exist"},
		{"mount dest under read-only parent",
			"bwrap: Can't mkdir parents for /usr/newdir/sub: Read-only file system\n",
			FailureMountDest, "/usr/newdir/sub"},
		{"file mount dest under read-only parent",
			"bwrap: Can't create file at /usr/newfile: Read-only file system\n",
			FailureMountDest, "/usr/newfile"},
		{"ptmx symlink",
			"bwrap: Can't mount on symlink destination /dev/ptmx\n",
			FailurePty, "pseudo-terminals"},
		{"devpts mount",
			"bwrap: Can't mount devpts on /newroot/dev/pts: Permission denied\n",
			FailurePty, "pseudo-terminals"},
		{"execvp",
			"bwrap: execvp nosuchcmd: No such file or directory\n",
			FailureEntrypoint, "entrypoint nosuchcmd"},
		{"other",
			"bwrap: Unexpected capabilities but not setuid, old file caps config?\n",
			FailureOther, "could not set up"},
		{"first bwrap line wins, other output ignored",
			"some agent output\nbwrap: Can't find source path /a: No such file or directory\nbwrap: later\n",
			FailureMountSource, "/a does not exist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := DiagnoseBwrapError(c.stderr, nil)
			if d == nil {
				t.Fatal("expected a diagnosis, got nil")
			}
			if d.Kind != c.wantKind {
				t.Errorf("kind = %q, want %q", d.Kind, c.wantKind)
			}
			if !strings.Contains(d.Summary, c.wantSummary) {
				t.Errorf("summary = %q, want it to contain %q", d.Summary, c.wantSummary)
			}
			if d.Hint == "" {
				t.Error("empty hint")
			}
			if !strings.HasPrefix(d.Detail, "bwrap: ") {
				t.Errorf("detail = %q, want the bwrap line", d.Detail)
			}
		})
	}
}

func TestDiagnoseBwrapError_noBwrapLine(t *testing.T) {
	for _, stderr := range []string{"", "error: the agent failed\n", "  bwrap: indented is not bwrap's\n"} {
		if d := DiagnoseBwrapError(stderr, nil); d != nil {
			t.Errorf("stderr %q: expected nil, got %+v", stderr, d)
		}
	}
}

// The userns hint names the host setting at fault when one is detected.
func TestDiagnoseBwrapError_userNSHintNamesSetting(t *testing.T) {
	restrictions := []runtime.UserNSRestriction{{
		Setting: "kernel.apparmor_restrict_unprivileged_userns = 1",
		Fix:     "load the bwrap AppArmor profile",
	}}
	d := DiagnoseBwrapError("bwrap: setting up uid map: Permission denied\n", restrictions)
	if d == nil || !strings.Contains(d.Hint, "apparmor_restrict_unprivileged_userns") {
		t.Fatalf("hint does not name the restriction: %+v", d)
	}
	d = DiagnoseBwrapError("bwrap: setting up uid map: Permission denied\n", nil)
	if !strings.Contains(d.Hint, "inner doctor") {
		t.Errorf("without a detected restriction the hint should point to inner doctor: %q", d.Hint)
	}
}
