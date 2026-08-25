package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/inner/internal/config"
)

// sampleGitconfig is a realistic gitconfig used as fixture across tests.
const sampleGitconfig = `[user]
	name = Test User
	email = test@example.com
[credential]
	helper = store
[core]
	hooksPath = ~/.config/git/hooks
	autocrlf = false
[push]
	default = current
`

// ── Process: strip entire section ────────────────────────────────────────────

func TestProcess_stripSection_removes(t *testing.T) {
	out := Process(sampleGitconfig, []string{"credential"}, nil)
	if strings.Contains(out, "[credential]") {
		t.Errorf("expected [credential] to be stripped, got:\n%s", out)
	}
	if strings.Contains(out, "helper") {
		t.Errorf("expected 'helper' key to be removed, got:\n%s", out)
	}
}

func TestProcess_stripSection_preservesOthers(t *testing.T) {
	out := Process(sampleGitconfig, []string{"credential"}, nil)
	if !strings.Contains(out, "[user]") {
		t.Errorf("expected [user] section to be preserved, got:\n%s", out)
	}
	if !strings.Contains(out, "name = Test User") {
		t.Errorf("expected user.name to be preserved, got:\n%s", out)
	}
}

func TestProcess_stripSection_nonExistent(t *testing.T) {
	out := Process(sampleGitconfig, []string{"nonexistent"}, nil)
	if out != Process(sampleGitconfig, nil, nil) {
		t.Error("stripping a non-existent section should be a no-op")
	}
}

func TestProcess_stripSection_multiple(t *testing.T) {
	out := Process(sampleGitconfig, []string{"credential", "push"}, nil)
	if strings.Contains(out, "[credential]") || strings.Contains(out, "[push]") {
		t.Errorf("expected both sections stripped, got:\n%s", out)
	}
	if !strings.Contains(out, "[core]") {
		t.Errorf("expected [core] preserved, got:\n%s", out)
	}
}

// ── Process: strip specific key ───────────────────────────────────────────────

func TestProcess_stripKey_removesKey(t *testing.T) {
	out := Process(sampleGitconfig, []string{"core.hooksPath"}, nil)
	if strings.Contains(out, "hooksPath") {
		t.Errorf("expected hooksPath to be stripped, got:\n%s", out)
	}
}

func TestProcess_stripKey_preservesSection(t *testing.T) {
	out := Process(sampleGitconfig, []string{"core.hooksPath"}, nil)
	if !strings.Contains(out, "[core]") {
		t.Errorf("expected [core] section to remain, got:\n%s", out)
	}
	if !strings.Contains(out, "autocrlf") {
		t.Errorf("expected autocrlf key to remain, got:\n%s", out)
	}
}

func TestProcess_stripKey_caseInsensitive(t *testing.T) {
	// "core.HOOKSPATH" should match "hooksPath"
	out := Process(sampleGitconfig, []string{"core.HOOKSPATH"}, nil)
	if strings.Contains(out, "hooksPath") {
		t.Errorf("expected case-insensitive strip of hooksPath, got:\n%s", out)
	}
}

func TestProcess_stripKey_nonExistentKey(t *testing.T) {
	before := Process(sampleGitconfig, nil, nil)
	after := Process(sampleGitconfig, []string{"core.nonexistent"}, nil)
	if before != after {
		t.Error("stripping a non-existent key should be a no-op")
	}
}

// ── Process: overrides ────────────────────────────────────────────────────────

func TestProcess_override_updatesExistingKey(t *testing.T) {
	out := Process(sampleGitconfig, nil, map[string]string{"push.default": "nothing"})
	if !strings.Contains(out, "nothing") {
		t.Errorf("expected push.default to be 'nothing', got:\n%s", out)
	}
	if strings.Contains(out, "current") {
		t.Errorf("expected old value 'current' to be replaced, got:\n%s", out)
	}
}

func TestProcess_override_addsKeyToExistingSection(t *testing.T) {
	out := Process(sampleGitconfig, nil, map[string]string{"push.autosetuprebase": "always"})
	if !strings.Contains(out, "[push]") {
		t.Errorf("expected [push] section, got:\n%s", out)
	}
	if !strings.Contains(out, "autosetuprebase") {
		t.Errorf("expected autosetuprebase key, got:\n%s", out)
	}
	if !strings.Contains(out, "always") {
		t.Errorf("expected value 'always', got:\n%s", out)
	}
	// Original key must still be present.
	if !strings.Contains(out, "default") {
		t.Errorf("expected push.default to still exist, got:\n%s", out)
	}
}

func TestProcess_override_createsNewSection(t *testing.T) {
	out := Process(sampleGitconfig, nil, map[string]string{"alias.st": "status"})
	if !strings.Contains(out, "[alias]") {
		t.Errorf("expected [alias] section to be created, got:\n%s", out)
	}
	if !strings.Contains(out, "st = status") {
		t.Errorf("expected 'st = status', got:\n%s", out)
	}
}

func TestProcess_override_caseInsensitiveSection(t *testing.T) {
	out := Process(sampleGitconfig, nil, map[string]string{"PUSH.default": "nothing"})
	if strings.Contains(out, "current") {
		t.Errorf("expected push.default replaced (case-insensitive), got:\n%s", out)
	}
}

func TestProcess_overrideEscapesValueInjection(t *testing.T) {
	out := Process("", nil, map[string]string{
		"user.name": "alice\n[core]\n\thooksPath = /tmp/evil",
	})

	if strings.Contains(out, "\n[core]\n") {
		t.Fatalf("override value injected a core section:\n%s", out)
	}
	if strings.Contains(out, "\thooksPath = /tmp/evil") {
		t.Fatalf("override value injected hooksPath:\n%s", out)
	}
	if !strings.Contains(out, `name = "alice\n[core]\n\thooksPath = /tmp/evil"`) {
		t.Fatalf("override value was not gitconfig-quoted safely:\n%s", out)
	}
}

// TestProcess_overrideQuotesCarriageReturn reproduces issue #7: the quoting
// switch had a `\r` case that emitted the literal `\n` escape (copy-pasted
// from the `\n` case above it), silently turning a CR into a newline escape.
// git's own config parser has no `\r` escape at all — a value containing the
// two-char sequence `\` `r` is a fatal "bad config line" — so the fix must not
// just swap in a `\r` escape either; it must emit a raw CR byte inside the
// quotes, matching what `git config` itself writes (verified below against
// the real git binary, when available).
func TestProcess_overrideQuotesCarriageReturn(t *testing.T) {
	out := Process("", nil, map[string]string{
		"user.name": "alice\rbob",
	})

	if !strings.Contains(out, "name = \"alice\rbob\"") {
		t.Fatalf("expected a raw CR inside the quoted value, got:\n%q", out)
	}
	if strings.Contains(out, `\r`) {
		t.Fatalf(`gitconfig has no \r escape; got a literal backslash-r sequence:%s`, out)
	}

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH, skipping round-trip verification")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfgPath, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gitPath, "config", "--file", cfgPath, "--get", "user.name")
	got, err := cmd.Output()
	if err != nil {
		t.Fatalf("real git rejected the sanitized config: %v", err)
	}
	if want := "alice\rbob\n"; string(got) != want {
		t.Fatalf("git config --get returned %q, want %q", got, want)
	}
}

func TestProcess_overrideSkipsUnsafeKey(t *testing.T) {
	out := Process("", nil, map[string]string{
		"user.name\n[core]\n\thooksPath": "/tmp/evil",
	})

	if out != "" {
		t.Fatalf("unsafe override key should be skipped, got:\n%s", out)
	}
}

// ── Process: combined operations ──────────────────────────────────────────────

func TestProcess_stripAndOverride(t *testing.T) {
	out := Process(sampleGitconfig,
		[]string{"credential"},
		map[string]string{"push.default": "nothing"},
	)
	if strings.Contains(out, "[credential]") {
		t.Errorf("expected credential stripped, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing") {
		t.Errorf("expected push.default overridden to nothing, got:\n%s", out)
	}
}

// ── Process: edge cases ───────────────────────────────────────────────────────

func TestProcess_emptyInput(t *testing.T) {
	out := Process("", nil, nil)
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for empty input, got: %q", out)
	}
}

func TestProcess_noOp(t *testing.T) {
	out := Process(sampleGitconfig, nil, nil)
	// Output must be non-empty and contain the original sections.
	if !strings.Contains(out, "[user]") || !strings.Contains(out, "[core]") {
		t.Errorf("no-op Process changed content unexpectedly:\n%s", out)
	}
}

func TestProcess_subsectionPreserved(t *testing.T) {
	input := `[remote "origin"]
	url = https://github.com/user/repo.git
[remote "upstream"]
	url = https://github.com/org/repo.git
`
	// Strip "remote" should remove BOTH remote sections.
	out := Process(input, []string{"remote"}, nil)
	if strings.Contains(out, "[remote") {
		t.Errorf("expected all remote sections stripped, got:\n%s", out)
	}
}

func TestProcess_override_multiInstanceSection_isNoOp(t *testing.T) {
	// [remote "origin"] and [remote "upstream"] both have section name "remote".
	// An override of "remote.url" cannot unambiguously target one subsection, so
	// it must be a no-op rather than silently modifying the first match.
	input := `[remote "origin"]
	url = https://github.com/user/repo.git
[remote "upstream"]
	url = https://github.com/org/repo.git
`
	out := Process(input, nil, map[string]string{"remote.url": "https://evil.example.com"})
	noopOut := Process(input, nil, nil)
	if out != noopOut {
		t.Errorf("override on multi-instance section must be a no-op, got:\n%s", out)
	}
}

func TestProcess_override_singleInstanceSection_stillApplied(t *testing.T) {
	// Overrides on sections that appear exactly once must still work.
	out := Process(sampleGitconfig, nil, map[string]string{"push.default": "nothing"})
	if !strings.Contains(out, "nothing") {
		t.Errorf("expected push.default overridden to 'nothing', got:\n%s", out)
	}
}

func TestProcess_trailingNewline(t *testing.T) {
	out := Process(sampleGitconfig, nil, nil)
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output should end with a newline, got: %q", out)
	}
	// Should not have double trailing newlines.
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("output should not have double trailing newline, got: %q", out)
	}
}

// ── SanitizeFrom ─────────────────────────────────────────────────────────────

func TestSanitizeFrom_createsFile(t *testing.T) {
	src := writeFixture(t, sampleGitconfig)
	cfg := &config.GitConfig{StripSections: []string{"credential"}}

	path, err := SanitizeFrom(src, cfg)
	if err != nil {
		t.Fatalf("SanitizeFrom: %v", err)
	}
	defer os.Remove(path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file not created: %v", err)
	}
}

func TestSanitizeFrom_contentIsProcessed(t *testing.T) {
	src := writeFixture(t, sampleGitconfig)
	cfg := &config.GitConfig{
		StripSections: []string{"credential"},
		Overrides:     map[string]string{"push.default": "nothing"},
	}

	path, err := SanitizeFrom(src, cfg)
	if err != nil {
		t.Fatalf("SanitizeFrom: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading temp file: %v", err)
	}
	out := string(data)

	if strings.Contains(out, "[credential]") {
		t.Errorf("expected credential stripped, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing") {
		t.Errorf("expected push.default = nothing, got:\n%s", out)
	}
}

func TestSanitizeFrom_missingSourceProducesEmptyFile(t *testing.T) {
	cfg := &config.GitConfig{}

	path, err := SanitizeFrom("/nonexistent/path/gitconfig-xyz", cfg)
	if err != nil {
		t.Fatalf("expected no error for missing source, got: %v", err)
	}
	defer os.Remove(path)

	data, _ := os.ReadFile(path)
	if strings.TrimSpace(string(data)) != "" {
		t.Errorf("expected empty file for missing source, got: %q", string(data))
	}
}

func TestSanitizeFrom_tempFileIsReadable(t *testing.T) {
	src := writeFixture(t, sampleGitconfig)
	cfg := &config.GitConfig{}

	path, err := SanitizeFrom(src, cfg)
	if err != nil {
		t.Fatalf("SanitizeFrom: %v", err)
	}
	defer os.Remove(path)

	if _, err := os.ReadFile(path); err != nil {
		t.Errorf("temp file not readable: %v", err)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "gitconfig-*")
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return f.Name()
}

// ── internal parser tests ─────────────────────────────────────────────────────

func TestParseSectionName(t *testing.T) {
	cases := []struct {
		line string
		name string
		ok   bool
	}{
		{"[user]", "user", true},
		{"[core]", "core", true},
		{`[remote "origin"]`, "remote", true},
		{`[branch "main"]`, "branch", true},
		{"    [indented]", "indented", true},
		{"key = value", "", false},
		{"# comment", "", false},
		{"", "", false},
		{"[unclosed", "", false},
	}
	for _, c := range cases {
		name, ok := parseSectionName(c.line)
		if ok != c.ok || name != c.name {
			t.Errorf("parseSectionName(%q) = (%q, %v), want (%q, %v)",
				c.line, name, ok, c.name, c.ok)
		}
	}
}

func TestParseKeyName(t *testing.T) {
	cases := []struct {
		line string
		key  string
		ok   bool
	}{
		{"\tname = John", "name", true},
		{"   email = x@x.com", "email", true},
		{"hooksPath = /foo", "hookspath", true}, // lowercased
		{"", "", false},
		{"# comment", "", false},
		{"; another comment", "", false},
		{"[section]", "", false},
		{"no-equals-sign", "", false},
	}
	for _, c := range cases {
		key, ok := parseKeyName(c.line)
		if ok != c.ok || key != c.key {
			t.Errorf("parseKeyName(%q) = (%q, %v), want (%q, %v)",
				c.line, key, ok, c.key, c.ok)
		}
	}
}
