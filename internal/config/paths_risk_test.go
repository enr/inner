package config

import "testing"

func TestPathCoveredBy(t *testing.T) {
	prefixes := []string{"/home/tester/.local/bin", "/opt/tools/"}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/tester/.local/bin", true},
		{"/home/tester/.local/bin/claude", true},
		{"/home/tester/.local/bin/../bin/claude", true}, // cleaned before comparing
		{"/home/tester/.local/bindir", false},           // prefix must end at a separator
		{"/home/tester/.local", false},                  // a parent is not covered by its child
		{"/opt/tools/gradle", true},                     // trailing separator in the prefix
		{"/opt/toolsx", false},
		{"", false},
	}
	for _, c := range cases {
		if got := PathCoveredBy(prefixes, c.path); got != c.want {
			t.Errorf("PathCoveredBy(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if PathCoveredBy(nil, "/anything") {
		t.Error("an empty prefix list covers nothing")
	}
	if PathCoveredBy([]string{""}, "/anything") {
		t.Error("an empty prefix must be ignored, not treated as the root")
	}
}

func TestIsFilesystemRoot(t *testing.T) {
	for _, p := range []string{"/", "//", "/."} {
		if !IsFilesystemRoot(p) {
			t.Errorf("IsFilesystemRoot(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", "/usr", "/home/tester"} {
		if IsFilesystemRoot(p) {
			t.Errorf("IsFilesystemRoot(%q) = true, want false", p)
		}
	}
}

func TestIsSystemDir(t *testing.T) {
	for _, p := range []string{"/usr", "/etc/", "/home", "/root", "/var"} {
		if !IsSystemDir(p) {
			t.Errorf("IsSystemDir(%q) = false, want true", p)
		}
	}
	// Sub-paths are ordinary: pinning a toolchain under /opt is a normal mount.
	for _, p := range []string{"/opt/jdk-21", "/home/tester", "/etc/inner", "/", ""} {
		if IsSystemDir(p) {
			t.Errorf("IsSystemDir(%q) = true, want false", p)
		}
	}
}

func TestCredentialAllowKeys_areValidAllowKeys(t *testing.T) {
	// A typo here would silently disable the network+credentials warning.
	valid := make(map[string]bool, len(ValidAllowKeys))
	for _, k := range ValidAllowKeys {
		valid[k] = true
	}
	for _, k := range CredentialAllowKeys {
		if !valid[k] {
			t.Errorf("credential key %q is not in ValidAllowKeys", k)
		}
	}
}
