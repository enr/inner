package gitguard

import (
	"os"
	"path/filepath"
	"strings"
)

// configEntry is one key/value pair of a git config file. Section and key are
// lower-cased (git treats both case-insensitively); the subsection keeps its
// case, as git does for the quoted form.
type configEntry struct {
	section    string
	subsection string
	key        string
	value      string
	// hasValue is false for a bare "key" line, which git reads as boolean true.
	hasValue bool
}

// maxIncludeDepth bounds include recursion, as git itself does (it stops at 10).
const maxIncludeDepth = 10

// readConfigChain parses the config file at path and, recursively, every file
// it pulls in through include.path / includeIf.<cond>.path. Conditions are
// deliberately ignored: the caller protects what a file COULD include, and
// whether a condition matches depends on state the sandbox can change.
//
// It returns the entries of all files in order, plus the resolved paths of the
// include targets (existing or not). A missing or unreadable root file yields
// nothing — there is nothing to protect in it.
func readConfigChain(path string) (entries []configEntry, includes []string) {
	seen := make(map[string]bool)
	var walk func(string, int)
	walk = func(file string, depth int) {
		if depth > maxIncludeDepth || seen[file] {
			return
		}
		seen[file] = true
		data, err := os.ReadFile(file)
		if err != nil {
			return
		}
		for _, e := range parseConfig(string(data)) {
			entries = append(entries, e)
			if e.key != "path" || !e.hasValue {
				continue
			}
			if e.section != "include" && e.section != "includeif" {
				continue
			}
			target := resolveIncludePath(e.value, filepath.Dir(file))
			if target == "" {
				continue
			}
			includes = append(includes, target)
			walk(target, depth+1)
		}
	}
	walk(path, 0)
	return entries, includes
}

// resolveIncludePath resolves an include path the way git does: "~/" is the
// home directory, and a relative path is relative to the directory of the file
// containing the include.
func resolveIncludePath(value, baseDir string) string {
	p := expandTilde(value)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	return filepath.Clean(p)
}

// expandTilde expands a leading "~" or "~/" to the home directory. Other
// "~user" forms are returned unchanged: they never point into a sandbox mount
// the planner could protect anyway.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// lookup returns the value of the last entry matching section.key (git's
// "last one wins"), and whether any matched.
func lookup(entries []configEntry, section, key string) (configEntry, bool) {
	var found configEntry
	ok := false
	for _, e := range entries {
		if e.section == section && e.subsection == "" && e.key == key {
			found, ok = e, true
		}
	}
	return found, ok
}

// isTrue interprets an entry as a git boolean. Unknown spellings count as
// true: the callers only use this to decide whether to protect more, so
// erring towards "enabled" is the safe side.
func (e configEntry) isTrue() bool {
	if !e.hasValue {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(e.value)) {
	case "false", "no", "off", "0", "":
		return false
	}
	return true
}

// parseConfig parses the text of a git config file. It implements the subset
// of the format git documents in git-config(1) "CONFIGURATION FILE": section
// headers (with quoted or legacy dotted subsections), bare and valued keys,
// quoting, the \n \t \b \" \\ escapes, trailing-backslash continuation and
// # / ; comments. Malformed lines are skipped rather than fatal: the planner
// must still protect the files it can see even if one line is odd.
func parseConfig(text string) []configEntry {
	var (
		out        []configEntry
		section    string
		subsection string
	)
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		rest := strings.TrimLeft(line, " \t")
		if rest == "" || rest[0] == '#' || rest[0] == ';' {
			continue
		}
		if rest[0] == '[' {
			end := headerEnd(rest)
			if end < 0 {
				continue
			}
			section, subsection = parseHeader(rest[1:end])
			// A key may follow the header on the same line: "[core] bare = true".
			rest = strings.TrimLeft(rest[end+1:], " \t")
			if rest == "" || rest[0] == '#' || rest[0] == ';' {
				continue
			}
		}
		if section == "" {
			continue
		}
		key, raw, hasValue := splitKey(rest)
		if key == "" {
			continue
		}
		value := ""
		if hasValue {
			// Continuation: an unquoted, unescaped trailing backslash joins
			// the next line.
			for continues(raw) && i+1 < len(lines) {
				i++
				raw = raw[:len(raw)-1] + strings.TrimRight(lines[i], "\r")
			}
			value = parseValue(raw)
		}
		out = append(out, configEntry{
			section:    section,
			subsection: subsection,
			key:        strings.ToLower(key),
			value:      value,
			hasValue:   hasValue,
		})
	}
	return out
}

// headerEnd returns the index of the "]" closing a section header, skipping
// any inside the quoted subsection, or -1.
func headerEnd(s string) int {
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch {
		case s[i] == '\\' && inQuote:
			i++
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == ']' && !inQuote:
			return i
		}
	}
	return -1
}

// parseHeader splits `name "sub"` or the legacy `name.sub` form.
func parseHeader(h string) (section, subsection string) {
	h = strings.TrimSpace(h)
	if q := strings.IndexByte(h, '"'); q >= 0 {
		section = strings.ToLower(strings.TrimSpace(h[:q]))
		sub := h[q+1:]
		sub = strings.TrimSuffix(strings.TrimSpace(sub), `"`)
		var b strings.Builder
		for i := 0; i < len(sub); i++ {
			if sub[i] == '\\' && i+1 < len(sub) {
				i++
			}
			b.WriteByte(sub[i])
		}
		return section, b.String()
	}
	if dot := strings.IndexByte(h, '.'); dot >= 0 {
		// Legacy [section.sub] is case-insensitive in full.
		return strings.ToLower(h[:dot]), strings.ToLower(h[dot+1:])
	}
	return strings.ToLower(h), ""
}

// splitKey splits "key = value" / "key". It returns the key, the raw text
// after "=", and whether a "=" was present.
func splitKey(s string) (key, raw string, hasValue bool) {
	i := 0
	for i < len(s) && (isAlnum(s[i]) || s[i] == '-') {
		i++
	}
	key = s[:i]
	rest := strings.TrimLeft(s[i:], " \t")
	if rest == "" || rest[0] == '#' || rest[0] == ';' {
		return key, "", false
	}
	if rest[0] != '=' {
		return "", "", false
	}
	return key, rest[1:], true
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// continues reports whether raw ends with a line-continuation backslash that
// is neither escaped nor inside quotes and not part of a comment.
func continues(raw string) bool {
	inQuote := false
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			if i == len(raw)-1 {
				return true
			}
			i++
		case '"':
			inQuote = !inQuote
		case '#', ';':
			if !inQuote {
				return false
			}
		}
	}
	return false
}

// parseValue decodes a raw value: quotes are removed, escapes decoded,
// unquoted leading/trailing whitespace trimmed and an unquoted comment dropped.
func parseValue(raw string) string {
	var b strings.Builder
	inQuote := false
	// pending holds unquoted whitespace, flushed only if more text follows, so
	// trailing whitespace outside quotes is dropped.
	pending := ""
	started := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			b.WriteString(pending)
			pending = ""
			started = true
		case c == '\\' && i+1 < len(raw):
			i++
			b.WriteString(pending)
			pending = ""
			started = true
			switch raw[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			default:
				b.WriteByte(raw[i])
			}
		case !inQuote && (c == '#' || c == ';'):
			return b.String()
		case !inQuote && (c == ' ' || c == '\t'):
			if started {
				pending += string(c)
			}
		default:
			b.WriteString(pending)
			pending = ""
			started = true
			b.WriteByte(c)
		}
	}
	return b.String()
}
