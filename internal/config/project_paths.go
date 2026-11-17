package config

import (
	"path/filepath"
	"strings"
)

// projectRootOf returns the project directory a project config file belongs
// to: the directory that holds its .config/ folder, or the file's own
// directory for the top-level inner.toml / inner.local.toml.
func projectRootOf(configPath string) string {
	dir := filepath.Dir(configPath)
	switch {
	case filepath.Base(dir) == ".config":
		return filepath.Dir(dir)
	case filepath.Base(dir) == "inner" && filepath.Base(filepath.Dir(dir)) == ".config":
		return filepath.Dir(filepath.Dir(dir))
	}
	return dir
}

// resolveProjectPaths makes the relative profile paths of a project config
// relative to its project root instead of the working directory. Project
// configs are inherited by every directory below them (see
// collectProjectConfigFiles), so a default_profile such as
// ".config/inner/profiles/dev.toml" written in ~/Projects must keep pointing
// at ~/Projects/.config/... when inner runs in ~/Projects/foo.
//
// It rewrites default_profile and the -p / --profile values of the aliases;
// profile names (no path separator, no .toml suffix) are left alone.
func resolveProjectPaths(cfg *GlobalConfig, root string) {
	cfg.DefaultProfile = resolveProfileValue(cfg.DefaultProfile, root)
	for name, expansion := range cfg.Aliases {
		cfg.Aliases[name] = resolveAliasProfiles(expansion, root)
	}
}

// resolveProfileValue anchors a relative profile path at root. Names, absolute
// paths and paths that are absolute once ~ and $VARs are expanded are
// returned unchanged.
func resolveProfileValue(v, root string) string {
	if v == "" || !IsProfilePath(v) || filepath.IsAbs(ExpandPath(v)) {
		return v
	}
	return filepath.Join(root, v)
}

// resolveAliasProfiles rewrites the profile values of -p / --profile flags in
// an alias expansion with resolveProfileValue. Arguments after "--" belong to
// the sandboxed command (e.g. `claude -p "prompt"`) and are never touched.
func resolveAliasProfiles(expansion, root string) string {
	spans := argSpans(expansion)
	var b strings.Builder
	last := 0
	replace := func(sp argSpan, val string) {
		b.WriteString(expansion[last:sp.start])
		b.WriteString(val)
		last = sp.end
	}
	for i := 0; i < len(spans); i++ {
		tok := spans[i].val
		if tok == "--" {
			break
		}
		switch {
		case (tok == "-p" || tok == "--profile") && i+1 < len(spans):
			i++
			if r := resolveProfileValue(spans[i].val, root); r != spans[i].val {
				replace(spans[i], quoteArg(r))
			}
		case strings.HasPrefix(tok, "--profile="):
			if r := resolveProfileValue(tok[len("--profile="):], root); r != tok[len("--profile="):] {
				replace(spans[i], quoteArg("--profile="+r))
			}
		case strings.HasPrefix(tok, "-p") && !strings.HasPrefix(tok, "--") && len(tok) > 2:
			// -pVALUE and -p=VALUE, as pflag accepts them.
			v := strings.TrimPrefix(tok[2:], "=")
			if r := resolveProfileValue(v, root); r != v {
				replace(spans[i], quoteArg("-p="+r))
			}
		}
	}
	if last == 0 {
		return expansion
	}
	b.WriteString(expansion[last:])
	return b.String()
}

// argSpan is one argument of a command string: its unquoted value and the
// byte range it occupies in the source.
type argSpan struct {
	val        string
	start, end int
}

// SplitArgs splits a command string into arguments, honouring single and
// double quotes so an alias expansion can carry arguments that contain spaces
// (e.g. `--arg "fix the bug"`). A backslash escapes the next character except
// inside single quotes. Unterminated quotes run to the end of the string.
// It is a small shell-like tokenizer, not a full POSIX word splitter.
func SplitArgs(s string) []string {
	var args []string
	for _, sp := range argSpans(s) {
		args = append(args, sp.val)
	}
	return args
}

// argSpans is SplitArgs keeping the source position of each argument.
func argSpans(s string) []argSpan {
	var spans []argSpan
	var cur strings.Builder
	inArg := false
	start := 0
	var quote rune // 0, '\'' or '"'
	escaped := false
	begin := func(i int) {
		if !inArg {
			inArg = true
			start = i
		}
	}
	for i, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			begin(i)
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			begin(i)
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				spans = append(spans, argSpan{cur.String(), start, i})
				cur.Reset()
				inArg = false
			}
		default:
			begin(i)
			cur.WriteRune(r)
		}
	}
	if inArg {
		spans = append(spans, argSpan{cur.String(), start, len(s)})
	}
	return spans
}

// quoteArg returns s as a single SplitArgs argument, single-quoting it when it
// contains whitespace, quotes or backslashes.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
