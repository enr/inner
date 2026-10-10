package main

import (
	"os"
	"slices"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/landlock"
	"github.com/enr/inner/internal/runtime"
)

func withLandlockABI(t *testing.T, abi int) {
	t.Helper()
	prev := landlockABI
	landlockABI = func() int { return abi }
	t.Cleanup(func() { landlockABI = prev })
}

func TestAbstractScopeWanted(t *testing.T) {
	full := func() *config.RunConfig { return &config.RunConfig{NetworkMode: config.NetworkFull} }

	withLandlockABI(t, landlock.ScopeABI)
	if !abstractScopeWanted(full()) {
		t.Error("network_mode = full on a supporting kernel must be scoped")
	}
	for _, mode := range []string{config.NetworkOff, config.NetworkAllowlist} {
		if abstractScopeWanted(&config.RunConfig{NetworkMode: mode}) {
			t.Errorf("network_mode = %s runs in a private network namespace: no scope needed", mode)
		}
	}
	rc := full()
	rc.Allow = []string{"abstract-sockets"}
	if abstractScopeWanted(rc) {
		t.Error(`allow = ["abstract-sockets"] must lift the scope`)
	}
	rc = full()
	rc.KeepHostAbstractSockets = true
	if abstractScopeWanted(rc) {
		t.Error("a step that needs a host abstract socket must lift the scope")
	}

	rc = full()
	rc.Clipboard = true
	prevDisplay := hostDisplay
	t.Cleanup(func() { hostDisplay = prevDisplay })
	hostDisplay = func() runtime.DisplayServer { return runtime.DisplayX11 }
	if abstractScopeWanted(rc) {
		t.Error("clipboard on X11 already hands over the X server: the scope would only break the X clients")
	}
	hostDisplay = func() runtime.DisplayServer { return runtime.DisplayWayland }
	if !abstractScopeWanted(rc) {
		t.Error("clipboard on Wayland uses a filesystem socket: the scope must stay")
	}

	withLandlockABI(t, landlock.ScopeABI-1)
	if abstractScopeWanted(full()) {
		t.Error("a kernel without the scope must not get the wrapper")
	}
}

func TestApplyAbstractScope_wrapsEntrypoint(t *testing.T) {
	withLandlockABI(t, landlock.ScopeABI)
	rc := &config.RunConfig{
		NetworkMode: config.NetworkFull,
		Entrypoint:  config.Entrypoint{Cmd: "claude", Args: []string{"--verbose"}},
	}
	if err := applyAbstractScope(rc); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if rc.Entrypoint.Cmd != self {
		t.Errorf("Cmd = %q, want the inner binary %q", rc.Entrypoint.Cmd, self)
	}
	if want := []string{"__scope", "--", "claude", "--verbose"}; !slices.Equal(rc.Entrypoint.Args, want) {
		t.Errorf("Args = %q, want %q", rc.Entrypoint.Args, want)
	}
}

func TestApplyAbstractScope_emptyCmdFallsBackToShell(t *testing.T) {
	withLandlockABI(t, landlock.ScopeABI)
	t.Setenv("SHELL", "/bin/zsh")
	rc := &config.RunConfig{NetworkMode: config.NetworkFull}
	if err := applyAbstractScope(rc); err != nil {
		t.Fatal(err)
	}
	if want := []string{"__scope", "--", "/bin/zsh"}; !slices.Equal(rc.Entrypoint.Args, want) {
		t.Errorf("Args = %q, want %q", rc.Entrypoint.Args, want)
	}
}

func TestApplyAbstractScope_noopWhenNotWanted(t *testing.T) {
	withLandlockABI(t, landlock.ScopeABI)
	rc := &config.RunConfig{NetworkMode: config.NetworkOff, Entrypoint: config.Entrypoint{Cmd: "sh"}}
	if err := applyAbstractScope(rc); err != nil {
		t.Fatal(err)
	}
	if rc.Entrypoint.Cmd != "sh" || len(rc.Entrypoint.Args) != 0 {
		t.Errorf("entrypoint changed: %+v", rc.Entrypoint)
	}
}

func TestPassSessionBusUnfiltered_abstractKeepsHostSockets(t *testing.T) {
	rc := &config.RunConfig{}
	passSessionBusUnfiltered(rc, "unix:abstract=/tmp/dbus-XXXX,guid=abc")
	if !rc.KeepHostAbstractSockets {
		t.Error("an abstract session bus must lift the abstract socket scope")
	}
}
