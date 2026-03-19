package executor

import (
	"regexp"
	"testing"
	"time"
)

var runIDPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`)

func TestGenerateRunID_format(t *testing.T) {
	id := GenerateRunID()
	if !runIDPattern.MatchString(id) {
		t.Errorf("RunID %q does not match expected format YYYYMMDD-HHMMSS-xxxx", id)
	}
}

func TestGenerateRunID_unique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 20; i++ {
		id := GenerateRunID()
		if ids[id] {
			t.Errorf("duplicate RunID generated: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateRunID_usesTime(t *testing.T) {
	fixed := time.Date(2026, 3, 19, 14, 30, 52, 0, time.UTC)
	id := generateRunID(fixed)
	if len(id) < 15 || id[:15] != "20260319-143052" {
		t.Errorf("RunID %q does not start with expected timestamp prefix", id)
	}
}
