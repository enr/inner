package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/enr/inner/internal/config"
)

// applyChromeBrowser records on rc what allow = ["chrome-browser"] exposes:
// the Claude in Chrome bridge directory and the extension's install
// directories (see config.ChromeBrowserPlan). A no-op otherwise.
//
// The isolated /tmp hides the native host's sockets, and the browser-profiles
// mask hides the directory the claude CLI checks before it dials them: without
// both the CLI reports the extension as missing.
func applyChromeBrowser(rc *config.RunConfig) error {
	if !config.AllowKeyEnabled(rc.Allow, "chrome-browser") {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("chrome-browser: cannot determine home directory: %w", err)
	}
	plan, err := chromeBrowserPlan(home, config.ChromeBridgeDir(config.CurrentUsername()))
	if err != nil {
		return err
	}
	rc.ChromeBrowser = plan
	return nil
}

// chromeBrowserPlan builds the plan for the given home and bridge directory.
//
// The bridge directory is created when missing, so the sandbox can start
// before the browser: the native host creates its socket there later, and a
// bind of the directory sees it. An existing directory must pass the same
// checks the claude CLI applies, since the sandbox gets it read-write.
func chromeBrowserPlan(home, bridgeDir string) (config.ChromeBrowserPlan, error) {
	if err := os.Mkdir(bridgeDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return config.ChromeBrowserPlan{}, fmt.Errorf("chrome-browser: creating %s: %w", bridgeDir, err)
	}
	info, err := os.Lstat(bridgeDir)
	if err != nil {
		return config.ChromeBrowserPlan{}, fmt.Errorf("chrome-browser: %w", err)
	}
	if !info.IsDir() {
		return config.ChromeBrowserPlan{}, fmt.Errorf("chrome-browser: %s is not a directory", bridgeDir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return config.ChromeBrowserPlan{}, fmt.Errorf("chrome-browser: %s is owned by uid %d, not by the current user", bridgeDir, st.Uid)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return config.ChromeBrowserPlan{}, fmt.Errorf("chrome-browser: %s has mode %04o, want 0700", bridgeDir, perm)
	}
	return config.ChromeBrowserPlan{
		BridgeDir:           bridgeDir,
		ExtensionDirs:       config.ChromeExtensionDirs(home),
		NativeHostManifests: config.ChromeNativeHostManifests(home),
	}, nil
}
