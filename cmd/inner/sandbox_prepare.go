package main

import (
	"fmt"
	"os"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/git"
	"github.com/enr/inner/internal/shim"
)

// sandboxOptions selects the preparation steps that `inner run` and
// `inner verify` do NOT agree on.
//
// The two commands build a sandbox from the same RunConfig, and it matters that
// they build the SAME one: verify's whole job is to certify the sandbox a run
// gets. They had drifted apart by accident — each new step was added to
// runSandbox and simply never to runVerifyOutside — so the divergence was
// invisible and grew for free.
//
// Making the differences named fields costs one line per call site and turns
// "nobody added it to verify" into a decision somebody has to write down.
type sandboxOptions struct {
	// GitConfig sanitizes the host gitconfig into a temp file the isolator
	// injects. Off for verify: no built-in check inspects the gitconfig, and
	// the [verify.custom] checks that might are the user's own to arrange.
	GitConfig bool

	// Capabilities runs the capability handlers (claude, gemini, cursor,
	// opencode).
	//
	// Off for verify, and this one is a real behavioural difference rather
	// than an omission: capability handlers are not pure mount injection. The
	// claude handler diagnoses the OAuth token, can launch `claude -p
	// /try-login` to trigger the OS keyring's graphical unlock dialog, and
	// waits for the user to press Enter. Having `inner verify` pop a keyring
	// prompt would make a read-only conformance check into something with side
	// effects on the user's credential store.
	//
	// The cost is that verify does not see the capability mounts, so it cannot
	// judge them. That is a known limitation, documented rather than hidden;
	// closing it means splitting each handler's mount injection from its
	// pre-flight actions, which is a larger change than this extraction.
	Capabilities bool

	// SafeMounts materialises "safe-rw" mounts (copy host source to a temp
	// dir, mount that read-write). Off for verify: it only affects what the
	// sandbox may write to, and verify never writes.
	SafeMounts bool
}

// runSandboxOptions is what `inner run` asks for: everything.
func runSandboxOptions() sandboxOptions {
	return sandboxOptions{GitConfig: true, Capabilities: true, SafeMounts: true}
}

// verifySandboxOptions is what `inner verify` asks for. Every field it leaves
// false has a reason recorded on the field itself.
func verifySandboxOptions() sandboxOptions {
	return sandboxOptions{}
}

// prepareSandbox applies the host-side steps that turn a loaded RunConfig into
// one the isolator can build: generating files, copying directories and
// recording their paths on rc.
//
// It returns a single cleanup that undoes every step that ran, in reverse
// order. Callers defer it. On failure it cleans up the steps that already
// succeeded and returns the error, so a half-prepared run never leaks a temp
// directory.
//
// New steps belong here rather than in runSandbox: that is the only way the
// next one cannot silently skip verify.
func prepareSandbox(rc *config.RunConfig, opts sandboxOptions) (func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	// fail runs the cleanups accumulated so far and wraps the error.
	fail := func(format string, err error) (func(), error) {
		cleanup()
		return nil, fmt.Errorf(format, err)
	}

	// Shim dir from [noop] config: blocked/rewritten commands.
	if len(rc.Noop.Block) > 0 || len(rc.Noop.Rewrite) > 0 {
		shimDir, err := shim.Builder{}.Build(rc.Noop)
		if err != nil {
			return fail("building shim dir: %w", err)
		}
		rc.ShimDir = shimDir
		cleanups = append(cleanups, func() { _ = os.RemoveAll(shimDir) })
	}

	// containers.conf override for rootless podman inside the sandbox.
	// A no-op unless nested-user-ns is allowed, and shared by both commands so
	// [verify.custom] checks see the same environment a run would.
	cleanupContainers, err := applyContainersConf(rc)
	if err != nil {
		return fail("containers config: %w", err)
	}
	cleanups = append(cleanups, cleanupContainers)

	if opts.GitConfig && rc.Git != nil {
		gitPath, err := git.Sanitize(rc.Git)
		if err != nil {
			return fail("sanitizing gitconfig: %w", err)
		}
		rc.GitConfigPath = gitPath
		cleanups = append(cleanups, func() { _ = os.Remove(gitPath) })
	}

	if opts.Capabilities {
		cleanupCaps, err := applyCapabilities(rc)
		if err != nil {
			cleanup()
			return nil, err
		}
		cleanups = append(cleanups, cleanupCaps)
	}

	if opts.SafeMounts {
		cleanupSafe, err := applyGenericSafeMounts(rc)
		if err != nil {
			cleanup()
			return nil, err
		}
		cleanups = append(cleanups, cleanupSafe)
	}

	return cleanup, nil
}
