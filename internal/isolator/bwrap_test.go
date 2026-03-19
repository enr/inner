package isolator

import (
	"os"
	"slices"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// testIsolator creates a BwrapIsolator without requiring bwrap on the host.
func testIsolator(info runtime.RuntimeInfo) *BwrapIsolator {
	return newBwrapIsolatorWithInfo("/fake/bwrap", info)
}

// cmdArgs returns the bwrap arguments (everything after the bwrap binary path).
func cmdArgs(t *testing.T, iso *BwrapIsolator, cfg config.RunConfig) []string {
	t.Helper()
	cmd, err := iso.Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// cmd.Args[0] is the bwrap binary path; the rest are the arguments.
	return cmd.Args[1:]
}

// hasFlag reports whether flag appears in args.
func hasFlag(args []string, flag string) bool {
	return slices.Contains(args, flag)
}

// hasSeq reports whether the subsequence needle appears in args.
func hasSeq(args []string, needle ...string) bool {
	for i := 0; i <= len(args)-len(needle); i++ {
		if slices.Equal(args[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// separatorIndex returns the index of "--" in args, or -1.
func separatorIndex(args []string) int {
	return slices.Index(args, "--")
}

// ── Base flags ────────────────────────────────────────────────────────────────

func TestBuild_baseFlags(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	for _, seq := range [][]string{
		{"--ro-bind", "/", "/"},
		{"--proc", "/proc"},
		{"--dev", "/dev"},
		{"--tmpfs", "/tmp"},
		{"--unshare-pid"},
		{"--die-with-parent"},
	} {
		if !hasSeq(args, seq...) {
			t.Errorf("expected %v in args %v", seq, args)
		}
	}
}

func TestBuild_separatorPresent(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "bash"},
	})
	if separatorIndex(args) == -1 {
		t.Errorf("expected -- separator in args %v", args)
	}
}

// ── Network ───────────────────────────────────────────────────────────────────

func TestBuild_networkDisabled(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Network:    false,
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasFlag(args, "--unshare-net") {
		t.Errorf("expected --unshare-net when Network=false, got %v", args)
	}
}

func TestBuild_networkEnabled(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Network:    true,
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasFlag(args, "--unshare-net") {
		t.Errorf("unexpected --unshare-net when Network=true, got %v", args)
	}
}

// ── Mounts ────────────────────────────────────────────────────────────────────

func TestBuild_mountRW(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Mounts: []config.Mount{
			{Src: "/host/work", Dest: "/workspace", Mode: "rw"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--bind", "/host/work", "/workspace") {
		t.Errorf("expected --bind for rw mount, got %v", args)
	}
}

func TestBuild_mountRO(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Mounts: []config.Mount{
			{Src: "/etc/hosts", Dest: "/etc/hosts", Mode: "ro"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--ro-bind", "/etc/hosts", "/etc/hosts") {
		t.Errorf("expected --ro-bind for ro mount, got %v", args)
	}
}

func TestBuild_multipleMounts(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Mounts: []config.Mount{
			{Src: "/a", Dest: "/da", Mode: "ro"},
			{Src: "/b", Dest: "/db", Mode: "rw"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--ro-bind", "/a", "/da") {
		t.Errorf("ro-bind /a missing in %v", args)
	}
	if !hasSeq(args, "--bind", "/b", "/db") {
		t.Errorf("bind /b missing in %v", args)
	}
}

// ── Environment ───────────────────────────────────────────────────────────────

func TestBuild_clearenv(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env:        config.EnvConfig{Clear: true},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasFlag(args, "--clearenv") {
		t.Errorf("expected --clearenv, got %v", args)
	}
}

func TestBuild_noClearenv(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env:        config.EnvConfig{Clear: false},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasFlag(args, "--clearenv") {
		t.Errorf("unexpected --clearenv when Clear=false, got %v", args)
	}
}

func TestBuild_inheritEnv(t *testing.T) {
	t.Setenv("INNER_TEST_KEY", "hello")
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			Clear:   true,
			Inherit: []string{"INNER_TEST_KEY"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--setenv", "INNER_TEST_KEY", "hello") {
		t.Errorf("expected --setenv INNER_TEST_KEY hello, got %v", args)
	}
}

func TestBuild_setEnv(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			Set: map[string]string{"MY_VAR": "my_value"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--setenv", "MY_VAR", "my_value") {
		t.Errorf("expected --setenv MY_VAR my_value, got %v", args)
	}
}

// ── Clipboard / display ───────────────────────────────────────────────────────

func TestBuild_clipboardWayland(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	iso := testIsolator(runtime.RuntimeInfo{
		Display:       runtime.DisplayWayland,
		WaylandSocket: "/run/user/1000/wayland-0",
	})
	args := cmdArgs(t, iso, config.RunConfig{
		Clipboard:  true,
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if !hasSeq(args, "--ro-bind", "/run/user/1000/wayland-0", "/run/user/1000/wayland-0") {
		t.Errorf("expected Wayland socket bind, got %v", args)
	}
	if !hasSeq(args, "--setenv", "WAYLAND_DISPLAY", "wayland-0") {
		t.Errorf("expected WAYLAND_DISPLAY setenv, got %v", args)
	}
}

func TestBuild_clipboardX11(t *testing.T) {
	t.Setenv("DISPLAY", ":0")

	iso := testIsolator(runtime.RuntimeInfo{
		Display:    runtime.DisplayX11,
		X11Display: ":0",
	})
	args := cmdArgs(t, iso, config.RunConfig{
		Clipboard:  true,
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if !hasSeq(args, "--ro-bind", "/tmp/.X11-unix", "/tmp/.X11-unix") {
		t.Errorf("expected X11 socket bind, got %v", args)
	}
	if !hasSeq(args, "--setenv", "DISPLAY", ":0") {
		t.Errorf("expected DISPLAY setenv, got %v", args)
	}
}

func TestBuild_clipboardFalse_noDisplayArgs(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{
		Display:       runtime.DisplayWayland,
		WaylandSocket: "/run/user/1000/wayland-0",
	})
	args := cmdArgs(t, iso, config.RunConfig{
		Clipboard:  false,
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if hasFlag(args, "WAYLAND_DISPLAY") || hasSeq(args, "--ro-bind", "/run/user/1000/wayland-0", "/run/user/1000/wayland-0") {
		t.Errorf("expected no display args when Clipboard=false, got %v", args)
	}
}

// ── Git config ────────────────────────────────────────────────────────────────

func TestBuild_gitConfigPath(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		GitConfigPath: "/tmp/inner-gitconfig-abc",
		Entrypoint:    config.Entrypoint{Cmd: "sh"},
	})

	if !hasSeq(args, "--ro-bind", "/tmp/inner-gitconfig-abc", "/tmp/inner-gitconfig-abc") {
		t.Errorf("expected gitconfig ro-bind, got %v", args)
	}
	if !hasSeq(args, "--setenv", "GIT_CONFIG_GLOBAL", "/tmp/inner-gitconfig-abc") {
		t.Errorf("expected GIT_CONFIG_GLOBAL setenv, got %v", args)
	}
}

func TestBuild_noGitConfigPath(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--setenv", "GIT_CONFIG_GLOBAL") {
		t.Errorf("unexpected GIT_CONFIG_GLOBAL when GitConfigPath is empty, got %v", args)
	}
}

// ── Entrypoint ────────────────────────────────────────────────────────────────

func TestBuild_entrypointCmd(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{
			Cmd:  "claude",
			Args: []string{"--help"},
		},
	})

	sep := separatorIndex(args)
	if sep == -1 {
		t.Fatalf("-- separator not found in %v", args)
	}
	afterSep := args[sep+1:]
	if len(afterSep) == 0 || afterSep[0] != "claude" {
		t.Errorf("expected entrypoint cmd after --, got %v", afterSep)
	}
	if len(afterSep) < 2 || afterSep[1] != "--help" {
		t.Errorf("expected entrypoint args after cmd, got %v", afterSep)
	}
}

func TestBuild_entrypointEmptyCmd_fallbackToSHELL(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: ""},
	})

	sep := separatorIndex(args)
	if sep == -1 {
		t.Fatalf("-- separator not found in %v", args)
	}
	afterSep := args[sep+1:]
	if len(afterSep) == 0 || afterSep[0] != "/bin/zsh" {
		t.Errorf("expected /bin/zsh as fallback entrypoint, got %v", afterSep)
	}
}

func TestBuild_entrypointEmptyCmd_fallbackToSh(t *testing.T) {
	os.Unsetenv("SHELL")
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: ""},
	})

	sep := separatorIndex(args)
	if sep == -1 {
		t.Fatalf("-- separator not found in %v", args)
	}
	afterSep := args[sep+1:]
	if len(afterSep) == 0 || afterSep[0] != "/bin/sh" {
		t.Errorf("expected /bin/sh as final fallback, got %v", afterSep)
	}
}

// ── Available ─────────────────────────────────────────────────────────────────

func TestAvailable_true(t *testing.T) {
	iso := newBwrapIsolatorWithInfo("/usr/bin/bwrap", runtime.RuntimeInfo{
		BwrapAvailable: true,
		BwrapPath:      "/usr/bin/bwrap",
		BwrapVersion:   "0.8.0",
	})
	ok, msg := iso.Available()
	if !ok {
		t.Errorf("expected Available=true, got false (%s)", msg)
	}
	if msg == "" {
		t.Error("expected non-empty diagnostic message")
	}
}

func TestAvailable_false(t *testing.T) {
	iso := newBwrapIsolatorWithInfo("", runtime.RuntimeInfo{BwrapAvailable: false})
	ok, msg := iso.Available()
	if ok {
		t.Errorf("expected Available=false, got true (%s)", msg)
	}
	if msg == "" {
		t.Error("expected non-empty diagnostic message")
	}
}
