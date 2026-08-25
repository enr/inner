package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/isolator"
)

// ── checkProfileDigest ────────────────────────────────────────────────────────

func TestCheckProfileDigest(t *testing.T) {
	sum := sha256.Sum256([]byte("payload"))
	digest := hex.EncodeToString(sum[:])

	tests := []struct {
		name    string
		pin     string
		wantErr string
	}{
		{"unpinned", "", ""},
		{"match", digest, ""},
		{"match with prefix", "sha256:" + strings.ToUpper(digest), ""},
		{"mismatch", strings.Repeat("a", 64), "does not match --sha256"},
		{"too short", "abc123", "64-character hex"},
		{"not hex", strings.Repeat("z", 64), "not hexadecimal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkProfileDigest(tt.pin, digest, "https://example.com/p.toml")
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// ── hardenRemoteProfile ───────────────────────────────────────────────────────

func hostileRunConfig() *config.RunConfig {
	return &config.RunConfig{
		Network:      true,
		PidNamespace: false,
		Env: config.EnvConfig{
			InheritAll: true,
			Inherit:    []string{"TERM", "GITHUB_TOKEN", "LANG", "AWS_SECRET_ACCESS_KEY"},
		},
		Allow: []string{"ssh-keys", "aws-credentials", "docker-socket", "nested-user-ns", "env-secrets"},
	}
}

func TestHardenRemoteProfile_stripsPrivilegeEscalatingSettings(t *testing.T) {
	rc := hostileRunConfig()
	applied := hardenRemoteProfile(rc)

	if rc.Env.InheritAll {
		t.Error("inherit_all survived hardening")
	}
	if got := strings.Join(rc.Env.Inherit, ","); got != "TERM,LANG" {
		t.Errorf("env inherit = %q, want the secret-looking names dropped", got)
	}
	if got := strings.Join(rc.Allow, ","); got != "env-secrets" {
		t.Errorf("allow = %q, want only the verify-only key kept", got)
	}
	if !rc.PidNamespace {
		t.Error("pid_namespace = false survived hardening")
	}
	// Network stays: it is reported and gated by the consent prompt, not stripped.
	if !rc.Network {
		t.Error("network was stripped; it should be reported instead")
	}
	if len(applied) != 4 {
		t.Errorf("applied = %v, want one line per change", applied)
	}
}

func TestHardenRemoteProfile_leavesBenignProfileUntouched(t *testing.T) {
	rc := &config.RunConfig{
		PidNamespace: true,
		Env:          config.EnvConfig{Inherit: []string{"TERM", "LANG"}},
		Allow:        []string{"shims-active"},
	}
	if applied := hardenRemoteProfile(rc); len(applied) != 0 {
		t.Errorf("applied = %v, want no changes for a benign profile", applied)
	}
}

// ── gateRemoteProfile ─────────────────────────────────────────────────────────

func gateFixture() (*config.RunConfig, remoteSource) {
	rc := hostileRunConfig()
	rc.Entrypoint = config.Entrypoint{Cmd: "/bin/sh", Args: []string{"-c", "curl evil"}}
	return rc, remoteSource{url: "https://example.com/p.toml", digest: strings.Repeat("ab", 32)}
}

func TestGateRemoteProfile_yesFlagDoesNotConsent(t *testing.T) {
	rc, src := gateFixture()
	var buf bytes.Buffer
	// --yes plus no terminal: consent must still be missing.
	proceed, err := gateRemoteProfile(&buf, strings.NewReader(""), rc, src, runCLIFlags{yes: true}, false)
	if err == nil {
		t.Fatal("expected an error when there is no terminal and no --allow-remote")
	}
	if proceed {
		t.Error("proceed = true without consent")
	}
	if !strings.Contains(err.Error(), "--allow-remote") {
		t.Errorf("error should name the flag to pass: %v", err)
	}
}

func TestGateRemoteProfile_allowRemoteConsentsAndHardens(t *testing.T) {
	rc, src := gateFixture()
	var buf bytes.Buffer
	proceed, err := gateRemoteProfile(&buf, strings.NewReader(""), rc, src, runCLIFlags{allowRemote: true}, false)
	if err != nil || !proceed {
		t.Fatalf("proceed = %v, err = %v; want true, nil", proceed, err)
	}
	if rc.Env.InheritAll || len(rc.Allow) != 1 {
		t.Errorf("profile was not hardened: inherit_all=%v allow=%v", rc.Env.InheritAll, rc.Allow)
	}
	out := buf.String()
	for _, want := range []string{src.url, src.digest, "hardened:", "curl evil", "network: enabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
}

func TestGateRemoteProfile_trustRemoteSkipsHardening(t *testing.T) {
	rc, src := gateFixture()
	var buf bytes.Buffer
	proceed, err := gateRemoteProfile(&buf, strings.NewReader(""), rc, src, runCLIFlags{trustRemote: true}, false)
	if err != nil || !proceed {
		t.Fatalf("proceed = %v, err = %v; want true, nil", proceed, err)
	}
	if !rc.Env.InheritAll {
		t.Error("--trust-remote must leave inherit_all in place")
	}
	if !strings.Contains(buf.String(), "--trust-remote") {
		t.Errorf("output should say the hardening was disabled:\n%s", buf.String())
	}
}

func TestGateRemoteProfile_interactiveAnswer(t *testing.T) {
	for _, tt := range []struct {
		answer string
		want   bool
	}{{"y\n", true}, {"Y\n", true}, {"n\n", false}, {"\n", false}, {"", false}} {
		rc, src := gateFixture()
		var buf bytes.Buffer
		proceed, err := gateRemoteProfile(&buf, strings.NewReader(tt.answer), rc, src, runCLIFlags{}, true)
		if err != nil {
			t.Fatalf("answer %q: unexpected error %v", tt.answer, err)
		}
		if proceed != tt.want {
			t.Errorf("answer %q: proceed = %v, want %v", tt.answer, proceed, tt.want)
		}
	}
}

// ── runSandbox integration ────────────────────────────────────────────────────

// noTerminal makes the consent gate believe there is nobody to prompt, so a run
// without --allow-remote must be refused instead of hanging on stdin.
func noTerminal(t *testing.T) {
	t.Helper()
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = old })
}

// capturingIsolator records the RunConfig the sandbox would have been built
// from, so a test can assert on what the remote profile actually obtained.
type capturingIsolator struct{ cfg config.RunConfig }

func (c *capturingIsolator) Build(cfg config.RunConfig) (*exec.Cmd, error) {
	c.cfg = cfg
	return exec.Command("true"), nil
}
func (c *capturingIsolator) Available() (bool, string) { return true, "fake" }

const hostileRemoteTOML = `schema_version = "1"
name = "hostile"

[sandbox]
network       = true
pid_namespace = false
allow         = ["ssh-keys", "aws-credentials"]

[env]
inherit_all = true

[entrypoint]
cmd         = "/bin/sh"
args        = ["-c", "true"]
interactive = false
`

func serveHostileProfile(t *testing.T) string {
	t.Helper()
	srv := newRunProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(hostileRemoteTOML))
	}))
	return srv.URL + "/hostile.toml"
}

func TestRunSandbox_remoteProfile_refusedWithoutConsent(t *testing.T) {
	noTerminal(t)

	url := serveHostileProfile(t)
	app, _ := newRunTestApp(t)
	// --yes must not stand in for consent to a downloaded profile.
	err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{profile: url, yes: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "without consent") {
		t.Fatalf("err = %v, want a refusal naming the missing consent", err)
	}
}

func TestRunSandbox_remoteProfile_hardenedWhenAllowed(t *testing.T) {
	noTerminal(t)

	url := serveHostileProfile(t)
	app, _ := newRunTestApp(t)
	iso := &capturingIsolator{}
	app.isolatorFn = func() (isolator.Isolator, error) { return iso, nil }
	app.launcherFn = executor.New

	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{profile: url, allowRemote: true}, nil); err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if iso.cfg.Env.InheritAll {
		t.Error("the sandbox was built with inherit_all from a remote profile")
	}
	if len(iso.cfg.Allow) != 0 {
		t.Errorf("allow = %v, want the credential keys stripped", iso.cfg.Allow)
	}
	if !iso.cfg.PidNamespace {
		t.Error("a remote profile turned off the PID namespace")
	}
}

func TestRunSandbox_remoteProfile_checksumMismatchAborts(t *testing.T) {
	url := serveHostileProfile(t)
	app, _ := newRunTestApp(t)
	err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{profile: url, allowRemote: true, sha256: strings.Repeat("0", 64)}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not match --sha256") {
		t.Fatalf("err = %v, want a checksum mismatch abort", err)
	}
}

func TestRunSandbox_remoteProfile_checksumMatchRuns(t *testing.T) {
	noTerminal(t)

	url := serveHostileProfile(t)
	app, _ := newRunTestApp(t)
	sum := sha256.Sum256([]byte(hostileRemoteTOML))
	flags := runCLIFlags{profile: url, allowRemote: true, sha256: hex.EncodeToString(sum[:]), dryRun: true}
	if err := app.runSandbox(&bytes.Buffer{}, flags, nil); err != nil {
		t.Fatalf("runSandbox with a matching pin: %v", err)
	}
}

func TestRunSandbox_localProfile_rejectsChecksumPin(t *testing.T) {
	app, dir := newRunTestApp(t)
	minimalProfile(t, dir, "local-pin", "schema_version = \"1\"\n[entrypoint]\ncmd = \"/bin/sh\"\n")
	err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{profile: "local-pin", sha256: strings.Repeat("0", 64), dryRun: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "local profile") {
		t.Fatalf("err = %v, want --sha256 to be rejected for a local profile", err)
	}
}
