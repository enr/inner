package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// trustFixture is a user config dir with a "safe" profile and a project
// directory, as a sandbox could leave it: a config file pointing at a planted
// profile, and a project profile shadowing the user's own by name.
func trustFixture(t *testing.T) (l *Loader, warn *bytes.Buffer, project string) {
	t.Helper()
	root := t.TempDir()
	userDir := filepath.Join(root, "user")
	project = filepath.Join(root, "project")
	writeFile(t, filepath.Join(userDir, "profiles", "safe.toml"), "name = \"safe\"\n")
	writeFile(t, filepath.Join(project, "inner.toml"), "log_dir = \"/tmp/planted\"\ndefault_profile = \"planted\"\n")
	writeFile(t, filepath.Join(project, ".config", "inner", "profiles", "planted.toml"), "name = \"planted\"\n[env]\ninherit_all = true\n")
	writeFile(t, filepath.Join(project, ".config", "inner", "profiles", "safe.toml"), "name = \"shadow\"\n[env]\ninherit_all = true\n")
	warn = &bytes.Buffer{}
	l = &Loader{Dir: userDir, WorkDir: project, RequireTrust: true, Warn: warn}
	return l, warn, project
}

func TestTrust_untrustedProjectConfigIsIgnored(t *testing.T) {
	l, warn, _ := trustFixture(t)
	g, err := l.LoadEffectiveGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.LogDir != "" || g.DefaultProfile != "" {
		t.Fatalf("untrusted project config was applied: %+v", g)
	}
	if !strings.Contains(warn.String(), "not trusted") {
		t.Fatalf("no warning: %q", warn.String())
	}
	// Warned once, however many times the config is loaded.
	_, _ = l.LoadEffectiveGlobal()
	if n := strings.Count(warn.String(), "ignoring"); n != 1 {
		t.Fatalf("warned %d times", n)
	}
}

func TestTrust_untrustedProjectProfileIsRefused(t *testing.T) {
	l, _, _ := trustFixture(t)
	// The project profile shadows the user's "safe" by name: refused, not
	// silently loaded and not silently replaced.
	if _, err := l.Build("safe"); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("Build(safe) err = %v, want untrusted refusal", err)
	}
	if _, err := l.Build(".config/inner/profiles/planted.toml"); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("Build(path) err = %v, want untrusted refusal", err)
	}
}

func TestTrust_approvedThenChanged(t *testing.T) {
	l, _, project := trustFixture(t)
	cfg := filepath.Join(project, "inner.toml")
	planted := filepath.Join(project, ".config", "inner", "profiles", "planted.toml")
	if _, err := l.Trust(cfg, planted); err != nil {
		t.Fatal(err)
	}
	g, err := l.LoadEffectiveGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.DefaultProfile != "planted" {
		t.Fatalf("trusted project config not applied: %+v", g)
	}
	if _, err := l.Build(""); err != nil {
		t.Fatalf("trusted profile refused: %v", err)
	}

	// A change — what a sandbox writing the workdir would do — withdraws it.
	writeFile(t, planted, "name = \"planted\"\n[env]\ninherit_all = true\n# changed\n")
	if _, err := l.Build(""); err == nil {
		t.Fatal("changed profile still trusted")
	}
	writeFile(t, cfg, "default_profile = \"other\"\n")
	if g, _ := l.LoadEffectiveGlobal(); g.DefaultProfile != "" {
		t.Fatalf("changed config still applied: %+v", g)
	}
}

func TestTrust_userFilesNeedNoApproval(t *testing.T) {
	l, _, _ := trustFixture(t)
	// The user's own profiles and config are never subject to trust, even when
	// the working directory contains the user config dir.
	writeFile(t, filepath.Join(l.Dir, "config.toml"), "default_profile = \"safe\"\n")
	l.WorkDir = filepath.Dir(l.Dir)
	rc, err := l.Build("")
	if err != nil {
		t.Fatal(err)
	}
	if rc.Name != "safe" {
		t.Fatalf("got profile %q", rc.Name)
	}
}

func TestTrust_exemptDownloadedProfile(t *testing.T) {
	l, _, project := trustFixture(t)
	tmp := filepath.Join(project, "inner-profile-1.toml")
	writeFile(t, tmp, "name = \"remote\"\n")
	if _, _, err := l.BuildUntrusted(tmp); err == nil {
		t.Fatal("expected refusal before exemption")
	}
	l.ExemptFromTrust(tmp)
	if _, _, err := l.BuildUntrusted(tmp); err != nil {
		t.Fatalf("exempt downloaded profile refused: %v", err)
	}
}

func TestTrust_offInTestConstructors(t *testing.T) {
	_, _, project := trustFixture(t)
	l := NewLoaderWithWorkDir(t.TempDir(), project)
	if g, _ := l.LoadEffectiveGlobal(); g.DefaultProfile != "planted" {
		t.Fatal("trust enforced without RequireTrust")
	}
}
