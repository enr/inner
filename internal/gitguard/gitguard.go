// Package gitguard stops a sandbox that can write a git repository from
// planting code the HOST runs the next time the user types a git command.
//
// A read-write workdir normally includes the repository's .git directory, and
// several files in there make git execute programs: hooks, core.fsmonitor,
// core.hooksPath, core.sshCommand, filter and diff drivers, "!" aliases. The
// sandboxed process never runs them — the user does, outside the sandbox, on
// the next `git status` or `git commit`. That turns a writable .git into a
// sandbox escape.
//
// The planner inspects the writable mounts of a run and returns the extra
// binds the isolator lays over them:
//
//	protected  .git stays writable (commit, branch, rebase work) but every file
//	           that can make git run code is bound read-only: config, hooks/,
//	           commondir, config.worktree, include targets and a core.hooksPath
//	           that points into the writable tree — for the repository, its
//	           linked worktrees and its submodules.
//	ro         .git is bound read-only as a whole (git can read, not write).
//	rw         nothing is protected.
//
// Some of those files usually do not exist ("hooks" may be missing,
// "commondir" never exists in a main repository), and a file that does not
// exist cannot be a bind-mount point: the sandbox could simply create it.
// Such paths become placeholders the caller creates on the host before the
// sandbox starts (see Materialize).
//
// What protected mode does NOT cover, because no mount can: the sandbox can
// still create a brand-new repository anywhere in the writable tree and, by
// adding it to the index as a gitlink, make the host's `git status` recurse
// into it. That residual risk is documented for users; the ro mode closes it
// for the repository root (the index is read-only there).
package gitguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Modes accepted in [sandbox] git_dir.
const (
	ModeProtected = "protected"
	ModeRO        = "ro"
	ModeRW        = "rw"
)

// Modes is the exhaustive set of values accepted in [sandbox] git_dir. Empty
// (unset) means ModeProtected.
var Modes = []string{ModeProtected, ModeRO, ModeRW}

// Mount is a writable host directory and where it appears in the sandbox.
type Mount struct {
	Src  string
	Dest string
}

// Bind is one bind mount the isolator emits: Src on the host, Dest in the
// sandbox.
type Bind struct {
	Src  string
	Dest string
}

// PlaceholderKind says how a missing protected path is created on the host.
type PlaceholderKind int

const (
	// PlaceholderDir is an empty directory (a missing hooks/ or hooksPath).
	// Left in place after the run: git creates the same thing itself.
	PlaceholderDir PlaceholderKind = iota
	// PlaceholderFile is an empty file (a missing config or config.worktree).
	// Left in place after the run: an empty config file is valid.
	PlaceholderFile
	// PlaceholderCommonDir is a "commondir" file containing ".", which points
	// the repository at itself — the same as having no such file. An EMPTY
	// commondir would make every git command on the host die, so this kind is
	// written with content, and removed again once no run uses it.
	PlaceholderCommonDir
)

// Placeholder is a host path the caller must create before the sandbox starts.
type Placeholder struct {
	Path string
	Kind PlaceholderKind
}

// Plan is what the isolator and the caller need to apply the protection.
type Plan struct {
	// Mode is the resolved mode the plan was built for.
	Mode string
	// Writable are git directories outside every writable mount that a linked
	// worktree (or a submodule checked out as the workdir) needs in order to
	// commit. Emitted before ReadOnly, which lays the protection over them.
	Writable []Bind
	// ReadOnly are bound read-only on top of the writable mounts, parents
	// before children.
	ReadOnly []Bind
	// Placeholders must exist on the host before the sandbox starts.
	Placeholders []Placeholder
	// Warnings describe paths that could not be protected.
	Warnings []string
}

// Empty reports whether the plan changes nothing.
func (p Plan) Empty() bool {
	return len(p.Writable) == 0 && len(p.ReadOnly) == 0 && len(p.Placeholders) == 0
}

// maxModuleDepth bounds the walk of .git/modules: submodule names may contain
// slashes, so a gitdir can sit several directories deep.
const maxModuleDepth = 16

type planner struct {
	mode   string
	mounts []Mount
	plan   Plan

	roSeen     map[string]bool
	gitDirSeen map[string]bool
	phSeen     map[string]bool
	// globalHooksPaths are core.hooksPath values from the host's global and
	// system git config. A relative one applies to every repository, so it can
	// point into the writable tree just like a repository-level one.
	globalHooksPaths []string
}

// Build returns the protection plan for the given writable mounts. An empty
// mode means ModeProtected. The mounts are inspected on the host filesystem;
// Build writes nothing.
//
// For each mount, the mount root and its direct subdirectories are checked for
// a .git entry, so a workdir that is a directory of projects is covered one
// level deep.
func Build(mode string, mounts []Mount) Plan {
	if mode == "" {
		mode = ModeProtected
	}
	p := &planner{
		mode:       mode,
		mounts:     cleanMounts(mounts),
		plan:       Plan{Mode: mode},
		roSeen:     make(map[string]bool),
		gitDirSeen: make(map[string]bool),
		phSeen:     make(map[string]bool),
	}
	if mode == ModeProtected {
		p.globalHooksPaths = hostHooksPaths()
	}
	// rw still scans: a linked worktree's gitdir is bound writable so commits
	// work there too.
	p.scan()
	// Parents before children: a bind of a directory emitted after a bind of a
	// file inside it would cover the inner one.
	slices.SortStableFunc(p.plan.Writable, byDepth)
	slices.SortStableFunc(p.plan.ReadOnly, byDepth)
	return p.plan
}

func byDepth(a, b Bind) int {
	return strings.Count(a.Dest, "/") - strings.Count(b.Dest, "/")
}

func cleanMounts(in []Mount) []Mount {
	out := make([]Mount, 0, len(in))
	for _, m := range in {
		if m.Src == "" || m.Dest == "" {
			continue
		}
		out = append(out, Mount{Src: filepath.Clean(m.Src), Dest: filepath.Clean(m.Dest)})
	}
	return out
}

func (p *planner) scan() {
	for _, m := range p.mounts {
		p.inspect(m.Src)
		entries, err := os.ReadDir(m.Src)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == ".git" {
				continue
			}
			p.inspect(filepath.Join(m.Src, e.Name()))
		}
	}
}

// inspect handles a directory that may be the top of a git worktree.
func (p *planner) inspect(root string) {
	dotgit := filepath.Join(root, ".git")
	fi, err := os.Lstat(dotgit)
	if err != nil {
		return
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		if p.mode != ModeRW {
			p.warn("%s is a symbolic link: the repository behind it is not protected; replace the link with the directory, or keep this run away from it", dotgit)
		}
	case fi.IsDir():
		switch p.mode {
		case ModeRO:
			p.ro(dotgit)
		case ModeProtected:
			p.gitDir(dotgit, root)
		}
	case fi.Mode().IsRegular():
		p.gitFile(dotgit, root)
	}
}

// gitFile handles a ".git" file: a linked worktree or a submodule checkout.
// The file redirects git to another directory, so outside rw mode it is bound
// read-only — otherwise the sandbox could point it at a gitdir of its own.
func (p *planner) gitFile(dotgit, root string) {
	if p.mode != ModeRW {
		p.ro(dotgit)
	}
	gitdir, err := readGitFile(dotgit)
	if err != nil {
		if p.mode != ModeRW {
			p.warn("%s: %v", dotgit, err)
		}
		return
	}
	common := commonDirOf(gitdir)

	if p.mode == ModeRO {
		// The gitdir of a linked worktree lives outside the workdir and is
		// read-only there already; one inside a writable mount is not.
		if p.writable(gitdir) {
			p.ro(gitdir)
		}
		if common != gitdir && p.writable(common) {
			p.ro(common)
		}
		return
	}

	// protected and rw: make commits possible by exposing the gitdir (and the
	// shared common dir of a linked worktree) writable at its own path.
	for _, dir := range []string{common, gitdir} {
		if p.writable(dir) {
			continue
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		p.plan.Writable = append(p.plan.Writable, Bind{Src: dir, Dest: dir})
	}
	if p.mode == ModeProtected {
		// protecting the common dir covers the worktree's own admin dir under
		// worktrees/, and a submodule gitdir is its own common dir.
		p.gitDir(common, root)
	}
}

// gitDir protects one git directory. worktree is the top of the working tree
// it serves ("" when unknown), used to resolve a relative core.hooksPath.
func (p *planner) gitDir(dir, worktree string) {
	dir = filepath.Clean(dir)
	if p.gitDirSeen[dir] {
		return
	}
	p.gitDirSeen[dir] = true

	cfgPath := filepath.Join(dir, "config")
	p.protectFile(cfgPath, PlaceholderFile, true)
	p.protectDir(filepath.Join(dir, "hooks"))
	// "commondir" redirects config and hooks to another directory. A main
	// repository has none — which is exactly why the sandbox could create one.
	p.protectFile(filepath.Join(dir, "commondir"), PlaceholderCommonDir, true)

	entries, includes := readConfigChain(cfgPath)
	wtExt := false
	if e, ok := lookup(entries, "extensions", "worktreeconfig"); ok && e.isTrue() {
		wtExt = true
	}
	// config.worktree is read only when extensions.worktreeConfig is on; the
	// sandbox cannot turn that on (config is read-only), so a missing file
	// needs a placeholder only when the extension is already enabled.
	wtCfg := filepath.Join(dir, "config.worktree")
	p.protectFile(wtCfg, PlaceholderFile, wtExt)
	if wtExt {
		e2, inc2 := readConfigChain(wtCfg)
		entries = append(entries, e2...)
		includes = append(includes, inc2...)
	}

	for _, inc := range includes {
		if !p.writable(inc) {
			continue
		}
		// A missing include target is ignored by git today and read tomorrow,
		// once the sandbox creates it. Creating it here would drop a stray
		// file into the user's tree, so it is reported instead.
		p.protectFile(inc, PlaceholderFile, false)
		if _, err := os.Lstat(inc); errors.Is(err, fs.ErrNotExist) {
			p.warn("git config includes %s, which does not exist and could be created inside the sandbox", inc)
		}
	}

	hooksPaths := slices.Clone(p.globalHooksPaths)
	if e, ok := lookup(entries, "core", "hookspath"); ok && e.hasValue {
		// The repository value overrides the global one.
		hooksPaths = []string{e.value}
	}
	for _, hp := range hooksPaths {
		resolved := resolveHooksPath(hp, worktree, dir)
		if resolved != "" && p.writable(resolved) {
			p.protectDir(resolved)
		}
	}

	// Linked worktrees: each admin dir has a "commondir" file pointing back
	// here, and possibly its own config.worktree.
	if wts, err := os.ReadDir(filepath.Join(dir, "worktrees")); err == nil {
		for _, wt := range wts {
			if !wt.IsDir() {
				continue
			}
			admin := filepath.Join(dir, "worktrees", wt.Name())
			cd := filepath.Join(admin, "commondir")
			if _, err := os.Lstat(cd); errors.Is(err, fs.ErrNotExist) {
				p.warn("%s has no commondir file: linked worktree %s is not protected", admin, wt.Name())
			} else {
				p.protectFile(cd, PlaceholderFile, false)
			}
			p.protectFile(filepath.Join(admin, "config.worktree"), PlaceholderFile, wtExt)
		}
	}

	p.modules(filepath.Join(dir, "modules"), 0)
}

// modules walks .git/modules for submodule gitdirs and protects each one,
// together with the ".git" file of its checkout.
func (p *planner) modules(dir string, depth int) {
	if depth > maxModuleDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if !isGitDir(sub) {
			// Submodule names may contain "/": keep descending.
			p.modules(sub, depth+1)
			continue
		}
		worktree := ""
		entries, _ := readConfigChain(filepath.Join(sub, "config"))
		if wt, ok := lookup(entries, "core", "worktree"); ok && wt.hasValue && wt.value != "" {
			worktree = wt.value
			if !filepath.IsAbs(worktree) {
				worktree = filepath.Join(sub, worktree)
			}
			worktree = filepath.Clean(worktree)
			dotgit := filepath.Join(worktree, ".git")
			if fi, err := os.Lstat(dotgit); err == nil && fi.Mode().IsRegular() && p.writable(dotgit) {
				p.ro(dotgit)
			}
		}
		p.gitDir(sub, worktree)
	}
}

// protectFile binds an existing regular file read-only. A missing one becomes
// a placeholder of the given kind when create is true, and is skipped
// otherwise. Anything else (a symlink, a directory) cannot be protected by a
// bind and is reported.
func (p *planner) protectFile(path string, kind PlaceholderKind, create bool) {
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.Mode().IsRegular():
		p.ro(path)
	case err == nil:
		p.warn("%s is not a regular file and cannot be protected", path)
	case errors.Is(err, fs.ErrNotExist):
		if create {
			p.placeholder(path, kind)
			p.ro(path)
		}
	default:
		p.warn("%s: %v", path, err)
	}
}

// protectDir binds an existing directory read-only, or a placeholder directory
// when it is missing.
func (p *planner) protectDir(path string) {
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.IsDir():
		p.ro(path)
	case err == nil:
		// A symlinked hooks dir: binding over the link would protect its
		// target, while the link itself stays replaceable.
		p.warn("%s is not a directory (a symbolic link?) and cannot be protected", path)
	case errors.Is(err, fs.ErrNotExist):
		p.placeholder(path, PlaceholderDir)
		p.ro(path)
	default:
		p.warn("%s: %v", path, err)
	}
}

func (p *planner) ro(host string) {
	host = filepath.Clean(host)
	dest := p.sandboxPath(host)
	if p.roSeen[dest] {
		return
	}
	p.roSeen[dest] = true
	p.plan.ReadOnly = append(p.plan.ReadOnly, Bind{Src: host, Dest: dest})
}

func (p *planner) placeholder(path string, kind PlaceholderKind) {
	if p.phSeen[path] {
		return
	}
	p.phSeen[path] = true
	p.plan.Placeholders = append(p.plan.Placeholders, Placeholder{Path: path, Kind: kind})
}

func (p *planner) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !slices.Contains(p.plan.Warnings, msg) {
		p.plan.Warnings = append(p.plan.Warnings, msg)
	}
}

// mountFor returns the writable mount containing host, preferring the deepest.
func (p *planner) mountFor(host string) (Mount, bool) {
	var best Mount
	found := false
	for _, m := range p.mounts {
		if within(host, m.Src) && (!found || len(m.Src) > len(best.Src)) {
			best, found = m, true
		}
	}
	return best, found
}

// writable reports whether host is writable inside the sandbox: under a
// writable mount, or under a gitdir this plan exposes writable.
func (p *planner) writable(host string) bool {
	host = filepath.Clean(host)
	if _, ok := p.mountFor(host); ok {
		return true
	}
	for _, b := range p.plan.Writable {
		if within(host, b.Src) {
			return true
		}
	}
	return false
}

// sandboxPath maps a host path to where it appears in the sandbox. Paths
// outside every mount keep their host path (the root is bound at /).
func (p *planner) sandboxPath(host string) string {
	m, ok := p.mountFor(host)
	if !ok {
		return host
	}
	rel, err := filepath.Rel(m.Src, host)
	if err != nil {
		return host
	}
	return filepath.Join(m.Dest, rel)
}

func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) || dir == "/"
}

// readGitFile parses a ".git" file ("gitdir: <path>") and returns the
// absolute, cleaned gitdir.
func readGitFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", errors.New(`not a "gitdir:" file`)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("empty gitdir")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), nil
}

// commonDirOf returns the common dir of gitdir: the target of its commondir
// file (linked worktrees), or gitdir itself.
func commonDirOf(gitdir string) string {
	data, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return gitdir
	}
	cd := strings.TrimSpace(string(data))
	if cd == "" {
		return gitdir
	}
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(gitdir, cd)
	}
	return filepath.Clean(cd)
}

// isGitDir is a cheap test for "this directory is a git directory".
func isGitDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	for _, name := range []string{"objects", "commondir"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// resolveHooksPath resolves core.hooksPath as git does: "~/" is the home
// directory; a relative path is relative to where hooks run, the top of the
// working tree (the gitdir when there is no known working tree).
func resolveHooksPath(value, worktree, gitdir string) string {
	p := expandTilde(value)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		base := worktree
		if base == "" {
			base = gitdir
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// hostHooksPaths returns core.hooksPath from the host's system and global git
// config (following includes), in increasing precedence.
func hostHooksPaths() []string {
	var files []string
	files = append(files, "/etc/gitconfig")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		files = append(files, filepath.Join(xdg, "git", "config"))
	} else if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".config", "git", "config"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".gitconfig"))
	}
	var last string
	for _, f := range files {
		entries, _ := readConfigChain(f)
		if e, ok := lookup(entries, "core", "hookspath"); ok && e.hasValue {
			last = e.value
		}
	}
	if last == "" {
		return nil
	}
	return []string{last}
}
