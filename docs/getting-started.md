---
title: Getting Started
description: Install inner and run your first sandboxed session
---

# Getting Started

## Prerequisites

`inner` uses [bubblewrap](https://github.com/containers/bubblewrap) for process isolation. Install it for your distro:

```bash
# Arch Linux
sudo pacman -S bubblewrap

# Debian / Ubuntu
sudo apt-get install bubblewrap

# Fedora
sudo dnf install bubblewrap
```

Also verify that unprivileged user namespaces are enabled:

```bash
cat /proc/sys/kernel/unprivileged_userns_clone
# must print: 1
```

## Installation

Build from source:

```bash
git clone https://github.com/enr/inner
cd inner
./.sdlc/build
# binary is placed in ./bin/inner
```

Move the binary somewhere on your `$PATH`:

```bash
sudo cp ./bin/inner /usr/local/bin/inner
```

## First Run

On the first invocation `inner` initializes `~/.inner/` and installs the built-in profiles:

```
~/.inner/
├── config.toml        # global configuration
├── profiles/          # sandbox profiles (TOML)
│   ├── default.toml
│   ├── shell.toml
│   ├── claude-interactive.toml
│   ├── claude-one-shot.toml
│   └── claude-containers.toml
└── logs/              # run logs
```

## Environment Check

Run `inner doctor` to verify that your environment is ready:

```bash
inner doctor
```

Example output:

```
[ok] bwrap found: /usr/bin/bwrap (version 0.9.0)
[ok] user namespaces: supported
[ok] profiles dir: /home/alice/.inner/profiles
[ok] logs dir: /home/alice/.inner/logs
[ok] ANTHROPIC_API_KEY: set
[ok] claude binary: /usr/local/bin/claude
[ok] display server: wayland (WAYLAND_DISPLAY=wayland-1)
```

Fix any `[FAIL]` items before proceeding. The most common issue is a missing `ANTHROPIC_API_KEY` — set it in your shell profile:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Quick Start

Start an interactive shell in the default sandbox:

```bash
inner run
```

Run Claude Code in interactive mode with network access:

```bash
inner run -p claude-interactive -w ~/my-project
```

Run a claude-one-shot agent task (non-interactive, returns when done):

```bash
inner run -p claude-one-shot -w ~/my-project --prompt "add docstrings to all exported functions"
```

See [Examples](examples.md) for more patterns, and [Profiles](profiles.md) to understand or customize the sandbox configuration.
