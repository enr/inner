package isolator

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// testIsolator creates a BwrapIsolator without requiring bwrap on the host.
func testIsolator(info runtime.RuntimeInfo) *BwrapIsolator {
	return newBwrapIsolatorWithInfo("/fake/bwrap", info)
}

// testIsolatorAllExist creates a BwrapIsolator where every path is reported as existing.
// Used to test sensitive resource hiding without needing real filesystem paths.
func testIsolatorAllExist(info runtime.RuntimeInfo) *BwrapIsolator {
	iso := newBwrapIsolatorWithInfo("/fake/bwrap", info)
	iso.statFn = func(string) (os.FileInfo, error) { return nil, nil }
	return iso
}

// testIsolatorNoneExist creates a BwrapIsolator where no path exists.
func testIsolatorNoneExist(info runtime.RuntimeInfo) *BwrapIsolator {
	iso := newBwrapIsolatorWithInfo("/fake/bwrap", info)
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
		{"--dev-bind", "/dev/pts", "/dev/pts"},
		{"--tmpfs", "/tmp"},
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

func TestBuild_noPidUnshare_interactive(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "claude", Interactive: true},
	})
	if hasFlag(args, "--unshare-pid") {
		t.Errorf("--unshare-pid must not be present for interactive runs: setsid() breaks TUI apps")
	}
}

func TestBuild_pidUnshare_nonInteractive(t *testing.T) {
	iso := testIsolator(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Entrypoint: config.Entrypoint{Cmd: "claude", Interactive: false},
	})
	if !hasFlag(args, "--unshare-pid") {
		t.Errorf("expected --unshare-pid for non-interactive runs, got %v", args)
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
