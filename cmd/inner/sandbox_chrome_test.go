package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/enr/inner/internal/config"
)

func TestChromeBrowserPlan_createsBridgeDirAndFindsBrowserFiles(t *testing.T) {
	home := t.TempDir()
	want := []string{
		filepath.Join(home, ".config", "google-chrome", "Default", "Extensions", config.ChromeExtensionID),
		filepath.Join(home, ".config", "google-chrome", "Profile 2", "Extensions", config.ChromeExtensionID),
	}
	for _, d := range want {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Another extension in the same profile must not be picked up.
	if err := os.MkdirAll(filepath.Join(home, ".config", "google-chrome", "Default", "Extensions", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(home, ".config", "chromium", "NativeMessagingHosts", config.ChromeNativeHostManifest)
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(t.TempDir(), "bridge")

	plan, err := chromeBrowserPlan(home, bridge)
	if err != nil {
		t.Fatalf("chromeBrowserPlan: %v", err)
	}
	if plan.BridgeDir != bridge {
		t.Errorf("BridgeDir = %q, want %q", plan.BridgeDir, bridge)
	}
	info, err := os.Stat(bridge)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("bridge directory: %v, mode %v; want 0700", err, info)
	}
	if !slices.Equal(plan.ExtensionDirs, want) {
		t.Errorf("ExtensionDirs = %v, want %v", plan.ExtensionDirs, want)
	}
	if !slices.Equal(plan.NativeHostManifests, []string{manifest}) {
		t.Errorf("NativeHostManifests = %v, want [%s]", plan.NativeHostManifests, manifest)
	}
}

func TestChromeBrowserPlan_rejectsLooseBridgeDir(t *testing.T) {
	bridge := filepath.Join(t.TempDir(), "bridge")
	if err := os.Mkdir(bridge, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bridge, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := chromeBrowserPlan(t.TempDir(), bridge); err == nil {
		t.Error("a 0755 bridge directory must be refused")
	}
}

func TestChromeBrowserPlan_rejectsSymlinkedBridgeDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(dir, "bridge")
	if err := os.Symlink(target, bridge); err != nil {
		t.Fatal(err)
	}
	if _, err := chromeBrowserPlan(t.TempDir(), bridge); err == nil {
		t.Error("a symlinked bridge directory must be refused")
	}
}

func TestApplyChromeBrowser_noopWithoutAllowKey(t *testing.T) {
	rc := &config.RunConfig{}
	if err := applyChromeBrowser(rc); err != nil {
		t.Fatal(err)
	}
	if rc.ChromeBrowser.BridgeDir != "" || len(rc.ChromeBrowser.ExtensionDirs) != 0 {
		t.Errorf("plan set without the allow key: %+v", rc.ChromeBrowser)
	}
}
