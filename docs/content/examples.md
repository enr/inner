---
title: Examples
description: Common usage patterns with explanations
weight: 4
---



## One-Shot Agent Command

A **claude-one-shot** run starts Claude Code in non-interactive mode, executes a single task, and exits. It is suitable for CI pipelines, scripted automation, and batch processing.

The `claude-one-shot` built-in profile configures:
- `--dangerously-skip-permissions` so Claude does not pause to ask for approval
- `interactive = false` so no PTY is allocated
- Network disabled (override with `--network` if needed)

### Basic claude-one-shot

```bash
inner run -p claude-one-shot -w ~/projects/myapp --arg "add type annotations to all Python functions"
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
  --arg "write unit tests for all exported Go functions"
```

The sandbox is killed after 5 minutes if still running.

### One-shot with network access

Some tasks need to fetch dependencies or call APIs:

```bash
inner run -p claude-one-shot --network -w ~/projects/myapp \
  --arg "update all npm dependencies to their latest minor version"
```

### One-shot in CI

```bash
# Set in environment: ANTHROPIC_API_KEY
inner run -p claude-one-shot -w "$(pwd)" --timeout 600 \
  --arg "review the diff in /workspace and write a summary to /workspace/review.md"
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
  --arg "build the Dockerfile and run a smoke test against the container"
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
  --arg "run the migration and confirm success"
```

---

## Pinning a Specific JDK Version

If the host has multiple JDKs installed side by side (e.g. `/opt/jdk/jdk-21` and `/opt/jdk/jdk-26`), use `[env] path_prepend` to make `java` resolve to one specific installation inside the sandbox, without rewriting the rest of `PATH`:

```toml
# ~/.config/inner/profiles/java-21.toml
schema_version = "1"
name           = "java-21"
description    = "Java shell pinned to JDK 21"
extends        = "shell"

[env]
path_prepend = ["/opt/jdk/jdk-21/bin"]
set          = { JAVA_HOME = "/opt/jdk/jdk-21" }

[verify.custom]
checks = [
  { name = "java resolves to the pinned JDK", cmd = "test \"$(readlink -f \"$(command -v java)\")\" = \"$(readlink -f /opt/jdk/jdk-21/bin/java)\"", severity = "critical" },
  { name = "JAVA_HOME matches the pinned JDK", cmd = "test \"$JAVA_HOME\" = /opt/jdk/jdk-21", severity = "high" },
]
```

```bash
inner run -p java-21
# inside the sandbox:
java -version   # reports 21, regardless of what "java" resolves to on the host

inner verify -p java-21   # confirms both custom checks pass
```

A ready-to-use version of this profile with a Maven cache mount is available as the `java-21` contrib profile (see [Contrib Profiles](profiles.md#contrib-profiles)).

Switch to a different installed version for a single run without editing the profile, using the `-e`/`--env` flag (values are expanded against the host environment the same way `[env] set` values are):

```bash
inner run -p java-21 \
  -e JAVA_HOME=/opt/jdk/jdk-26 \
  -e "PATH=/opt/jdk/jdk-26/bin:$PATH"
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

## Multi-Source Workspace

When a project depends on a library and you want the agent to see both source trees at the same time, define a profile with multiple mounts. This pattern is useful for tasks like "refactor myapp to use the new API in mylib" where the agent needs to read the library source, not just its compiled artifacts.

### Directory layout

```
~/Projects/
  myapp/                        ← your application
  mylib/                        ← the dependency you're also working on
  workspaces/                   ← mount-point staging area (created automatically)
  .config/
    inner.toml                  ← local config with workspaces_path and aliases
    inner/
      profiles/
        myapp-workspace.toml    ← profile that mounts both trees
```

The `.config/inner.toml` file sits at the root of your projects folder and is picked up automatically by `inner` as a [local config](/inner/configuration) during the root-to-cwd traversal.

### Local config: `~/Projects/.config/inner.toml`

```toml
workspaces_path = "~/Projects/workspaces"

[aliases]
myapp = "run -p myapp-workspace"
```

`workspaces_path` points to a directory on the host where `inner` will pre-create the
mount-point directories before starting `bwrap`. The `myapp` alias is a shorthand for
the full profile run command.

> `~/Projects/workspaces/` must exist on the host before running:
> ```bash
> mkdir -p ~/Projects/workspaces
> ```

### Profile: `myapp-workspace.toml`

```toml
schema_version = "1"
name           = "myapp-workspace"
description    = "Interactive session with myapp (rw) and mylib sources (ro)"
extends        = "shell-with-claude"

[mounts]
"~/Projects/myapp" = { dest = "${workspaces_path}/myapp", mode = "rw" }
"~/Projects/mylib" = { dest = "${workspaces_path}/mylib", mode = "ro" }

[entrypoint]
workdir = "${workspaces_path}/myapp"
```

`${workspaces_path}` is substituted with `~/Projects/workspaces` at runtime.
`inner` pre-creates `workspaces/myapp` and `workspaces/mylib` before starting `bwrap`,
so the mount destinations exist when needed. `entrypoint.workdir` sets the initial
working directory inside the sandbox.

The agent sees:

```
~/Projects/workspaces/
  myapp/    ← read-write: the agent can edit files here
  mylib/    ← read-only: reference only, cannot be modified
```

### Usage

Start an interactive session (lands in `workspaces/myapp`):

```bash
cd ~/Projects
inner myapp
```

One-shot task referencing both trees:

```bash
inner run -p myapp-workspace \
  --arg "update myapp to use the new Config API introduced in mylib v2"
```

Override workdir at runtime (e.g. to start in the library tree):

```bash
inner run -p myapp-workspace -w ~/Projects/workspaces/mylib
```

With an explicit extra mount added at runtime:

```bash
inner run -p myapp-workspace -m /tmp/build-cache:/cache:rw \
  --arg "rebuild and run the integration tests"
```

### When to use an alias vs a profile

Use a **profile** when the set of source trees is fixed and you use it regularly — it is versioned alongside your projects and readable at a glance.

Use **CLI flags** when the combination is ad-hoc:

```bash
inner run -p claude-interactive \
  -m ~/Projects/myapp:~/Projects/myapp:rw \
  -m ~/Projects/mylib:~/Projects/mylib:ro
```

With CLI flags the dest paths must already exist on the host (or be existing host
paths used as src=dest). The profile + `${workspaces_path}` approach handles
pre-creation automatically.

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
