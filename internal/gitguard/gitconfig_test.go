package gitguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfig(t *testing.T) {
	text := `# comment
[core]
	bare = false
	HooksPath = "my hooks" ; trailing comment
	fsmonitor
[core] sshCommand = ssh -i key # inline
[remote "Origin"]
	url = https://example.com/r.git
[includeIf "gitdir:~/work/"]
	path = work.inc
[branch.Main]
	remote = origin
[alias]
	x = "!echo \"hi\"\tthere"
	long = one \
two
`
	entries := parseConfig(text)

	get := func(section, sub, key string) (configEntry, bool) {
		for _, e := range entries {
			if e.section == section && e.subsection == sub && e.key == key {
				return e, true
			}
		}
		return configEntry{}, false
	}

	cases := []struct {
		section, sub, key, want string
	}{
		{"core", "", "bare", "false"},
		{"core", "", "hookspath", "my hooks"},
		{"core", "", "sshcommand", "ssh -i key"},
		{"remote", "Origin", "url", "https://example.com/r.git"},
		{"includeif", "gitdir:~/work/", "path", "work.inc"},
		{"branch", "main", "remote", "origin"},
		{"alias", "", "x", "!echo \"hi\"\tthere"},
		{"alias", "", "long", "one two"},
	}
	for _, c := range cases {
		e, ok := get(c.section, c.sub, c.key)
		if !ok {
			t.Errorf("%s.%s.%s: missing", c.section, c.sub, c.key)
			continue
		}
		if e.value != c.want {
			t.Errorf("%s.%s.%s = %q, want %q", c.section, c.sub, c.key, e.value, c.want)
		}
	}

	fsm, ok := get("core", "", "fsmonitor")
	if !ok || fsm.hasValue || !fsm.isTrue() {
		t.Errorf("bare key: got %+v, want boolean true without value", fsm)
	}
}

func TestReadConfigChain_followsIncludes(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	write(t, root, "[include]\n\tpath = sub/a.inc\n[includeIf \"onbranch:x\"]\n\tpath = /abs/missing.inc\n")
	write(t, filepath.Join(dir, "sub", "a.inc"), "[include]\n\tpath = b.inc\n[core]\n\thooksPath = h\n")
	write(t, filepath.Join(dir, "sub", "b.inc"), "[include]\n\tpath = a.inc\n") // cycle

	entries, includes := readConfigChain(root)

	want := []string{
		filepath.Join(dir, "sub", "a.inc"),
		filepath.Join(dir, "sub", "b.inc"),
		filepath.Join(dir, "sub", "a.inc"), // the cycle is reported, not followed
		"/abs/missing.inc",
	}
	if len(includes) != len(want) {
		t.Fatalf("includes = %v, want %v", includes, want)
	}
	for i := range want {
		if includes[i] != want[i] {
			t.Errorf("includes[%d] = %q, want %q", i, includes[i], want[i])
		}
	}
	if e, ok := lookup(entries, "core", "hookspath"); !ok || e.value != "h" {
		t.Errorf("hooksPath from included file: got %+v, %v", e, ok)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
