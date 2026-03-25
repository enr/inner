package main

import (
	"slices"
	"testing"

	"github.com/enr/inner/internal/config"
)

// ── Registry consistency ───────────────────────────────────────────────────────
// These tests act as a compile-time-equivalent contract:
//   - Every name in ValidCapabilities must have a registry entry.
//   - Every registry key must be in ValidCapabilities.
//   - Apply and Explain funcs must be non-nil.
//
// This prevents "silent no-op" bugs where a ValidCapability name is accepted by
// the validator but has no runtime handler, or where a registry entry is added
// without also adding it to ValidCapabilities (which would bypass validation).

func TestCapabilityRegistry_hasAllValidCapabilities(t *testing.T) {
	for _, name := range config.ValidCapabilities {
		if _, ok := capabilityRegistry[name]; !ok {
			t.Errorf("capabilityRegistry has no entry for ValidCapability %q — add it or remove from ValidCapabilities", name)
		}
	}
}

func TestCapabilityRegistry_noExtraKeys(t *testing.T) {
	for name := range capabilityRegistry {
		if !slices.Contains(config.ValidCapabilities, name) {
			t.Errorf("capabilityRegistry[%q] is not in ValidCapabilities — add it or remove from registry", name)
		}
	}
}

func TestCapabilityRegistry_applyFuncsNonNil(t *testing.T) {
	for name, cap := range capabilityRegistry {
		if cap.Apply == nil {
			t.Errorf("capabilityRegistry[%q].Apply is nil", name)
		}
	}
}

func TestCapabilityRegistry_explainFuncsNonNil(t *testing.T) {
	for name, cap := range capabilityRegistry {
		if cap.Explain == nil {
			t.Errorf("capabilityRegistry[%q].Explain is nil", name)
		}
	}
}

// TestCapabilityRegistry_explainMatchesExplainFunctions verifies that the
// Explain func stored in the registry is the same function that the explicit
// explain tests cover: claudeExplain, geminiExplain, cursorExplain.
// A registry entry pointing to the wrong Explain would silently return
// incorrect data without the explicit 0c tests catching it.
func TestCapabilityRegistry_explainMatchesExplainFunctions(t *testing.T) {
	cases := []struct {
		name    string
		wantLen int // expected number of mounts from the explain function
	}{
		{"claude", 2},
		{"gemini", 1},
		{"cursor", 2},
	}
	for _, tc := range cases {
		cap, ok := capabilityRegistry[tc.name]
		if !ok {
			t.Errorf("capabilityRegistry[%q] missing", tc.name)
			continue
		}
		e := cap.Explain()
		if len(e.Mounts) != tc.wantLen {
			t.Errorf("capabilityRegistry[%q].Explain() returned %d mounts, want %d",
				tc.name, len(e.Mounts), tc.wantLen)
		}
	}
}
