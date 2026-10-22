package isolator

import (
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

func chromeConfig(allow ...string) config.RunConfig {
	return config.RunConfig{
		Allow: allow,
		ChromeBrowser: config.ChromeBrowserPlan{
			BridgeDir:           "/tmp/claude-mcp-browser-bridge-tester",
			ExtensionDirs:       []string{"/home/tester/.config/google-chrome/Default/Extensions/" + config.ChromeExtensionID},
			NativeHostManifests: []string{"/home/tester/.config/google-chrome/NativeMessagingHosts/" + config.ChromeNativeHostManifest},
		},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	}
}

func TestBuild_chromeBrowser_bindsBridgeAndExtensionOverTheMask(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	cfg := chromeConfig("chrome-browser")
	args := cmdArgs(t, iso, cfg)

	bridge := cfg.ChromeBrowser.BridgeDir
	ext := cfg.ChromeBrowser.ExtensionDirs[0]
	bridgeAt := indexSeq(args, "--bind", bridge, bridge)
	if bridgeAt < 0 {
		t.Fatalf("missing read-write bind of the bridge directory, got %v", args)
	}
	if bridgeAt < indexSeq(args, "--tmpfs", "/tmp") {
		t.Error("the bridge bind must come after the /tmp tmpfs")
	}
	extAt := indexSeq(args, "--ro-bind", ext, ext)
	if extAt < 0 {
		t.Fatalf("missing read-only bind of the extension directory, got %v", args)
	}
	// The browser-profiles mask stays in place: only the extension comes back.
	maskAt := indexSeq(args, "--tmpfs", "/home/tester/.config/google-chrome")
	if maskAt < 0 {
		t.Fatal("chrome-browser must not lift the browser-profiles mask")
	}
	if extAt < maskAt {
		t.Error("the extension bind must come after the browser-profiles mask")
	}
	manifest := cfg.ChromeBrowser.NativeHostManifests[0]
	if at := indexSeq(args, "--ro-bind", manifest, manifest); at < maskAt {
		t.Errorf("the native host manifest must be bound read-only after the mask, got %v", args)
	}
}

func TestBuild_chromeBrowser_ignoredWithoutAllowKey(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	cfg := chromeConfig()
	args := cmdArgs(t, iso, cfg)

	if hasSeq(args, "--bind", cfg.ChromeBrowser.BridgeDir, cfg.ChromeBrowser.BridgeDir) {
		t.Error("bridge directory bound without the allow key")
	}
	ext := cfg.ChromeBrowser.ExtensionDirs[0]
	if hasSeq(args, "--ro-bind", ext, ext) {
		t.Error("extension directory bound without the allow key")
	}
}

func TestBuild_chromeBrowser_skipsMissingPaths(t *testing.T) {
	withHome(t, "/home/tester")
	iso := testIsolatorNoneExist(runtime.RuntimeInfo{})
	cfg := chromeConfig("chrome-browser")
	args := cmdArgs(t, iso, cfg)

	if hasSeq(args, "--bind", cfg.ChromeBrowser.BridgeDir, cfg.ChromeBrowser.BridgeDir) {
		t.Error("a missing bridge directory must not be bound")
	}
	ext := cfg.ChromeBrowser.ExtensionDirs[0]
	if hasSeq(args, "--ro-bind", ext, ext) {
		t.Error("a missing extension directory must not be bound")
	}
}
