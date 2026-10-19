package gitguard

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolateHostConfig keeps the host's own git config out of the planner, so a
// core.hooksPath in the developer's ~/.gitconfig cannot change the results.
func isolateHostConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return home
}

// fakeRepo creates the minimal layout of a git directory at root/.git.
func fakeRepo(t *testing.T, root string, withHooks bool) string {
	t.Helper()
	gitdir := filepath.Join(root, ".git")
	write(t, filepath.Join(gitdir, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(gitdir, "config"), "[core]\n\tbare = false\n")
	if err := os.MkdirAll(filepath.Join(gitdir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withHooks {
		if err := os.MkdirAll(filepath.Join(gitdir, "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return gitdir
}

func roSrcs(p Plan) []string {
	var out []string
	for _, b := range p.ReadOnly {
		out = append(out, b.Src)
	}
	return out
}

func hasPlaceholder(p Plan, path string, kind PlaceholderKind) bool {
	return slices.Contains(p.Placeholders, Placeholder{Path: path, Kind: kind})
}

func TestBuild_protected_plainRepo(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)

	plan := Build("", []Mount{{Src: root, Dest: root}})

	if plan.Mode != ModeProtected {
		t.Errorf("Mode = %q, want %q (empty means protected)", plan.Mode, ModeProtected)
	}
	ro := roSrcs(plan)
	for _, want := range []string{
		filepath.Join(gitdir, "config"),
		filepath.Join(gitdir, "hooks"),
		filepath.Join(gitdir, "commondir"),
	} {
		if !slices.Contains(ro, want) {
			t.Errorf("ReadOnly is missing %s: %v", want, ro)
		}
	}
	if slices.Contains(ro, gitdir) {
		t.Error("protected mode must not bind the whole .git read-only")
	}
	if !hasPlaceholder(plan, filepath.Join(gitdir, "commondir"), PlaceholderCommonDir) {
		t.Errorf("missing commondir must become a placeholder: %+v", plan.Placeholders)
	}
	if hasPlaceholder(plan, filepath.Join(gitdir, "config.worktree"), PlaceholderFile) {
		t.Error("config.worktree needs no placeholder while extensions.worktreeConfig is off")
	}
	if len(plan.Warnings) > 0 {
		t.Errorf("unexpected warnings: %v", plan.Warnings)
	}
}

func TestBuild_protected_missingHooksBecomesPlaceholder(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, false)

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	hooks := filepath.Join(gitdir, "hooks")
	if !hasPlaceholder(plan, hooks, PlaceholderDir) {
		t.Errorf("missing hooks dir must become a placeholder: %+v", plan.Placeholders)
	}
	if !slices.Contains(roSrcs(plan), hooks) {
		t.Error("the hooks placeholder must be bound read-only")
	}
}

func TestBuild_protected_mapsToMountDest(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	fakeRepo(t, root, true)

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: "/work/app"}})

	for _, b := range plan.ReadOnly {
		if !strings.HasPrefix(b.Dest, "/work/app/.git/") {
			t.Errorf("bind %s -> %s: dest must be under the mount dest", b.Src, b.Dest)
		}
		if !strings.HasPrefix(b.Src, root) {
			t.Errorf("bind %s -> %s: src must be the host path", b.Src, b.Dest)
		}
	}
}

func TestBuild_ro_bindsWholeGitDir(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)

	plan := Build(ModeRO, []Mount{{Src: root, Dest: root}})

	if got := roSrcs(plan); !slices.Equal(got, []string{gitdir}) {
		t.Errorf("ReadOnly = %v, want only %s", got, gitdir)
	}
	if len(plan.Placeholders) > 0 {
		t.Errorf("ro mode needs no placeholders: %+v", plan.Placeholders)
	}
}

func TestBuild_rw_protectsNothing(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	fakeRepo(t, root, true)

	plan := Build(ModeRW, []Mount{{Src: root, Dest: root}})

	if !plan.Empty() {
		t.Errorf("rw plan should be empty: %+v", plan)
	}
}

func TestBuild_scansNestedProjectTrees(t *testing.T) {
	isolateHostConfig(t)
	parent := t.TempDir()
	shallow := fakeRepo(t, filepath.Join(parent, "proj"), true)
	// ~/src/github.com/org/repo layout: three levels below the mount.
	deep := fakeRepo(t, filepath.Join(parent, "github.com", "org", "repo"), true)
	// A repository nested inside another one's working tree.
	nested := fakeRepo(t, filepath.Join(parent, "proj", "vendor", "lib"), true)

	plan := Build(ModeProtected, []Mount{{Src: parent, Dest: parent}})

	ro := roSrcs(plan)
	for _, gitdir := range []string{shallow, deep, nested} {
		for _, f := range []string{"config", "hooks"} {
			if want := filepath.Join(gitdir, f); !slices.Contains(ro, want) {
				t.Errorf("ReadOnly is missing %s: %v", want, ro)
			}
		}
	}
	if len(plan.Warnings) > 0 {
		t.Errorf("unexpected warnings: %v", plan.Warnings)
	}
}

func TestBuild_scanSkipsNodeModulesAndSymlinks(t *testing.T) {
	isolateHostConfig(t)
	parent := t.TempDir()
	fakeRepo(t, filepath.Join(parent, "node_modules", "pkg"), true)
	outside := t.TempDir()
	fakeRepo(t, filepath.Join(outside, "repo"), true)
	if err := os.Symlink(outside, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}

	plan := Build(ModeProtected, []Mount{{Src: parent, Dest: parent}})

	if !plan.Empty() {
		t.Errorf("node_modules and symlinked directories must not be searched: %+v", plan)
	}
}

func TestBuild_scanDepthLimit(t *testing.T) {
	isolateHostConfig(t)
	old := maxScanDepth
	maxScanDepth = 2
	t.Cleanup(func() { maxScanDepth = old })
	parent := t.TempDir()
	atLimit := fakeRepo(t, filepath.Join(parent, "a", "b"), true)
	fakeRepo(t, filepath.Join(parent, "a", "b", "c"), true) // depth 3: past the limit

	plan := Build(ModeProtected, []Mount{{Src: parent, Dest: parent}})

	ro := roSrcs(plan)
	if !slices.Contains(ro, filepath.Join(atLimit, "config")) {
		t.Errorf("repository at the depth limit must be protected: %v", ro)
	}
	for _, src := range ro {
		if strings.Contains(src, filepath.Join("b", "c")) {
			t.Errorf("unexpected bind past the depth limit: %s", src)
		}
	}
}

func TestBuild_scanBudgetWarns(t *testing.T) {
	isolateHostConfig(t)
	old := maxScanDirs
	maxScanDirs = 3
	t.Cleanup(func() { maxScanDirs = old })
	parent := t.TempDir()
	first := fakeRepo(t, filepath.Join(parent, "a"), true)
	for _, d := range []string{"b", "c", "d"} {
		fakeRepo(t, filepath.Join(parent, d), true)
	}

	plan := Build(ModeProtected, []Mount{{Src: parent, Dest: parent}})

	if !slices.Contains(roSrcs(plan), filepath.Join(first, "config")) {
		t.Errorf("repositories within the budget must still be protected: %v", roSrcs(plan))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "too large") {
		t.Errorf("an exhausted budget must be reported once: %v", plan.Warnings)
	}
}

func TestBuild_scanBudgetIsPerMount(t *testing.T) {
	isolateHostConfig(t)
	old := maxScanDirs
	maxScanDirs = 3
	t.Cleanup(func() { maxScanDirs = old })
	huge := t.TempDir()
	for _, d := range []string{"a", "b", "c", "d"} {
		if err := os.MkdirAll(filepath.Join(huge, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	gitdir := fakeRepo(t, work, true)

	plan := Build(ModeProtected, []Mount{{Src: huge, Dest: huge}, {Src: work, Dest: work}})

	if !slices.Contains(roSrcs(plan), filepath.Join(gitdir, "config")) {
		t.Errorf("a mount after an exhausted one must still be protected: %v", roSrcs(plan))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], huge) {
		t.Errorf("only the mount that exhausted its budget must be reported: %v", plan.Warnings)
	}
}

func TestBuild_containerStorageIsNotSearched(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	storage := filepath.Join(root, "storage")
	write(t, filepath.Join(storage, "storage.lock"), "")
	if err := os.MkdirAll(filepath.Join(storage, "overlay-layers"), 0o755); err != nil {
		t.Fatal(err)
	}
	layer := fakeRepo(t, filepath.Join(storage, "overlay", "abc", "diff", "build"), false)
	beside := fakeRepo(t, filepath.Join(root, "project"), true)

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	for _, src := range roSrcs(plan) {
		if strings.HasPrefix(src, layer) {
			t.Errorf("a repository in an image layer must not be protected: %s", src)
		}
	}
	for _, ph := range plan.Placeholders {
		if strings.HasPrefix(ph.Path, storage) {
			t.Errorf("no placeholder may be written into container storage: %s", ph.Path)
		}
	}
	if !slices.Contains(roSrcs(plan), filepath.Join(beside, "config")) {
		t.Errorf("a repository beside the storage must still be protected: %v", roSrcs(plan))
	}
}

func TestBuild_nonDirectoryMountsAreSkipped(t *testing.T) {
	isolateHostConfig(t)
	old := maxScanDirs
	maxScanDirs = 0
	t.Cleanup(func() { maxScanDirs = old })
	dir := t.TempDir()
	file := filepath.Join(dir, "settings.json")
	write(t, file, "{}")
	sock := filepath.Join(dir, "bus")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	plan := Build(ModeProtected, []Mount{{Src: file, Dest: file}, {Src: sock, Dest: sock}})

	if len(plan.Warnings) != 0 {
		t.Errorf("a file or socket mount must not be searched: %v", plan.Warnings)
	}
}

func TestBuild_protected_worktreeConfigExtension(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)
	write(t, filepath.Join(gitdir, "config"), "[extensions]\n\tworktreeConfig = true\n")

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	if !hasPlaceholder(plan, filepath.Join(gitdir, "config.worktree"), PlaceholderFile) {
		t.Errorf("config.worktree must be a placeholder when the extension is on: %+v", plan.Placeholders)
	}
}

func TestBuild_protected_includeAndHooksPathInsideWorkdir(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)
	write(t, filepath.Join(gitdir, "config"),
		"[include]\n\tpath = ../project.gitconfig\n[core]\n\thooksPath = .husky/_\n")
	write(t, filepath.Join(root, "project.gitconfig"), "[user]\n\tname = x\n")

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	ro := roSrcs(plan)
	if !slices.Contains(ro, filepath.Join(root, "project.gitconfig")) {
		t.Errorf("an include target inside the workdir must be read-only: %v", ro)
	}
	hooksPath := filepath.Join(root, ".husky", "_")
	if !slices.Contains(ro, hooksPath) {
		t.Errorf("a hooksPath inside the workdir must be read-only: %v", ro)
	}
	if !hasPlaceholder(plan, hooksPath, PlaceholderDir) {
		t.Errorf("a missing hooksPath dir must become a placeholder: %+v", plan.Placeholders)
	}
}

func TestBuild_protected_globalRelativeHooksPath(t *testing.T) {
	home := isolateHostConfig(t)
	write(t, filepath.Join(home, ".gitconfig"), "[core]\n\thooksPath = .githooks\n")
	root := t.TempDir()
	fakeRepo(t, root, true)
	if err := os.MkdirAll(filepath.Join(root, ".githooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	if !slices.Contains(roSrcs(plan), filepath.Join(root, ".githooks")) {
		t.Errorf("a global relative hooksPath resolves into the workdir and must be read-only: %v", roSrcs(plan))
	}
}

func TestBuild_protected_missingIncludeWarns(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)
	write(t, filepath.Join(gitdir, "config"), "[include]\n\tpath = ../local.gitconfig\n")

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "local.gitconfig") {
		t.Errorf("a missing include inside the workdir must be reported: %v", plan.Warnings)
	}
	if hasPlaceholder(plan, filepath.Join(root, "local.gitconfig"), PlaceholderFile) {
		t.Error("no placeholder may be dropped into the working tree for an include")
	}
}

func TestBuild_protected_symlinkedHooksWarns(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, false)
	if err := os.MkdirAll(filepath.Join(root, "githooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../githooks", filepath.Join(gitdir, "hooks")); err != nil {
		t.Fatal(err)
	}

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	if len(plan.Warnings) == 0 {
		t.Error("a symlinked hooks dir cannot be protected and must be reported")
	}
}

func TestBuild_protected_submodules(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)
	// Submodule named "libs/a" (names may contain slashes), checked out at libs/a.
	mod := filepath.Join(gitdir, "modules", "libs", "a")
	write(t, filepath.Join(mod, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(mod, "config"), "[core]\n\tworktree = ../../../../libs/a\n")
	if err := os.MkdirAll(filepath.Join(mod, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "libs", "a", ".git"), "gitdir: ../../.git/modules/libs/a\n")

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	ro := roSrcs(plan)
	for _, want := range []string{
		filepath.Join(mod, "config"),
		filepath.Join(mod, "hooks"),
		filepath.Join(mod, "commondir"),
		filepath.Join(root, "libs", "a", ".git"),
	} {
		if !slices.Contains(ro, want) {
			t.Errorf("ReadOnly is missing %s: %v", want, ro)
		}
	}
	if len(plan.Writable) != 0 {
		t.Errorf("a submodule gitdir inside the workdir needs no extra writable bind: %v", plan.Writable)
	}
}

func TestBuild_protected_linkedWorktreeOutsideWorkdir(t *testing.T) {
	isolateHostConfig(t)
	main := t.TempDir()
	common := fakeRepo(t, main, true)
	admin := filepath.Join(common, "worktrees", "feat")
	write(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/feat\n")
	write(t, filepath.Join(admin, "commondir"), "../..\n")
	wt := t.TempDir()
	write(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")

	plan := Build(ModeProtected, []Mount{{Src: wt, Dest: wt}})

	// The admin dir lives inside the common dir, so one writable bind covers both.
	if len(plan.Writable) != 1 || plan.Writable[0].Src != common || plan.Writable[0].Dest != common {
		t.Errorf("Writable = %v, want only %s at its own path", plan.Writable, common)
	}
	ro := roSrcs(plan)
	for _, want := range []string{
		filepath.Join(wt, ".git"),
		filepath.Join(common, "config"),
		filepath.Join(common, "hooks"),
		filepath.Join(admin, "commondir"),
	} {
		if !slices.Contains(ro, want) {
			t.Errorf("ReadOnly is missing %s: %v", want, ro)
		}
	}
}

func TestBuild_ro_linkedWorktreeStaysReadOnly(t *testing.T) {
	isolateHostConfig(t)
	main := t.TempDir()
	common := fakeRepo(t, main, true)
	admin := filepath.Join(common, "worktrees", "feat")
	write(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/feat\n")
	write(t, filepath.Join(admin, "commondir"), "../..\n")
	wt := t.TempDir()
	write(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")

	plan := Build(ModeRO, []Mount{{Src: wt, Dest: wt}})

	if len(plan.Writable) != 0 {
		t.Errorf("ro mode must not expose the gitdir writable: %v", plan.Writable)
	}
	if got := roSrcs(plan); !slices.Equal(got, []string{filepath.Join(wt, ".git")}) {
		t.Errorf("ReadOnly = %v, want only the .git file", got)
	}
}

func TestBuild_parentsBeforeChildren(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)
	write(t, filepath.Join(gitdir, "config"), "[core]\n\thooksPath = .git/hooks/custom\n")

	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})

	hooks := slices.IndexFunc(plan.ReadOnly, func(b Bind) bool { return b.Src == filepath.Join(gitdir, "hooks") })
	custom := slices.IndexFunc(plan.ReadOnly, func(b Bind) bool { return b.Src == filepath.Join(gitdir, "hooks", "custom") })
	if hooks < 0 || custom < 0 || hooks > custom {
		t.Errorf("hooks (%d) must be bound before hooks/custom (%d): %v", hooks, custom, plan.ReadOnly)
	}
}

func TestMaterialize_commonDirLifecycle(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, false)
	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})
	commondir := filepath.Join(gitdir, "commondir")

	first, err := Materialize(plan)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(commondir)
	if err != nil || string(data) != commonDirContent {
		t.Fatalf("commondir placeholder = %q, %v; want %q", data, err, commonDirContent)
	}
	if fi, err := os.Stat(filepath.Join(gitdir, "hooks")); err != nil || !fi.IsDir() {
		t.Fatalf("hooks placeholder dir not created: %v", err)
	}

	// A concurrent run using the same repository.
	second, err := Materialize(plan)
	if err != nil {
		t.Fatal(err)
	}
	first()
	if _, err := os.Stat(commondir); err != nil {
		t.Fatalf("the first run to exit must leave the placeholder to the one still running: %v", err)
	}
	second()
	if _, err := os.Stat(commondir); !os.IsNotExist(err) {
		t.Fatalf("the last run to exit must remove the placeholder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitdir, "hooks")); err != nil {
		t.Error("the hooks placeholder dir is left in place")
	}
}

func TestMaterialize_keepsChangedCommonDir(t *testing.T) {
	isolateHostConfig(t)
	root := t.TempDir()
	gitdir := fakeRepo(t, root, true)
	plan := Build(ModeProtected, []Mount{{Src: root, Dest: root}})
	commondir := filepath.Join(gitdir, "commondir")

	cleanup, err := Materialize(plan)
	if err != nil {
		t.Fatal(err)
	}
	write(t, commondir, "/elsewhere\n")
	cleanup()
	if _, err := os.Stat(commondir); err != nil {
		t.Error("a commondir inner did not write must not be removed")
	}
}

// TestMaterialize_realGitAcceptsPlaceholders runs the host git against a
// repository carrying every placeholder: they must not break it, since the
// user keeps using git on the host while the sandbox runs.
func TestMaterialize_realGitAcceptsPlaceholders(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	isolateHostConfig(t)
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	_ = os.RemoveAll(filepath.Join(root, ".git", "hooks"))

	cleanup, err := Materialize(Build(ModeProtected, []Mount{{Src: root, Dest: root}}))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	run("commit", "-q", "--allow-empty", "-m", "x")
	run("status", "--short")
	if got := strings.TrimSpace(run("rev-parse", "--git-common-dir")); got != filepath.Join(root, ".git") && got != ".git" {
		t.Errorf("git-common-dir = %q, want the repository itself", got)
	}
}

func TestBuild_noScanMountIsNotSearched(t *testing.T) {
	isolateHostConfig(t)
	old := maxScanDirs
	maxScanDirs = 1
	t.Cleanup(func() { maxScanDirs = old })
	cache := t.TempDir()
	inCache := fakeRepo(t, filepath.Join(cache, "a", "b"), true)
	hooks := filepath.Join(cache, "hooks")
	work := t.TempDir()
	write(t, filepath.Join(fakeRepo(t, work, true), "config"), "[core]\n\thooksPath = "+hooks+"\n")

	plan := Build(ModeProtected, []Mount{{Src: cache, Dest: cache, NoScan: true}, {Src: work, Dest: work}})

	if len(plan.Warnings) != 0 {
		t.Errorf("a mount that is not searched must not exhaust its budget: %v", plan.Warnings)
	}
	for _, src := range roSrcs(plan) {
		if strings.HasPrefix(src, inCache) {
			t.Errorf("a repository in a NoScan mount must not be protected: %s", src)
		}
	}
	if !slices.Contains(roSrcs(plan), hooks) {
		t.Errorf("a hooksPath pointing into a NoScan mount is still writable and must be protected: %v", roSrcs(plan))
	}
}
