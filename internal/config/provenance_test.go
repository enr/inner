package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeProvenanceProfile(t *testing.T, dir, name, content string) {
	t.Helper()
	profiles := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profiles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profiles, name+".toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// explainedLayer returns the layer of the value with key and item, failing the
// test when the value is missing.
func explainedLayer(t *testing.T, e *ProfileExplanation, key, item string) string {
	t.Helper()
	for _, v := range e.Values {
		if v.Key == key && v.Item == item {
			return v.Layer
		}
	}
	t.Fatalf("no explained value %s %q in %+v", key, item, e.Values)
	return ""
}

// TestExplainProfile_twoLevelExtends is the ISS-17 acceptance test: with
// leaf → mid → base, every value is attributed to the layer that defines it.
func TestExplainProfile_twoLevelExtends(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceProfile(t, dir, "base", `
[sandbox]
network = false
home = "host-ro"
allow = ["ssh-agent"]

[env]
inherit = ["TERM", "LANG"]
set = { EDITOR = "vi", PAGER = "less" }

[entrypoint]
cmd = "bash"
args = ["-l"]

[noop]
block = ["rm"]

[[verify.custom.checks]]
name = "base-check"
cmd = "true"
`)
	writeProvenanceProfile(t, dir, "mid", `
extends = "base"

[sandbox]
home = "isolated"
allow = ["gpg-agent", "ssh-agent"]

[mounts]
"/src" = { dest = "/work", mode = "rw" }

[env]
set = { EDITOR = "nano" }

[[verify.custom.checks]]
name = "mid-check"
cmd = "true"
`)
	writeProvenanceProfile(t, dir, "leaf", `
extends = "mid"

[sandbox]
network_mode = "allowlist"
network_allow = ["example.com"]

[env]
inherit = ["LANG", "TZ"]

[entrypoint]
args = ["-c", "make"]

[noop]
block = ["curl"]
`)

	e, err := NewLoader(dir).ExplainProfile("leaf")
	if err != nil {
		t.Fatal(err)
	}

	var labels []string
	for _, l := range e.Layers {
		labels = append(labels, l.Label)
	}
	if want := []string{"base", "mid", "leaf"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("layers = %v, want %v", labels, want)
	}

	cases := []struct{ key, item, want string }{
		// scalars: last layer that sets the key
		{"sandbox.home", "", "mid"},
		{"entrypoint.cmd", "", "base"},
		// network and network_mode are one decision
		{"sandbox.network_mode", "", "leaf"},
		{"sandbox.network", "", "leaf"},
		// replaced list: the whole list belongs to the last setter
		{"entrypoint.args", "", "leaf"},
		// unioned lists: first layer that introduces the element
		{"sandbox.allow", "ssh-agent", "base"},
		{"sandbox.allow", "gpg-agent", "mid"},
		{"env.inherit", "TERM", "base"},
		{"env.inherit", "LANG", "base"},
		{"env.inherit", "TZ", "leaf"},
		{"noop.block", "rm", "base"},
		{"noop.block", "curl", "leaf"},
		{"sandbox.network_allow", "example.com", "leaf"},
		// maps: last layer that sets the key
		{"env.set", "EDITOR", "mid"},
		{"env.set", "PAGER", "base"},
		{"mounts", "/src", "mid"},
		// appended checks
		{"verify.custom.checks", "base-check", "base"},
		{"verify.custom.checks", "mid-check", "mid"},
	}
	for _, c := range cases {
		if got := explainedLayer(t, e, c.key, c.item); got != c.want {
			t.Errorf("%s %q: layer = %q, want %q", c.key, c.item, got, c.want)
		}
	}

	// The merged profile is exactly what a run would load.
	want, err := NewLoader(dir).LoadProfileAuto("leaf")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.Profile, want) {
		t.Errorf("explained profile differs from LoadProfileAuto:\n got %+v\nwant %+v", e.Profile, want)
	}
}

// TestExplainProfile_everyValueHasALayer guards against mergeProfiles growing a
// field the provenance walk does not know about: every non-zero value of a
// merged profile must be attributed to a layer.
func TestExplainProfile_everyValueHasALayer(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceProfile(t, dir, "base", `
schema_version = "1"
description = "base"
workspaces_path = "/ws"
capabilities = ["claude"]

[sandbox]
network_mode = "allowlist"
clipboard = true
pid_namespace = false
home = "isolated"
home_allow = ["~/.local/bin"]
git_dir = "ro"
network_allow = ["a.example"]
network_deny = ["b.example"]
cgroup_manager = "systemd"

[sandbox.limits]
memory = "1G"
cpu = "50%"
pids = 100

[env]
clearenv = true
path_prepend = ["/opt/bin"]

[git]
strip_sections = ["credential"]
overrides = { "user.name" = "x" }

[entrypoint]
interactive = true
tui = true
cursor_fix = "auto"
workdir = "/work"
history = ["~/.bash_history"]

[output]
summary = true
log = "full"
timeout_seconds = 10

[noop]
rewrite = { npm = "pnpm" }
`)
	writeProvenanceProfile(t, dir, "leaf", "extends = \"base\"\ndescription = \"leaf\"\n")

	e, err := NewLoader(dir).ExplainProfile("leaf")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range e.Values {
		if v.Layer == "" {
			t.Errorf("%s %q = %s has no layer", v.Key, v.Item, v.Value)
		}
	}
	if got := explainedLayer(t, e, "description", ""); got != "leaf" {
		t.Errorf("description: layer = %q, want leaf", got)
	}
	if got := explainedLayer(t, e, "sandbox.limits.pids", ""); got != "base" {
		t.Errorf("sandbox.limits.pids: layer = %q, want base", got)
	}
}

// TestExplainProfile_builtinDefault: a value no layer sets is reported with an
// empty layer (the built-in default), not attributed to a file.
func TestExplainProfile_builtinDefault(t *testing.T) {
	dir := t.TempDir()
	writeProvenanceProfile(t, dir, "solo", "description = \"no network key\"\n")
	e, err := NewLoader(dir).ExplainProfile("solo")
	if err != nil {
		t.Fatal(err)
	}
	if got := explainedLayer(t, e, "sandbox.network_mode", ""); got != "" {
		t.Errorf("network_mode: layer = %q, want built-in default", got)
	}
}

// TestExplainProfile_duplicateNamesUsePaths: a chain where two layers share a
// name labels them by path so the attribution stays unambiguous.
func TestExplainProfile_duplicateNamesUsePaths(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	writeProvenanceProfile(t, dir, "claude", "[sandbox]\nhome = \"isolated\"\n")
	local := filepath.Join(work, ".config", "inner", "profiles")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "claude.toml"), []byte("extends = \"claude\"\n[sandbox]\nclipboard = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := NewLoaderWithWorkDir(dir, work).ExplainProfile("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Layers) != 2 || e.Layers[0].Label == e.Layers[1].Label {
		t.Fatalf("layers = %+v, want two distinct labels", e.Layers)
	}
	if got := explainedLayer(t, e, "sandbox.home", ""); got != e.Layers[0].Label {
		t.Errorf("sandbox.home: layer = %q, want %q", got, e.Layers[0].Label)
	}
	if got := explainedLayer(t, e, "sandbox.clipboard", ""); got != e.Layers[1].Label {
		t.Errorf("sandbox.clipboard: layer = %q, want %q", got, e.Layers[1].Label)
	}
}

func TestExplainProfile_notFound(t *testing.T) {
	if _, err := NewLoader(t.TempDir()).ExplainProfile("missing"); err == nil {
		t.Fatal("expected an error for a missing profile")
	}
}
