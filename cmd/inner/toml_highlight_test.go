package main

import (
	"strings"
	"testing"
)

func TestHighlightTOML_commentIsDim(t *testing.T) {
	out := colourLine("# this is a comment")
	if !strings.Contains(out, ansiDim) {
		t.Errorf("comment line should be dim, got: %q", out)
	}
}

func TestHighlightTOML_sectionIsCyan(t *testing.T) {
	out := colourLine("[entrypoint]")
	if !strings.Contains(out, ansiCyan) {
		t.Errorf("section header should be cyan, got: %q", out)
	}
}

func TestHighlightTOML_keyValueStringIsGreen(t *testing.T) {
	out := colourLine(`cmd = "bash"`)
	if !strings.Contains(out, ansiGreen) {
		t.Errorf("string value should be green, got: %q", out)
	}
}

func TestHighlightTOML_boolIsYellow(t *testing.T) {
	out := colourLine("interactive = true")
	if !strings.Contains(out, ansiYellow) {
		t.Errorf("bool value should be yellow, got: %q", out)
	}
}

func TestHighlightTOML_numberIsBlue(t *testing.T) {
	out := colourLine("timeout = 120")
	if !strings.Contains(out, ansiBlue) {
		t.Errorf("numeric value should be blue, got: %q", out)
	}
}

func TestHighlightTOML_emptyLinePassthrough(t *testing.T) {
	out := colourLine("")
	if out != "" {
		t.Errorf("empty line should be unchanged, got: %q", out)
	}
}

func TestHighlightTOML_preservesLineCount(t *testing.T) {
	input := "[section]\nkey = \"val\"\n# comment\n"
	out := highlightTOML(input)
	if strings.Count(out, "\n") != strings.Count(input, "\n") {
		t.Errorf("line count changed: input %d, output %d",
			strings.Count(input, "\n"), strings.Count(out, "\n"))
	}
}
