---
title: inner
description: Run agentic tools in isolated sandbox environments
---

# inner

`inner` is a Linux CLI tool that runs agentic tools — Claude Code, Aider, interactive shells — in isolated, reproducible sandbox environments backed by [bubblewrap (bwrap)](https://github.com/containers/bubblewrap).

## Why inner?

Agentic tools have broad filesystem access and execute arbitrary commands. `inner` provides a layer of isolation so you can run them safely:

- **Process isolation** via Linux kernel namespaces
- **Filesystem separation** — the sandbox sees only what you explicitly mount
- **Environment sanitization** — sensitive variables and git credentials are stripped
- **Security verification** — detect if sensitive host resources leak into the sandbox
- **Reproducibility** — configuration-driven profiles define exactly what each run can access

## How it works

`inner` reads a **profile** (a TOML file in `~/.inner/profiles/`) and uses it to construct a `bwrap` command that wraps your tool. The profile controls network access, mounted paths, environment variables, command shimming, and more.

```
inner run -p claude-one-shot --prompt "refactor the auth module"
```

## Documentation

| Page | Description |
|------|-------------|
| [Getting Started](getting-started.md) | Install, first run, environment check |
| [Commands](commands.md) | Full command and flag reference |
| [Profiles](profiles.md) | Profile TOML configuration reference |
| [Examples](examples.md) | Common usage patterns with explanations |
| [Development](development.md) | Build, test, dev mode, release |

## Requirements

- Linux (kernel namespaces required)
- `bwrap` (bubblewrap) installed
- Unprivileged user namespaces enabled (`/proc/sys/kernel/unprivileged_userns_clone` = 1)
- Go 1.24+ (to build from source)
