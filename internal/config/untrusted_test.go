package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeUntrustedProfile(t *testing.T, body string) (*Loader, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "remote.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewLoader(dir), path
}

// The attack BuildUntrusted exists for: a downloaded profile naming host
// secrets through $VAR expansion, and choosing host-side paths.
func TestBuildUntrusted_refusesHostSideChoices(t *testing.T) {
	t.Setenv("INNER_TEST_SECRET", "s3cret")
	home, _ := os.UserHomeDir()
	l, path := writeUntrustedProfile(t, `
[sandbox]
clipboard = true
home_allow = ["$INNER_TEST_SECRET/x"]
[env]
set = { A = "${INNER_TEST_SECRET}", B = "$HOME/bin", C = "u$UID" }
path_prepend = ["$INNER_TEST_SECRET"]
[entrypoint]
cmd = "sh"
workdir = "~/.config/systemd/user"
[output]
log = "~/.bashrc.d"
[mounts]
"/opt/$INNER_TEST_SECRET" = { dest = "/tmp/$INNER_TEST_SECRET" }
`)
	rc, notes, err := l.BuildUntrusted(path)
	if err != nil {
		t.Fatalf("BuildUntrusted: %v", err)
	}
	joined := strings.Join(notes, "\n")

	for field, got := range map[string]string{
		"env.set A":    rc.Env.Set["A"],
		"path_prepend": strings.Join(rc.Env.PathPrepend, ":"),
		"home_allow":   strings.Join(rc.HomeAllow, ":"),
		"mount src":    rc.Mounts[0].Src,
		"mount dest":   rc.Mounts[0].Dest,
	} {
		if strings.Contains(got, "s3cret") {
			t.Errorf("%s = %q: host variable expanded for a remote profile", field, got)
		}
	}
	if got := rc.Env.Set["B"]; got != home+"/bin" {
		t.Errorf("$HOME is not a secret and must still resolve: B = %q", got)
	}
	if got := rc.Env.Set["C"]; got != "u"+strconv.Itoa(os.Getuid()) {
		t.Errorf("$UID must still resolve: C = %q", got)
	}
	if !strings.Contains(joined, "INNER_TEST_SECRET") {
		t.Errorf("notes do not name the refused variable:\n%s", joined)
	}
	if strings.Count(joined, "INNER_TEST_SECRET") != 1 {
		t.Errorf("a refused variable is reported once, not per use:\n%s", joined)
	}

	if rc.Workdir != "" {
		t.Errorf("workdir = %q survived", rc.Workdir)
	}
	if strings.Contains(rc.LogDir, ".bashrc.d") {
		t.Errorf("log dir = %q survived", rc.LogDir)
	}
	if rc.Clipboard {
		t.Error("clipboard survived")
	}
	for _, want := range []string{"[output] log", "[entrypoint] workdir", "clipboard"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes do not mention %q:\n%s", want, joined)
		}
	}
}

// The global config is the user's own: its log_dir and workspaces_path still
// apply to a remote profile, with full expansion.
func TestBuildUntrusted_keepsGlobalHostPaths(t *testing.T) {
	t.Setenv("INNER_TEST_LOGROOT", "/var/tmp/inner-logs")
	l, path := writeUntrustedProfile(t, `
workspaces_path = "/tmp/evil"
[entrypoint]
cmd = "sh"
[output]
log = "/tmp/evil-log"
`)
	global := "log_dir = \"$INNER_TEST_LOGROOT\"\nworkspaces_path = \"/srv/ws\"\n"
	if err := os.WriteFile(l.GlobalConfigPath(), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, _, err := l.BuildUntrusted(path)
	if err != nil {
		t.Fatal(err)
	}
	if rc.LogDir != "/var/tmp/inner-logs" {
		t.Errorf("LogDir = %q, want the global log_dir", rc.LogDir)
	}
	if rc.WorkspacesPath != "/srv/ws" {
		t.Errorf("WorkspacesPath = %q, want the global one", rc.WorkspacesPath)
	}
}

// A benign profile builds exactly as Build builds it, with no notes.
func TestBuildUntrusted_benignProfileMatchesBuild(t *testing.T) {
	l, path := writeUntrustedProfile(t, `
[sandbox]
network = true
home = "isolated"
home_allow = ["~/.local/bin"]
[env]
inherit = ["TERM"]
set = { EDITOR = "vi", GOPATH = "$HOME/go" }
[entrypoint]
cmd = "claude"
args = ["--x"]
[mounts]
"~/.m2" = { dest = "~/.m2" }
`)
	trusted, err := l.Build(path)
	if err != nil {
		t.Fatal(err)
	}
	untrusted, notes, err := l.BuildUntrusted(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none for a benign profile", notes)
	}
	if trusted.Env.Set["GOPATH"] != untrusted.Env.Set["GOPATH"] ||
		strings.Join(trusted.HomeAllow, ",") != strings.Join(untrusted.HomeAllow, ",") ||
		trusted.Mounts[0] != untrusted.Mounts[0] ||
		trusted.Entrypoint.Cmd != untrusted.Entrypoint.Cmd {
		t.Errorf("benign profile differs:\ntrusted   %+v\nuntrusted %+v", trusted, untrusted)
	}
}
