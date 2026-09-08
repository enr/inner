package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestBuild_emptyNoop_returnsEmptyPath(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{}, nil)
	if err != nil {
		t.Fatalf("Build empty noop: %v", err)
	}
	if dir != "" {
		t.Errorf("expected empty path for empty noop, got %q", dir)
	}
}

func TestBuild_blockCreatesScript(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{
		Block: []string{"apt-get", "brew"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, cmd := range []string{"apt-get", "brew"} {
		path := filepath.Join(dir, cmd)

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("shim %q not found: %v", cmd, err)
		}
		// Executable bit must be set.
		if info.Mode()&0o111 == 0 {
			t.Errorf("shim %q is not executable (mode %v)", cmd, info.Mode())
		}

		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading shim %q: %v", cmd, err)
		}
		s := string(content)
		if !strings.HasPrefix(s, "#!/bin/sh") {
			t.Errorf("shim %q missing shebang, got: %q", cmd, s)
		}
		if !strings.Contains(s, "exit 1") {
			t.Errorf("shim %q should exit 1, got: %q", cmd, s)
		}
		if !strings.Contains(s, cmd) {
			t.Errorf("shim %q should mention its own name in error message", cmd)
		}
	}
}

func TestBuild_blockScript_mentionsNoop(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{
		Block: []string{"apt"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	content, _ := os.ReadFile(filepath.Join(dir, "apt"))
	s := string(content)
	if !strings.Contains(s, "noop.block") {
		t.Errorf("block shim should mention [noop.block] hint, got: %q", s)
	}
}

func TestBuild_rewriteCreatesScript(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{
		Rewrite: map[string]string{
			"docker": "podman",
			"rm":     "rm -i",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cases := map[string]string{
		"docker": "podman",
		"rm":     "rm -i",
	}
	for cmd, replacement := range cases {
		path := filepath.Join(dir, cmd)

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("shim %q not found: %v", cmd, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("shim %q is not executable", cmd)
		}

		content, _ := os.ReadFile(path)
		s := string(content)
		if !strings.Contains(s, "exec "+replacement) {
			t.Errorf("shim %q should exec %q, got: %q", cmd, replacement, s)
		}
		if !strings.Contains(s, `"$@"`) {
			t.Errorf("shim %q should forward args via \"$@\", got: %q", cmd, s)
		}
	}
}

func TestBuild_mixedBlockAndRewrite(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{
		Block:   []string{"apt-get"},
		Rewrite: map[string]string{"docker": "podman"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, name := range []string{"apt-get", "docker"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("shim %q not found: %v", name, err)
		}
	}
}

func TestBuild_rewrite_rejectsShellMetacharacters(t *testing.T) {
	cases := []struct {
		name        string
		replacement string
	}{
		{"semicolon", "echo pwned > /tmp/x; true"},
		{"pipe", "cat | sh"},
		{"redirect", "cmd > /tmp/out"},
		{"backtick", "cmd`id`"},
		{"dollar", "cmd$(id)"},
		{"ampersand", "cmd && evil"},
		{"newline", "cmd\nevil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Builder{}.BuildWith(config.NoopConfig{
				Rewrite: map[string]string{"docker": tc.replacement},
			}, nil)
			if err == nil {
				t.Fatalf("expected error for replacement %q, got nil", tc.replacement)
			}
		})
	}
}

func TestBuild_block_rejectsPathTraversal(t *testing.T) {
	cases := []string{
		"../../../tmp/pwned",
		"sub/dir",
		"./local",
		"/absolute",
		"a\\b",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			_, err := Builder{}.BuildWith(config.NoopConfig{
				Block: []string{cmd},
			}, nil)
			if err == nil {
				t.Fatalf("expected error for block key %q, got nil", cmd)
			}
		})
	}
}

func TestBuild_rewrite_rejectsPathTraversalInKey(t *testing.T) {
	cases := []string{
		"../../../tmp/pwned",
		"sub/dir",
		"./local",
		"/absolute",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			_, err := Builder{}.BuildWith(config.NoopConfig{
				Rewrite: map[string]string{cmd: "safe-replacement"},
			}, nil)
			if err == nil {
				t.Fatalf("expected error for rewrite key %q, got nil", cmd)
			}
		})
	}
}

func TestBuild_onlyBlockCreatesDir(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{
		Block: []string{"curl"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	if dir == "" {
		t.Fatal("expected non-empty dir for non-empty noop")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("shim dir %q not found: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory at %q", dir)
	}
}

// BuildWith writes caller-supplied scripts alongside the [noop] ones.
func TestBuildWith_extraScripts(t *testing.T) {
	dir, err := Builder{}.BuildWith(
		config.NoopConfig{Rewrite: map[string]string{"apt-get": "/usr/bin/true"}},
		map[string]string{"claude": "#!/bin/sh\nexec /real/claude \"$@\"\n", "foo": "#!/bin/sh\n"},
	)
	if err != nil {
		t.Fatalf("BuildWith: %v", err)
	}
	defer os.RemoveAll(dir)

	got, err := os.ReadFile(filepath.Join(dir, "claude"))
	if err != nil {
		t.Fatalf("reading claude shim: %v", err)
	}
	if !strings.Contains(string(got), "/real/claude") {
		t.Errorf("extra script not written as given, got:\n%s", got)
	}
	for _, name := range []string{"foo", "apt-get"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected shim %q to be written: %v", name, err)
		}
	}
}

// A [noop] entry is the profile's explicit decision: a runtime shim must not
// silently replace it, or the next capability to register one would un-block a
// command the profile blocked.
func TestBuildWith_rejectsNoopCollision(t *testing.T) {
	for name, noop := range map[string]config.NoopConfig{
		"rewrite": {Rewrite: map[string]string{"claude": "/usr/bin/true"}},
		"block":   {Block: []string{"claude"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir, err := Builder{}.BuildWith(noop, map[string]string{"claude": "#!/bin/sh\n"})
			if err == nil {
				os.RemoveAll(dir)
				t.Fatalf("expected an error when an extra shim collides with [noop] %s", name)
			}
			if dir != "" {
				t.Errorf("no shim dir should be returned on error, got %q", dir)
			}
		})
	}
}

// An extra script with no noop config still produces a shim directory.
func TestBuildWith_extraOnly(t *testing.T) {
	dir, err := Builder{}.BuildWith(config.NoopConfig{}, map[string]string{"claude": "#!/bin/sh\n"})
	if err != nil {
		t.Fatalf("BuildWith: %v", err)
	}
	if dir == "" {
		t.Fatal("expected a shim dir for an extra-only build")
	}
	defer os.RemoveAll(dir)
	if _, err := os.Stat(filepath.Join(dir, "claude")); err != nil {
		t.Errorf("expected the extra script to be written: %v", err)
	}
}

// A path separator in an extra shim name must be rejected, not written.
func TestBuildWith_rejectsUnsafeName(t *testing.T) {
	_, err := Builder{}.BuildWith(config.NoopConfig{}, map[string]string{"../evil": "#!/bin/sh\n"})
	if err == nil {
		t.Fatal("expected an error for a name with a path separator")
	}
}
