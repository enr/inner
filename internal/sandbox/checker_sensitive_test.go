package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resultByID(results []CheckResult, id string) (CheckResult, bool) {
	for _, r := range results {
		if r.ID == id {
			return r, true
		}
	}
	return CheckResult{}, false
}

// A home where nothing was planted looks exactly like a correctly hidden one:
// every derived check passes.
func TestCheckSensitiveResources_passOnEmptyHome(t *testing.T) {
	c := &Checker{HomeDir: t.TempDir()}
	for _, r := range c.checkSensitiveResources() {
		if !r.Passed {
			t.Errorf("check %q failed on an empty home: %s", r.ID, r.Detail)
		}
	}
}

// The planted-secret case: a readable ~/.config/gh/hosts.yml (a path the old
// hide list did not know about) must be reported.
func TestCheckSensitiveResources_failOnPlantedSecret(t *testing.T) {
	home := t.TempDir()
	ghDir := filepath.Join(home, ".config", "gh")
	if err := os.MkdirAll(ghDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte("oauth_token: ghp_planted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Checker{HomeDir: home}
	r, ok := resultByID(c.checkSensitiveResources(), "gh-config")
	if !ok {
		t.Fatal("no gh-config check produced")
	}
	if r.Passed {
		t.Fatal("gh-config passed while ~/.config/gh/hosts.yml is readable")
	}
	if !strings.Contains(r.Detail, "~/.config/gh") {
		t.Errorf("detail %q should name the exposed path relative to home", r.Detail)
	}
	if r.Severity != SeverityHigh {
		t.Errorf("gh-config severity = %v, want HIGH (it is a credential)", r.Severity)
	}
}

// A multi-path key fails on any of its paths, and one key yields one result.
func TestCheckSensitiveResources_multiPathKey(t *testing.T) {
	home := t.TempDir()
	chromium := filepath.Join(home, ".config", "chromium", "Default")
	if err := os.MkdirAll(chromium, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chromium, "Cookies"), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}

	results := (&Checker{HomeDir: home}).checkSensitiveResources()
	count := 0
	for _, r := range results {
		if r.ID == "browser-profiles" {
			count++
			if r.Passed {
				t.Error("browser-profiles passed while ~/.config/chromium is populated")
			}
		}
	}
	if count != 1 {
		t.Errorf("browser-profiles produced %d results, want exactly 1", count)
	}
}

// An empty directory is what a tmpfs hide looks like from inside the sandbox:
// it must not be reported as exposed.
func TestCheckSensitiveResources_emptyDirIsHidden(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".terraform.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A /dev/null bind shows up as an existing, zero-length file.
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	results := (&Checker{HomeDir: home}).checkSensitiveResources()
	for _, id := range []string{"terraform-credentials", "pgpass"} {
		r, ok := resultByID(results, id)
		if !ok {
			t.Fatalf("no %s check produced", id)
		}
		if !r.Passed {
			t.Errorf("%s failed on an emptied path: %s", id, r.Detail)
		}
	}
}

// The generic pass must not duplicate the hand-written checks.
func TestCheckSensitiveResources_noOverlapWithDedicatedChecks(t *testing.T) {
	results := (&Checker{HomeDir: t.TempDir()}).checkSensitiveResources()
	for _, r := range results {
		for _, id := range dedicatedResourceKeys {
			if r.ID == id {
				t.Errorf("key %q is checked twice: once by hand, once generically", id)
			}
		}
	}
}

// An explicitly allowed key must be declassified like any other check.
func TestRun_sensitiveResource_declassifiedByAllow(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".pgpass"), []byte("host:5432:db:user:pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := (&Checker{HomeDir: home, Allow: []string{"pgpass"}}).Run()
	r, ok := resultByID(report.Results, "pgpass")
	if !ok {
		t.Fatal("no pgpass check in report")
	}
	if !r.AllowOverride || !r.Passed || r.Severity != SeverityInfo {
		t.Errorf("pgpass = %+v, want passed INFO with AllowOverride", r)
	}
}
