# inner

`inner` is a Linux CLI tool that runs agentic tools — Claude Code, Aider, interactive shells — in isolated, reproducible sandbox environments backed by [bubblewrap (bwrap)](https://github.com/containers/bubblewrap).

## Why inner?

Agentic tools have broad filesystem access and execute arbitrary commands. `inner` provides a layer of isolation so you can run them safely:

- **Process isolation** via Linux kernel namespaces
- **Filesystem separation** — the sandbox sees only what you explicitly mount
- **Environment sanitization** — sensitive variables and git credentials are stripped
- **Security verification** — detect if sensitive host resources leak into the sandbox
- **Reproducibility** — configuration-driven profiles define exactly what each run can access

## Requirements

- Linux (kernel namespaces required)
- `bwrap` (bubblewrap) installed
- Unprivileged user namespaces enabled (`/proc/sys/kernel/unprivileged_userns_clone` = 1)
- Go 1.24+ (to build from source)

## Installation

```bash
git clone https://github.com/enr/inner
cd inner
./.sdlc/build
sudo cp ./bin/inner /usr/local/bin/inner
```

Or download a pre-built binary from the [releases page](https://github.com/enr/inner/releases).

## Quick Start

```bash
# check your environment
inner doctor

# interactive shell in the default sandbox
inner run

# Claude Code interactive session on a project
inner run -p claude-interactive -w ~/my-project

# one-shot agent task
inner run -p claude-one-shot -w ~/my-project --prompt "add docstrings to all exported functions"
```

## Documentation

| Page | Description |
|------|-------------|
| [Getting Started](docs/getting-started.md) | Install, first run, environment check |
| [Commands](docs/commands.md) | Full command and flag reference |
| [Profiles](docs/profiles.md) | Profile TOML configuration reference |
| [Examples](docs/examples.md) | Common usage patterns |
| [Development](docs/development.md) | Build, test, release |

## License

[Apache 2.0](LICENSE)
