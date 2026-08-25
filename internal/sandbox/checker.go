package sandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	// HomeIsolated is true when the profile declares [sandbox] home =
	// "isolated". The home-isolated check then asserts that $HOME really is the
	// empty tmpfs the profile asked for, instead of the host home showing
	// through the read-only root bind.
	HomeIsolated bool
	// ShimsExpected is true when the profile declares [noop] block/rewrite
	// entries, meaning a shim directory should be mounted and active in PATH.
	// When false the shims-active check passes unconditionally: a profile with
	// no noop config is not expected to have shims, so their absence is correct.
	ShimsExpected bool

	// Injectable for tests.
	HomeDir       string                                                                 // defaults to os.UserHomeDir()
	UsrDir        string                                                                 // defaults to "/usr"
	ShimMountPath string                                                                 // defaults to "/tmp/inner-shims"
	MountInfoPath string                                                                 // defaults to "/proc/self/mountinfo"
	dialFn        func(network, address string, timeout time.Duration) (net.Conn, error) // defaults to net.DialTimeout
}

func (c *Checker) mountInfoPath() string {
	if c.MountInfoPath != "" {
		return c.MountInfoPath
	}
	return "/proc/self/mountinfo"
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
		c.checkHomeIsolated(),
		c.checkShimsActive(),
		c.checkNetworkPolicy(),
	}
	results = append(results, c.checkSensitiveResources()...)
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

// checkUsrReadonly used to probe by attempting to create a file in usrDir and
// treating write failure as "read-only". That fails open: run outside a
// sandbox as a normal, non-root user, /usr is not writable anyway (regular
// Unix permissions already forbid it), so the check reported "conformant"
// while having verified nothing about the sandbox's own read-only bind. A
// check that can only fail open gives false assurance, which is the wrong
// direction for a Critical check.
//
// It now reads the mount actually covering usrDir from mountinfo and checks
// its "ro" option directly — the same signal the kernel itself uses, and the
// same approach checkHomeIsolated already takes for $HOME.
func (c *Checker) checkUsrReadonly() CheckResult {
	r := CheckResult{ID: "usr-readonly", Name: "/usr is read-only", Severity: SeverityCritical}
	data, err := os.ReadFile(c.mountInfoPath())
	if err != nil {
		r.Passed = false
		r.Detail = fmt.Sprintf("cannot read %s: %v", c.mountInfoPath(), err)
		return r
	}
	ro, found := mountReadOnly(string(data), c.usrDir())
	if !found {
		r.Passed = false
		r.Detail = c.usrDir() + ": no covering mount found in " + c.mountInfoPath()
		return r
	}
	if !ro {
		r.Passed = false
		r.Detail = c.usrDir() + " is mounted read-write"
		return r
	}
	r.Passed = true
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

// checkEnvSecrets is a name-only heuristic, not a guarantee: it flags
// environment variable *names* that look like they hold a secret. It has no
// way to know that, say, OPENAI_API_KEY or AWS_ACCESS_KEY_ID holds one when
// the name doesn't contain a pattern it looks for, and no way to know that a
// variable matching a pattern doesn't just hold a placeholder. Severity is
// Medium (not High/Critical) precisely because a name match is suggestive,
// not conclusive — this check should nudge a profile author to double-check
// [env] inherit, not fail a sandbox that already handles its secrets some
// other way.
func (c *Checker) checkEnvSecrets() CheckResult {
	r := CheckResult{ID: "env-secrets", Name: "no obviously-named secrets in env vars", Severity: SeverityMedium}
	patterns := []string{
		"PASSWORD", "PASSWD", "SECRET", "TOKEN", "CREDENTIAL",
		"PRIVATE_KEY", "API_KEY", "ACCESS_KEY", "_KEY", "AUTH",
	}
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
		r.Detail = "env vars with names that look like secrets (heuristic, may include false positives): " + strings.Join(found, ", ")
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

// checkHomeIsolated asserts that a profile declaring home = "isolated" really
// runs on an empty home tmpfs. It is the direct counter-check to the denylist
// model: if the home mount is anything other than a tmpfs, the host home is
// showing through the read-only root bind and every path the hide list does not
// know about (browser profiles, ~/.config/gh, .env files) is readable.
func (c *Checker) checkHomeIsolated() CheckResult {
	r := CheckResult{ID: "home-isolated", Name: "home directory isolated (tmpfs)", Severity: SeverityHigh}
	if !c.HomeIsolated {
		r.Passed = true
		r.Detail = `profile does not request home = "isolated"`
		return r
	}
	home := c.homeDir()
	if home == "" {
		r.Passed = false
		r.Detail = "cannot determine home directory"
		return r
	}
	data, err := os.ReadFile(c.mountInfoPath())
	if err != nil {
		r.Passed = false
		r.Detail = fmt.Sprintf("cannot read %s: %v", c.mountInfoPath(), err)
		return r
	}
	fsType, found := mountFsType(string(data), filepath.Clean(home))
	if !found {
		r.Passed = false
		r.Detail = home + " is not a mount point — the host home is visible through the root bind"
		return r
	}
	if fsType != "tmpfs" {
		r.Passed = false
		r.Detail = fmt.Sprintf("%s is mounted as %q, expected tmpfs", home, fsType)
		return r
	}
	r.Passed = true
	return r
}

// mountFsType returns the filesystem type mounted at mountPoint according to
// the content of a /proc/<pid>/mountinfo file, and whether such a mount exists.
// The LAST matching entry wins: mounts are listed in mount order and a later
// one shadows an earlier one at the same path.
//
// Line layout (mountinfo(5)):
//
//	36 35 98:0 /src /mount/point rw,… [optional fields] - ext3 /dev/root rw,…
//	                     ^ field 4                        ^ fstype after "-"
func mountFsType(mountInfo, mountPoint string) (string, bool) {
	fsType := ""
	found := false
	for _, line := range strings.Split(mountInfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		// Mount points are octal-escaped in mountinfo; only the space escape is
		// plausible in a home path.
		if strings.ReplaceAll(fields[4], `\040`, " ") != mountPoint {
			continue
		}
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(fields) {
			continue
		}
		fsType = fields[sep+1]
		found = true
	}
	return fsType, found
}

// mountReadOnly reports whether the mount covering path has the "ro" option,
// according to the content of a /proc/<pid>/mountinfo file, and whether such a
// covering mount was found at all. Unlike mountFsType (which matches an exact
// mount point), this resolves the mount the way the kernel would for a path
// with no mount of its own — e.g. /usr with no dedicated mount entry is
// covered by the "/" entry — by picking, among every mount point that is a
// prefix of path, the longest (most specific) one. The LAST line for that
// mount point wins, matching mountFsType's shadowing rule.
func mountReadOnly(mountInfo, path string) (ro bool, found bool) {
	path = filepath.Clean(path)
	bestLen := -1
	for _, line := range strings.Split(mountInfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		// Mount points are octal-escaped in mountinfo; only the space escape is
		// plausible in a system path.
		mp := strings.ReplaceAll(fields[4], `\040`, " ")
		if !isMountPrefix(path, mp) {
			continue
		}
		if len(mp) < bestLen {
			continue
		}
		bestLen = len(mp)
		found = true
		ro = slices.Contains(strings.Split(fields[5], ","), "ro")
	}
	return ro, found
}

// isMountPrefix reports whether mp is the mount point covering path: either
// path is exactly mp, mp is an ancestor directory of path, or mp is "/" (which
// covers everything).
func isMountPrefix(path, mp string) bool {
	if mp == "/" {
		return true
	}
	return path == mp || strings.HasPrefix(path, mp+"/")
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

// ── Denylist coverage checks ──────────────────────────────────────────────────

// dedicatedResourceKeys are the hide keys that already have a hand-written
// check above (with resource-specific logic, e.g. "a private key file", not
// just "the directory is non-empty"). They are skipped by the generic pass so
// no key is reported twice.
var dedicatedResourceKeys = []string{
	"ssh-keys", "gpg-keys", "git-credentials", "netrc", "docker-socket",
}

// checkSensitiveResources derives one check per remaining key in the isolator's
// hide list (config.SensitiveResources), so that `inner verify` proves the
// denylist actually took effect instead of covering only the five resources
// someone wrote a bespoke check for. Growing the hide list grows verify
// automatically.
//
// A key fails when any of its paths is readable and non-empty inside the
// sandbox: a hidden directory is an empty tmpfs, a hidden file is /dev/null
// (size 0), and a path absent on the host is absent here too.
func (c *Checker) checkSensitiveResources() []CheckResult {
	home := c.homeDir()
	resources := config.SensitiveResources(home, strconv.Itoa(os.Getuid()))

	var order []string
	byKey := map[string][]config.SensitiveResource{}
	for _, res := range resources {
		if slices.Contains(dedicatedResourceKeys, res.Key) {
			continue
		}
		if _, seen := byKey[res.Key]; !seen {
			order = append(order, res.Key)
		}
		byKey[res.Key] = append(byKey[res.Key], res)
	}

	results := make([]CheckResult, 0, len(order))
	for _, key := range order {
		severity := SeverityMedium
		if slices.Contains(config.CredentialAllowKeys, key) {
			severity = SeverityHigh
		}
		r := CheckResult{
			ID:       key,
			Name:     key + " not accessible",
			Severity: severity,
			Suggest:  suggestAllow(key),
			Passed:   true,
		}
		for _, res := range byKey[key] {
			if exposed, detail := resourceExposed(res); exposed {
				r.Passed = false
				r.Detail = tildify(home, detail)
				break
			}
		}
		results = append(results, r)
	}
	return results
}

// resourceExposed reports whether a hidden resource still carries content
// inside the sandbox, and which path proves it.
func resourceExposed(res config.SensitiveResource) (bool, string) {
	if res.Dir {
		entries, err := os.ReadDir(res.Path)
		if err != nil || len(entries) == 0 {
			return false, ""
		}
		return true, fmt.Sprintf("%s not empty (%d entries)", res.Path, len(entries))
	}
	info, err := os.Stat(res.Path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false, ""
	}
	return true, res.Path + " found"
}

// tildify shortens a home-relative path for display, matching the "~/.ssh"
// style of the hand-written checks.
func tildify(home, s string) string {
	if home == "" {
		return s
	}
	return strings.ReplaceAll(s, home+"/", "~/")
}
