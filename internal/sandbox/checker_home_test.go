package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleMountInfo = `25 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
26 30 0:5 / /dev rw,nosuid - devtmpfs devtmpfs rw,size=4096k
31 30 0:1 / /home/tester rw,relatime shared:1 - ext4 /dev/sda2 rw
40 30 0:44 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw
`

func TestMountFsType(t *testing.T) {
	cases := []struct {
		mountPoint string
		wantType   string
		wantFound  bool
	}{
		{"/tmp", "tmpfs", true},
		{"/home/tester", "ext4", true},
		{"/proc", "proc", true},
		{"/home/other", "", false},
	}
	for _, c := range cases {
		got, found := mountFsType(sampleMountInfo, c.mountPoint)
		if found != c.wantFound || got != c.wantType {
			t.Errorf("mountFsType(%q) = (%q, %v), want (%q, %v)", c.mountPoint, got, found, c.wantType, c.wantFound)
		}
	}
}

func TestMountFsType_lastMountWins(t *testing.T) {
	// bwrap mounts the home tmpfs on top of whatever was there: the later entry
	// is the one the process actually sees.
	info := sampleMountInfo + "55 31 0:60 / /home/tester rw,nosuid - tmpfs tmpfs rw\n"
	got, found := mountFsType(info, "/home/tester")
	if !found || got != "tmpfs" {
		t.Errorf("mountFsType = (%q, %v), want (tmpfs, true)", got, found)
	}
}

func TestMountFsType_escapedSpace(t *testing.T) {
	info := `31 30 0:23 / /home/my\040user rw,relatime - tmpfs tmpfs rw` + "\n"
	got, found := mountFsType(info, "/home/my user")
	if !found || got != "tmpfs" {
		t.Errorf("mountFsType = (%q, %v), want (tmpfs, true)", got, found)
	}
}

// writeMountInfo writes a mountinfo fixture and returns its path.
func writeMountInfo(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing mountinfo fixture: %v", err)
	}
	return path
}

func TestCheckHomeIsolated_passesOnTmpfs(t *testing.T) {
	c := &Checker{
		HomeIsolated:  true,
		HomeDir:       "/home/tester",
		MountInfoPath: writeMountInfo(t, `31 30 0:60 / /home/tester rw,nosuid - tmpfs tmpfs rw`+"\n"),
	}
	r := c.checkHomeIsolated()
	if !r.Passed {
		t.Errorf("expected the check to pass on a tmpfs home, detail: %s", r.Detail)
	}
}

func TestCheckHomeIsolated_failsWhenHostHomeShowsThrough(t *testing.T) {
	c := &Checker{
		HomeIsolated:  true,
		HomeDir:       "/home/tester",
		MountInfoPath: writeMountInfo(t, sampleMountInfo),
	}
	r := c.checkHomeIsolated()
	if r.Passed {
		t.Fatal("expected the check to fail when the home is a real filesystem")
	}
	if !strings.Contains(r.Detail, "ext4") {
		t.Errorf("detail should name the unexpected filesystem, got %q", r.Detail)
	}
	if r.Severity != SeverityHigh {
		t.Errorf("severity = %v, want HIGH", r.Severity)
	}
}

func TestCheckHomeIsolated_failsWhenHomeIsNotAMountPoint(t *testing.T) {
	c := &Checker{
		HomeIsolated:  true,
		HomeDir:       "/home/tester",
		MountInfoPath: writeMountInfo(t, "40 30 0:44 / /tmp rw - tmpfs tmpfs rw\n"),
	}
	if r := c.checkHomeIsolated(); r.Passed {
		t.Error("expected the check to fail when the home is not a mount point at all")
	}
}

func TestCheckHomeIsolated_skippedWhenNotRequested(t *testing.T) {
	c := &Checker{HomeDir: "/home/tester", MountInfoPath: "/nonexistent"}
	r := c.checkHomeIsolated()
	if !r.Passed {
		t.Errorf("a profile that does not request isolation must not fail this check: %s", r.Detail)
	}
}

func TestRun_includesHomeIsolatedCheck(t *testing.T) {
	c := &Checker{HomeDir: t.TempDir(), UsrDir: t.TempDir(), MountInfoPath: "/nonexistent"}
	report := c.Run()
	for _, r := range report.Results {
		if r.ID == "home-isolated" {
			return
		}
	}
	t.Errorf("home-isolated check missing from the report: %+v", report.Results)
}
