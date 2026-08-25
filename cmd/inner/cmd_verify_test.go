package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestLookupBoolEnv(t *testing.T) {
	if v, ok := lookupBoolEnv("INNER_VERIFY_TEST_BOOL"); ok || v {
		t.Errorf("unset variable = (%v, %v), want (false, false)", v, ok)
	}
	t.Setenv("INNER_VERIFY_TEST_BOOL", "1")
	if v, ok := lookupBoolEnv("INNER_VERIFY_TEST_BOOL"); !ok || !v {
		t.Errorf(`"1" = (%v, %v), want (true, true)`, v, ok)
	}
	t.Setenv("INNER_VERIFY_TEST_BOOL", "0")
	if v, ok := lookupBoolEnv("INNER_VERIFY_TEST_BOOL"); !ok || v {
		t.Errorf(`"0" = (%v, %v), want (false, true)`, v, ok)
	}
}

// An empty list variable means "the profile has no allow keys", not "the host
// side said nothing" — the difference decides whether the profile is re-read.
func TestLookupListEnv(t *testing.T) {
	if v, ok := lookupListEnv("INNER_VERIFY_TEST_LIST"); ok || v != nil {
		t.Errorf("unset variable = (%v, %v), want (nil, false)", v, ok)
	}
	t.Setenv("INNER_VERIFY_TEST_LIST", "")
	if v, ok := lookupListEnv("INNER_VERIFY_TEST_LIST"); !ok || v != nil {
		t.Errorf("empty variable = (%v, %v), want (nil, true)", v, ok)
	}
	t.Setenv("INNER_VERIFY_TEST_LIST", "ssh-keys,npmrc")
	v, ok := lookupListEnv("INNER_VERIFY_TEST_LIST")
	if !ok || len(v) != 2 || v[0] != "ssh-keys" || v[1] != "npmrc" {
		t.Errorf("list = (%v, %v), want ([ssh-keys npmrc], true)", v, ok)
	}
}

func TestLookupCustomChecksEnv(t *testing.T) {
	if v, ok := lookupCustomChecksEnv("INNER_VERIFY_TEST_CUSTOM"); ok || v != nil {
		t.Errorf("unset variable = (%v, %v), want (nil, false)", v, ok)
	}
	t.Setenv("INNER_VERIFY_TEST_CUSTOM", `[{"name":"probe","cmd":"true","severity":"high"}]`)
	v, ok := lookupCustomChecksEnv("INNER_VERIFY_TEST_CUSTOM")
	if !ok || len(v) != 1 || v[0].Name != "probe" || v[0].Cmd != "true" {
		t.Fatalf("decoded = (%v, %v), want one check named probe", v, ok)
	}
	// Malformed JSON must read as "absent" so the profile fallback still runs.
	t.Setenv("INNER_VERIFY_TEST_CUSTOM", "{not json")
	if v, ok := lookupCustomChecksEnv("INNER_VERIFY_TEST_CUSTOM"); ok || v != nil {
		t.Errorf("malformed JSON = (%v, %v), want (nil, false)", v, ok)
	}
}

// The regression this guards: under home = "isolated" the profile file lives in
// the hidden home and `inner verify` sets no workdir, so nothing binds it back.
// Re-reading the profile from inside the sandbox then failed silently and the
// checks ran with a default context — network=false in particular, which made
// every agent profile report a false "network restricted" failure.
func TestRunVerifyInside_usesEnvContextWhenProfileIsUnreadable(t *testing.T) {
	app, _ := newRunTestApp(t)

	t.Setenv("INNER_VERIFY_PROFILE", "/nonexistent/profile.toml")
	t.Setenv("INNER_VERIFY_HOME_MODE", config.HomeIsolated)
	t.Setenv("INNER_VERIFY_NETWORK", "1")
	t.Setenv("INNER_VERIFY_SHIMS", "0")
	t.Setenv("INNER_VERIFY_ALLOW", "")
	t.Setenv("INNER_VERIFY_CUSTOM", "")

	var buf bytes.Buffer
	// The report is expected to be non-conformant here (the test process is not
	// in a sandbox, so home-isolated fails); only the network line matters.
	_ = app.runVerifyInside(&buf, false)

	out := buf.String()
	if !strings.Contains(out, "network=true in profile") {
		t.Errorf("network check did not see network=true from the environment:\n%s", out)
	}
	if strings.Contains(out, "TCP connection to") {
		t.Errorf("network check dialled out despite network=true:\n%s", out)
	}
}

// Custom checks travel through the environment too: a profile that is
// unreadable from inside must not silently lose its [verify.custom] checks.
func TestRunVerifyInside_customChecksFromEnv(t *testing.T) {
	app, _ := newRunTestApp(t)

	t.Setenv("INNER_VERIFY_PROFILE", "/nonexistent/profile.toml")
	t.Setenv("INNER_VERIFY_HOME_MODE", config.HomeHostRO)
	t.Setenv("INNER_VERIFY_NETWORK", "1")
	t.Setenv("INNER_VERIFY_SHIMS", "0")
	t.Setenv("INNER_VERIFY_ALLOW", "")
	t.Setenv("INNER_VERIFY_CUSTOM", `[{"name":"always-fails","cmd":"false","severity":"medium"}]`)

	var buf bytes.Buffer
	err := app.runVerifyInside(&buf, false)
	if err == nil {
		t.Errorf("expected a non-zero exit: the custom check fails\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "always-fails") {
		t.Errorf("custom check from the environment was not run:\n%s", buf.String())
	}
}
