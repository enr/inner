package config

import (
	"os"
	"path/filepath"
	"slices"
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

func TestUndefinedVarRefs(t *testing.T) {
	os.Unsetenv("INNER_TEST_UNDEF_A")
	os.Unsetenv("INNER_TEST_UNDEF_B")
	t.Setenv("INNER_TEST_DEFINED", "value")

	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"no vars", "/plain/path", nil},
		{"defined var, shorthand", "$INNER_TEST_DEFINED/sub", nil},
		{"defined var, braces", "${INNER_TEST_DEFINED}/sub", nil},
		{"one undefined, shorthand", "$INNER_TEST_UNDEF_A/sub", []string{"INNER_TEST_UNDEF_A"}},
		{"one undefined, braces", "${INNER_TEST_UNDEF_A}/sub", []string{"INNER_TEST_UNDEF_A"}},
		{"mixed defined and undefined", "${INNER_TEST_DEFINED}:${INNER_TEST_UNDEF_A}", []string{"INNER_TEST_UNDEF_A"}},
		{"duplicate undefined reported once", "${INNER_TEST_UNDEF_A}:${INNER_TEST_UNDEF_A}", []string{"INNER_TEST_UNDEF_A"}},
		{"two distinct undefined, in order", "${INNER_TEST_UNDEF_A}:${INNER_TEST_UNDEF_B}", []string{"INNER_TEST_UNDEF_A", "INNER_TEST_UNDEF_B"}},
		{"UID never reported", "/run/user/$UID/sock", nil},
		{"workdir token never reported", "${workdir}/sub", nil},
		{"workspaces_path token never reported", "${workspaces_path}/sub", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := UndefinedVarRefs(c.input)
			if !slices.Equal(got, c.want) {
				t.Errorf("UndefinedVarRefs(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

func TestUndefinedVarRefs_definedButEmpty_notReported(t *testing.T) {
	t.Setenv("INNER_TEST_EMPTY", "")
	got := UndefinedVarRefs("${INNER_TEST_EMPTY}")
	if got != nil {
		t.Errorf("UndefinedVarRefs = %v, want nil (defined-but-empty is not undefined)", got)
	}
}
