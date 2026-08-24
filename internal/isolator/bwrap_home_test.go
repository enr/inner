package isolator

import (
	"os"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// withHome pins the home directory seen by the isolator for the duration of a
// test, so assertions do not depend on the machine running them.
func withHome(t *testing.T, home string) {
	t.Helper()
	original := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = original })
}

func TestBuild_homeHostRO_isTheDefault(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})

	for _, mode := range []string{"", config.HomeHostRO} {
		args := cmdArgs(t, iso, config.RunConfig{
			HomeMode:   mode,
			Entrypoint: config.Entrypoint{Cmd: "sh"},
		})
		if hasSeq(args, "--tmpfs", "/home/tester") {
			t.Errorf("home mode %q: unexpected home tmpfs, got %v", mode, args)
		}
	}
}

func TestBuild_homeIsolated_tmpfsBeforeMounts(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		HomeMode: config.HomeIsolated,
		Mounts: []config.Mount{
			{Src: "/home/tester/projects/app", Dest: "/home/tester/projects/app", Mode: "rw"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	tmpfsIdx := indexSeq(args, "--tmpfs", "/home/tester")
	if tmpfsIdx < 0 {
		t.Fatalf("expected --tmpfs /home/tester, got %v", args)
	}
	bindIdx := indexSeq(args, "--bind", "/home/tester/projects/app", "/home/tester/projects/app")
	if bindIdx < 0 {
		t.Fatalf("expected the workdir bind, got %v", args)
	}
	// Order is the whole point: a mount emitted before the tmpfs would be erased
	// by it, leaving the sandbox without its workdir.
	if bindIdx < tmpfsIdx {
		t.Errorf("mount emitted before the home tmpfs (%d < %d): it would be erased, got %v", bindIdx, tmpfsIdx, args)
	}
}

func TestBuild_homeIsolated_allowlistOnlyExistingPaths(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolator(runtime.RuntimeInfo{})
	iso.statFn = func(path string) (os.FileInfo, error) {
		if path == "/home/tester/.local/bin" {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}

	args := cmdArgs(t, iso, config.RunConfig{
		HomeMode:   config.HomeIsolated,
		HomeAllow:  []string{"/home/tester/.local/bin", "/home/tester/.nvm"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if !hasSeq(args, "--ro-bind", "/home/tester/.local/bin", "/home/tester/.local/bin") {
		t.Errorf("expected an allowlist ro-bind for the existing path, got %v", args)
	}
	// A missing path must be skipped, not passed to bwrap: bwrap aborts the run
	// when a bind source does not exist, and a shared profile is expected to
	// list toolchain paths that only some machines have.
	if hasSeq(args, "--ro-bind", "/home/tester/.nvm", "/home/tester/.nvm") {
		t.Errorf("unexpected ro-bind for a path missing on the host, got %v", args)
	}
}

func TestBuild_homeIsolated_allowlistNotEmittedInHostRO(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		HomeMode:   config.HomeHostRO,
		HomeAllow:  []string{"/home/tester/.local/bin"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--ro-bind", "/home/tester/.local/bin", "/home/tester/.local/bin") {
		t.Errorf("home_allow must be inert outside isolated mode, got %v", args)
	}
}

func TestBuild_homeIsolated_skipsHomeSensitiveHiding(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		HomeMode:   config.HomeIsolated,
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	// The tmpfs already erased the whole home: re-hiding a path inside it would
	// make bwrap fail on a mount point that no longer exists.
	for _, path := range []string{
		"/home/tester/.ssh",
		"/home/tester/.gnupg",
		"/home/tester/.aws",
	} {
		if hasSeq(args, "--tmpfs", path) {
			t.Errorf("unexpected hide tmpfs for %s inside an isolated home, got %v", path, args)
		}
	}
	if hasSeq(args, "--bind", "/dev/null", "/home/tester/.netrc") {
		t.Errorf("unexpected /dev/null bind inside an isolated home, got %v", args)
	}
	// Resources outside the home are still hidden: only $HOME inverts.
	if !hasSeq(args, "--bind", "/dev/null", "/var/run/docker.sock") {
		t.Errorf("expected the docker socket to stay hidden, got %v", args)
	}
}

func TestBuild_homeIsolated_hidesSecretsCarriedBackByAMount(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	// A profile mounting ~/.cargo read-write (rust toolchains do exactly this)
	// also brings ~/.cargo/credentials back inside the isolated home.
	args := cmdArgs(t, iso, config.RunConfig{
		HomeMode:  config.HomeIsolated,
		HomeAllow: []string{"/home/tester/.ssh"},
		Mounts: []config.Mount{
			{Src: "/home/tester/.cargo", Dest: "/home/tester/.cargo", Mode: "rw"},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})

	if !hasSeq(args, "--bind", "/dev/null", "/home/tester/.cargo/credentials") {
		t.Errorf("a secret carried back by a mount must still be hidden, got %v", args)
	}
	// Same for an allowlist entry: [sandbox] allow stays the only switch that
	// declassifies a sensitive resource, in both home modes.
	if !hasSeq(args, "--tmpfs", "/home/tester/.ssh") {
		t.Errorf("home_allow must not silently declassify ssh keys, got %v", args)
	}
	// Nothing re-exposes ~/.aws, so no phantom empty directory is created.
	if hasSeq(args, "--tmpfs", "/home/tester/.aws") {
		t.Errorf("unexpected hide mount for a path nothing re-exposes, got %v", args)
	}
}

func TestBuild_homeIsolated_allowKeyDeclassifiesReexposedSecret(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		HomeMode:   config.HomeIsolated,
		HomeAllow:  []string{"/home/tester/.ssh"},
		Allow:      []string{"ssh-keys"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--tmpfs", "/home/tester/.ssh") {
		t.Errorf("ssh-keys in allow must keep the allowlisted directory visible, got %v", args)
	}
	if !hasSeq(args, "--ro-bind", "/home/tester/.ssh", "/home/tester/.ssh") {
		t.Errorf("expected the allowlist bind for the declassified path, got %v", args)
	}
}

func TestBuild_homeIsolated_rejectsUnsafeHome(t *testing.T) {
	for _, home := range []string{"/", "relative/home", ""} {
		withHome(t, home)
		iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
		_, err := iso.Build(config.RunConfig{
			HomeMode:   config.HomeIsolated,
			Entrypoint: config.Entrypoint{Cmd: "sh"},
		})
		if err == nil {
			t.Errorf("home %q: expected Build to refuse isolating this home directory", home)
		}
	}
}

func TestIsUnderHome(t *testing.T) {
	cases := []struct {
		home, path string
		want       bool
	}{
		{"/home/tester", "/home/tester", true},
		{"/home/tester", "/home/tester/.ssh", true},
		{"/home/tester", "/home/tester2/.ssh", false},
		{"/home/tester", "/var/run/docker.sock", false},
		{"", "/home/tester/.ssh", false},
	}
	for _, c := range cases {
		if got := isUnderHome(c.home, c.path); got != c.want {
			t.Errorf("isUnderHome(%q, %q) = %v, want %v", c.home, c.path, got, c.want)
		}
	}
}
