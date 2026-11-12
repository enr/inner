package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Project trust.
//
// The files inner finds through the working directory — the project config
// files (inner.toml, .config/inner.toml, …, in every ancestor) and the project
// profiles (.config/inner/profiles/*.toml, or a profile path under the working
// directory) — are trusted as if the user had written them. They decide the
// next sandbox: its profile, its mounts, where inner writes the log on the
// host. But the working directory is exactly what a sandbox can write: an
// agent that drops an inner.toml, or rewrites a project profile, rewrites the
// policy of the next run. A freshly cloned repository can do the same.
//
// So, when RequireTrust is set, such a file is honored only if its exact
// content was approved by the user with `inner trust`. The approval is a
// sha256 recorded in the user's config directory, which the sandbox cannot
// write; any change to the file — by the sandbox or anyone else — withdraws it.
//
//   - an untrusted project config file is skipped, with a warning;
//   - an untrusted project profile is an error: the user asked for it.
//
// Files under the user and system config directories are the user's own and
// never need approval.

// ExemptFromTrust exempts one file from project trust: the temporary copy of
// a downloaded profile, which has its own consent gate (remote_profile.go)
// and may land under the working directory when that is the temp dir.
func (l *Loader) ExemptFromTrust(path string) {
	l.trustExempt = canonicalPath(path)
}

// TrustFileName is the name of the trust store inside the user config dir.
const TrustFileName = "trusted"

// TrustStorePath returns the path of the trust store.
func (l *Loader) TrustStorePath() string {
	return filepath.Join(l.Dir, TrustFileName)
}

// fileDigest returns the hex sha256 of data.
func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// canonicalPath resolves path to an absolute path with symlinks evaluated.
// A path that cannot be resolved is returned cleaned and absolute.
func canonicalPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// isUnder reports whether path is dir or lies inside it. Both must be
// canonical.
func isUnder(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ownConfigFile reports whether path is in the user or system config
// directory: the user's own files, never subject to project trust.
func (l *Loader) ownConfigFile(path string) bool {
	return isUnder(path, canonicalPath(l.Dir)) ||
		(l.SystemDir != "" && isUnder(path, canonicalPath(l.SystemDir)))
}

// projectProfile reports whether the profile at path (canonical) needs
// approval: it lies under the working directory and outside the user's own
// config directories.
func (l *Loader) projectProfile(path string) bool {
	if !l.RequireTrust || l.WorkDir == "" || path == l.trustExempt || l.ownConfigFile(path) {
		return false
	}
	return isUnder(path, canonicalPath(l.WorkDir))
}

// projectConfigNeedsTrust reports whether a project config file (canonical
// path) needs approval.
func (l *Loader) projectConfigNeedsTrust(path string) bool {
	return l.RequireTrust && !l.ownConfigFile(path)
}

// readTrustStore returns the recorded digests by canonical path. A missing
// store is empty.
func (l *Loader) readTrustStore() (map[string]string, error) {
	f, err := os.Open(l.TrustStorePath())
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	store := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		digest, path, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		store[strings.TrimSpace(path)] = digest
	}
	return store, sc.Err()
}

// trusted reports whether data, read from the canonical path, matches the
// approval recorded for that path.
func (l *Loader) trusted(path string, data []byte) bool {
	store, err := l.readTrustStore()
	if err != nil {
		return false
	}
	return store[path] == fileDigest(data)
}

// TrustState is what an approval did to a file's standing.
type TrustState int

const (
	// TrustNew: the file had never been approved.
	TrustNew TrustState = iota
	// TrustChanged: an approval existed for different content. The file
	// changed since the user last reviewed it — possibly by a sandbox.
	TrustChanged
	// TrustUnchanged: the content was already approved; nothing to record.
	TrustUnchanged
)

// TrustResult reports one file approved by Trust.
type TrustResult struct {
	Path   string // canonical path
	Digest string // hex sha256 of the approved content
	State  TrustState
}

// Trust records the current content of each file as approved. It returns,
// in order, the canonical path and digest of each and whether it was new,
// changed since its last approval, or already approved as it is.
func (l *Loader) Trust(paths ...string) ([]TrustResult, error) {
	store, err := l.readTrustStore()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", l.TrustStorePath(), err)
	}
	var out []TrustResult
	for _, p := range paths {
		canon := canonicalPath(p)
		info, err := os.Stat(canon)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", p)
		}
		data, err := os.ReadFile(canon)
		if err != nil {
			return nil, err
		}
		digest := fileDigest(data)
		state := TrustNew
		if prev, ok := store[canon]; ok {
			state = TrustChanged
			if prev == digest {
				state = TrustUnchanged
			}
		}
		store[canon] = digest
		out = append(out, TrustResult{Path: canon, Digest: digest, State: state})
	}
	if err := l.writeTrustStore(store); err != nil {
		return nil, err
	}
	return out, nil
}

// writeTrustStore replaces the store atomically.
func (l *Loader) writeTrustStore(store map[string]string) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	paths := make([]string, 0, len(store))
	for p := range store {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString("# inner trust store: sha256 and path of approved project files. Managed by `inner trust`.\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "%s %s\n", store[p], p)
	}
	tmp, err := os.CreateTemp(l.Dir, ".trusted-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), l.TrustStorePath())
}

// IsTrustedProjectConfig reports whether the project config file at path is
// honored: approved, or not subject to approval.
func (l *Loader) IsTrustedProjectConfig(path string) bool {
	canon := canonicalPath(path)
	if !l.projectConfigNeedsTrust(canon) {
		return true
	}
	data, err := os.ReadFile(canon)
	return err == nil && l.trusted(canon, data)
}

// warnUntrusted prints, once per path, that an untrusted file was skipped.
func (l *Loader) warnUntrusted(path string) {
	if l.Warn == nil {
		return
	}
	if l.warned == nil {
		l.warned = map[string]bool{}
	}
	if l.warned[path] {
		return
	}
	l.warned[path] = true
	fmt.Fprintf(l.Warn, "inner: warning: ignoring %s: project config not trusted (new or changed since `inner trust`); review it, then run: inner trust %s\n", path, path)
}

// untrustedProfileError is returned for a project profile that is not approved.
func untrustedProfileError(path string) error {
	return fmt.Errorf("profile %s is a project file and is not trusted (new or changed since `inner trust`; a sandbox can write the working directory): review it, then run: inner trust %s", path, path)
}

// readProjectConfig reads a project config file, enforcing trust. It returns
// nil data (no error) when the file is missing or skipped as untrusted. The
// bytes returned are the ones checked: decoding them, rather than re-reading
// the file, leaves no window for a running sandbox to swap the content.
func (l *Loader) readProjectConfig(path string) ([]byte, error) {
	canon := canonicalPath(path)
	data, err := os.ReadFile(canon)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	if l.projectConfigNeedsTrust(canon) && !l.trusted(canon, data) {
		l.warnUntrusted(path)
		return nil, nil
	}
	return data, nil
}

// readProfileFile reads a profile file, enforcing trust for project profiles.
func (l *Loader) readProfileFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	canon := canonicalPath(path)
	if l.projectProfile(canon) && !l.trusted(canon, data) {
		return nil, untrustedProfileError(path)
	}
	return data, nil
}
