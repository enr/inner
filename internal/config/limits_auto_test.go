package config

import (
	"strings"
	"testing"
)

// TestAutoLimits_fields verifies AutoLimits returns non-empty, plausible values.
func TestAutoLimits_fields(t *testing.T) {
	l := AutoLimits()
	if l.Memory == "" {
		t.Error("AutoLimits.Memory is empty")
	}
	if !strings.HasSuffix(l.Memory, "M") && !strings.HasSuffix(l.Memory, "G") {
		t.Errorf("AutoLimits.Memory %q does not end with M or G", l.Memory)
	}
	if l.CPU == "" {
		t.Error("AutoLimits.CPU is empty")
	}
	if !strings.HasSuffix(l.CPU, "%") {
		t.Errorf("AutoLimits.CPU %q does not end with %%", l.CPU)
	}
	if l.Pids <= 0 {
		t.Errorf("AutoLimits.Pids = %d, want > 0", l.Pids)
	}
}

// TestMemoryStringFromTotalKB exercises the memory-calculation helper with
// representative values, without touching /proc/meminfo.
func TestMemoryStringFromTotalKB(t *testing.T) {
	cases := []struct {
		totalKB int64
		want    string
	}{
		// below 512 MB half → clamp to "512M"
		{256 * 1024, "512M"},      // 256 MB total → half 128 MB → clamped
		{800 * 1024, "512M"},      // 800 MB total → half 400 MB → clamped
		// exactly 512 MB half (1 GB total) → "512M"
		{1024 * 1024, "512M"},
		// 1 GB half → "1G"
		{2 * 1024 * 1024, "1G"},
		// 2 GB half → "2G"
		{4 * 1024 * 1024, "2G"},
		// 4 GB half → "4G"
		{8 * 1024 * 1024, "4G"},
		// at the cap boundary: half == 8 GB → "8G"
		{16 * 1024 * 1024, "8G"},
		// above cap: 64 GB total → "8G"
		{64 * 1024 * 1024, "8G"},
	}
	for _, tc := range cases {
		got := memoryStringFromTotalKB(tc.totalKB)
		if got != tc.want {
			t.Errorf("memoryStringFromTotalKB(%d KiB) = %q, want %q", tc.totalKB, got, tc.want)
		}
	}
}

// TestNormalizeCPUQuota covers all accepted input forms and expected errors.
func TestNormalizeCPUQuota(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"200%", "200%", false},
		{"100%", "100%", false},
		{"50%", "50%", false},
		{"2.0", "200%", false},
		{"0.5", "50%", false},
		{"4", "400%", false},
		{"1", "100%", false},
		{"  2.0  ", "200%", false}, // leading/trailing space
		{"", "", false},            // empty → no-op
		{"abc", "", true},
		{"0", "", true},    // zero not allowed
		{"-1.0", "", true}, // negative not allowed
	}
	for _, tc := range cases {
		got, err := NormalizeCPUQuota(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeCPUQuota(%q): expected error, got %q", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeCPUQuota(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeCPUQuota(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestResourceLimitsIsZero checks IsZero for the zero value and each field set.
func TestResourceLimitsIsZero(t *testing.T) {
	if !(ResourceLimits{}).IsZero() {
		t.Error("empty ResourceLimits should be zero")
	}
	if (ResourceLimits{Memory: "1G"}).IsZero() {
		t.Error("ResourceLimits{Memory} should not be zero")
	}
	if (ResourceLimits{CPU: "100%"}).IsZero() {
		t.Error("ResourceLimits{CPU} should not be zero")
	}
	if (ResourceLimits{Pids: 1}).IsZero() {
		t.Error("ResourceLimits{Pids} should not be zero")
	}
}
