package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/enr/inner/internal/config"
)

// ── File / dir copy helpers ───────────────────────────────────────────────────
//
// Capabilities and safe-rw mounts give the sandbox a *copy* of a host tree, so
// the agent cannot corrupt the original. The copy runs on the host with the
// user's full read access, so it must never read more than the tree itself:
// a symlink planted in it (~/.claude/skills/evil -> ~/.ssh/id_rsa) would
// otherwise have its target's contents copied in and mounted into the sandbox,
// bypassing the sensitive-path hiding entirely.
//
// The rules:
//   - a file is read only if it is a regular file, opened with O_NOFOLLOW so a
//     symlink swapped in after the check is refused by the kernel too, and
//     with O_NONBLOCK so a FIFO cannot hang inner;
//   - a symlink inside a copied tree is recreated as a symlink, never
//     dereferenced on the host. Inside the sandbox it resolves in the
//     sandbox's own view of the filesystem, where hidden paths stay hidden and
//     an isolated home is empty — so it grants nothing the sandbox could not
//     already read, and relative or tool-managed links keep working;
//   - sockets, devices and FIFOs are skipped: they cannot be copied.

// openRegularNoFollow opens path for reading only if it is a regular file and
// not a symlink.
func openRegularNoFollow(path string) (*os.File, os.FileInfo, error) {
	if info, err := os.Lstat(path); err != nil {
		return nil, nil, err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("refusing to copy %q: it is a symlink — inner does not follow symlinks into the sandbox; replace it with a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("refusing to copy %q: not a regular file (%s)", path, info.Mode().Type())
	}
	return f, info, nil
}

func copyFile(src, dst string) error {
	in, info, err := openRegularNoFollow(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	// Permission bits only: setuid/setgid/sticky have no business on a copy.
	return os.WriteFile(dst, data, info.Mode().Perm())
}

// copySettingsStripped copies src to dst as JSON with keys that would start
// external processes (enabledPlugins, mcpServers) removed. These cause MCP
// servers to be launched at interactive startup, which hangs inside the sandbox.
// If src doesn't exist or can't be parsed, no dst is written and the error is
// returned; callers ignore it, leaving the clone without settings.json, which
// is a valid fresh state (claude recreates its defaults).
//
// Unlike copyFile, a symlinked settings.json is followed: dotfile managers
// (stow, chezmoi, home-manager) routinely link it, and refusing it would drop
// the user's settings from every sandbox. The target must still be a regular
// file outside the paths the sandbox hides, and only a JSON object is written
// back — re-serialised, never copied byte for byte.
func copySettingsStripped(src, dst string) error {
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	if hidden := hiddenResourceCovering(resolved); hidden != "" {
		return fmt.Errorf("refusing to copy %q: it resolves to %q, under %s, which the sandbox hides", src, resolved, hidden)
	}
	in, _, err := openRegularNoFollow(resolved)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	delete(settings, "enabledPlugins")
	delete(settings, "mcpServers")
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

// hiddenResourceCovering returns the sensitive resource path that covers
// path, or "" if none does.
func hiddenResourceCovering(path string) string {
	home, _ := os.UserHomeDir()
	for _, r := range config.SensitiveResources(home, strconv.Itoa(os.Getuid())) {
		if config.PathCoveredBy([]string{r.Path}, path) {
			return r.Path
		}
		if resolved, err := filepath.EvalSymlinks(r.Path); err == nil && config.PathCoveredBy([]string{resolved}, path) {
			return r.Path
		}
	}
	return ""
}

// copyDir copies the tree at src into dst (created if missing). See the rules
// above: regular files are copied, symlinks recreated, everything else
// skipped. A symlink at src itself is recreated at dst like any other; callers
// that want the target's content (safe-rw) resolve src first.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		dstPath := filepath.Join(dst, rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			// WalkDir does not descend into a symlinked directory, and a
			// symlinked file must not be read: recreate the link itself.
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, dstPath)
		case d.IsDir():
			return os.MkdirAll(dstPath, 0o755)
		case !d.Type().IsRegular():
			return nil // socket, device, FIFO: nothing to copy
		}
		return copyFile(path, dstPath)
	})
}
