---
title: Development
description: Build, test, dev mode, and release workflows for inner
weight: 5
---



All SDLC commands live in `.sdlc/`. They are plain bash scripts that resolve the project root automatically, so they can be run from any directory.

## Requirements

- Go 1.26+ (the version in `go.mod`)
- `git`
- `bwrap` (to run the built binary)

## Build

Compile the binary with version metadata embedded at link time:

```bash
./.sdlc/build
```

The binary is written to `./bin/inner`.

**What the build script does:**

- Takes the version from `APP_VERSION` if set, else from the `VERSION` file, else from `git describe --tags --always --dirty` (`"dev"` if that fails too)
- Records the build timestamp (UTC) and the git commit hash (`+CHANGES` appended when the tree is dirty)
- Passes all three as `-ldflags` to `go build`

You can override the version with an environment variable:

```bash
APP_VERSION=1.2.3 ./.sdlc/build
```

## Dev Mode

Run the tool directly via `go run` without building a binary first:

```bash
./.sdlc/run [command] [flags]
```

This is equivalent to `go run ./cmd/inner` and forwards all arguments:

```bash
./.sdlc/run doctor
./.sdlc/run profile list
./.sdlc/run run -p shell --dry-run
```

Changes to Go source files are picked up immediately on the next invocation — no rebuild step needed.

## Tests

Run the full test suite (build check + tests):

```bash
./.sdlc/ci
```

**What the CI script does:**

1. `go build ./...` — verifies all packages compile
2. `go test -count=1 -v ./...` — runs all tests, bypassing the test cache
3. Prints a summary line per package and the total test count

Other checks:

| Script | What it does |
|--------|--------------|
| `./.sdlc/lint` | `gofmt` check, `go vet`, and `staticcheck` when installed |
| `./.sdlc/fmt` | `gofmt -w` on every Go file |
| `./.sdlc/e2e` | end-to-end tests against a real `bwrap`; needs `inner` on `PATH` |
| `./.sdlc/e2e-security` | end-to-end checks of the security fixes a unit test cannot prove (session bus, remote profiles, symlinks, Maven); run as your normal user in a desktop session, outside any sandbox |

Run tests for a single package directly:

```bash
go test -v ./internal/config/...
go test -v ./internal/isolator/...
```

## Developing inside inner

The repository ships a local profile, `.config/inner/profiles/inner-dev.toml`, for developing inner from inside a sandbox: a bash shell (`cursor_fix = "shell"`) with the `claude` capability and the network on, so you can start Claude Code from it. Two settings are specific to Go development:

- `GOPATH=/tmp/gopath` and `GOCACHE=/tmp/gocache` — the sandbox root is read-only by default, so the Go toolchain needs writable paths for module cache and build cache
- `/run/user/$UID` is replaced by a tmpfs, which hides the host's runtime sockets

The repository itself is the workdir, mounted read-write at its own path. Being a local profile, it is found by name from the project root:

```bash
inner run -p inner-dev
```

Inside the sandbox you can build and test normally:

```bash
GOCACHE=/tmp/gocache GOPATH=/tmp/gopath go test ./...
GOCACHE=/tmp/gocache GOPATH=/tmp/gopath ./.sdlc/ci
```

The env vars are already set by the profile, so if you rely on Claude Code running those commands they will work without extra flags.

## Project Layout

```
inner/
├── cmd/inner/          # main() entry point, all Cobra commands, host-side sandbox preparation
├── internal/
│   ├── config/         # profile loading, TOML parsing, merge, RunConfig
│   ├── containers/     # containers.conf override for rootless Podman inside the sandbox
│   ├── executor/       # process launch, PTY, logging, timeout
│   ├── git/            # gitconfig sanitization
│   ├── gitguard/       # .git protection plan and placeholders
│   ├── isolator/       # bwrap command builder
│   ├── landlock/       # Landlock scope for host abstract unix sockets
│   ├── netproxy/       # allowlist proxy and its policy
│   ├── profile/        # profile validation
│   ├── runtime/        # host detection (bwrap, namespaces, display)
│   ├── sandbox/        # security checks and verification report
│   ├── setup/          # ~/.config/inner init and built-in profile installation
│   ├── shim/           # blocking/rewriting shim generator
│   ├── version/        # version metadata (set via ldflags)
│   └── workspace/      # ${workspaces_path} directories and run locks
├── contrib/profiles/   # optional profiles, installed with `inner profile install`
├── docs/               # this site (Hugo)
├── .sdlc/
│   ├── build           # build binary
│   ├── install         # build and copy to ~/bin (or the given directory)
│   ├── run             # dev mode (go run)
│   ├── ci              # build + test
│   ├── lint, fmt       # formatting and static checks
│   ├── e2e*            # end-to-end tests with a real bwrap
│   ├── update          # update dependencies, go mod tidy
│   ├── website         # build / serve documentation site
│   └── release         # tag and push a release
└── go.mod              # module: github.com/enr/inner
```

## Release

The release script tags the current commit and pushes to the remote. It requires:

- A clean working tree (no uncommitted changes)
- The local branch to be in sync with its upstream

```bash
./.sdlc/release <release-version> <next-snapshot-version>
```

**Arguments:**

| Argument | Example | Description |
|----------|---------|-------------|
| `release-version` | `1.2.3` | Version to release; creates git tag `v1.2.3` |
| `next-snapshot-version` | `1.3.0` | Version for the next development cycle; `-SNAPSHOT` is appended |

**Example:**

```bash
./.sdlc/release 1.2.3 1.3.0
```

**What the script does:**

1. Validates that `git` and `grep` are available, the branch is aligned with upstream, the tag does not exist yet and the tree is clean
2. Writes `1.2.3` to `VERSION` and to `currentAppVersion` in `docs/hugo.toml`, and commits them as `release 1.2.3`
3. Creates the annotated tag `v1.2.3`
4. Writes `1.3.0-SNAPSHOT` to the same two files and commits them as `[skip ci] prepare snapshot 1.3.0-SNAPSHOT`
5. Pushes commits and tags to the remote

`./.sdlc/build` reads the version from `VERSION`, so a build of the tagged commit reports `1.2.3` and later builds `1.3.0-SNAPSHOT`.

## Adding a New Command

1. Create `cmd/inner/cmd_<name>.go` with an `(a *App) new<Name>Cmd() *cobra.Command` constructor
2. Register it in `buildRootCmd` (`cmd/inner/root.go`) with `root.AddCommand(app.new<Name>Cmd())`
3. Implement logic in `internal/<package>/`
4. Add tests in `internal/<package>/<file>_test.go`
5. Run `./.sdlc/ci` to verify

## Dependencies

```
github.com/spf13/cobra       CLI framework
github.com/BurntSushi/toml   TOML parsing
golang.org/x/sys             Linux system calls (Landlock, flock, ioctl)
golang.org/x/term            Terminal size and raw mode
```

Add a dependency:

```bash
go get github.com/some/package
```

The `go.sum` file is committed alongside `go.mod`.
