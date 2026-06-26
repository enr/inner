package config

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// AutoLimits returns resource limits inferred from the host's available
// resources. It is used as the lowest-priority source in the resolution chain:
// auto-detected values are overridden by GlobalConfig.DefaultLimits and then
// by per-profile [sandbox.limits] and CLI flags.
//
// Algorithm:
//   Memory: half of total RAM, capped at [512M, 8G].
//   CPU:    50 % of logical CPUs (minimum 100 %).
//   Pids:   512 (conservative default for agent workloads).
func AutoLimits() ResourceLimits {
	return ResourceLimits{
		Memory: autoMemory(),
		CPU:    autoCPU(),
		Pids:   512,
	}
}

// autoMemory reads MemTotal from /proc/meminfo and returns half of it,
// clamped to the range [512M, 8G]. Falls back to "2G" if /proc/meminfo is
// unreadable (e.g. macOS, test environments).
func autoMemory() string {
	kb, err := readMemTotalKB()
	if err != nil {
		return "2G"
	}
	return memoryStringFromTotalKB(kb)
}

// memoryStringFromTotalKB converts a total-RAM value in KiB to the limit
// string that inner will apply (half of total, clamped to [512M, 8G]).
// Separated from autoMemory so tests can call it without needing /proc/meminfo.
func memoryStringFromTotalKB(totalKB int64) string {
	halfKB := totalKB / 2
	switch {
	case halfKB < 512*1024:
		return "512M"
	case halfKB >= 8*1024*1024:
		return "8G"
	default:
		gb := halfKB / (1024 * 1024)
		if gb == 0 {
			mb := halfKB / 1024
			return fmt.Sprintf("%dM", mb)
		}
		return fmt.Sprintf("%dG", gb)
	}
}

// autoCPU returns 50% of logical CPUs as a systemd CPUQuota string,
// minimum "100%".
func autoCPU() string {
	ncpu := runtime.NumCPU()
	pct := ncpu * 50
	if pct < 100 {
		pct = 100
	}
	return fmt.Sprintf("%d%%", pct)
}

// readMemTotalKB parses MemTotal from /proc/meminfo and returns the value in
// kibibytes. Returns an error if the file is absent or the line is missing.
func readMemTotalKB() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("unexpected MemTotal line: %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing MemTotal %q: %w", fields[1], err)
		}
		return kb, nil
	}
	return 0, fmt.Errorf("MemTotal not found in /proc/meminfo")
}

// NormalizeCPUQuota converts a CPU limit string to the systemd CPUQuota format
// (e.g. "200%"). Accepts:
//   - already-normalised strings ending in "%" (returned unchanged)
//   - decimal core counts as floats ("2.0" → "200%", "0.5" → "50%")
//
// Returns an error if the string cannot be parsed.
func NormalizeCPUQuota(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.HasSuffix(s, "%") {
		// Validate the numeric part.
		num := strings.TrimSuffix(s, "%")
		if _, err := strconv.ParseFloat(num, 64); err != nil {
			return "", fmt.Errorf("invalid CPU quota %q: %w", s, err)
		}
		return s, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("invalid CPU limit %q: expected a percentage (e.g. \"200%%\") or a decimal core count (e.g. \"2.0\"): %w", s, err)
	}
	if f <= 0 {
		return "", fmt.Errorf("invalid CPU limit %q: must be positive", s)
	}
	return fmt.Sprintf("%.0f%%", f*100), nil
}
