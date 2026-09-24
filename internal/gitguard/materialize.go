package gitguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// commonDirContent points a git directory at itself: git reads "." relative
// to the gitdir, which is what it does when no commondir file exists.
const commonDirContent = ".\n"

// Materialize creates the plan's placeholders on the host and returns a
// cleanup that removes the ones that must not outlive the run. The cleanup is
// never nil.
//
// Directories and empty files are left in place: git creates the same things
// itself, and removing them could race with a concurrent run that has them
// bound. A commondir placeholder is different — it is a file git does not
// expect — so it is reference-counted across concurrent inner runs with a
// shared flock and removed by the last one to leave.
//
// Why not remove it unconditionally: unlinking a file that another run has
// bind-mounted detaches that mount in the other run's namespace, and the
// sandbox there could then create its own commondir.
func Materialize(plan Plan) (func(), error) {
	var locks []*os.File
	var commonDirs []string
	cleanup := func() {
		for i, f := range locks {
			releaseCommonDir(f, commonDirs[i])
		}
	}

	for _, ph := range plan.Placeholders {
		switch ph.Kind {
		case PlaceholderDir:
			if err := os.MkdirAll(ph.Path, 0o755); err != nil {
				cleanup()
				return func() {}, fmt.Errorf("git protection: creating %s: %w", ph.Path, err)
			}
		case PlaceholderFile:
			if err := createFile(ph.Path, nil); err != nil {
				cleanup()
				return func() {}, fmt.Errorf("git protection: creating %s: %w", ph.Path, err)
			}
		case PlaceholderCommonDir:
			lock, err := lockCommonDir(ph.Path)
			if err != nil {
				cleanup()
				return func() {}, fmt.Errorf("git protection: locking %s: %w", ph.Path, err)
			}
			locks = append(locks, lock)
			commonDirs = append(commonDirs, ph.Path)
			if err := createFile(ph.Path, []byte(commonDirContent)); err != nil {
				cleanup()
				return func() {}, fmt.Errorf("git protection: creating %s: %w", ph.Path, err)
			}
		}
	}
	return cleanup, nil
}

// createFile creates path with content unless it already exists.
func createFile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// lockCommonDir takes a shared lock standing for "a run uses this placeholder".
// It blocks while another run holds the exclusive lock, i.e. while that run is
// deciding to remove the file — so a new run never creates the placeholder
// just before an exiting one deletes it.
func lockCommonDir(path string) (*os.File, error) {
	dir, err := lockDir()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(path))
	f, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:12])+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// releaseCommonDir removes the placeholder when no other run holds it, and
// only if it still has the content inner wrote.
func releaseCommonDir(lock *os.File, path string) {
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return // another run still uses it; the last one out removes it
	}
	if data, err := os.ReadFile(path); err == nil && bytes.Equal(data, []byte(commonDirContent)) {
		_ = os.Remove(path)
	}
}

// lockDir returns the per-user directory holding the placeholder locks.
func lockDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		// A shared /tmp: another user could pre-create the directory, so it
		// must be ours and not a symlink.
		base = filepath.Join(os.TempDir(), "inner-"+strconv.Itoa(os.Getuid()))
		if err := os.Mkdir(base, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		var st unix.Stat_t
		if err := unix.Lstat(base, &st); err != nil {
			return "", err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Getuid() {
			return "", fmt.Errorf("%s is not a directory owned by the current user", base)
		}
	}
	dir := filepath.Join(base, "inner", "gitguard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
