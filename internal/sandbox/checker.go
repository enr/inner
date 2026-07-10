package sandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/enr/inner/internal/config"
)

// Severity represents the importance of a check failure.
type Severity int

const (
	SeverityInfo     Severity = iota // resource explicitly allowed, or informational
	SeverityMedium                   // notable but not blocking
	SeverityHigh                     // significant risk
	SeverityCritical                 // must be fixed
)

// String returns the display label for a severity level.
func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "INFO"
	case SeverityMedium:
		return "MEDIUM"
	case SeverityHigh:
		return "HIGH"
	case SeverityCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// ParseSeverity parses a severity string (case-insensitive).
// Unknown strings default to SeverityMedium.
func ParseSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "critical":
		return SeverityCritical
	case "high":
		return SeverityHigh
	default:
		return SeverityMedium
	}
}

// CheckResult is the outcome of a single check.
type CheckResult struct {
	ID            string
	Name          string
	Passed        bool
	Severity      Severity
	Detail        string // extra context shown on failure (e.g. path found)
	Suggest       string // full suggest text shown with --suggest
	AllowOverride bool   // true when downgraded to INFO via [sandbox.allow]
}

// Report is the aggregated result of all checks.
type Report struct {
	Results []CheckResult
}

// HasCriticalFailures reports whether any CRITICAL check failed.
func (r Report) HasCriticalFailures() bool {
	for _, c := range r.Results {
		if !c.Passed && c.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

// HasHighFailures reports whether any HIGH check failed.
func (r Report) HasHighFailures() bool {
	for _, c := range r.Results {
		if !c.Passed && c.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// Conformant reports whether the sandbox passes all non-INFO checks.
func (r Report) Conformant() bool {
	for _, c := range r.Results {
		if !c.Passed && c.Severity > SeverityInfo {
			return false
		}
	}
	return true
}

// Render writes a human-readable report to w.
// If suggest is true, TOML snippets are appended to failed checks that have them.
func (r Report) Render(w io.Writer, suggest bool) {
	passed := 0
	counts := map[Severity]int{}

	for _, c := range r.Results {
		var symbol string
		switch {
		case c.AllowOverride:
			symbol = "[--]"
		case c.Passed:
			symbol = "[ok]"
		default:
			symbol = "[!!]"
		}

		if c.AllowOverride {
			fmt.Fprintf(w, "%s  %-8s  %s: explicitly allowed in profile\n", symbol, c.Severity, c.Name)
		} else {
			fmt.Fprintf(w, "%s  %-8s  %s\n", symbol, c.Severity, c.Name)
		}

		if c.Detail != "" && !c.AllowOverride {
			fmt.Fprintf(w, "             -> %s\n", c.Detail)
		}

		if suggest && !c.Passed && !c.AllowOverride && c.Suggest != "" {
			fmt.Fprintln(w)
			for _, line := range strings.Split(c.Suggest, "\n") {
				fmt.Fprintf(w, "             %s\n", line)
			}
			fmt.Fprintln(w)
		}

		if c.Passed {
			passed++
		} else {
			counts[c.Severity]++
		}
	}

	total := len(r.Results)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Result: %d/%d checks passed\n", passed, total)
	fmt.Fprintf(w, "CRITICAL: %d   HIGH: %d   MEDIUM: %d\n",
		counts[SeverityCritical], counts[SeverityHigh], counts[SeverityMedium])
	fmt.Fprintln(w)

	if r.Conformant() {
		fmt.Fprintln(w, "[ok]  sandbox conformant")
	} else {
		fmt.Fprintln(w, "[??]  sandbox non-conformant")
	}
}

// Checker executes built-in and custom sandbox checks.
// All checks are read-only — no side effects.
//
// Fields with zero values fall back to real OS calls and paths.
// Set them in tests to avoid filesystem and network dependencies.
type Checker struct {
	Allow          []string
	Custom         []config.CustomCheck
	NetworkEnabled bool // if true, network-policy check is skipped (network intentionally open)
	// ShimsExpected is true when the profile declares [noop] block/rewrite
	// entries, meaning a shim directory should be mounted and active in PATH.
	// When false the shims-active check passes unconditionally: a profile with
	// no noop config is not expected to have shims, so their absence is correct.
	ShimsExpected bool

	// Injectable for tests.
	HomeDir       string                                                                 // defaults to os.UserHomeDir()
	UsrDir        string                                                                 // defaults to "/usr"
	ShimMountPath string                                                                 // defaults to "/tmp/inner-shims"
	dialFn        func(network, address string, timeout time.Duration) (net.Conn, error) // defaults to net.DialTimeout
}

func (c *Checker) homeDir() string {
	if c.HomeDir != "" {
		return c.HomeDir
	}
	home, _ := os.UserHomeDir()
	return home
}

func (c *Checker) usrDir() string {
	if c.UsrDir != "" {
		return c.UsrDir
	}
	return "/usr"
}

func (c *Checker) shimMountPath() string {
	if c.ShimMountPath != "" {
		return c.ShimMountPath
	}
	return "/tmp/inner-shims"
}

func (c *Checker) dial(network, address string, timeout time.Duration) (net.Conn, error) {
	if c.dialFn != nil {
		return c.dialFn(network, address, timeout)
	}
	return net.DialTimeout(network, address, timeout)
}

func (c *Checker) isAllowed(checkID string) bool {
	for _, k := range c.Allow {
		if k == checkID {
			return true
		}
	}
	return false
}

// Run executes all built-in and custom checks and returns a Report.
func (c *Checker) Run() Report {
	results := []CheckResult{
		c.checkNoRoot(),
		c.checkUsrReadonly(),
		c.checkGitCredentials(),
		c.checkSSHKeys(),
		c.checkGPGKeys(),
		c.checkEnvSecrets(),
		c.checkDockerSocket(),
		c.checkNetrc(),
		c.checkShimsActive(),
		c.checkNetworkPolicy(),
	}
	for _, cc := range c.Custom {
		results = append(results, c.runCustomCheck(cc))
	}

	// Declassify results for explicitly allowed keys.
	for i := range results {
		if c.isAllowed(results[i].ID) && !results[i].Passed {
			results[i].Severity = SeverityInfo
			results[i].Passed = true
			results[i].AllowOverride = true
		}
	}
	return Report{Results: results}
}

// ── Built-in checks ───────────────────────────────────────────────────────────

func (c *Checker) checkNoRoot() CheckResult {
	r := CheckResult{ID: "no-root", Name: "user is not root", Severity: SeverityCritical}
	r.Passed = os.Getuid() != 0
	if !r.Passed {
		r.Detail = "process running as root (uid 0)"
	}
	return r
}

func (c *Checker) checkUsrReadonly() CheckResult {
	r := CheckResult{ID: "usr-readonly", Name: "/usr is read-only", Severity: SeverityCritical}
	probe, err := os.CreateTemp(c.usrDir(), ".inner-probe-*")
	if err != nil {
		r.Passed = true // can't write → read-only, as expected
	} else {
		probe.Close()
		os.Remove(probe.Name()) //nolint:errcheck
		r.Passed = false
		r.Detail = c.usrDir() + " is writable"
	}
	return r
}

func (c *Checker) checkGitCredentials() CheckResult {
	r := CheckResult{
		ID:       "git-credentials",
		Name:     "git credentials not exposed",
		Severity: SeverityCritical,
		Suggest:  suggestAllow("git-credentials"),
	}
	path := filepath.Join(c.homeDir(), ".git-credentials")
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		r.Passed = false
		r.Detail = "~/.git-credentials found"
		return r
	}
	r.Passed = true
	return r
}

func (c *Checker) checkSSHKeys() CheckResult {
	r := CheckResult{
		ID:       "ssh-keys",
		Name:     "~/.ssh not accessible",
		Severity: SeverityHigh,
		Suggest:  suggestAllow("ssh-keys"),
	}
	sshDir := filepath.Join(c.homeDir(), ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		r.Passed = true
		return r
	}
	for _, e := range entries {
		if isPrivateKeyFile(e.Name()) {
			r.Passed = false
			r.Detail = "~/.ssh/" + e.Name() + " found"
			return r
		}
	}
	r.Passed = true
	return r
}

// isPrivateKeyFile reports whether name looks like an SSH private key file
// (not a public key, known_hosts, config, or authorized_keys).
func isPrivateKeyFile(name string) bool {
	switch name {
	case "known_hosts", "config", "authorized_keys":
		return false
	}
	return !strings.HasSuffix(name, ".pub")
}

func (c *Checker) checkGPGKeys() CheckResult {
	r := CheckResult{
		ID:       "gpg-keys",
		Name:     "~/.gnupg not accessible",
		Severity: SeverityHigh,
		Suggest:  suggestAllow("gpg-keys"),
	}
	gnupgDir := filepath.Join(c.homeDir(), ".gnupg")
	entries, err := os.ReadDir(gnupgDir)
	if err != nil || len(entries) == 0 {
		r.Passed = true
		return r
	}
	r.Passed = false
	r.Detail = fmt.Sprintf("~/.gnupg not empty (%d entries)", len(entries))
	return r
}

func (c *Checker) checkEnvSecrets() CheckResult {
	r := CheckResult{ID: "env-secrets", Name: "no secrets in env vars", Severity: SeverityHigh}
	patterns := []string{"PASSWORD", "SECRET", "TOKEN", "CREDENTIAL", "PRIVATE_KEY"}
	var found []string
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		upper := strings.ToUpper(key)
		for _, p := range patterns {
			if strings.Contains(upper, p) {
				found = append(found, key)
				break
			}
		}
	}
	if len(found) > 0 {
		r.Passed = false
		r.Detail = "env vars with sensitive names: " + strings.Join(found, ", ")
		return r
	}
	r.Passed = true
	return r
}

func (c *Checker) checkDockerSocket() CheckResult {
	r := CheckResult{
		ID:       "docker-socket",
		Name:     "docker socket not accessible",
		Severity: SeverityMedium,
		Suggest:  suggestAllow("docker-socket"),
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		r.Passed = false
		r.Detail = "/var/run/docker.sock accessible"
		return r
	}
	r.Passed = true
	return r
}

func (c *Checker) checkNetrc() CheckResult {
	r := CheckResult{
		ID:       "netrc",
		Name:     "~/.netrc not accessible",
		Severity: SeverityMedium,
		Suggest:  suggestAllow("netrc"),
	}
	path := filepath.Join(c.homeDir(), ".netrc")
	info, err := os.Stat(path)
	if err == nil && info.Size() > 0 {
		r.Passed = false
		r.Detail = "~/.netrc found"
		return r
	}
	r.Passed = true
	return r
}

func (c *Checker) checkShimsActive() CheckResult {
	r := CheckResult{ID: "shims-active", Name: "shims active in PATH", Severity: SeverityMedium}
	if !c.ShimsExpected {
		r.Passed = true
		r.Detail = "no command shims configured for this profile"
		return r
	}
	shimPath := c.shimMountPath()
	if !strings.HasPrefix(os.Getenv("PATH"), shimPath) {
		r.Passed = false
		r.Detail = "PATH does not start with " + shimPath
		return r
	}
	if _, err := os.Stat(shimPath); err != nil {
		r.Passed = false
		r.Detail = shimPath + " not found in filesystem"
		return r
	}
	r.Passed = true
	return r
}

func (c *Checker) checkNetworkPolicy() CheckResult {
	r := CheckResult{ID: "network-policy", Name: "network restricted", Severity: SeverityMedium}
	if c.NetworkEnabled {
		r.Passed = true
		r.Detail = "network=true in profile"
		return r
	}
	conn, err := c.dial("tcp", "8.8.8.8:53", 2*time.Second)
	if err != nil {
		r.Passed = true
		return r
	}
	conn.Close()
	r.Passed = false
	r.Detail = "TCP connection to 8.8.8.8:53 succeeded"
	return r
}

// ── Custom checks ─────────────────────────────────────────────────────────────

func (c *Checker) runCustomCheck(cc config.CustomCheck) CheckResult {
	r := CheckResult{
		ID:       "custom:" + cc.Name,
		Name:     cc.Name,
		Severity: ParseSeverity(cc.Severity),
	}
	cmd := exec.Command("sh", "-c", cc.Cmd)
	r.Passed = cmd.Run() == nil
	if !r.Passed {
		r.Detail = "command exited with error"
	}
	return r
}

// ── Suggest helpers ───────────────────────────────────────────────────────────

func suggestAllow(key string) string {
	return fmt.Sprintf(
		"to hide it (recommended):\n  add nothing — this is the default behaviour\n\nto explicitly allow it if the agent needs it:\n  [sandbox]\n  allow = [\"%s\"]",
		key,
	)
}
