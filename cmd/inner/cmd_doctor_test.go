package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctor_outputCoversAllChecks(t *testing.T) {
	app, _ := newTestApp(t)

	var buf bytes.Buffer
	if err := app.doctor(&buf); err != nil {
		t.Fatalf("doctor: %v", err)
	}

	out := buf.String()
	checks := []string{"bwrap", "user namespaces", "profiles", "logs", "ANTHROPIC_API_KEY", "claude", "display"}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("expected %q in doctor output, got:\n%s", check, out)
		}
	}
}

func TestDoctor_noError(t *testing.T) {
	app, _ := newTestApp(t)
	if err := app.doctor(&bytes.Buffer{}); err != nil {
		t.Errorf("doctor should not return an error, got: %v", err)
	}
}
