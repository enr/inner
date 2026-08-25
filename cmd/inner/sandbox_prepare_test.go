package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestPrepareSandbox_shimDirIsCreatedAndRemoved(t *testing.T) {
	rc := &config.RunConfig{Noop: config.NoopConfig{Block: []string{"rm"}}}

	cleanup, err := prepareSandbox(rc, runSandboxOptions())
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	if rc.ShimDir == "" {
		t.Fatal("ShimDir was not recorded on the RunConfig")
	}
	if _, err := os.Stat(rc.ShimDir); err != nil {
		t.Fatalf("shim dir was not created: %v", err)
	}

	cleanup()
	if _, err := os.Stat(rc.ShimDir); !os.IsNotExist(err) {
		t.Errorf("shim dir survived cleanup: %v", err)
	}
}

func TestPrepareSandbox_noNoopConfigMeansNoShimDir(t *testing.T) {
	rc := &config.RunConfig{}
	cleanup, err := prepareSandbox(rc, runSandboxOptions())
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	defer cleanup()
	if rc.ShimDir != "" {
		t.Errorf("ShimDir = %q, want empty when the profile declares no shims", rc.ShimDir)
	}
}

// A failure halfway through must not leave the earlier steps' temp directories
// behind: a run that never started should cost nothing.
func TestPrepareSandbox_rollsBackEarlierStepsOnFailure(t *testing.T) {
	rc := &config.RunConfig{
		Noop: config.NoopConfig{Block: []string{"rm"}},
		Mounts: []config.Mount{
			{Src: filepath.Join(t.TempDir(), "does-not-exist"), Dest: "/x", Mode: "safe-rw"},
		},
	}

	cleanup, err := prepareSandbox(rc, runSandboxOptions())
	if err == nil {
		cleanup()
		t.Fatal("expected prepareSandbox to fail on an unreadable safe-rw source")
	}
	if cleanup != nil {
		t.Error("a failed prepareSandbox must return a nil cleanup, not one the caller might defer twice")
	}
	if rc.ShimDir == "" {
		t.Skip("shim step did not run; nothing to assert about rollback")
	}
	if _, statErr := os.Stat(rc.ShimDir); !os.IsNotExist(statErr) {
		t.Errorf("shim dir leaked after a failed prepare: %v", statErr)
	}
}

// The substantive difference between the two commands, pinned so flipping it
// is a deliberate act. Capability handlers are not pure mount injection: the
// claude one can launch a keyring unlock dialog and wait for the user. A
// read-only conformance check must not do that.
func TestVerifySandboxOptions_doesNotRunCapabilityHandlers(t *testing.T) {
	rc := &config.RunConfig{Capabilities: []string{"claude"}}

	cleanup, err := prepareSandbox(rc, verifySandboxOptions())
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	defer cleanup()

	if len(rc.Mounts) != 0 {
		t.Errorf("verify injected capability mounts %v: capability handlers have side effects "+
			"(credential unlock prompts) that a conformance check must not trigger", rc.Mounts)
	}
}

// Conversely, `inner run` must keep asking for everything: the options exist to
// record a difference, not to let one drift in unnoticed.
func TestRunSandboxOptions_asksForEveryStep(t *testing.T) {
	opts := runSandboxOptions()
	if !opts.GitConfig || !opts.Capabilities || !opts.SafeMounts {
		t.Errorf("runSandboxOptions() = %+v, want every step enabled", opts)
	}
}

// The gitconfig is sanitized into a temp file that must not outlive the run.
func TestPrepareSandbox_gitConfigIsSanitizedAndRemoved(t *testing.T) {
	rc := &config.RunConfig{Git: &config.GitConfig{StripSections: []string{"credential"}}}

	cleanup, err := prepareSandbox(rc, runSandboxOptions())
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	if rc.GitConfigPath == "" {
		t.Fatal("GitConfigPath was not recorded on the RunConfig")
	}
	path := rc.GitConfigPath

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("sanitized gitconfig survived cleanup: %v", err)
	}
}

func TestPrepareSandbox_verifySkipsGitConfig(t *testing.T) {
	rc := &config.RunConfig{Git: &config.GitConfig{StripSections: []string{"credential"}}}

	cleanup, err := prepareSandbox(rc, verifySandboxOptions())
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	defer cleanup()

	if rc.GitConfigPath != "" {
		t.Errorf("GitConfigPath = %q, want empty: verify opts out of gitconfig sanitization", rc.GitConfigPath)
	}
}
