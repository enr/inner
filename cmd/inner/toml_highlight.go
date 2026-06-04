package main

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ANSI colour codes.
const (
	ansiReset      = "\033[0m"
	ansiBold       = "\033[1m"
	ansiDim        = "\033[2m"
	ansiCyan       = "\033[36m"
	ansiGreen      = "\033[32m"
	ansiYellow     = "\033[33m"
	ansiBlue       = "\033[34m"
	ansiRed        = "\033[31m"
	ansiBoldRed    = "\033[1;31m"
	ansiBoldYellow = "\033[1;33m"
)

// colorizeW wraps text in ANSI colour codes only when w is a real TTY.
// Falls back to plain text for pipes, redirections, and when NO_COLOR is set.
func colorizeW(w io.Writer, code, text string) string {
	f, ok := w.(*os.File)
	if !ok || !isTTY(f) {
		return text
	}
	return code + text + ansiReset
}

// highlightTOML applies simple ANSI colours to a TOML string.
// It is line-oriented and covers the common cases without a full parser:
//
//	# comment          → dim
//	[section]          → bold cyan
//	key = "string"     → key bold, string green
//	key = true/false   → key bold, value yellow
//	key = 123          → key bold, value blue
//	key = [...]        → key bold, array items coloured individually
func highlightTOML(src string) string {
	lines := strings.Split(src, "\n")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(colourLine(line))
		sb.WriteByte('\n')
	}
	// Remove the trailing extra newline added by Split+Join.
	result := sb.String()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}
	return result
}

func colourLine(line string) string {
	trimmed := strings.TrimSpace(line)

	// Empty line.
	if trimmed == "" {
		return line
	}

	// Comment.
	if strings.HasPrefix(trimmed, "#") {
		return ansiDim + line + ansiReset
	}

	// Section header: [section] or [section.sub] — but NOT array values.
	// A section header line starts with '[' after optional whitespace.
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return ansiBold + ansiCyan + line + ansiReset
	}

	// Key = value.
	if idx := strings.Index(line, "="); idx > 0 {
		key := line[:idx]
		rest := line[idx+1:] // everything after '='
		return ansiBold + key + ansiReset + "=" + colourValue(rest)
	}

	return line
}

func colourValue(s string) string {
	trimmed := strings.TrimSpace(s)

	switch {
	case trimmed == "true" || trimmed == "false":
		return " " + ansiYellow + trimmed + ansiReset

	case len(trimmed) > 0 && trimmed[0] == '"':
		// Quoted string — colour the whole quoted span green.
		return " " + ansiGreen + trimmed + ansiReset

	case len(trimmed) > 0 && trimmed[0] == '[':
		// Inline array — colour brackets dim, items individually.
		return " " + colourArray(trimmed)

	case isNumeric(trimmed):
		return " " + ansiBlue + trimmed + ansiReset
	}

	return s
}

// colourArray colours an inline TOML array like ["a", "b"] or [true, 1].
func colourArray(s string) string {
	if len(s) < 2 {
		return s
	}
	inner := s[1 : len(s)-1] // strip outer [ ]
	items := strings.Split(inner, ",")
	var coloured []string
	for _, item := range items {
		coloured = append(coloured, colourValue(item))
	}
	return ansiDim + "[" + ansiReset + strings.Join(coloured, ansiDim+","+ansiReset) + ansiDim + "]" + ansiReset
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c == '.' {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isTTY reports whether w is a real terminal (honours NO_COLOR).
func isTTY(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
