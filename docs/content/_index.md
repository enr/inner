---
title: inner
description: Isolated sandbox environments to run agents and scripts
---



`inner` is a Linux CLI tool that runs scripts and agentic tools — Claude Code, Gemini CLI, Cursor Agent, OpenCode, interactive shells — in isolated, reproducible sandbox environments backed by [bubblewrap (bwrap)](https://github.com/containers/bubblewrap).

<div class="download-banner">
  <strong>Latest release:</strong>
  <a class="download-btn" href="https://github.com/enr/inner/releases/latest">Download from GitHub Releases</a>
</div>

## Why inner?

Agentic tools have broad filesystem access and execute arbitrary commands. `inner` provides a layer of isolation so you can run them safely:

- **Process isolation** via Linux kernel namespaces
- **Filesystem separation** — the sandbox sees only what you explicitly mount
- **Repository protection** — git hooks and `.git/config` stay read-only in the sandbox, so an agent cannot plant code your next `git` command runs; commits still work
- **Environment sanitization** — the host environment is cleared by default, and git credential helpers are stripped from the injected gitconfig
- **Network control** — no network, full network, or an allowlist of destinations enforced by a host-side proxy; ports can be published to the host without opening the sandbox's network
- **Security verification** — detect if sensitive host resources leak into the sandbox
- **Reproducibility** — configuration-driven profiles define exactly what each run can access

## How it works

`inner` reads a **profile** (a TOML file in `~/.config/inner/profiles/`) and uses it to construct a `bwrap` command that wraps your tool. The profile controls network access, mounted paths, environment variables, command shimming, and more.

```
inner run -p claude-one-shot --arg "refactor the auth module"
```

## Documentation

| Page | Description |
|------|-------------|
| [Getting Started](/inner/getting-started/) | Install, first run, environment check |
| [Commands](/inner/commands/) | Full command and flag reference |
| [Profiles](/inner/profiles/) | Profile TOML configuration reference |
| [Aliases](/inner/aliases/) | Define short names for frequently used commands |
| [Examples](/inner/examples/) | Common usage patterns with explanations |
| [Cheat Sheet](/inner/cheatsheet/) | Quick reference for commands and tasks |
| [Development](/inner/development/) | Build, test, dev mode, release |
| [Internals](/inner/internals/) | bwrap command construction, flag decisions, architecture |

## Requirements

- Linux (kernel namespaces required)
- `bwrap` (bubblewrap) installed
- Unprivileged user namespaces enabled (`/proc/sys/kernel/unprivileged_userns_clone` = 1)
- Go 1.26+ (to build from source)

## License

`inner` is released under the [Apache License, Version 2.0](https://www.apache.org/licenses/LICENSE-2.0).
