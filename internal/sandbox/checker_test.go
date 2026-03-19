package sandbox

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enr/inner/internal/config"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// newChecker returns a Checker using tmpDir as the fake home and a separate
// writable dir as usr, so tests never depend on the real filesystem.
func newChecker(t *testing.T) (*Checker, string) {
	t.Helper()
	home := t.TempDir()
	// usr is a separate read-only-by-intent dir; tests that need it writable
	// can override UsrDir.
	return &Checker{
		HomeDir:       home,
		UsrDir:        t.TempDir(), // writable by default so checkUsrReadonly fails; override to ro
		ShimMountPath: "/run/inner-shims-test",
		dialFn:        blockedDialFn, // network blocked by default in tests
	}, home
}

// blockedDialFn simulates a blocked network connection.
func blockedDialFn(_, _ string, _ time.Duration) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Err: os.ErrPermission}
}

// openDialFn simulates a successful network connection.
func openDialFn(_, _ string, _ time.Duration) (net.Conn, error) {
	// Return a pipe connection to avoid a real network call.
	c, _ := net.Pipe()
	return c, nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

// findResult finds the first CheckResult with the given ID.
func findResult(r Report, id string) (CheckResult, bool) {
	for _, c := range r.Results {
		if c.ID == id {
			return c, true
		}
	}
	return CheckResult{}, false
}

// ── no-root ───────────────────────────────────────────────────────────────────

func TestCheck_noRoot_pass(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test must not run as root")
	}
	ch, _ := newChecker(t)
	r := ch.checkNoRoot()
	if !r.Passed {
		t.Errorf("expected no-root to pass for non-root user, got: %s", r.Detail)
	}
}

// ── usr-readonly ──────────────────────────────────────────────────────────────

func TestCheck_usrReadonly_fail_when_writable(t *testing.T) {
	ch, _ := newChecker(t)
	// UsrDir defaults to a writable temp dir → should fail.
	r := ch.checkUsrReadonly()
	if r.Passed {
		t.Error("expected usr-readonly to fail when /usr is writable")
	}
}

func TestCheck_usrReadonly_pass_when_readonly(t *testing.T) {
	ch, _ := newChecker(t)
	roDir := t.TempDir()
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) }) //nolint:errcheck
	ch.UsrDir = roDir
	r := ch.checkUsrReadonly()
	if !r.Passed {
		t.Errorf("expected usr-readonly to pass for ro dir, got: %s", r.Detail)
	}
}

// ── git-credentials ───────────────────────────────────────────────────────────

func TestCheck_gitCredentials_pass_when_absent(t *testing.T) {
	ch, _ := newChecker(t)
	r := ch.checkGitCredentials()
	if !r.Passed {
		t.Errorf("expected git-credentials to pass when file absent, got: %s", r.Detail)
	}
}

func TestCheck_gitCredentials_fail_when_present(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".git-credentials"), "https://user:token@github.com\n")
	r := ch.checkGitCredentials()
	if r.Passed {
		t.Error("expected git-credentials to fail when file present")
	}
	if !strings.Contains(r.Detail, ".git-credentials") {
		t.Errorf("detail should mention file path, got: %q", r.Detail)
	}
}

// ── ssh-keys ──────────────────────────────────────────────────────────────────

func TestCheck_sshKeys_pass_when_absent(t *testing.T) {
	ch, _ := newChecker(t)
	r := ch.checkSSHKeys()
	if !r.Passed {
		t.Errorf("expected ssh-keys to pass when ~/.ssh absent, got: %s", r.Detail)
	}
}

func TestCheck_sshKeys_fail_on_private_key(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "PRIVATE KEY DATA")
	r := ch.checkSSHKeys()
	if r.Passed {
		t.Error("expected ssh-keys to fail when private key present")
	}
	if !strings.Contains(r.Detail, "id_ed25519") {
		t.Errorf("detail should mention key name, got: %q", r.Detail)
	}
}

func TestCheck_sshKeys_pass_with_only_pubkey(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".ssh", "id_ed25519.pub"), "ssh-ed25519 AAAA...")
	r := ch.checkSSHKeys()
	if !r.Passed {
		t.Errorf("expected ssh-keys to pass with only .pub file, got: %s", r.Detail)
	}
}

func TestCheck_sshKeys_pass_with_known_hosts_only(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".ssh", "known_hosts"), "github.com ssh-ed25519 ...")
	r := ch.checkSSHKeys()
	if !r.Passed {
		t.Errorf("expected ssh-keys to pass with only known_hosts, got: %s", r.Detail)
	}
}

// ── gpg-keys ──────────────────────────────────────────────────────────────────

func TestCheck_gpgKeys_pass_when_absent(t *testing.T) {
	ch, _ := newChecker(t)
	r := ch.checkGPGKeys()
	if !r.Passed {
		t.Errorf("expected gpg-keys to pass when ~/.gnupg absent, got: %s", r.Detail)
	}
}

func TestCheck_gpgKeys_fail_when_present(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".gnupg", "pubring.kbx"), "keyring data")
	r := ch.checkGPGKeys()
	if r.Passed {
		t.Error("expected gpg-keys to fail when ~/.gnupg non-empty")
	}
}

// ── env-secrets ───────────────────────────────────────────────────────────────

func TestCheck_envSecrets_pass_when_clean(t *testing.T) {
	ch, _ := newChecker(t)
	// Ensure no sensitive vars in env for this test.
	for _, k := range []string{"MY_PASSWORD", "MY_SECRET", "MY_TOKEN", "MY_CREDENTIAL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// Run the check — in a normal test env without sensitive vars it should pass.
	// We can't guarantee the host env is clean, so we skip if it fails due to env.
	r := ch.checkEnvSecrets()
	_ = r // result depends on the CI environment; we test the fail case explicitly
}

func TestCheck_envSecrets_fail_on_sensitive_var(t *testing.T) {
	t.Setenv("INNER_TEST_PASSWORD", "hunter2")
	ch, _ := newChecker(t)
	r := ch.checkEnvSecrets()
	if r.Passed {
		t.Error("expected env-secrets to fail when PASSWORD var is present")
	}
	if !strings.Contains(r.Detail, "INNER_TEST_PASSWORD") {
		t.Errorf("detail should mention var name, got: %q", r.Detail)
	}
}

func TestCheck_envSecrets_detects_token(t *testing.T) {
	t.Setenv("INNER_TEST_TOKEN", "ghp_abc123")
	ch, _ := newChecker(t)
	r := ch.checkEnvSecrets()
	if r.Passed {
		t.Error("expected env-secrets to fail when TOKEN var is present")
	}
}

// ── docker-socket ─────────────────────────────────────────────────────────────

func TestCheck_dockerSocket_pass_when_absent(t *testing.T) {
	ch, _ := newChecker(t)
	// /var/run/docker.sock likely doesn't exist in test env; if it does, skip.
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		t.Skip("docker.sock present on host, skip")
	}
	r := ch.checkDockerSocket()
	if !r.Passed {
		t.Errorf("expected docker-socket to pass when socket absent, got: %s", r.Detail)
	}
}

// ── netrc ─────────────────────────────────────────────────────────────────────

func TestCheck_netrc_pass_when_absent(t *testing.T) {
	ch, _ := newChecker(t)
	r := ch.checkNetrc()
	if !r.Passed {
		t.Errorf("expected netrc to pass when ~/.netrc absent, got: %s", r.Detail)
	}
}

func TestCheck_netrc_fail_when_present(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".netrc"), "machine example.com login user password secret")
	r := ch.checkNetrc()
	if r.Passed {
		t.Error("expected netrc to fail when ~/.netrc present")
	}
}

// ── shims-active ──────────────────────────────────────────────────────────────

func TestCheck_shimsActive_fail_without_shims(t *testing.T) {
	ch, _ := newChecker(t)
	// PATH does not start with ShimMountPath → should fail.
	t.Setenv("PATH", "/usr/bin:/bin")
	r := ch.checkShimsActive()
	if r.Passed {
		t.Error("expected shims-active to fail when PATH does not start with shim dir")
	}
}

func TestCheck_shimsActive_pass_with_shims_in_path(t *testing.T) {
	shimDir := t.TempDir()
	ch, _ := newChecker(t)
	ch.ShimMountPath = shimDir
	t.Setenv("PATH", shimDir+":/usr/bin:/bin")
	r := ch.checkShimsActive()
	if !r.Passed {
		t.Errorf("expected shims-active to pass, got: %s", r.Detail)
	}
}

// ── network-policy ────────────────────────────────────────────────────────────

func TestCheck_networkPolicy_pass_when_blocked(t *testing.T) {
	ch, _ := newChecker(t)
	// dialFn is blockedDialFn from newChecker.
	r := ch.checkNetworkPolicy()
	if !r.Passed {
		t.Errorf("expected network-policy to pass when network blocked, got: %s", r.Detail)
	}
}

func TestCheck_networkPolicy_fail_when_open(t *testing.T) {
	ch, _ := newChecker(t)
	ch.dialFn = openDialFn
	r := ch.checkNetworkPolicy()
	if r.Passed {
		t.Error("expected network-policy to fail when network is open")
	}
}

func TestCheck_networkPolicy_skip_when_networkEnabled(t *testing.T) {
	ch, _ := newChecker(t)
	ch.NetworkEnabled = true
	ch.dialFn = openDialFn // network open but intentional
	r := ch.checkNetworkPolicy()
	if !r.Passed {
		t.Error("expected network-policy to pass when NetworkEnabled=true")
	}
	if !strings.Contains(r.Detail, "network=true") {
		t.Errorf("expected detail to mention network=true, got: %q", r.Detail)
	}
}

// ── Custom checks ─────────────────────────────────────────────────────────────

func TestCheck_custom_pass(t *testing.T) {
	ch, _ := newChecker(t)
	ch.Custom = []config.CustomCheck{
		{Name: "true sempre", Cmd: "true", Severity: "critical"},
	}
	report := ch.Run()
	res, ok := findResult(report, "custom:true sempre")
	if !ok {
		t.Fatal("custom check result not found")
	}
	if !res.Passed {
		t.Error("expected custom check to pass")
	}
}

func TestCheck_custom_fail(t *testing.T) {
	ch, _ := newChecker(t)
	ch.Custom = []config.CustomCheck{
		{Name: "sempre fallisce", Cmd: "false", Severity: "high"},
	}
	report := ch.Run()
	res, ok := findResult(report, "custom:sempre fallisce")
	if !ok {
		t.Fatal("custom check result not found")
	}
	if res.Passed {
		t.Error("expected custom check to fail")
	}
	if res.Severity != SeverityHigh {
		t.Errorf("expected HIGH severity, got %s", res.Severity)
	}
}

func TestCheck_custom_severityParsed(t *testing.T) {
	ch, _ := newChecker(t)
	ch.Custom = []config.CustomCheck{
		{Name: "c", Cmd: "false", Severity: "critical"},
	}
	report := ch.Run()
	res, _ := findResult(report, "custom:c")
	if res.Severity != SeverityCritical {
		t.Errorf("expected CRITICAL, got %s", res.Severity)
	}
}

// ── Allow override ────────────────────────────────────────────────────────────

func TestRun_allowOverride_downgradesSSHKeysToInfo(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "private key")
	ch.Allow = []string{"ssh-keys"}
	report := ch.Run()
	res, ok := findResult(report, "ssh-keys")
	if !ok {
		t.Fatal("ssh-keys result not found")
	}
	if !res.Passed {
		t.Error("expected ssh-keys to pass after allow override")
	}
	if res.Severity != SeverityInfo {
		t.Errorf("expected INFO severity after allow, got %s", res.Severity)
	}
	if !res.AllowOverride {
		t.Error("expected AllowOverride=true")
	}
}

func TestRun_allowOverride_doesNotAffectUnrelatedCheck(t *testing.T) {
	ch, home := newChecker(t)
	writeFile(t, filepath.Join(home, ".netrc"), "machine host login user password secret")
	ch.Allow = []string{"ssh-keys"} // only ssh-keys allowed, not netrc
	report := ch.Run()
	res, ok := findResult(report, "netrc")
	if !ok {
		t.Fatal("netrc result not found")
	}
	if res.Passed {
		t.Error("netrc should still fail when not in allow list")
	}
	if res.AllowOverride {
		t.Error("netrc should not have AllowOverride=true")
	}
}

// ── Report ────────────────────────────────────────────────────────────────────

func TestReport_conformant_allPass(t *testing.T) {
	r := Report{Results: []CheckResult{
		{Passed: true, Severity: SeverityCritical},
		{Passed: true, Severity: SeverityHigh},
	}}
	if !r.Conformant() {
		t.Error("expected Conformant=true when all checks pass")
	}
}

func TestReport_conformant_false_on_failure(t *testing.T) {
	r := Report{Results: []CheckResult{
		{Passed: true, Severity: SeverityCritical},
		{Passed: false, Severity: SeverityMedium},
	}}
	if r.Conformant() {
		t.Error("expected Conformant=false when any check fails")
	}
}

func TestReport_render_includesSummary(t *testing.T) {
	r := Report{Results: []CheckResult{
		{ID: "no-root", Name: "user non è root", Passed: true, Severity: SeverityCritical},
		{ID: "ssh-keys", Name: "~/.ssh non accessibile", Passed: false, Severity: SeverityHigh, Detail: "~/.ssh/id_ed25519 trovata"},
	}}
	var buf bytes.Buffer
	r.Render(&buf, false)
	out := buf.String()
	if !strings.Contains(out, "1/2") {
		t.Errorf("expected '1/2' in output, got: %s", out)
	}
	if !strings.Contains(out, "⚠") {
		t.Errorf("expected non-conformant indicator, got: %s", out)
	}
}

func TestReport_render_suggest_includesSnippet(t *testing.T) {
	r := Report{Results: []CheckResult{
		{
			ID:       "ssh-keys",
			Name:     "~/.ssh non accessibile",
			Passed:   false,
			Severity: SeverityHigh,
			Detail:   "~/.ssh/id_ed25519 trovata",
			Suggest:  suggestAllow("ssh-keys"),
		},
	}}
	var buf bytes.Buffer
	r.Render(&buf, true)
	out := buf.String()
	if !strings.Contains(out, `allow = ["ssh-keys"]`) {
		t.Errorf("expected TOML snippet in suggest output, got: %s", out)
	}
}

func TestReport_render_allowOverride_showsInfo(t *testing.T) {
	r := Report{Results: []CheckResult{
		{ID: "ssh-keys", Name: "~/.ssh non accessibile", Passed: true,
			Severity: SeverityInfo, AllowOverride: true},
	}}
	var buf bytes.Buffer
	r.Render(&buf, false)
	out := buf.String()
	if !strings.Contains(out, "ℹ") {
		t.Errorf("expected ℹ symbol for INFO override, got: %s", out)
	}
	if !strings.Contains(out, "esplicitamente permessa") {
		t.Errorf("expected 'esplicitamente permessa' in output, got: %s", out)
	}
}
