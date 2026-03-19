package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandPath expands ~ and environment variables ($VAR, ${VAR}) in a path.
func ExpandPath(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return os.ExpandEnv(path)
}
