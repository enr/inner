package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A run can change the configuration the NEXT run loads. Project config files
// (inner.toml, .config/inner.toml, …) and project profiles
// (.config/inner/profiles/*.toml) live in the workdir, which the sandbox
// writes; a project profile shadows the global one of the same name. So an
// agent asked to "set up inner for this repo" — or just tidying up — can
// write a profile with inherit_all or allow = ["ssh-keys"], and the next
// `inner run -p <name>` starts from it.
//
// Making those files read-only would refuse exactly that legitimate request,
// and would not stop a file from being created where none existed. inner
// instead takes a snapshot of every file a later run could load before the
// sandbox starts, compares it when the run ends, and names what changed. The
// user decides; nothing is blocked.

// configWatchMaxRead bounds how much of one file is hashed: config files are
// small, and an odd multi-megabyte one is still told apart by size.
const configWatchMaxRead = 1 << 20

// configSnapshot maps a config file path to a fingerprint of its content.
type configSnapshot map[string]string

// configChange is one difference between two snapshots.
type configChange struct {
	Kind string // "created", "modified", "removed"
	Path string
}

// projectConfigFiles lists the files inner reads its configuration from, for
// a run started in any of dirs or with dirs as its workdir: the project config
// files in each directory and its ancestors, the project profiles of each
// directory, and the user config directory (config.toml and profiles/).
// Missing files are listed too, so a snapshot sees them being created.
func projectConfigFiles(userDir string, dirs []string) []string {
	seen := map[string]bool{}
	var files []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	addProfiles := func(dir string) {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
		for _, m := range matches {
			add(m)
		}
	}
	for _, dir := range dirs {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		for d := dir; ; d = filepath.Dir(d) {
			for _, rel := range projectConfigNames {
				add(filepath.Join(d, rel))
			}
			if parent := filepath.Dir(d); parent == d {
				break
			}
		}
		addProfiles(filepath.Join(dir, ".config", "inner", "profiles"))
	}
	if userDir != "" {
		add(filepath.Join(userDir, "config.toml"))
		addProfiles(filepath.Join(userDir, "profiles"))
	}
	return files
}

// projectConfigNames are the project config files of one directory, as the
// loader reads them (config.collectProjectConfigFiles).
var projectConfigNames = []string{
	filepath.Join(".config", "inner.toml"),
	filepath.Join(".config", "inner", "config.toml"),
	filepath.Join(".config", "inner.local.toml"),
	filepath.Join(".config", "inner", "config.local.toml"),
	"inner.toml",
	"inner.local.toml",
}

// takeConfigSnapshot fingerprints the configuration files that exist now.
// Each call lists the profile directories again, so a profile created during
// the run shows up in the second snapshot.
func takeConfigSnapshot(userDir string, dirs []string) configSnapshot {
	snap := configSnapshot{}
	for _, p := range projectConfigFiles(userDir, dirs) {
		if fp, ok := fingerprintFile(p); ok {
			snap[p] = fp
		}
	}
	return snap
}

func fingerprintFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, configWatchMaxRead)); err != nil {
		return "", false
	}
	return fmt.Sprintf("%d:%s", info.Size(), hex.EncodeToString(h.Sum(nil))), true
}

// diffConfigSnapshots returns what changed from before to after, by path.
func diffConfigSnapshots(before, after configSnapshot) []configChange {
	var changes []configChange
	for p, fp := range after {
		switch old, ok := before[p]; {
		case !ok:
			changes = append(changes, configChange{"created", p})
		case old != fp:
			changes = append(changes, configChange{"modified", p})
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			changes = append(changes, configChange{"removed", p})
		}
	}
	slices.SortFunc(changes, func(a, b configChange) int { return strings.Compare(a.Path, b.Path) })
	return changes
}

// reportConfigChanges tells the user which configuration files the run
// changed. Silent when there are none.
func reportConfigChanges(w io.Writer, changes []configChange) {
	if len(changes) == 0 {
		return
	}
	home, _ := os.UserHomeDir()
	fmt.Fprintf(w, "%s: this run changed inner configuration that later runs load:\n", colorizeW(w, ansiBoldYellow, "inner: warning"))
	for _, c := range changes {
		path := c.Path
		if home != "" && strings.HasPrefix(path, home+"/") {
			path = "~" + path[len(home):]
		}
		fmt.Fprintf(w, "  %-8s  %s\n", c.Kind, path)
	}
	fmt.Fprintln(w, "  review them before the next run: a profile can open the network, un-hide credentials or forward host variables")
}
