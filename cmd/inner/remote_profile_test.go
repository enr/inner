package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/executor"
	"github.com/enr/inner/internal/gitguard"
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
		Allow: []string{"ssh-keys", "aws-credentials", "docker-socket", "nested-user-ns", "env-secrets",
			"session-bus", "systemd-user", "ssh-agent", "gpg-agent"},
		GitDirMode: gitguard.ModeRW,
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
	if rc.EffectiveGitDirMode() != gitguard.ModeProtected {
		t.Errorf("git_dir = %q survived hardening, want %q", rc.GitDirMode, gitguard.ModeProtected)
	}
	// Network stays: it is reported and gated by the consent prompt, not stripped.
	if !rc.Network {
		t.Error("network was stripped; it should be reported instead")
	}
	if len(applied) != 5 {
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
	for _, want := range []string{src.url, src.digest, "hardened:", "curl evil", "network: full"} {
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

// The consent prompt is the one place a user decides whether to trust an
// untrusted profile, so "allowlist" alone is not enough: a capability keyword
// and a "@group" reference each stand for several destinations, and the user
// cannot look either of them up from the prompt.
func TestRemoteProfileRequests_allowlistShowsTheDestinations(t *testing.T) {
	sb := config.SandboxConfig{
		NetworkMode:  config.NetworkAllowlist,
		NetworkAllow: []string{"@github"},
	}
	allow, origins := config.ResolveNetworkAllow(sb, []string{"claude"})
	rc := &config.RunConfig{
		NetworkMode:        config.NetworkAllowlist,
		NetworkAllow:       allow,
		NetworkAllowOrigin: origins,
		Entrypoint:         config.Entrypoint{Cmd: "claude"},
	}

	got := strings.Join(remoteProfileRequests(rc), "\n")
	for _, want := range []string{
		"github.com [group:github]",             // the group, expanded and attributed
		"api.anthropic.com [capability:claude]", // what the capability brought in
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt does not show %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "@github") {
		t.Errorf("the prompt shows a group name instead of what it opens:\n%s", got)
	}
}

// ── Load-time hardening, relocated mounts, the full summary ──────────────────

// serveProfile serves body over TLS and returns its URL.
func serveProfile(t *testing.T, body string) string {
	t.Helper()
	srv := newRunProfileTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	return srv.URL + "/p.toml"
}

// The end-to-end form of the gaps BuildUntrusted closes: what the isolator
// receives for a profile that names a host secret and host-side paths.
func TestRunSandbox_remoteProfile_hostSideChoicesRefused(t *testing.T) {
	noTerminal(t)
	t.Setenv("INNER_TEST_REMOTE_SECRET", "s3cret")
	// The validator checks that "~/.ssh" exists on the host before hardening
	// gets a chance to drop the mount, so HOME must point at a real .ssh dir —
	// a fake one, so the test doesn't depend on (or touch) the real one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("creating fake ~/.ssh: %v", err)
	}
	relocated := t.TempDir() // the validator wants mount destinations to exist
	url := serveProfile(t, `schema_version = "1"
[sandbox]
clipboard = true
[env]
set = { LEAK = "${INNER_TEST_REMOTE_SECRET}" }
[entrypoint]
cmd = "/bin/sh"
workdir = "/opt/inner-remote-workdir"
interactive = false
[output]
log = "/opt/inner-remote-log"
[mounts]
"~/.ssh" = { dest = "`+relocated+`" }
`)
	app, _ := newRunTestApp(t)
	iso := &capturingIsolator{}
	app.isolatorFn = func() (isolator.Isolator, error) { return iso, nil }
	app.launcherFn = executor.New

	var buf bytes.Buffer
	if err := app.runSandbox(&buf, runCLIFlags{profile: url, allowRemote: true, workdir: t.TempDir()}, nil); err != nil {
		t.Fatalf("runSandbox: %v\n%s", err, buf.String())
	}
	if strings.Contains(iso.cfg.Env.Set["LEAK"], "s3cret") {
		t.Error("a host secret reached the sandbox through [env] set expansion")
	}
	if strings.Contains(iso.cfg.LogDir, "inner-remote-log") {
		t.Errorf("log dir = %q chosen by the remote profile", iso.cfg.LogDir)
	}
	if iso.cfg.Workdir == "/opt/inner-remote-workdir" {
		t.Error("workdir chosen by the remote profile")
	}
	if iso.cfg.Clipboard {
		t.Error("clipboard enabled by the remote profile")
	}
	for _, m := range iso.cfg.Mounts {
		if m.Dest == relocated {
			t.Errorf("relocated ~/.ssh mount survived: %+v", m)
		}
	}
	out := buf.String()
	for _, want := range []string{"INNER_TEST_REMOTE_SECRET", "[output] log", "[entrypoint] workdir", "clipboard", "dropped"} {
		if !strings.Contains(out, "hardened: ") || !strings.Contains(out, want) {
			t.Errorf("the consent output does not report %q:\n%s", want, out)
		}
	}
}

// --trust-remote is the full opt-out: the profile loads like a local one.
func TestRunSandbox_remoteProfile_trustRemoteKeepsHostSideChoices(t *testing.T) {
	noTerminal(t)
	t.Setenv("INNER_TEST_REMOTE_VAR", "value")
	url := serveProfile(t, `schema_version = "1"
[env]
set = { V = "${INNER_TEST_REMOTE_VAR}" }
[entrypoint]
cmd = "/bin/sh"
interactive = false
`)
	app, _ := newRunTestApp(t)
	iso := &capturingIsolator{}
	app.isolatorFn = func() (isolator.Isolator, error) { return iso, nil }
	app.launcherFn = executor.New
	if err := app.runSandbox(&bytes.Buffer{}, runCLIFlags{profile: url, trustRemote: true, workdir: t.TempDir()}, nil); err != nil {
		t.Fatalf("runSandbox: %v", err)
	}
	if iso.cfg.Env.Set["V"] != "value" {
		t.Errorf("V = %q, want host expansion under --trust-remote", iso.cfg.Env.Set["V"])
	}
}

// A profile with validation errors is refused before the consent prompt.
func TestRunSandbox_remoteProfile_validatedBeforeConsent(t *testing.T) {
	url := serveProfile(t, `schema_version = "1"
[sandbox]
allow = ["no-such-key"]
[entrypoint]
cmd = "/bin/sh"
`)
	app, _ := newRunTestApp(t)
	var buf bytes.Buffer
	err := app.runSandbox(&buf, runCLIFlags{profile: url}, nil)
	if err == nil {
		t.Fatal("expected the invalid profile to be refused")
	}
	if strings.Contains(buf.String(), "run this downloaded profile?") || strings.Contains(err.Error(), "without consent") {
		t.Errorf("the user was asked to consent to an invalid profile:\n%s\nerr: %v", buf.String(), err)
	}
}

func TestRelocatedSensitivePath(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		name string
		m    config.Mount
		hide bool
	}{
		{"ssh relocated", config.Mount{Src: home + "/.ssh", Dest: "/tmp/k", Mode: "ro"}, true},
		{"file inside ssh relocated", config.Mount{Src: home + "/.ssh/id_ed25519", Dest: "/tmp/k", Mode: "ro"}, true},
		{"root relocated", config.Mount{Src: "/", Dest: "/tmp/root", Mode: "ro"}, true},
		{"home relocated", config.Mount{Src: home, Dest: "/tmp/h", Mode: "ro"}, true},
		{"safe-rw relocation counts too", config.Mount{Src: home + "/.aws", Dest: "/tmp/a", Mode: "safe-rw"}, true},
		{"ssh in place (hide rule applies)", config.Mount{Src: home + "/.ssh", Dest: home + "/.ssh", Mode: "ro"}, false},
		{"unrelated relocation", config.Mount{Src: "/opt/tools", Dest: "/tmp/tools", Mode: "ro"}, false},
		{"tmpfs", config.Mount{Src: home, Dest: "/tmp/x", Mode: "tmpfs"}, false},
	}
	for _, tc := range cases {
		if got := relocatedSensitivePath(tc.m) != ""; got != tc.hide {
			t.Errorf("%s: relocatedSensitivePath = %v, want %v", tc.name, got, tc.hide)
		}
	}
}

// A symlink cannot disguise a relocation: ~/innocent -> ~/.ssh mounted at
// ~/innocent puts ~/.ssh at a path the hide rule does not cover.
func TestRelocatedSensitivePath_symlinkedSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "innocent")
	if err := os.Symlink(filepath.Join(home, ".ssh"), link); err != nil {
		t.Fatal(err)
	}
	if relocatedSensitivePath(config.Mount{Src: link, Dest: link, Mode: "ro"}) == "" {
		t.Error("a symlink to ~/.ssh mounted at its own path was not detected")
	}
}

func TestRemoteProfileRequests_listsEverythingTheProfileStillGets(t *testing.T) {
	rc := &config.RunConfig{
		HomeMode:  config.HomeIsolated,
		HomeAllow: []string{"/home/u/.local/bin"},
		Env: config.EnvConfig{
			Set:         map[string]string{"EDITOR": "vi"},
			PathPrepend: []string{"/opt/jdk/bin"},
		},
		Mounts: []config.Mount{
			{Src: "/data", Dest: "/data", Mode: "ro"},
			{Src: "/run/user/1000", Dest: "/run/user/1000", Mode: "tmpfs"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	}
	got := strings.Join(remoteProfileRequests(rc), "\n")
	for _, want := range []string{
		"home_allow: /home/u/.local/bin",
		"env set: EDITOR=vi",
		"PATH prepend: /opt/jdk/bin",
		"mount: /data -> /data (ro)",
		"mount: empty tmpfs at /run/user/1000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary misses %q:\n%s", want, got)
		}
	}
}

func TestPrintable_escapesControlCharacters(t *testing.T) {
	in := "ok\x1b[2K\rfake line\x07\u009b"
	got := printable(in)
	if strings.ContainsAny(got, "\x1b\r\x07\u009b") {
		t.Errorf("printable(%q) = %q still carries control characters", in, got)
	}
	if !strings.Contains(got, `\x1b`) || !strings.HasPrefix(got, "ok") {
		t.Errorf("printable(%q) = %q", in, got)
	}
	if printable("tab\tand ünïcode") != "tab\tand ünïcode" {
		t.Error("printable must leave tabs and printable unicode alone")
	}
}

func TestLooksLikeSecretEnvName(t *testing.T) {
	for name, want := range map[string]bool{
		"GITHUB_TOKEN": true, "GH_PAT": true, "SENTRY_DSN": true, "DATABASE_URL": true,
		"DB_PASS": true, "SSH_PRIVATE_KEY": true,
		"PATH": false, "TERM": false, "LANG": false, "PWD": false, "PASSENGER_HOME": false,
		"JAVA_HOME": false, "PATH_PREFIX": false,
	} {
		if got := looksLikeSecretEnvName(name); got != want {
			t.Errorf("looksLikeSecretEnvName(%q) = %v, want %v", name, got, want)
		}
	}
}

// remoteFieldPolicy classifies every field of a profile for the remote-profile
// trust boundary. A field is trusted by default the moment it exists, so this
// test makes adding one a decision: it fails until the new field is listed
// here with how a downloaded profile is kept from abusing it.
//
//	hardened  — stripped or neutralised (BuildUntrusted / hardenRemoteProfile)
//	shown     — kept, and listed in the consent prompt
//	inert     — cannot reach the host or a secret (sandbox-internal behaviour,
//	            metadata, or only read by `inner verify`, which takes no URL)
var remoteFieldPolicy = map[string]string{
	"schema_version": "inert", "name": "inert", "description": "inert",
	"extends":         "inert", // local profiles only; hardening runs on the merged result
	"workspaces_path": "hardened",
	"experimental":    "inert",
	"capabilities":    "shown",

	"sandbox.network": "shown", "sandbox.network_mode": "shown",
	"sandbox.network_allow": "shown", "sandbox.network_deny": "shown",
	"sandbox.clipboard":      "hardened",
	"sandbox.pid_namespace":  "hardened",
	"sandbox.allow":          "hardened",
	"sandbox.home":           "shown",
	"sandbox.home_allow":     "shown", // bound at their own path: the hide rules still apply
	"sandbox.git_dir":        "hardened",
	"sandbox.cgroup_manager": "inert", // only read with nested-user-ns, which is hardened
	"sandbox.limits.memory":  "inert", "sandbox.limits.cpu": "inert", "sandbox.limits.pids": "inert",

	"mounts.dest": "hardened", "mounts.mode": "shown", // relocated secrets dropped, the rest listed

	"env.clearenv":     "inert",
	"env.inherit_all":  "hardened",
	"env.inherit":      "hardened",
	"env.set":          "hardened", // no host $VAR expansion; listed too
	"env.path_prepend": "hardened",

	"git.strip_sections": "inert", "git.overrides": "inert", // shape the sandbox's gitconfig copy

	"entrypoint.cmd": "shown", "entrypoint.args": "shown",
	"entrypoint.interactive": "inert", "entrypoint.tui": "inert",
	"entrypoint.cursor_fix": "inert", "entrypoint.history": "inert",
	"entrypoint.workdir": "hardened",

	"output.summary": "inert", "output.timeout_seconds": "inert",
	"output.log": "hardened",

	"noop.block": "shown", "noop.rewrite": "shown",

	"verify.custom.checks.name": "inert", "verify.custom.checks.cmd": "inert",
	"verify.custom.checks.severity": "inert",
}

func TestRemoteFieldPolicy_coversEveryProfileField(t *testing.T) {
	var fields []string
	var walk func(typ reflect.Type, prefix string)
	walk = func(typ reflect.Type, prefix string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			fields = append(fields, prefix)
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("toml"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			name := tag
			if prefix != "" {
				name = prefix + "." + tag
			}
			walk(f.Type, name)
		}
	}
	walk(reflect.TypeOf(config.Profile{}), "")

	for _, f := range fields {
		if _, ok := remoteFieldPolicy[f]; !ok {
			t.Errorf("profile field %q has no remote-profile policy: decide whether a downloaded profile may set it (see remoteFieldPolicy)", f)
		}
	}
	for f := range remoteFieldPolicy {
		if !slices.Contains(fields, f) {
			t.Errorf("remoteFieldPolicy lists %q, which is not a profile field any more", f)
		}
	}
}
