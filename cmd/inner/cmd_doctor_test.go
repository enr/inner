package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctor_outputCoversAllChecks(t *testing.T) {
	app, _ := newTestApp(t)

	var buf bytes.Buffer
	if err := app.doctor(&buf, false); err != nil {
		t.Fatalf("doctor: %v", err)
	}

	out := buf.String()
	checks := []string{"bwrap", "user namespaces", "profiles", "logs", "claude auth", "claude", "display", "terminal injection (TIOCSTI)"}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Errorf("expected %q in doctor output, got:\n%s", check, out)
		}
	}
}

func TestDoctor_noError(t *testing.T) {
	app, _ := newTestApp(t)
	if err := app.doctor(&bytes.Buffer{}, false); err != nil {
		t.Errorf("doctor should not return an error, got: %v", err)
	}
}

func TestDoctor_json(t *testing.T) {
	app, _ := newTestApp(t)
	var buf bytes.Buffer
	if err := app.doctor(&buf, true); err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	var report doctorReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	errs, warns := 0, 0
	seen := map[string]bool{}
	for _, c := range report.Checks {
		switch c.Status {
		case "error":
			errs++
		case "warning":
			warns++
		case "ok":
		default:
			t.Errorf("unexpected status %q", c.Status)
		}
		seen[c.Check] = true
	}
	if errs != report.Errors || warns != report.Warnings {
		t.Errorf("counts %d/%d do not match checks %d/%d", report.Errors, report.Warnings, errs, warns)
	}
	for _, want := range []string{"bwrap", "user namespaces", "profiles"} {
		if !seen[want] {
			t.Errorf("check %q missing from %v", want, seen)
		}
	}
}
