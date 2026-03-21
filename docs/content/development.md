---
title: Development
description: Build, test, dev mode, and release workflows for inner
weight: 5
---

# Development

All SDLC commands live in `.sdlc/`. They are plain bash scripts that resolve the project root automatically, so they can be run from any directory.

## Requirements

- Go 1.24+
- `git`
- `bwrap` (to run the built binary)

## Build

Compile the binary with version metadata embedded at link time:

```bash
./.sdlc/build
```

The binary is written to `./bin/inner`.

**What the build script does:**

- Derives the version from `git describe --tags --always --dirty` (falls back to `"dev"` if no tags exist)
- Records the build timestamp (UTC) and git commit hash
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

Run tests for a single package directly:

```bash
go test -v ./internal/config/...
go test -v ./internal/isolator/...
```

## Developing inside inner

The repository ships a `profiles/inner-dev.toml` profile for running a Claude Code session sandboxed inside inner itself. It extends `claude-interactive` with two adjustments needed for Go development:

- `GOPATH=/tmp/gopath` and `GOCACHE=/tmp/gocache` — the sandbox root is read-only by default, so the Go toolchain needs writable paths for module cache and build cache
- `~/Projects/inner` mounted read-write — the project directory is accessible inside the sandbox at the same host path

Start the session from the project root:

```bash
inner run -p profiles/inner-dev.toml
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
├── cmd/inner/          # main() entry point + all Cobra commands
├── internal/
│   ├── config/         # profile loading, TOML parsing, RunConfig
│   ├── executor/       # process launch, PTY, logging, timeout
│   ├── git/            # gitconfig sanitization
│   ├── isolator/       # bwrap command builder
│   ├── profile/        # profile validation
│   ├── runtime/        # host detection (bwrap, namespaces, display)
│   ├── sandbox/        # security checks and verification report
│   ├── setup/          # ~/.inner init and default profile installation
│   ├── shim/           # blocking/rewriting shim generator
│   └── version/        # version metadata (set via ldflags)
├── .sdlc/
│   ├── build           # build binary
│   ├── run             # dev mode (go run)
│   ├── ci              # build + test
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
| `next-snapshot-version` | `1.3.0` | Version for the next development cycle |

**Example:**

```bash
./.sdlc/release 1.2.3 1.3.0
```

**What the script does:**

1. Validates that `git`, `grep` are available and the branch is aligned with upstream
2. Commits any staged changes with message `release 1.2.3`
3. Creates tag `v1.2.3`
4. Commits with message `[skip ci] back to snapshot` (for next dev cycle)
5. Pushes commits and tags to the remote

After release, `git describe --tags` will return `v1.2.3`, which `./sdlc/build` picks up automatically.

## Adding a New Command

1. Create `cmd/inner/<name>.go` with a Cobra `Command` struct
2. Register it in `cmd/inner/root.go` via `rootCmd.AddCommand(...)`
3. Implement logic in `internal/<package>/`
4. Add tests in `internal/<package>/<file>_test.go`
5. Run `./.sdlc/ci` to verify

## Dependencies

```
github.com/spf13/cobra       CLI framework
github.com/BurntSushi/toml   TOML parsing
github.com/creack/pty        PTY handling for interactive sessions
golang.org/x/term            Terminal size and raw mode
```

Add a dependency:

```bash
go get github.com/some/package
```

The `go.sum` file is committed alongside `go.mod`.
