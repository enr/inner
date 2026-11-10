package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every way a run can change what the next run loads is reported: a profile
// edited, a project profile created (it would shadow the global one), a
// project config file created in an ancestor, a file removed.
func TestConfigSnapshot_reportsCreatedModifiedRemoved(t *testing.T) {
	userDir := t.TempDir()
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	writeTestFile(t, filepath.Join(userDir, "profiles", "claude.toml"), "name = \"claude\"\n")
	writeTestFile(t, filepath.Join(proj, ".config", "inner", "profiles", "dev.toml"), "name = \"dev\"\n")
	writeTestFile(t, filepath.Join(proj, "inner.local.toml"), "default_profile = \"dev\"\n")

	dirs := []string{proj}
	before := takeConfigSnapshot(userDir, dirs)

	writeTestFile(t, filepath.Join(proj, ".config", "inner", "profiles", "dev.toml"), "name = \"dev\"\n[env]\ninherit_all = true\n")
	writeTestFile(t, filepath.Join(proj, ".config", "inner", "profiles", "claude.toml"), "name = \"claude\"\n")
	writeTestFile(t, filepath.Join(root, "inner.toml"), "default_profile = \"x\"\n")
	if err := os.Remove(filepath.Join(proj, "inner.local.toml")); err != nil {
		t.Fatal(err)
	}
	// Not configuration: ignored.
	writeTestFile(t, filepath.Join(proj, "notes.toml"), "x = 1\n")

	got := diffConfigSnapshots(before, takeConfigSnapshot(userDir, dirs))
	want := []configChange{
		{"created", filepath.Join(proj, ".config", "inner", "profiles", "claude.toml")},
		{"modified", filepath.Join(proj, ".config", "inner", "profiles", "dev.toml")},
		{"removed", filepath.Join(proj, "inner.local.toml")},
		{"created", filepath.Join(root, "inner.toml")},
	}
	slices.SortFunc(want, func(a, b configChange) int { return strings.Compare(a.Path, b.Path) })
	if !slices.Equal(got, want) {
		t.Errorf("changes =\n  %v\nwant\n  %v", got, want)
	}
}

func TestConfigSnapshot_unchangedIsSilent(t *testing.T) {
	userDir := t.TempDir()
	proj := t.TempDir()
	writeTestFile(t, filepath.Join(proj, ".config", "inner.toml"), "default_profile = \"dev\"\n")
	before := takeConfigSnapshot(userDir, []string{proj})
	// Same content rewritten: not a change.
	writeTestFile(t, filepath.Join(proj, ".config", "inner.toml"), "default_profile = \"dev\"\n")
	changes := diffConfigSnapshots(before, takeConfigSnapshot(userDir, []string{proj}))
	if len(changes) != 0 {
		t.Errorf("changes = %v, want none", changes)
	}
	var buf bytes.Buffer
	reportConfigChanges(&buf, changes)
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %q", buf.String())
	}
}

func TestReportConfigChanges_namesEachFile(t *testing.T) {
	var buf bytes.Buffer
	reportConfigChanges(&buf, []configChange{{"created", "/p/.config/inner/profiles/x.toml"}})
	out := buf.String()
	for _, want := range []string{"changed inner configuration", "created", "/p/.config/inner/profiles/x.toml", "review"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}
