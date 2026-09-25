package isolator

import (
	"os"
	"strconv"
	"testing"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/runtime"
)

// The runtime sockets under /run/user/<uid> are reachable through the root
// bind unless hidden: a read-only bind does not stop connect(2), and neither
// does a private network namespace.
func TestBuild_runtimeSockets_hiddenByDefault(t *testing.T) {
	run := "/run/user/" + strconv.Itoa(os.Getuid())
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{Entrypoint: config.Entrypoint{Cmd: "sh"}})

	for _, file := range []string{"/bus", "/ssh-agent.socket", "/openssh_agent", "/gcr/ssh", "/keyring/ssh", "/keyring/control"} {
		if !hasSeq(args, "--bind", "/dev/null", run+file) {
			t.Errorf("expected %s%s to be shadowed by /dev/null, got %v", run, file, args)
		}
	}
	for _, dir := range []string{"/systemd", "/gnupg"} {
		if !hasSeq(args, "--tmpfs", run+dir) {
			t.Errorf("expected --tmpfs %s%s, got %v", run, dir, args)
		}
	}
}

func TestBuild_runtimeSockets_allowKeys(t *testing.T) {
	run := "/run/user/" + strconv.Itoa(os.Getuid())
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:      []string{"session-bus", "systemd-user", "ssh-agent", "gpg-agent"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--bind", "/dev/null", run+"/bus") {
		t.Errorf("session-bus allowed but the bus is still hidden: %v", args)
	}
	if hasSeq(args, "--tmpfs", run+"/systemd") || hasSeq(args, "--tmpfs", run+"/gnupg") {
		t.Errorf("systemd-user/gpg-agent allowed but still hidden: %v", args)
	}
	if hasSeq(args, "--bind", "/dev/null", run+"/gcr/ssh") {
		t.Errorf("ssh-agent allowed but still hidden: %v", args)
	}
	// keyring/control belongs to "keyrings", which was not allowed.
	if !hasSeq(args, "--bind", "/dev/null", run+"/keyring/control") {
		t.Errorf("keyrings not allowed but keyring/control is exposed: %v", args)
	}
}

// A profile tmpfs over /run/user/<uid> (the built-in agent profiles) already
// empties every runtime socket: no redundant hide mount inside it.
func TestBuild_runtimeSockets_underProfileTmpfs(t *testing.T) {
	run := "/run/user/" + strconv.Itoa(os.Getuid())
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Mounts:     []config.Mount{{Src: run, Dest: run, Mode: "tmpfs"}},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--bind", "/dev/null", run+"/bus") || hasSeq(args, "--tmpfs", run+"/gnupg") {
		t.Errorf("unexpected hide mount under the profile tmpfs: %v", args)
	}
}

// HideExempt lets a capability bind a hidden path back on purpose without the
// hide rule covering its bind, and without touching any other resource.
func TestBuild_hideExempt(t *testing.T) {
	run := "/run/user/" + strconv.Itoa(os.Getuid())
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Mounts:     []config.Mount{{Src: run + "/bus", Dest: run + "/bus", Mode: "rw"}},
		HideExempt: []string{run + "/bus"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--bind", "/dev/null", run+"/bus") {
		t.Errorf("exempt path was hidden anyway: %v", args)
	}
	if !hasSeq(args, "--tmpfs", run+"/systemd") {
		t.Errorf("exemption leaked to another resource: %v", args)
	}
}

// A hidden file with a placeholder is covered by the placeholder, read-only;
// without one it falls back to /dev/null.
func TestBuild_hidePlaceholder(t *testing.T) {
	home, _ := os.UserHomeDir()
	settings := home + "/.m2/settings.xml"
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})

	args := cmdArgs(t, iso, config.RunConfig{
		HidePlaceholders: map[string]string{settings: "/tmp/inner-hide-x/maven-settings-settings.xml"},
		Entrypoint:       config.Entrypoint{Cmd: "sh"},
	})
	if !hasSeq(args, "--ro-bind", "/tmp/inner-hide-x/maven-settings-settings.xml", settings) {
		t.Errorf("expected the placeholder bound over %s, got %v", settings, args)
	}
	if hasSeq(args, "--bind", "/dev/null", settings) {
		t.Errorf("/dev/null bound over %s despite a placeholder", settings)
	}

	args = cmdArgs(t, iso, config.RunConfig{Entrypoint: config.Entrypoint{Cmd: "sh"}})
	if !hasSeq(args, "--bind", "/dev/null", settings) {
		t.Errorf("without a placeholder, expected /dev/null over %s, got %v", settings, args)
	}
}

// allow = ["ssh-keys"] / ["gpg-keys"] keep the agents reachable, as they were
// before the runtime sockets were hidden: a profile allowing the keys to sign
// or push must not break.
func TestBuild_keyAllowImpliesAgent(t *testing.T) {
	run := "/run/user/" + strconv.Itoa(os.Getuid())
	iso := testIsolatorAllExist(runtime.RuntimeInfo{})
	args := cmdArgs(t, iso, config.RunConfig{
		Allow:      []string{"ssh-keys", "gpg-keys"},
		Entrypoint: config.Entrypoint{Cmd: "sh"},
	})
	if hasSeq(args, "--bind", "/dev/null", run+"/gcr/ssh") || hasSeq(args, "--tmpfs", run+"/gnupg") {
		t.Errorf("ssh-keys/gpg-keys allowed but the agents are hidden: %v", args)
	}
	if !hasSeq(args, "--bind", "/dev/null", run+"/bus") {
		t.Errorf("the implication leaked to session-bus: %v", args)
	}
}
