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

// alwaysDefinedRefs lists $VAR names that UndefinedVarRefs never reports,
// because they are not resolved against the process environment: "UID" is
// always substituted with the numeric UID by ExpandPath, and "workdir" /
// "workspaces_path" are magic tokens substituted elsewhere in the loader
// for mount and entrypoint.workdir fields (see loader.go). They are not
// currently substituted in [env] set values, but flagging them as
// "undefined host variable" would be misleading rather than helpful.
var alwaysDefinedRefs = map[string]bool{
	"UID":             true,
	"workdir":         true,
	"workspaces_path": true,
}

// UndefinedVarRefs returns the names of $VAR / ${VAR} references in s that
// are not defined in the host environment, in the order they first appear
// (without duplicates). Names in alwaysDefinedRefs are never reported.
func UndefinedVarRefs(s string) []string {
	var undefined []string
	seen := make(map[string]bool)
	os.Expand(s, func(key string) string {
		if alwaysDefinedRefs[key] || seen[key] {
			return ""
		}
		seen[key] = true
		if _, ok := os.LookupEnv(key); !ok {
			undefined = append(undefined, key)
		}
		return ""
	})
	return undefined
}
