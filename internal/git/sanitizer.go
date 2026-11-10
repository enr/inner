package git

import (
	"fmt"
	"os"
	"path/filepath"
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
	for key, val := range overrides {
		sections = applyOverride(sections, key, val)
	}
	return serialize(sections)
}

// Sanitize reads ~/.gitconfig, applies cfg, writes the result to a temp file,
// and returns its path. The caller must os.Remove(path) when done.
func Sanitize(cfg *config.GitConfig) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return SanitizeFrom(filepath.Join(home, ".gitconfig"), cfg)
}

// SanitizeFrom is like Sanitize but reads from an explicit source path.
// If the source does not exist, the output is produced from an empty input.
// Used by tests to avoid touching the real ~/.gitconfig.
func SanitizeFrom(sourcePath string, cfg *config.GitConfig) (string, error) {
	data, err := os.ReadFile(sourcePath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("reading gitconfig %s: %w", sourcePath, err)
	}

	output := Process(string(data), cfg.StripSections, cfg.Overrides)

	f, err := os.CreateTemp("", "inner-gitconfig-*")
	if err != nil {
		return "", fmt.Errorf("creating temp gitconfig: %w", err)
	}
	if _, err := f.WriteString(output); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("writing temp gitconfig: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
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
// goes into the last of them — git reads the last value of a key, and
// sections may repeat — or into a new section when none matches.
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
	if last >= 0 {
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
