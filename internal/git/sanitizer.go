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
	header string   // e.g. `[core]` or `[remote "origin"]`; empty = pre-section
	name   string   // lowercase section name, e.g. "core"
	lines  []string // raw content lines: key-values, comments, blank lines
}

// Process applies strip and override operations to a gitconfig string.
// It is a pure function: no I/O, no side effects.
//
// strip format:
//   - "section"     → remove the entire [section] block
//   - "section.key" → remove only that key from [section]
//
// overrides format: "section.key" → value (upserts the key, creates section if needed)
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
		if name, ok := parseSectionName(line); ok {
			sections = append(sections, current)
			current = rawSection{header: line, name: name}
		} else {
			current.lines = append(current.lines, line)
		}
	}
	return append(sections, current)
}

// parseSectionName extracts the lowercase section name from a header line.
// Handles both [section] and [section "subsection"] forms.
func parseSectionName(line string) (name string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[1:end])
	// For [section "subsection"], stop at the first space or quote.
	if i := strings.IndexAny(inner, " \t\""); i >= 0 {
		return strings.ToLower(inner[:i]), true
	}
	return strings.ToLower(inner), true
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
	parts := strings.SplitN(strings.ToLower(strip), ".", 2)
	sectionName := parts[0]

	if len(parts) == 1 {
		// Remove entire matching sections.
		var out []rawSection
		for _, s := range sections {
			if s.name != sectionName {
				out = append(out, s)
			}
		}
		return out
	}

	// Remove a specific key from matching sections.
	keyName := parts[1]
	for i, s := range sections {
		if s.name != sectionName {
			continue
		}
		var filtered []string
		for _, line := range s.lines {
			if k, ok := parseKeyName(line); ok && k == keyName {
				continue
			}
			filtered = append(filtered, line)
		}
		sections[i].lines = filtered
	}
	return sections
}

// applyOverride sets section.key = value, upserting the key and creating
// the section if necessary. overrideKey format: "section.key".
// If multiple sections share the same name (e.g. [remote "origin"] and
// [remote "upstream"] both have name "remote"), the override is skipped: there
// is no way to target a specific subsection, and silently modifying the first
// match would be incorrect.
func applyOverride(sections []rawSection, overrideKey, value string) []rawSection {
	parts := strings.SplitN(strings.ToLower(overrideKey), ".", 2)
	if len(parts) != 2 {
		return sections // malformed key — skip silently
	}
	sectionName, keyName := parts[0], parts[1]
	if !isSafeOverrideNamePart(sectionName) || !isSafeOverrideNamePart(keyName) {
		return sections
	}

	// Count how many sections share this name; reject ambiguous multi-instance cases.
	count := 0
	for _, s := range sections {
		if s.name == sectionName {
			count++
		}
	}
	if count > 1 {
		return sections
	}

	newLine := fmt.Sprintf("\t%s = %s", keyName, quoteGitConfigValue(value))

	for i, s := range sections {
		if s.name != sectionName {
			continue
		}
		// Section found — update existing key or append.
		for j, line := range s.lines {
			if k, ok := parseKeyName(line); ok && k == keyName {
				sections[i].lines[j] = newLine
				return sections
			}
		}
		sections[i].lines = append(sections[i].lines, newLine)
		return sections
	}

	// Section not found — append a new one.
	return append(sections, rawSection{
		header: fmt.Sprintf("[%s]", sectionName),
		name:   sectionName,
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
