package isolator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// blockLayout builds a fake root under a temp dir:
//
//	usr/bin/rm        regular file
//	usr/bin/busybox   regular file, with usr/bin/ls and usr/bin/cat -> busybox
//	usr/bin/dnf-3     regular file, with usr/bin/dnf and usr/bin/yum -> dnf-3
//	usr/bin/dash      regular file, with sh -> usr/bin/dash (the shim shell)
//	usr/bin/dir       a directory
//	bin -> usr/bin    merged-/usr symlink
//	opt/bin/tool      regular file, reachable only through the profile PATH
func blockLayout(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ln := func(target, rel string) {
		if err := os.Symlink(target, filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
	}
	mk("usr/bin/rm")
	mk("usr/bin/busybox")
	mk("usr/bin/dash")
	mk("usr/bin/dnf-3")
	mk("opt/bin/tool")
	if err := os.MkdirAll(filepath.Join(root, "usr/bin/dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln("busybox", "usr/bin/ls")
	ln("busybox", "usr/bin/cat")
	ln("dnf-3", "usr/bin/dnf")
	ln("dnf-3", "usr/bin/yum")
	ln("usr/bin", "bin")
	ln("usr/bin/dash", "sh")
	return root
}

// blockIsolator searches only the fake root, never the host's /usr/bin.
func blockIsolator(root string, warn *bytes.Buffer) *BwrapIsolator {
	iso := testIsolator(runtime.RuntimeInfo{})
	iso.binDirs = []string{filepath.Join(root, "bin"), filepath.Join(root, "usr/bin")}
	iso.shellPath = filepath.Join(root, "sh")
	iso.warnW = warn
	return iso
}

func blockConfig(root string, block ...string) config.RunConfig {
	return config.RunConfig{
		ShimDir:    "/tmp/inner-shims-test",
		Noop:       config.NoopConfig{Block: block},
		Env:        config.EnvConfig{Set: map[string]string{"PATH": filepath.Join(root, "opt/bin")}},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	}
}

// shimBinds returns the destinations the shim dir's files are bound over.
func shimBinds(args []string) []string {
	var dests []string
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--ro-bind" && strings.HasPrefix(args[i+1], "/tmp/inner-shims-test/") {
			dests = append(dests, args[i+2])
		}
	}
	return dests
}

// ISS-13: rm is shadowed at its canonical path, once, even though it is
// reachable as both bin/rm and usr/bin/rm.
func TestBuild_blockedCommandShadowsCanonicalPath(t *testing.T) {
	root := blockLayout(t)
	var warn bytes.Buffer
	args := cmdArgs(t, blockIsolator(root, &warn), blockConfig(root, "rm"))

	want := filepath.Join(root, "usr/bin/rm")
	if got := shimBinds(args); len(got) != 1 || got[0] != want {
		t.Fatalf("shim binds = %v, want [%s]", got, want)
	}
	if !hasSeq(args, "--ro-bind", "/tmp/inner-shims-test/rm", want) {
		t.Errorf("expected the rm shim bound over %s, got %v", want, args)
	}
	// The binds come after the read-only root, or the root would cover them.
	if lastIndexOfSeq(args, "--ro-bind", "/", "/") > lastIndexOfSeq(args, "--ro-bind", "/tmp/inner-shims-test/rm", want) {
		t.Error("shim bind must come after --ro-bind / /")
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning: %s", warn.String())
	}
}

func TestBuild_blockedCommandOnProfilePath(t *testing.T) {
	root := blockLayout(t)
	args := cmdArgs(t, blockIsolator(root, &bytes.Buffer{}), blockConfig(root, "tool"))
	if got := shimBinds(args); len(got) != 1 || got[0] != filepath.Join(root, "opt/bin/tool") {
		t.Fatalf("shim binds = %v, want the tool on the profile PATH", got)
	}
}

// A multi-call binary is not shadowed: it would block every other applet.
func TestBuild_blockedMultiCallBinaryIsNotShadowed(t *testing.T) {
	root := blockLayout(t)
	var warn bytes.Buffer
	args := cmdArgs(t, blockIsolator(root, &warn), blockConfig(root, "ls"))
	if got := shimBinds(args); len(got) != 0 {
		t.Fatalf("shim binds = %v, want none for a multi-call binary", got)
	}
	if !strings.Contains(warn.String(), "busybox, shared with cat") {
		t.Errorf("expected a warning naming the shared binary, got %q", warn.String())
	}
}

// The shims run on /bin/sh: shadowing it would break every shim.
func TestBuild_blockedShellInterpreterIsNotShadowed(t *testing.T) {
	root := blockLayout(t)
	var warn bytes.Buffer
	args := cmdArgs(t, blockIsolator(root, &warn), blockConfig(root, "dash"))
	if got := shimBinds(args); len(got) != 0 {
		t.Fatalf("shim binds = %v, want none for the shims' interpreter", got)
	}
	if !strings.Contains(warn.String(), "interpreter") {
		t.Errorf("expected an interpreter warning, got %q", warn.String())
	}
}

func TestBuild_blockedCommandMissingOrDirectory(t *testing.T) {
	root := blockLayout(t)
	var warn bytes.Buffer
	args := cmdArgs(t, blockIsolator(root, &warn), blockConfig(root, "no-such-cmd", "dir"))
	if got := shimBinds(args); len(got) != 0 {
		t.Fatalf("shim binds = %v, want none", got)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning: %s", warn.String())
	}
}

// Rewrite shims stay PATH-only: the real binary is what they hand over to.
func TestBuild_rewriteIsNotBoundOverBinary(t *testing.T) {
	root := blockLayout(t)
	cfg := blockConfig(root)
	cfg.Noop.Rewrite = map[string]string{"rm": "echo"}
	args := cmdArgs(t, blockIsolator(root, &bytes.Buffer{}), cfg)
	if got := shimBinds(args); len(got) != 0 {
		t.Fatalf("shim binds = %v, want none for a rewrite", got)
	}
}

// A binary whose every name is blocked is shadowed: yum and dnf → dnf-3.
func TestBuild_blockedAliasesAllBlockedAreShadowed(t *testing.T) {
	root := blockLayout(t)
	var warn bytes.Buffer
	args := cmdArgs(t, blockIsolator(root, &warn), blockConfig(root, "yum", "dnf"))
	want := filepath.Join(root, "usr/bin/dnf-3")
	if got := shimBinds(args); len(got) != 1 || got[0] != want {
		t.Fatalf("shim binds = %v, want [%s]", got, want)
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning: %s", warn.String())
	}

	// With only yum blocked, dnf still runs dnf-3: not shadowed.
	warn.Reset()
	args = cmdArgs(t, blockIsolator(root, &warn), blockConfig(root, "yum"))
	if got := shimBinds(args); len(got) != 0 {
		t.Fatalf("shim binds = %v, want none while dnf is not blocked", got)
	}
	if !strings.Contains(warn.String(), "shared with dnf") {
		t.Errorf("expected a warning naming dnf, got %q", warn.String())
	}
}
