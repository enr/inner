---
title: Examples
description: Common usage patterns with explanations
weight: 4
---

# Examples

## One-Shot Agent Command

A **claude-one-shot** run starts Claude Code in non-interactive mode, executes a single task, and exits. It is suitable for CI pipelines, scripted automation, and batch processing.

The `claude-one-shot` built-in profile configures:
- `--dangerously-skip-permissions` so Claude does not pause to ask for approval
- `interactive = false` so no PTY is allocated
- Network disabled (override with `--network` if needed)

### Basic claude-one-shot

```bash
inner run -p claude-one-shot -w ~/projects/myapp --prompt "add type annotations to all Python functions"
```

**What happens:**
1. `inner` builds a sandbox using the `claude-one-shot` profile
2. `~/projects/myapp` is mounted read-write at `/workspace`
3. Claude Code starts, receives the prompt, performs the task
4. The process exits when Claude is done; you get back a shell

### One-shot with a timeout

Prevent runaway sessions by setting a timeout in seconds:

```bash
inner run -p claude-one-shot -w ~/projects/myapp --timeout 300 \
  --prompt "write unit tests for all exported Go functions"
```

The sandbox is killed after 5 minutes if still running.

### One-shot with network access

Some tasks need to fetch dependencies or call APIs:

```bash
inner run -p claude-one-shot --network -w ~/projects/myapp \
  --prompt "update all npm dependencies to their latest minor version"
```

### One-shot in CI

```bash
# Set in environment: ANTHROPIC_API_KEY
inner run -p claude-one-shot -w "$(pwd)" --timeout 600 \
  --prompt "review the diff in /workspace and write a summary to /workspace/review.md"
```

### Extra arguments via `--`

Pass arbitrary arguments to Claude Code directly:

```bash
inner run -p claude-one-shot -w ~/myapp -- --model claude-opus-4-5 --print "explain the architecture"
```

---

## Running a Docker Image (Podman Rootless)

`inner` does not natively run Docker images, but the `claude-containers` profile grants Claude Code access to a **Podman rootless** socket so the agent can build and run containers inside the sandbox.

The `claude-containers` profile sets:
- `allow = ["nested-user-ns", "podman-socket"]` — grants nested namespaces and the Podman socket
- `rewrite = { "docker" = "podman" }` — transparently rewrites `docker` CLI calls to `podman`

### Prerequisite: Podman socket running

```bash
systemctl --user start podman.socket
systemctl --user enable podman.socket
```

Verify:

```bash
podman info --format '{{.Host.RemoteSocket.Path}}'
# /run/user/1000/podman/podman.sock
```

### Start an agent session with container access

```bash
inner run -p claude-containers -w ~/projects/myapp
```

Inside this sandbox, Claude Code can:

```bash
# These all work inside the claude-containers sandbox:
docker run --rm hello-world           # rewritten to: podman run ...
docker build -t myapp .
docker-compose up -d                  # rewritten to: podman-compose up -d
podman pull nginx:latest
```

### One-shot with container access

```bash
inner run -p claude-containers -w ~/projects/myapp \
  --prompt "build the Dockerfile and run a smoke test against the container"
```

### Verify the sandbox sees the Podman socket

```bash
inner verify -p claude-containers
```

The verifier checks that the socket is accessible and the Podman API responds.

---

## Interactive Agent Session

Start a full interactive Claude Code session inside an isolated workspace:

```bash
inner run -p claude-interactive -w ~/projects/myapp
```

The agent has:
- Full read-write access to `/workspace` (your project)
- Network access enabled
- Git credentials and SSH keys stripped

End the session normally (`/exit` inside Claude, or `Ctrl-D`).

---

## Interactive Shell

Start a plain bash shell in the sandbox to inspect the environment:

```bash
inner run -p shell
```

Useful for:
- Debugging what the sandbox looks like before running an agent
- Testing which commands are blocked
- Manually running scripts in isolation

Mount a directory for inspection:

```bash
inner run -p shell -w ~/projects/myapp
```

---

## Extra Mounts

Mount additional directories with explicit access modes:

```bash
# Read-only reference data
inner run -p claude-interactive -w ~/myapp -m ~/datasets:/data:ro

# Read-write shared build artifacts
inner run -p claude-interactive -w ~/myapp -m /tmp/artifacts:/artifacts:rw
```

Multiple `-m` flags are allowed.

---

## Passing Environment Variables

Inject variables into the sandbox at runtime:

```bash
inner run -p claude-one-shot \
  -e DATABASE_URL=postgres://localhost/mydb \
  -e LOG_LEVEL=debug \
  -w ~/myapp \
  --prompt "run the migration and confirm success"
```

---

## Dry Run: Inspect the bwrap Command

See exactly what `bwrap` command `inner` would execute, without actually running it:

```bash
inner run -p claude-interactive -w ~/myapp --dry-run
```

This is useful for debugging profile configuration or understanding what the sandbox exposes.

---

## Security Verification

After writing or modifying a profile, verify that sensitive resources are not exposed:

```bash
inner verify -p my-profile
```

If checks fail, add `--suggest` to get TOML snippets to fix them:

```bash
inner verify -p my-profile --suggest
```

---

## Cleaning Up Logs

List all run logs:

```bash
inner log list
```

Preview what would be deleted (dry run, older than 7 days):

```bash
inner log clean --dry-run --older-than 7
```

Delete logs older than 30 days:

```bash
inner log clean --older-than 30
```
