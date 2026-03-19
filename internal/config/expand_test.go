package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	cases := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/foo", filepath.Join(home, "foo")},
		{"~/foo/bar", filepath.Join(home, "foo/bar")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, c := range cases {
		got := ExpandPath(c.input)
		if got != c.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestExpandPathEnvVar(t *testing.T) {
	t.Setenv("INNER_TEST_VAR", "/some/path")
	got := ExpandPath("$INNER_TEST_VAR/sub")
	if got != "/some/path/sub" {
		t.Errorf("got %q, want %q", got, "/some/path/sub")
	}
}

func TestExpandPathUID(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())

	cases := []struct {
		input string
		want  string
	}{
		{"/run/user/$UID/podman.sock", "/run/user/" + uid + "/podman.sock"},
		{"/run/user/${UID}/podman.sock", "/run/user/" + uid + "/podman.sock"},
	}
	for _, c := range cases {
		got := ExpandPath(c.input)
		if got != c.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestExpandPathUID_notOverriddenByEnv(t *testing.T) {
	// Even if UID is set in the environment, we always use the real numeric UID.
	t.Setenv("UID", "99999")
	uid := strconv.Itoa(os.Getuid())
	got := ExpandPath("/run/user/$UID/sock")
	want := "/run/user/" + uid + "/sock"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
