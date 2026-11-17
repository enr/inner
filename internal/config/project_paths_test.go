package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestProjectRootOf(t *testing.T) {
	cases := map[string]string{
		"/p/.config/inner.toml":              "/p",
		"/p/.config/inner.local.toml":        "/p",
		"/p/.config/inner/config.toml":       "/p",
		"/p/.config/inner/config.local.toml": "/p",
		"/p/inner.toml":                      "/p",
		"/p/inner/inner.toml":                "/p/inner",
	}
	for in, want := range cases {
		if got := projectRootOf(in); got != want {
			t.Errorf("projectRootOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveProfileValue(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	cases := map[string]string{
		"":                       "",
		"shell":                  "shell",
		".config/inner/p/x.toml": "/root/.config/inner/p/x.toml",
		"x.toml":                 "/root/x.toml",
		"../other/x.toml":        "/other/x.toml",
		"/abs/x.toml":            "/abs/x.toml",
		"~/x.toml":               "~/x.toml",
		"$HOME/x.toml":           "$HOME/x.toml",
	}
	for in, want := range cases {
		if got := resolveProfileValue(in, "/root"); got != want {
			t.Errorf("resolveProfileValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveAliasProfiles(t *testing.T) {
	cases := []struct{ in, want string }{
		{"run -p shell", "run -p shell"},
		{"run --profile p/x.toml", "run --profile /r/p/x.toml"},
		{"run -p p/x.toml --arg y", "run -p /r/p/x.toml --arg y"},
		{"run --profile=p/x.toml", "run --profile=/r/p/x.toml"},
		{"run -pp/x.toml", "run -p=/r/p/x.toml"},
		{"run -p=p/x.toml", "run -p=/r/p/x.toml"},
		{`run -p "my dir/x.toml"`, `run -p '/r/my dir/x.toml'`},
		{"run -p /abs/x.toml", "run -p /abs/x.toml"},
		{"run -p shell -- claude -p fix/this.toml", "run -p shell -- claude -p fix/this.toml"},
		{`run --arg "a b" -p x.toml`, `run --arg "a b" -p /r/x.toml`},
	}
	for _, c := range cases {
		if got := resolveAliasProfiles(c.in, "/r"); got != c.want {
			t.Errorf("resolveAliasProfiles(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Quoted results must split back to the resolved path.
	got := SplitArgs(resolveAliasProfiles(`run -p "it's dir/x.toml"`, "/r"))
	want := []string{"run", "-p", "/r/it's dir/x.toml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitArgs(resolved) = %#v, want %#v", got, want)
	}
}

// A project config in an ancestor directory is inherited by subdirectories:
// its relative profile paths must resolve against the ancestor, not the
// working directory.
func TestBuild_ancestorProjectDefaultProfilePath(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	workDir := filepath.Join(root, "sub", "project")
	writeFile(t, filepath.Join(workDir, "README"), "x")
	writeFile(t, filepath.Join(root, ".config", "inner", "config.toml"), `
default_profile = ".config/inner/profiles/dev.toml"

[aliases]
d = "run --profile .config/inner/profiles/dev.toml"
`)
	profile := filepath.Join(root, ".config", "inner", "profiles", "dev.toml")
	writeFile(t, profile, `[entrypoint]
cmd = "zsh"
`)

	l := NewLoaderWithWorkDir(dir, workDir)
	if got := l.DefaultProfileName(); got != profile {
		t.Errorf("DefaultProfileName() = %q, want %q", got, profile)
	}
	rc, err := l.Build("")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if rc.Entrypoint.Cmd != "zsh" {
		t.Errorf("Entrypoint.Cmd = %q, want zsh", rc.Entrypoint.Cmd)
	}
	aliases, err := l.Aliases()
	if err != nil {
		t.Fatalf("Aliases: %v", err)
	}
	if want := "run --profile " + profile; aliases["d"] != want {
		t.Errorf("alias d = %q, want %q", aliases["d"], want)
	}
}
