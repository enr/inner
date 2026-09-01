package isolator

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/netproxy"
	"github.com/enr/inner/internal/runtime"
)

// testIsolator creates a BwrapIsolator without requiring bwrap on the host.
func testIsolator(info runtime.RuntimeInfo) *BwrapIsolator {
	iso := newBwrapIsolatorWithInfo("/fake/bwrap", info)
	// Pin the pty-multiplexer probe so the emitted args do not depend on how
	// the host mounted devpts. Both branches are covered by their own tests.
	iso.ptmxUsableFn = func() bool { return true }
	return iso
}

// testIsolatorAllExist creates a BwrapIsolator where every path is reported as existing.
// Used to test sensitive resource hiding without needing real filesystem paths.
func testIsolatorAllExist(info runtime.RuntimeInfo) *BwrapIsolator {
	iso := newBwrapIsolatorWithInfo("/fake/bwrap", info)
	iso.ptmxUsableFn = func() bool { return true }
	iso.statFn = func(string) (os.FileInfo, error) { return nil, nil }
	// Return the path unchanged so tests are deterministic regardless of the
	// host filesystem layout (e.g. whether /var/run is a symlink to /run).
	iso.evalSymlinksFn = func(p string) (string, error) { return p, nil }
	return iso
}

// testIsolatorNoneExist creates a BwrapIsolator where no path exists.
func testIsolatorNoneExist(info runtime.RuntimeInfo) *BwrapIsolator {
	iso := newBwrapIsolatorWithInfo("/fake/bwrap", info)
	iso.ptmxUsableFn = func() bool { return true }
	iso.statFn = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	return iso
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

// lastIndexOfSeq returns the index of the last occurrence of the subsequence
// needle in args, or -1 if absent.
func lastIndexOfSeq(args []string, needle ...string) int {
	for i := len(args) - len(needle); i >= 0; i-- {
		if slices.Equal(args[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
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

// indexSeq returns the starting index of the first occurrence of needle in
// args, or -1 if not found.
func indexSeq(args []string, needle ...string) int {
	for i := 0; i <= len(args)-len(needle); i++ {
		if slices.Equal(args[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
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
		{"--bind", "/dev/pts", "/dev/pts"},
		{"--tmpfs", "/tmp"},
		{"--die-with-parent"},
	} {
		if !hasSeq(args, seq...) {
			t.Errorf("expected %v in args %v", seq, args)
		}
	}
}

// The host's /dev/ptmx must never be bind-mounted: inside the sandbox that
// path is a symlink to pts/ptmx, which bwrap >= 0.11 refuses to mount over
// ("Can't mount on symlink destination /dev/ptmx").
func TestBuild_neverBindsDevPtmx(t *testing.T) {
	for _, usable := range []bool{true, false} {
		iso := testIsolator(runtime.RuntimeInfo{})
		iso.ptmxUsableFn = func() bool { return usable }

		args := cmdArgs(t, iso, config.RunConfig{
			Entrypoint: config.Entrypoint{Cmd: "sh"},
		})

		if i := indexSeq(args, "/dev/ptmx"); i != -1 {
			t.Errorf("ptmxUsable=%v: unexpected /dev/ptmx mount in args %v", usable, args)
		}
	}
}

// Where devpts is mounted with ptmxmode=000 the host's multiplexer cannot be
// opened, so binding the host's /dev/pts would break forkpty(3). bwrap's own
// devpts instance is kept instead.
func TestBuild_hostPtsNotBound_whenPtmxUnusable(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	iso.ptmxUsableFn = func() bool { return false }

	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if hasSeq(args, "--bind", "/dev/pts", "/dev/pts") {
		t.Errorf("unexpected host /dev/pts bind when ptmx is not usable, got %v", args)
	}
	if !hasSeq(args, "--dev", "/dev") {
		t.Errorf("expected --dev /dev in args %v", args)
	}
}

func TestBuild_tmpfsSlashTmp_comesAfter_rootBind(t *testing.T) {
	// Safety invariant: --tmpfs /tmp must be emitted AFTER --ro-bind / / so the
	// tmpfs shadows the host's /tmp. If the order were reversed, host /tmp would
	// be visible inside the sandbox.
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	rootIdx := indexSeq(args, "--ro-bind", "/", "/")
	tmpfsIdx := indexSeq(args, "--tmpfs", "/tmp")
	if rootIdx == -1 {
		t.Fatal("--ro-bind / / not found in args")
	}
	if tmpfsIdx == -1 {
		t.Fatal("--tmpfs /tmp not found in args")
	}
	if tmpfsIdx <= rootIdx {
		t.Errorf("--tmpfs /tmp (idx %d) must come after --ro-bind / / (idx %d) or host /tmp leaks into sandbox", tmpfsIdx, rootIdx)
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

// PID isolation is now driven by RunConfig.PidNamespace, NOT by Interactive.
// An interactive run with PidNamespace=true MUST still get --unshare-pid:
// verified that --unshare-pid alone keeps the controlling terminal on
// bubblewrap >= 0.9, so TUI apps are unaffected. The TTY-breaking flag is
// --new-session, which inner never emits.
func TestBuild_pidUnshare_interactive(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		PidNamespace: true,
		Entrypoint:   config.Entrypoint{Cmd: "claude", Interactive: true},
	})
	if !hasFlag(args, "--unshare-pid") {
		t.Errorf("expected --unshare-pid for interactive run with PidNamespace=true, got %v", args)
	}
}

func TestBuild_pidUnshare_nonInteractive(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		PidNamespace: true,
		Entrypoint:   config.Entrypoint{Cmd: "claude", Interactive: false},
	})
	if !hasFlag(args, "--unshare-pid") {
		t.Errorf("expected --unshare-pid for non-interactive runs, got %v", args)
	}
}

// The pid_namespace = false escape hatch must drop --unshare-pid.
func TestBuild_pidUnshare_disabled(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		PidNamespace: false,
		Entrypoint:   config.Entrypoint{Cmd: "claude", Interactive: true},
	})
	if hasFlag(args, "--unshare-pid") {
		t.Errorf("--unshare-pid must be absent when PidNamespace=false, got %v", args)
	}
}

// Guard against the regression that breaks TUI apps: inner must NEVER emit
// --new-session (it calls setsid(), detaching the controlling terminal →
// ENXIO on open("/dev/tty")).
func TestBuild_neverEmitsNewSession(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	for _, interactive := range []bool{true, false} {
		args := cmdArgs(t, iso, config.RunConfig{
			PidNamespace: true,
			Entrypoint:   config.Entrypoint{Cmd: "claude", Interactive: interactive},
		})
		if hasFlag(args, "--new-session") {
			t.Errorf("--new-session must never be emitted (breaks TUI controlling terminal); interactive=%v args=%v", interactive, args)
		}
	}
}

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

// Only NetworkFull keeps the host network namespace. Everything else — "off",
// the reserved "allowlist", and any value this build does not recognise — must
// get --unshare-net: a mode this binary cannot enforce may never silently mean
// "open network".
func TestBuild_networkMode_failsClosed(t *testing.T) {
	tests := []struct {
		mode        string
		legacyBool  bool
		wantUnshare bool
	}{
		{config.NetworkFull, true, false},
		{config.NetworkOff, false, true},
		// The legacy bool is left true on purpose: the mode must win.
		{config.NetworkAllowlist, true, true},
		{"bogus", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			iso := testIsolator(runtime.RuntimeInfo{})
			args := cmdArgs(t, iso, config.RunConfig{
				NetworkMode: tt.mode,
				Network:     tt.legacyBool,
				Entrypoint:  config.Entrypoint{Cmd: "sh"},
			})
			if got := hasFlag(args, "--unshare-net"); got != tt.wantUnshare {
				t.Errorf("mode %q: --unshare-net = %v, want %v", tt.mode, got, tt.wantUnshare)
			}
		})
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

func TestBuild_workspaceMountsAfterWorkdirBind(t *testing.T) {
	// Workspace mounts must be emitted AFTER any parent-directory bind (e.g. a
	// workdir --bind ~/Projects ~/Projects) to ensure they shadow it. A recursive
	// bind of the parent directory (MS_BIND|MS_REC) would otherwise overwrite
	// workspace sub-paths with the host's empty view.
	iso := testIsolator(runtime.RuntimeInfo{})
	wsDest := "/home/user/projects/workspaces/myapp"
	args := cmdArgs(t, iso, config.RunConfig{
		Mounts: []config.Mount{
			// workdir bind (parent of workspace dest) — listed first in Mounts
			{Src: "/home/user/projects", Dest: "/home/user/projects", Mode: "rw"},
			// workspace mount — must appear AFTER workdir in bwrap args
			{Src: "/host/myapp", Dest: wsDest, Mode: "rw"},
		},
		WorkspaceDests: []string{wsDest},
		Entrypoint:     config.Entrypoint{Cmd: "sh"},
	})
	// Both mounts must be present.
	if !hasSeq(args, "--bind", "/home/user/projects", "/home/user/projects") {
		t.Errorf("workdir bind missing in %v", args)
	}
	if !hasSeq(args, "--bind", "/host/myapp", wsDest) {
		t.Errorf("workspace bind missing in %v", args)
	}
	// The workspace bind must come AFTER the workdir bind.
	wdIdx := indexSeq(args, "--bind", "/home/user/projects", "/home/user/projects")
	wsIdx := indexSeq(args, "--bind", "/host/myapp", wsDest)
	if wsIdx <= wdIdx {
		t.Errorf("workspace bind (idx %d) must come after workdir bind (idx %d)", wsIdx, wdIdx)
	}
}

// ── Environment ───────────────────────────────────────────────────────────────

func TestBuild_clearenv_byDefault(t *testing.T) {
	// When Env is the zero value (no clearenv field in profile), the sandbox
	// must still clear the host environment. Inheriting it silently leaks
	// secrets (AWS_*, GITHUB_TOKEN, etc.) into the sandbox.
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env:        config.EnvConfig{}, // zero value — no explicit clearenv field
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasFlag(args, "--clearenv") {
		t.Errorf("expected --clearenv by default (zero Env), got %v", args)
	}
}

func TestBuild_clearenv_explicit(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env:        config.EnvConfig{Clear: true},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasFlag(args, "--clearenv") {
		t.Errorf("expected --clearenv when Clear=true, got %v", args)
	}
}

func TestBuild_inheritAll_noClearing(t *testing.T) {
	// InheritAll=true is the explicit opt-in to full host env inheritance.
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env:        config.EnvConfig{InheritAll: true},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasFlag(args, "--clearenv") {
		t.Errorf("unexpected --clearenv when InheritAll=true, got %v", args)
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

func TestBuild_inheritEnv_unsetOnHost_omitted(t *testing.T) {
	os.Unsetenv("INNER_TEST_UNSET_KEY")
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			Clear:   true,
			Inherit: []string{"INNER_TEST_UNSET_KEY"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--setenv" && args[i+1] == "INNER_TEST_UNSET_KEY" {
			t.Errorf("expected no --setenv for unset host variable, got %v", args)
		}
	}
}

func TestBuild_inheritEnv_setButEmptyOnHost_forwarded(t *testing.T) {
	t.Setenv("INNER_TEST_EMPTY_KEY", "")
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			Clear:   true,
			Inherit: []string{"INNER_TEST_EMPTY_KEY"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--setenv", "INNER_TEST_EMPTY_KEY", "") {
		t.Errorf("expected --setenv INNER_TEST_EMPTY_KEY \"\", got %v", args)
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

func TestBuild_setEnv_deterministicOrder(t *testing.T) {
	// Map iteration in Go is randomised; --setenv entries must be emitted in
	// sorted key order so that dry-run output and command logging are
	// reproducible across runs.
	iso := testIsolator(runtime.RuntimeInfo{})
	cfg := config.RunConfig{
		Env: config.EnvConfig{
			Set: map[string]string{
				"ZEBRA": "z",
				"ALPHA": "a",
				"MANGO": "m",
			},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	}

	// Collect indices of the three --setenv KEY triples.
	collectSetenvPositions := func() (int, int, int) {
		args := cmdArgs(t, iso, cfg)
		alpha, mango, zebra := -1, -1, -1
		for i := 0; i+2 < len(args); i++ {
			if args[i] == "--setenv" {
				switch args[i+1] {
				case "ALPHA":
					alpha = i
				case "MANGO":
					mango = i
				case "ZEBRA":
					zebra = i
				}
			}
		}
		return alpha, mango, zebra
	}

	alpha, mango, zebra := collectSetenvPositions()
	if alpha == -1 || mango == -1 || zebra == -1 {
		t.Fatalf("not all --setenv entries found; ALPHA=%d MANGO=%d ZEBRA=%d", alpha, mango, zebra)
	}
	if !(alpha < mango && mango < zebra) {
		t.Errorf("--setenv entries not in sorted key order: ALPHA@%d MANGO@%d ZEBRA@%d", alpha, mango, zebra)
	}

	// Run several times to catch non-determinism that might not show on first call.
	for range 20 {
		a, m, z := collectSetenvPositions()
		if a != alpha || m != mango || z != zebra {
			t.Errorf("non-deterministic order: ALPHA@%d MANGO@%d ZEBRA@%d (first run: %d %d %d)",
				a, m, z, alpha, mango, zebra)
		}
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

	const sandboxPath = "/tmp/inner/gitconfig"
	if !hasSeq(args, "--ro-bind", "/tmp/inner-gitconfig-abc", sandboxPath) {
		t.Errorf("expected gitconfig bound to sandbox path %q, got %v", sandboxPath, args)
	}
	if !hasSeq(args, "--setenv", "GIT_CONFIG_GLOBAL", sandboxPath) {
		t.Errorf("expected GIT_CONFIG_GLOBAL=%q, got %v", sandboxPath, args)
	}
}

func TestBuild_gitConfigPath_hostPathNotLeaked(t *testing.T) {
	// The temp gitconfig lives at a host-only path like /tmp/inner-gitconfig-XXXX.
	// Mounting it at that same path inside the sandbox leaks the host /tmp layout.
	// It must be mounted at a fixed in-sandbox path instead.
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		GitConfigPath: "/tmp/inner-gitconfig-abc",
		Entrypoint:    config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--ro-bind", "/tmp/inner-gitconfig-abc", "/tmp/inner-gitconfig-abc") {
		t.Errorf("gitconfig must not be mounted at its host path (leaks host /tmp layout), got %v", args)
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

// ── Sensitive resource hiding ─────────────────────────────────────────────────

func TestBuild_sensitiveHidden_byDefault(t *testing.T) {
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	// Directories must be overlaid with tmpfs.
	for _, flag := range []string{"--tmpfs"} {
		if !hasFlag(args, flag) {
			t.Errorf("expected %s in args when sensitive paths exist, got %v", flag, args)
		}
	}
	// At least one --bind /dev/null must be present (for file/socket resources).
	if !hasSeq(args, "--bind", "/dev/null") {
		t.Errorf("expected --bind /dev/null for file/socket resources, got %v", args)
	}
}

func TestBuild_sshKeys_hiddenByDefault(t *testing.T) {
	home, _ := os.UserHomeDir()
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	sshPath := home + "/.ssh"
	if !hasSeq(args, "--tmpfs", sshPath) {
		t.Errorf("expected --tmpfs %s to hide ssh-keys, got %v", sshPath, args)
	}
}

func TestBuild_sshKeys_allowedNotHidden(t *testing.T) {
	home, _ := os.UserHomeDir()
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:      []string{"ssh-keys"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	sshPath := home + "/.ssh"
	if hasSeq(args, "--tmpfs", sshPath) {
		t.Errorf("unexpected --tmpfs %s when ssh-keys is in Allow, got %v", sshPath, args)
	}
}

func TestBuild_dockerSocket_hiddenByDefault(t *testing.T) {
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--bind", "/dev/null", "/var/run/docker.sock") {
		t.Errorf("expected /var/run/docker.sock to be shadowed, got %v", args)
	}
}

func TestBuild_dockerSocket_hiddenViaSymlink(t *testing.T) {
	// Simulate systems where /var/run is a symlink to /run (common on systemd
	// distros). EvalSymlinks resolves /var/run/docker.sock → /run/docker.sock.
	// bwrap must receive the canonical path so it can create the bind mount
	// point without having to traverse a symlink.
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	iso.evalSymlinksFn = func(p string) (string, error) {
		if p == "/var/run/docker.sock" {
			return "/run/docker.sock", nil
		}
		return p, nil
	}
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--bind", "/dev/null", "/run/docker.sock") {
		t.Errorf("expected canonical /run/docker.sock to be shadowed, got %v", args)
	}
	if hasSeq(args, "--bind", "/dev/null", "/var/run/docker.sock") {
		t.Errorf("expected symlink path /var/run/docker.sock NOT to be used, got %v", args)
	}
}

func TestBuild_dockerSocket_allowedNotHidden(t *testing.T) {
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:      []string{"docker-socket"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--bind", "/dev/null", "/var/run/docker.sock") {
		t.Errorf("unexpected docker socket shadow when docker-socket is in Allow, got %v", args)
	}
}

func TestBuild_netrc_hiddenByDefault(t *testing.T) {
	home, _ := os.UserHomeDir()
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	netrcPath := home + "/.netrc"
	if !hasSeq(args, "--bind", "/dev/null", netrcPath) {
		t.Errorf("expected --bind /dev/null %s, got %v", netrcPath, args)
	}
}

func TestBuild_cloudCredentials_hiddenByDefault(t *testing.T) {
	home, _ := os.UserHomeDir()
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	dirs := []struct {
		name string
		path string
	}{
		{"aws-credentials", home + "/.aws"},
		{"gcloud-credentials", home + "/.config/gcloud"},
		{"kube-config", home + "/.kube"},
		{"azure-credentials", home + "/.azure"},
		{"password-store", home + "/.password-store"},
	}
	for _, d := range dirs {
		if !hasSeq(args, "--tmpfs", d.path) {
			t.Errorf("expected --tmpfs %s (%s) to be hidden, got %v", d.path, d.name, args)
		}
	}

	files := []struct {
		name string
		path string
	}{
		{"docker-config", home + "/.docker/config.json"},
		{"npmrc", home + "/.npmrc"},
		{"pypirc", home + "/.pypirc"},
		{"cargo-credentials", home + "/.cargo/credentials"},
	}
	for _, f := range files {
		if !hasSeq(args, "--bind", "/dev/null", f.path) {
			t.Errorf("expected --bind /dev/null %s (%s) to be hidden, got %v", f.path, f.name, args)
		}
	}
}

func TestBuild_cloudCredentials_allowedNotHidden(t *testing.T) {
	home, _ := os.UserHomeDir()
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})

	cases := []struct {
		allowKey string
		path     string
		dir      bool
	}{
		{"aws-credentials", home + "/.aws", true},
		{"gcloud-credentials", home + "/.config/gcloud", true},
		{"kube-config", home + "/.kube", true},
		{"azure-credentials", home + "/.azure", true},
		{"docker-config", home + "/.docker/config.json", false},
		{"npmrc", home + "/.npmrc", false},
		{"pypirc", home + "/.pypirc", false},
		{"cargo-credentials", home + "/.cargo/credentials", false},
		{"password-store", home + "/.password-store", true},
	}
	for _, c := range cases {
		args := cmdArgs(t, iso, config.RunConfig{
			Allow:      []string{c.allowKey},
			Entrypoint: config.Entrypoint{Cmd: "sh"},
		})
		if c.dir {
			if hasSeq(args, "--tmpfs", c.path) {
				t.Errorf("key %s: unexpected --tmpfs %s when in Allow", c.allowKey, c.path)
			}
		} else {
			if hasSeq(args, "--bind", "/dev/null", c.path) {
				t.Errorf("key %s: unexpected --bind /dev/null %s when in Allow", c.allowKey, c.path)
			}
		}
	}
}

func TestBuild_sensitiveResources_nonexistentSkipped(t *testing.T) {
	// When paths don't exist, no hide args should be added.
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--bind", "/dev/null") {
		t.Errorf("unexpected --bind /dev/null when no sensitive paths exist, got %v", args)
	}
	// Count --tmpfs args: only /tmp should be present (base), not sensitive dirs.
	count := 0
	for i, a := range args {
		if a == "--tmpfs" && i+1 < len(args) && args[i+1] != "/tmp" {
			count++
		}
	}
	if count > 0 {
		t.Errorf("unexpected --tmpfs for sensitive dirs when paths don't exist, got %v", args)
	}
}

func TestBuild_brokenSymlink_failsClosed(t *testing.T) {
	// A sensitive path that exists as a broken symlink: Lstat succeeds but
	// EvalSymlinks fails. Before the fix, Build silently used the unresolved
	// path, causing bwrap to skip the mount and leave the path readable.
	home, _ := os.UserHomeDir()
	sshPath := filepath.Join(home, ".ssh")
	iso := testIsolator(runtime.RuntimeInfo{})
	iso.statFn = func(p string) (os.FileInfo, error) {
		if p == sshPath {
			return nil, nil // broken symlink: Lstat sees the entry
		}
		return nil, os.ErrNotExist
	}
	iso.evalSymlinksFn = func(p string) (string, error) {
		if p == sshPath {
			return "", fmt.Errorf("lstat %s: no such file or directory", p)
		}
		return p, nil
	}
	_, err := iso.Build(config.RunConfig{Entrypoint: config.Entrypoint{Cmd: "sh"}})
	if err == nil {
		t.Error("Build should return an error when a sensitive path is a broken symlink, got nil")
	}
}

func TestBuild_partialAllow(t *testing.T) {
	home, _ := os.UserHomeDir()
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:      []string{"ssh-keys"}, // only ssh-keys allowed
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	// ssh-keys: not hidden.
	if hasSeq(args, "--tmpfs", home+"/.ssh") {
		t.Errorf("ssh-keys should not be hidden when in Allow, got %v", args)
	}
	// gpg-keys: still hidden.
	if !hasSeq(args, "--tmpfs", home+"/.gnupg") {
		t.Errorf("gpg-keys should still be hidden when not in Allow, got %v", args)
	}
}

// ── Shim dir ──────────────────────────────────────────────────────────────────

func TestBuild_shimDir_mountAndPath(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		ShimDir:    "/tmp/inner-shims-test",
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if !hasSeq(args, "--ro-bind", "/tmp/inner-shims-test", "/tmp/inner-shims") {
		t.Errorf("expected shim dir ro-bind, got %v", args)
	}
	if !hasSeq(args, "--dir", "/tmp/inner-shims") {
		t.Errorf("expected --dir /tmp/inner-shims before ro-bind, got %v", args)
	}
	// PATH must be set and start with /tmp/inner-shims.
	idx := slices.Index(args, "PATH")
	if idx == -1 || args[idx-1] != "--setenv" {
		t.Fatalf("expected --setenv PATH in args %v", args)
	}
	pathVal := args[idx+1]
	if !strings.HasPrefix(pathVal, "/tmp/inner-shims:") {
		t.Errorf("PATH %q should start with /tmp/inner-shims:", pathVal)
	}
}

func TestBuild_noShimDir_noPathOverride(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		ShimDir:    "",
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if hasSeq(args, "--ro-bind", "/tmp/inner-shims") {
		t.Errorf("unexpected shim dir bind when ShimDir is empty, got %v", args)
	}
	// No explicit PATH override added.
	idx := slices.Index(args, "PATH")
	if idx != -1 && args[idx-1] == "--setenv" {
		t.Errorf("unexpected --setenv PATH when ShimDir is empty, got %v", args)
	}
}

func TestBuild_shimDir_pathPreservesProfilePath(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		ShimDir: "/tmp/inner-shims-test",
		Env: config.EnvConfig{
			Set: map[string]string{"PATH": "/custom/bin:/usr/bin"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	// Find the PATH setenv that comes after the shim dir bind.
	// There may be multiple --setenv PATH entries; the last one wins in bwrap.
	// We verify the last one has the custom path appended.
	lastPathVal := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--setenv" && i+2 < len(args) && args[i+1] == "PATH" {
			lastPathVal = args[i+2]
		}
	}
	if !strings.HasPrefix(lastPathVal, "/tmp/inner-shims:") {
		t.Errorf("PATH should start with /tmp/inner-shims:, got %q", lastPathVal)
	}
	if !strings.Contains(lastPathVal, "/custom/bin") {
		t.Errorf("PATH should contain original /custom/bin, got %q", lastPathVal)
	}
}

// ── path_prepend ──────────────────────────────────────────────────────────────

// lastPathValue returns the value of the last --setenv PATH entry in args,
// mirroring bwrap's own "last --setenv for a key wins" semantics.
func lastPathValue(args []string) (string, bool) {
	val, found := "", false
	for i := 0; i < len(args)-2; i++ {
		if args[i] == "--setenv" && args[i+1] == "PATH" {
			val, found = args[i+2], true
		}
	}
	return val, found
}

func TestBuild_pathPrepend_onlyPrepend(t *testing.T) {
	t.Setenv("PATH", "/host/bin")
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			InheritAll:  true, // simplest way to make basePathValue fall back to host PATH
			PathPrepend: []string{"/opt/jdk/jdk-21/bin"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	got, found := lastPathValue(args)
	if !found {
		t.Fatalf("expected --setenv PATH, got %v", args)
	}
	if got != "/opt/jdk/jdk-21/bin:/host/bin" {
		t.Errorf("PATH = %q, want %q", got, "/opt/jdk/jdk-21/bin:/host/bin")
	}
}

func TestBuild_pathPrepend_withExplicitSetPath(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			Set:         map[string]string{"PATH": "/custom/bin:/usr/bin"},
			PathPrepend: []string{"/opt/jdk/jdk-21/bin"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	got, found := lastPathValue(args)
	if !found {
		t.Fatalf("expected --setenv PATH, got %v", args)
	}
	want := "/opt/jdk/jdk-21/bin:/custom/bin:/usr/bin"
	if got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
}

func TestBuild_pathPrepend_withShimDir(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		ShimDir: "/tmp/inner-shims-test",
		Env: config.EnvConfig{
			Set:         map[string]string{"PATH": "/custom/bin"},
			PathPrepend: []string{"/opt/jdk/jdk-21/bin"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	got, found := lastPathValue(args)
	if !found {
		t.Fatalf("expected --setenv PATH, got %v", args)
	}
	want := "/tmp/inner-shims:/opt/jdk/jdk-21/bin:/custom/bin"
	if got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
}

func TestBuild_noPathPrepend_unchangedBehavior(t *testing.T) {
	// Regression: without path_prepend, PATH handling must be identical to
	// before this feature existed (no extra --setenv PATH beyond Set/shim).
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Env: config.EnvConfig{
			Set: map[string]string{"PATH": "/custom/bin"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	count := 0
	for i := 0; i < len(args)-2; i++ {
		if args[i] == "--setenv" && args[i+1] == "PATH" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 --setenv PATH entry, got %d in %v", count, args)
	}
	got, _ := lastPathValue(args)
	if got != "/custom/bin" {
		t.Errorf("PATH = %q, want %q", got, "/custom/bin")
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

// ── containers.conf override (rootless podman) ───────────────────────────────

func TestBuild_containersConf_boundAndExported(t *testing.T) {
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:              []string{"nested-user-ns"},
		ContainersConfPath: "/tmp/inner-containers-abc/containers.conf",
		Entrypoint:         config.Entrypoint{Cmd: "sh"},
	})

	const sandboxPath = "/tmp/inner/containers.conf"
	if !hasSeq(args, "--ro-bind", "/tmp/inner-containers-abc/containers.conf", sandboxPath) {
		t.Errorf("expected containers.conf bound at %q, got %v", sandboxPath, args)
	}
	if !hasSeq(args, "--setenv", "CONTAINERS_CONF_OVERRIDE", sandboxPath) {
		t.Errorf("expected CONTAINERS_CONF_OVERRIDE=%q, got %v", sandboxPath, args)
	}
	// The host tmp layout must not leak into the sandbox.
	if hasSeq(args, "--ro-bind", "/tmp/inner-containers-abc/containers.conf", "/tmp/inner-containers-abc/containers.conf") {
		t.Errorf("containers.conf must not be mounted at its host path, got %v", args)
	}
}

// Without nested-user-ns nothing runs containers inside the sandbox, so the
// override must not be emitted even if a path somehow ended up in the config.
func TestBuild_containersConf_ignoredWithoutNestedUserNs(t *testing.T) {
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		ContainersConfPath: "/tmp/inner-containers-abc/containers.conf",
		Entrypoint:         config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--setenv", "CONTAINERS_CONF_OVERRIDE") {
		t.Errorf("CONTAINERS_CONF_OVERRIDE must not be set without nested-user-ns, got %v", args)
	}
}

func TestBuild_containersConf_absentWhenUnset(t *testing.T) {
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:      []string{"nested-user-ns"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--setenv", "CONTAINERS_CONF_OVERRIDE") {
		t.Errorf("no override generated: CONTAINERS_CONF_OVERRIDE must be absent, got %v", args)
	}
}

// Regression: bwrap applies --clearenv when it parses it, so a --setenv
// emitted earlier is wiped. The override must be exported AFTER the
// environment section.
func TestBuild_containersConf_setenvAfterClearenv(t *testing.T) {
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:              []string{"nested-user-ns"},
		ContainersConfPath: "/tmp/inner-containers-abc/containers.conf",
		Entrypoint:         config.Entrypoint{Cmd: "sh"},
	})

	clearenv := slices.Index(args, "--clearenv")
	setenv := lastIndexOfSeq(args, "--setenv", "CONTAINERS_CONF_OVERRIDE", "/tmp/inner/containers.conf")
	if clearenv < 0 || setenv < 0 {
		t.Fatalf("expected both --clearenv and the override --setenv, got %v", args)
	}
	if setenv < clearenv {
		t.Errorf("--setenv CONTAINERS_CONF_OVERRIDE must come after --clearenv, got %v", args)
	}
}

// ── Network allowlist proxy ───────────────────────────────────────────────────

// In allowlist mode the sandbox keeps its private namespace AND gets the one
// socket that leads out of it, plus every environment variable an HTTP client
// might consult to find the proxy.
func TestBuild_allowlistBindsTheProxySocketAndSetsTheProxyEnv(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		NetworkMode:        config.NetworkAllowlist,
		NetProxySocketPath: "/tmp/inner-net-abc/proxy.sock",
		Entrypoint:         config.Entrypoint{Cmd: "sh"},
	})

	if !hasFlag(args, "--unshare-net") {
		t.Error("allowlist mode must still isolate the network namespace")
	}
	if !hasPair(args, "--ro-bind", "/tmp/inner-net-abc/proxy.sock") {
		t.Errorf("proxy socket was not bind-mounted: %v", args)
	}
	if !hasPair(args, "--dir", "/tmp/inner") {
		t.Errorf("the mount point directory was not created: %v", args)
	}

	// All four spellings: Go checks the upper and lower case forms, and non-Go
	// tools are split between them. Missing one is a tool with no network.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if v := setenvValue(args, key); v != netproxy.ProxyURL() {
			t.Errorf("%s = %q, want %q", key, v, netproxy.ProxyURL())
		}
	}
	// Forced empty so a profile-set NO_PROXY cannot punch a bypass.
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		if v := setenvValue(args, key); v != "" {
			t.Errorf("%s = %q, want it forced empty", key, v)
		}
	}
	// Node 26+ ignores the proxy env for fetch()/undici without this.
	if v := setenvValue(args, "NODE_USE_ENV_PROXY"); v != "1" {
		t.Errorf("NODE_USE_ENV_PROXY = %q, want \"1\"", v)
	}
}

// bwrap applies --clearenv when it parses it, wiping every --setenv that came
// before. The proxy variables would be silently erased if this block were
// emitted too early — and the symptom would be an agent with no network and no
// explanation.
func TestBuild_proxyEnvIsEmittedAfterClearenv(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		NetworkMode:        config.NetworkAllowlist,
		NetProxySocketPath: "/tmp/inner-net-abc/proxy.sock",
		Entrypoint:         config.Entrypoint{Cmd: "sh"},
	})

	clearenv := indexOfFlag(args, "--clearenv")
	if clearenv < 0 {
		t.Fatal("expected --clearenv by default")
	}
	proxy := indexOfSetenv(args, "HTTP_PROXY")
	if proxy < 0 {
		t.Fatal("HTTP_PROXY was not set")
	}
	if proxy < clearenv {
		t.Errorf("HTTP_PROXY at %d is emitted before --clearenv at %d: bwrap would wipe it", proxy, clearenv)
	}
}

// Without a socket path there is no proxy to point at, so pointing HTTP_PROXY
// at one would leave every request failing for a reason nothing explains.
func TestBuild_allowlistWithoutASocketSetsNoProxyEnv(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		NetworkMode: config.NetworkAllowlist,
		Entrypoint:  config.Entrypoint{Cmd: "sh"},
	})
	if !hasFlag(args, "--unshare-net") {
		t.Error("the namespace must still be isolated")
	}
	if indexOfSetenv(args, "HTTP_PROXY") >= 0 {
		t.Error("HTTP_PROXY must not be set when no proxy socket is available")
	}
}

func TestBuild_fullModeBindsNoProxySocket(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		NetworkMode:        config.NetworkFull,
		NetProxySocketPath: "/tmp/inner-net-abc/proxy.sock",
		Entrypoint:         config.Entrypoint{Cmd: "sh"},
	})
	if indexOfSetenv(args, "HTTP_PROXY") >= 0 {
		t.Error("full mode reaches the network directly; it must not be pointed at a proxy")
	}
}

// hasPair reports whether flag is immediately followed by value.
func hasPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func indexOfFlag(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// indexOfSetenv returns the position of the LAST "--setenv key" for key, since
// bwrap honours the last one it parses.
func indexOfSetenv(args []string, key string) int {
	last := -1
	for i := 0; i < len(args)-2; i++ {
		if args[i] == "--setenv" && args[i+1] == key {
			last = i
		}
	}
	return last
}

func setenvValue(args []string, key string) string {
	if i := indexOfSetenv(args, key); i >= 0 {
		return args[i+2]
	}
	return "\x00absent"
}
