package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/enr/inner/internal/config"
)

// rawSection holds one parsed gitconfig section.
// The first element in a parsed slice may have an empty header
// when the file contains content before the first [section].
type rawSection struct {
	header string // e.g. `[core]` or `[remote "origin"]`; empty = pre-section
	name   string // lowercase section name, e.g. "core"
	// sub is the subsection ("origin" in [remote "origin"]), compared
	// case-sensitively as git does; hasSub tells [remote ""] from [remote].
	sub    string
	hasSub bool
	lines  []string // raw content lines: key-values, comments, blank lines
}

// Process applies strip and override operations to a gitconfig string.
// It is a pure function: no I/O, no side effects.
//
// Keys are spelled the way `git config` spells them: "section.key" or
// "section.subsection.key", where the subsection is everything between the
// first and the last dot ("url.https://github.com/.insteadOf").
//
// strip format:
//   - "section"                → remove every [section] block, subsections included
//   - "section.key"            → remove that key from every [section …] block
//   - "section.subsection.key" → remove that key from [section "subsection"] only
//
// overrides format: "section[.subsection].key" → value (upserts the key,
// creates the section if needed)
func Process(input string, stripSections []string, overrides map[string]string) string {
	sections := parse(input)
	for _, strip := range stripSections {
		sections = applyStrip(sections, strip)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		sections = applyOverride(sections, key, overrides[key])
	}
	return serialize(sections)
}

// MainFile is the name of the sanitized global config inside the directory
// Sanitize writes; the copies of [includeIf] targets sit next to it.
const MainFile = "config"

// maxIncludeDepth is git's own limit on nested includes.
const maxIncludeDepth = 10

// Sanitize builds the git configuration the sandbox gets as its global one
// (GIT_CONFIG_GLOBAL) from the host's: $XDG_CONFIG_HOME/git/config (or
// ~/.config/git/config) and then ~/.gitconfig, the order git itself reads
// them in — once GIT_CONFIG_GLOBAL is set, git reads neither.
//
// It writes a fresh temp directory and returns the path of MainFile in it;
// the caller removes the directory. sandboxDir is where the isolator binds
// that directory in the sandbox: [includeIf] paths are rewritten to point
// there. See SanitizeFrom for how includes are handled.
func Sanitize(cfg *config.GitConfig, sandboxDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return SanitizeFrom([]string{filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig")}, cfg, sandboxDir)
}

// SanitizeFrom is Sanitize reading the given sources, in increasing
// precedence. Missing sources are skipped.
//
// Includes are resolved here, on the host, so that strip_sections reaches
// what they contain — a credential.helper kept in an included file used to
// survive the strip, read by git inside the sandbox:
//   - [include] path = … is inlined where it stands, recursively;
//   - [includeIf "…"] path = … is conditional and cannot be inlined: its
//     target is copied, with its own includes resolved and strip_sections
//     applied, next to the main file, and the path is rewritten to the copy
//     as the sandbox sees it (sandboxDir).
//
// Relative include paths resolve against the including file's directory, as
// in git; a target that cannot be read is dropped, as git ignores it.
// Overrides are applied last, at the end of the main file, so they win over
// every included value.
func SanitizeFrom(sources []string, cfg *config.GitConfig, sandboxDir string) (string, error) {
	dir, err := os.MkdirTemp("", "inner-gitconfig-*")
	if err != nil {
		return "", fmt.Errorf("creating gitconfig dir: %w", err)
	}
	home, _ := os.UserHomeDir()
	e := &includeExpander{strip: cfg.StripSections, home: home, sandboxDir: sandboxDir, files: map[string]string{}}

	var main strings.Builder
	for _, src := range sources {
		data, err := os.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("reading gitconfig %s: %w", src, err)
		}
		main.WriteString(e.expand(string(data), filepath.Dir(src), 0))
	}
	e.files[MainFile] = Process(main.String(), cfg.StripSections, cfg.Overrides)

	for name, content := range e.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("writing gitconfig: %w", err)
		}
	}
	return filepath.Join(dir, MainFile), nil
}

// includeExpander resolves the includes of one Sanitize call.
type includeExpander struct {
	strip      []string
	home       string
	sandboxDir string
	files      map[string]string // file name in the output dir → content
	n          int
}

// expand returns content with its [include] targets inlined and its
// [includeIf] targets replaced by sanitized copies (see SanitizeFrom).
func (e *includeExpander) expand(content, baseDir string, depth int) string {
	var out []rawSection
	for _, s := range parse(content) {
		switch {
		case s.name == "include" && !s.hasSub:
			for _, line := range s.lines {
				target, ok := includePath(line, baseDir, e.home)
				if !ok || depth >= maxIncludeDepth {
					continue
				}
				data, err := os.ReadFile(target)
				if err != nil {
					continue
				}
				out = append(out, parse(e.expand(string(data), filepath.Dir(target), depth+1))...)
			}
		case s.name == "includeif":
			kept := s
			kept.lines = nil
			for _, line := range s.lines {
				target, ok := includePath(line, baseDir, e.home)
				if !ok {
					kept.lines = append(kept.lines, line)
					continue
				}
				data, err := os.ReadFile(target)
				if err != nil || depth >= maxIncludeDepth {
					continue
				}
				name := fmt.Sprintf("include-%d.config", e.n)
				e.n++
				e.files[name] = Process(e.expand(string(data), filepath.Dir(target), depth+1), e.strip, nil)
				kept.lines = append(kept.lines, "\tpath = "+quoteGitConfigValue(e.sandboxDir+"/"+name))
			}
			out = append(out, kept)
		default:
			out = append(out, s)
		}
	}
	return serialize(out)
}

// includePath returns the file a "path = …" line of an include section names,
// resolved the way git resolves it: ~/ is the home directory, a relative path
// is relative to the directory of the file holding the include.
func includePath(line, baseDir, home string) (string, bool) {
	if key, ok := parseKeyName(line); !ok || key != "path" {
		return "", false
	}
	_, raw, _ := strings.Cut(line, "=")
	p := unquoteGitConfigValue(strings.TrimSpace(raw))
	switch {
	case p == "":
		return "", false
	case p == "~" || strings.HasPrefix(p, "~/"):
		if home == "" {
			return "", false
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	case !filepath.IsAbs(p):
		p = filepath.Join(baseDir, p)
	}
	return filepath.Clean(p), true
}

// unquoteGitConfigValue undoes the quoting and escapes of a config value.
func unquoteGitConfigValue(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"':
		case c == '\\' && i+1 < len(v):
			i++
			switch v[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			default:
				b.WriteByte(v[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ── Internal parser ───────────────────────────────────────────────────────────

// parse splits a gitconfig string into sections.
func parse(input string) []rawSection {
	input = strings.TrimRight(input, "\n")
	if input == "" {
		return nil
	}

	var sections []rawSection
	current := rawSection{}

	for _, line := range strings.Split(input, "\n") {
		if name, sub, hasSub, ok := parseSectionHeader(line); ok {
			sections = append(sections, current)
			current = rawSection{header: line, name: name, sub: sub, hasSub: hasSub}
		} else {
			current.lines = append(current.lines, line)
		}
	}
	return append(sections, current)
}

// parseSectionName extracts the lowercase section name from a header line.
// Handles both [section] and [section "subsection"] forms.
func parseSectionName(line string) (name string, ok bool) {
	name, _, _, ok = parseSectionHeader(line)
	return name, ok
}

// parseSectionHeader parses a section header line: [section],
// [section "subsection"] (the subsection keeps its case; \" and \\ are
// unescaped) and the legacy [section.subsection] (subsection lower-cased, as
// git does).
func parseSectionHeader(line string) (name, sub string, hasSub, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", "", false, false
	}
	end := strings.LastIndexByte(trimmed, ']')
	if end < 0 {
		return "", "", false, false
	}
	inner := strings.TrimSpace(trimmed[1:end])
	if i := strings.IndexAny(inner, " \t\""); i >= 0 {
		name = strings.ToLower(inner[:i])
		rest := strings.TrimSpace(inner[i:])
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			return name, unescapeSubsection(rest[1 : len(rest)-1]), true, true
		}
		return name, "", false, true
	}
	if i := strings.IndexByte(inner, '.'); i >= 0 {
		return strings.ToLower(inner[:i]), strings.ToLower(inner[i+1:]), true, true
	}
	return strings.ToLower(inner), "", false, true
}

func unescapeSubsection(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ValidKey reports whether s is a git config key [git] overrides can set:
// "section.key" or "section.subsection.key", with no character that could
// break out of the generated header or key line.
func ValidKey(s string) bool {
	k, ok := parseConfigKey(s)
	return ok && isSafeOverrideNamePart(k.section) && isSafeOverrideNamePart(k.key) &&
		!strings.ContainsAny(k.section, " \"\t.") &&
		!(k.hasSub && strings.ContainsAny(k.sub, "\n\r\x00"))
}

// configKey is a "section[.subsection].key" spelling split into its parts.
type configKey struct {
	section string // as written; compared case-insensitively
	sub     string
	hasSub  bool
	key     string // as written; compared case-insensitively
}

// parseConfigKey splits a key the way git does: the section is up to the
// first dot, the key after the last one, the subsection whatever lies
// between (it may contain dots itself: a URL).
func parseConfigKey(s string) (configKey, bool) {
	first, last := strings.IndexByte(s, '.'), strings.LastIndexByte(s, '.')
	if first < 0 {
		return configKey{}, false
	}
	k := configKey{section: s[:first], key: s[last+1:]}
	if first != last {
		k.sub, k.hasSub = s[first+1:last], true
	}
	return k, true
}

// matches reports whether section s holds this key's section and subsection.
func (k configKey) matches(s rawSection) bool {
	return s.name == strings.ToLower(k.section) && s.hasSub == k.hasSub && s.sub == k.sub
}

// header renders the section header for k, quoting the subsection.
func (k configKey) header() string {
	if !k.hasSub {
		return "[" + k.section + "]"
	}
	sub := strings.ReplaceAll(k.sub, `\`, `\\`)
	sub = strings.ReplaceAll(sub, `"`, `\"`)
	return "[" + k.section + ` "` + sub + `"]`
}

// parseKeyName extracts the lowercase key from a "  key = value" line.
// Returns ("", false) for blank lines, comments, and section headers.
func parseKeyName(line string) (key string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" ||
		strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, ";") ||
		strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	idx := strings.IndexByte(trimmed, '=')
	if idx < 0 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(trimmed[:idx])), true
}

// applyStrip removes an entire section or a specific key within a section.
func applyStrip(sections []rawSection, strip string) []rawSection {
	k, isKey := parseConfigKey(strip)
	if !isKey {
		// Remove entire matching sections, subsections included.
		sectionName := strings.ToLower(strip)
		var out []rawSection
		for _, s := range sections {
			if s.name != sectionName {
				out = append(out, s)
			}
		}
		return out
	}

	// Remove a specific key: from every section of that name for
	// "section.key", from the named subsection only for a three-part key.
	keyName := strings.ToLower(k.key)
	for i, s := range sections {
		if s.name != strings.ToLower(k.section) || (k.hasSub && (!s.hasSub || s.sub != k.sub)) {
			continue
		}
		var filtered []string
		for _, line := range s.lines {
			if name, ok := parseKeyName(line); ok && name == keyName {
				continue
			}
			filtered = append(filtered, line)
		}
		sections[i].lines = filtered
	}
	return sections
}

// applyOverride sets section[.subsection].key = value. Every existing
// occurrence of the key in the matching sections is removed and the new line
// goes at the end of the file — git reads the last value of a key.
//
// [remote "origin"] and [remote "upstream"] are different sections: an
// override names the one it means ("remote.origin.url"), and "remote.url"
// matches neither.
func applyOverride(sections []rawSection, overrideKey, value string) []rawSection {
	if !ValidKey(overrideKey) {
		return sections // malformed or unsafe key: the validator reports it
	}
	k, _ := parseConfigKey(overrideKey)
	keyName := strings.ToLower(k.key)
	newLine := fmt.Sprintf("\t%s = %s", k.key, quoteGitConfigValue(value))

	last := -1
	for i, s := range sections {
		if !k.matches(s) {
			continue
		}
		last = i
		var kept []string
		for _, line := range s.lines {
			if name, ok := parseKeyName(line); ok && name == keyName {
				continue
			}
			kept = append(kept, line)
		}
		sections[i].lines = kept
	}
	// At the very end of the file: after every include, so the override wins
	// over a value an included file sets. Into the last section when it is a
	// matching one, otherwise in a new section.
	if last >= 0 && last == len(sections)-1 {
		sections[last].lines = append(sections[last].lines, newLine)
		return sections
	}
	return append(sections, rawSection{
		header: k.header(),
		name:   strings.ToLower(k.section),
		sub:    k.sub,
		hasSub: k.hasSub,
		lines:  []string{newLine},
	})
}

func isSafeOverrideNamePart(part string) bool {
	return part != "" &&
		strings.TrimSpace(part) == part &&
		!strings.ContainsAny(part, "\n\r[]=")
}

func quoteGitConfigValue(value string) string {
	if !needsGitConfigQuoting(value) {
		return value
	}

	var sb strings.Builder
	sb.Grow(len(value) + 2)
	sb.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\', '"':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			// Not `\r`: git's own config parser has no such escape (a value
			// containing a literal `\r` two-char sequence is a fatal "bad
			// config line"). git itself writes a raw CR byte inside the
			// quotes, so match that instead of inventing an escape.
			sb.WriteByte('\r')
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func needsGitConfigQuoting(value string) bool {
	return strings.ContainsAny(value, "\\\"\n\r\t\b[]")
}

// serialize converts sections back to a gitconfig string.
func serialize(sections []rawSection) string {
	var sb strings.Builder
	for _, s := range sections {
		if s.header == "" && len(s.lines) == 0 {
			continue
		}
		if s.header != "" {
			sb.WriteString(s.header)
			sb.WriteByte('\n')
		}
		for _, line := range s.lines {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	result := sb.String()
	if result == "" {
		return ""
	}
	return strings.TrimRight(result, "\n") + "\n"
}
