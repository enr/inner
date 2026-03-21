---
title: inner
description: Run agentic tools in isolated sandbox environments
---

# inner

`inner` is a Linux CLI tool that runs agentic tools — Claude Code, Aider, interactive shells — in isolated, reproducible sandbox environments backed by [bubblewrap (bwrap)](https://github.com/containers/bubblewrap).

<div class="download-banner">
  <strong>Latest release:</strong>
  <a class="download-btn" href="https://github.com/enr/inner/releases/latest">Download from GitHub Releases</a>
</div>

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
| [Getting Started](getting-started/) | Install, first run, environment check |
| [Commands](commands/) | Full command and flag reference |
| [Profiles](profiles/) | Profile TOML configuration reference |
| [Examples](examples/) | Common usage patterns with explanations |
| [Development](development/) | Build, test, dev mode, release |
| [Internals](internals/) | bwrap command construction, flag decisions, architecture |

## Requirements

- Linux (kernel namespaces required)
- `bwrap` (bubblewrap) installed
- Unprivileged user namespaces enabled (`/proc/sys/kernel/unprivileged_userns_clone` = 1)
- Go 1.24+ (to build from source)

## License

`inner` is released under the [Apache License, Version 2.0](https://www.apache.org/licenses/LICENSE-2.0).
