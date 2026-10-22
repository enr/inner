package config

import (
	"os"
	"os/user"
	"path/filepath"
	"slices"
)

// ChromeExtensionID is the Chrome Web Store id of the Claude in Chrome
// extension, the only one the claude CLI looks for.
const ChromeExtensionID = "fcoeoabgfenejglbffodgkkbkcdhcgfn"

// chromiumBrowserDirs are the profile roots, relative to $HOME, of the
// Chromium-based browsers the claude CLI scans on Linux for the extension.
var chromiumBrowserDirs = []string{
	filepath.Join(".config", "google-chrome"),
	filepath.Join(".config", "BraveSoftware", "Brave-Browser"),
	filepath.Join(".config", "microsoft-edge"),
	filepath.Join(".config", "chromium"),
	filepath.Join(".config", "vivaldi"),
	filepath.Join(".config", "opera"),
}

// ChromeBrowserPlan lists the host paths "chrome-browser" exposes.
type ChromeBrowserPlan struct {
	// BridgeDir is ChromeBridgeDir, bound read-write; empty when not allowed.
	BridgeDir string
	// ExtensionDirs are bound read-only, and nothing else of the profiles.
	ExtensionDirs []string
	// NativeHostManifests are bound read-only too: when the CLI cannot find
	// them it takes the run for a first install and launches the browser on
	// its reconnect page from inside the sandbox.
	NativeHostManifests []string
}

// ChromeNativeHostManifest is the file name of the native messaging manifest
// the claude CLI installs in each browser's NativeMessagingHosts directory.
const ChromeNativeHostManifest = "com.anthropic.claude_code_browser_extension.json"

// ChromeBridgeDir returns the directory where the Claude in Chrome native host
// (spawned by the browser) listens, one <pid>.sock per browser. The claude CLI
// dials every socket in it, and refuses the directory unless it is owned by
// the user, mode 0700 and not a symlink.
func ChromeBridgeDir(username string) string {
	return "/tmp/claude-mcp-browser-bridge-" + username
}

// CurrentUsername returns the name the claude CLI uses in ChromeBridgeDir:
// the account name of the current uid, falling back to $USER.
func CurrentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "default"
}

// ChromeExtensionDirs returns the install directories of the Claude in Chrome
// extension found in the browser profiles under home. The claude CLI decides
// whether the extension is installed by looking for exactly these
// directories, and only dials the native host when it finds one.
func ChromeExtensionDirs(home string) []string {
	var dirs []string
	for _, browser := range chromiumBrowserDirs {
		root := filepath.Join(home, browser)
		for _, profile := range []string{"Default", "Profile *"} {
			matches, _ := filepath.Glob(filepath.Join(root, profile, "Extensions", ChromeExtensionID))
			dirs = append(dirs, matches...)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// ChromeNativeHostManifests returns the native messaging manifests of the
// claude CLI found in the browser configuration directories under home.
func ChromeNativeHostManifests(home string) []string {
	var files []string
	for _, browser := range chromiumBrowserDirs {
		path := filepath.Join(home, browser, "NativeMessagingHosts", ChromeNativeHostManifest)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			files = append(files, path)
		}
	}
	return files
}
