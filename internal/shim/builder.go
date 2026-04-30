package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/enr/inner/internal/config"
)

// Builder creates a directory of shim scripts from a NoopConfig.
// It has no knowledge of bwrap or any other isolator.
// The caller is responsible for removing the directory after use.
type Builder struct{}

// Build creates a temporary directory containing shim scripts derived from noop.
// Returns the path to the directory, or an empty string if noop has no entries.
// On error the directory is cleaned up before returning.
func (Builder) Build(noop config.NoopConfig) (string, error) {
	if len(noop.Block) == 0 && len(noop.Rewrite) == 0 {
		return "", nil
	}

	dir, err := os.MkdirTemp("", "inner-shims-*")
	if err != nil {
		return "", fmt.Errorf("creating shim dir: %w", err)
	}

	if err := writeShims(dir, noop); err != nil {
		os.RemoveAll(dir) //nolint:errcheck
		return "", err
	}

	return dir, nil
}

func writeShims(dir string, noop config.NoopConfig) error {
	for _, cmd := range noop.Block {
		if !isSafeShimName(cmd) {
			return fmt.Errorf("noop.block %q: key must be a plain filename with no path separators", cmd)
		}
		content := fmt.Sprintf(blockScript, cmd, cmd)
		if err := writeScript(dir, cmd, content); err != nil {
			return err
		}
	}
	for cmd, replacement := range noop.Rewrite {
		if !isSafeShimName(cmd) {
			return fmt.Errorf("noop.rewrite %q: key must be a plain filename with no path separators", cmd)
		}
		if !isSafeShimReplacement(replacement) {
			return fmt.Errorf("noop.rewrite %q: replacement %q contains shell metacharacters", cmd, replacement)
		}
		content := fmt.Sprintf(rewriteScript, replacement)
		if err := writeScript(dir, cmd, content); err != nil {
			return err
		}
	}
	return nil
}

// isSafeShimName reports whether name is a plain filename that cannot escape
// the shim directory via path traversal. Rejects anything containing a path
// separator or that does not round-trip through filepath.Base unchanged.
func isSafeShimName(name string) bool {
	if name == "" {
		return false
	}
	// filepath.Base catches '/' and '..' on all platforms; reject '\' explicitly
	// since on Linux it is not a path separator but is still unsafe.
	if strings.ContainsRune(name, '\\') {
		return false
	}
	return filepath.Base(name) == name
}

// isSafeShimReplacement reports whether s is safe to interpolate into a sh
// script's exec line. Rejects shell metacharacters that could break out of the
// intended "exec <replacement> "$@"" pattern.
func isSafeShimReplacement(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch r {
		case ';', '|', '&', '>', '<', '`', '$', '!', '(', ')', '{', '}',
			'*', '?', '~', '#', '\\', '\'', '"', '\n', '\r':
			return false
		}
	}
	return true
}

func writeScript(dir, name, content string) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return fmt.Errorf("writing shim %q: %w", name, err)
	}
	return nil
}

// blockScript is the template for a blocking shim.
// Args: cmd (for error message), cmd (for hint message).
const blockScript = `#!/bin/sh
echo "inner: '%s' is not available in this sandbox" >&2
echo "inner: remove '%s' from [noop.block] to enable it" >&2
exit 1
`

// rewriteScript is the template for a rewriting shim.
// Args: replacement command.
const rewriteScript = `#!/bin/sh
exec %s "$@"
`
