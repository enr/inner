package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExpandPath expands ~ and environment variables ($VAR, ${VAR}) in a path.
// In addition to the process environment, $UID and ${UID} are always resolved
// to the current user's numeric UID, even when the variable is not exported.
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
	uid := strconv.Itoa(os.Getuid())
	return os.Expand(path, func(key string) string {
		if key == "UID" {
			return uid
		}
		return os.Getenv(key)
	})
}
