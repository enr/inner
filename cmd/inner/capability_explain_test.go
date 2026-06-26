package main

import (
	"strings"
	"testing"
)

// ── 0c: Explain spec tests ─────────────────────────────────────────────────────
// These are pure-function tests that serve as executable specs: they define
// exactly what each capability must declare to inject at runtime.
// They must be written and GREEN before starting the capability migration.

func TestClaudeExplain_hasTwoMounts(t *testing.T) {
	e := claudeExplain()
	if len(e.Mounts) != 2 {
		t.Errorf("claudeExplain().Mounts: want 2 entries, got %d", len(e.Mounts))
	}
}

func TestClaudeExplain_firstMountIsClaudeHome(t *testing.T) {
	e := claudeExplain()
	if len(e.Mounts) == 0 {
		t.Fatal("claudeExplain().Mounts is empty")
	}
	if e.Mounts[0].Src != "~/.claude" {
		t.Errorf("first mount Src = %q, want ~/.claude", e.Mounts[0].Src)
	}
}

func TestClaudeExplain_secondMountIsClaudeJson(t *testing.T) {
	e := claudeExplain()
	if len(e.Mounts) < 2 {
		t.Fatal("claudeExplain() has fewer than 2 mounts")
	}
	if e.Mounts[1].Src != "~/.claude.json" {
		t.Errorf("second mount Src = %q, want ~/.claude.json", e.Mounts[1].Src)
	}
}

func TestClaudeExplain_hasPreRunTokenRefresh(t *testing.T) {
	e := claudeExplain()
	if len(e.PreRun) == 0 {
		t.Fatal("claudeExplain().PreRun is empty; expected at least one entry describing token refresh")
	}
	found := false
	for _, s := range e.PreRun {
		if strings.Contains(strings.ToLower(s), "token") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("claudeExplain().PreRun has no entry mentioning 'token': %v", e.PreRun)
	}
}

func TestGeminiExplain_hasOneMount(t *testing.T) {
	e := geminiExplain()
	if len(e.Mounts) != 1 {
		t.Errorf("geminiExplain().Mounts: want 1 entry, got %d", len(e.Mounts))
	}
}

func TestGeminiExplain_mountIsGeminiHome(t *testing.T) {
	e := geminiExplain()
	if len(e.Mounts) == 0 {
		t.Fatal("geminiExplain().Mounts is empty")
	}
	if e.Mounts[0].Src != "~/.gemini" {
		t.Errorf("mount Src = %q, want ~/.gemini", e.Mounts[0].Src)
	}
}

func TestOpencodeExplain_hasTwoMounts(t *testing.T) {
	e := opencodeExplain()
	if len(e.Mounts) != 2 {
		t.Errorf("opencodeExplain().Mounts: want 2 entries, got %d", len(e.Mounts))
	}
}

func TestOpencodeExplain_hasConfigMount(t *testing.T) {
	e := opencodeExplain()
	found := false
	for _, m := range e.Mounts {
		if m.Src == "~/.config/opencode" {
			found = true
			break
		}
	}
	if !found {
		t.Error("opencodeExplain().Mounts has no entry with Src=~/.config/opencode")
	}
}

func TestOpencodeExplain_hasDataMount(t *testing.T) {
	e := opencodeExplain()
	found := false
	for _, m := range e.Mounts {
		if m.Src == "~/.local/share/opencode" {
			found = true
			break
		}
	}
	if !found {
		t.Error("opencodeExplain().Mounts has no entry with Src=~/.local/share/opencode")
	}
}

func TestCursorExplain_hasTwoMounts(t *testing.T) {
	e := cursorExplain()
	if len(e.Mounts) != 2 {
		t.Errorf("cursorExplain().Mounts: want 2 entries, got %d", len(e.Mounts))
	}
}

func TestCursorExplain_hasCursorHomeMount(t *testing.T) {
	e := cursorExplain()
	found := false
	for _, m := range e.Mounts {
		if m.Src == "~/.cursor" {
			found = true
			break
		}
	}
	if !found {
		t.Error("cursorExplain().Mounts has no entry with Src=~/.cursor")
	}
}

func TestCursorExplain_hasCursorConfigMount(t *testing.T) {
	e := cursorExplain()
	found := false
	for _, m := range e.Mounts {
		if strings.Contains(m.Src, "cursor") && m.Src != "~/.cursor" {
			found = true
			break
		}
	}
	if !found {
		t.Error("cursorExplain().Mounts has no second cursor mount (expected ~/.config/cursor)")
	}
}
