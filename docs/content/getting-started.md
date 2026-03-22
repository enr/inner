---
title: Getting Started
description: Install inner and run your first sandboxed session
weight: 1
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

Download the latest binary from [GitHub Releases](https://github.com/enr/inner/releases/latest):

```bash
# Download and extract (replace VERSION and ARCH as needed)
curl -L https://github.com/enr/inner/releases/latest/download/inner-linux-amd64.zip -o inner.zip
unzip inner.zip
sudo mv inner /usr/local/bin/inner
```

Or build from source:

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
├── config.toml        # global configuration (default_profile = "shell")
├── profiles/          # sandbox profiles (TOML)
│   ├── shell.toml
│   ├── shell-oneshot.toml
│   ├── shell-with-claude.toml
│   ├── shell-containers.toml
│   ├── claude-interactive.toml
│   ├── claude-one-shot.toml
│   ├── claude-containers.toml
│   ├── gemini-interactive.toml
│   └── gemini-one-shot.toml
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

Start an interactive shell in the default sandbox (`shell` profile by default):

```bash
inner run
```

To change the default profile, edit `~/.inner/config.toml`:

```toml
default_profile = "claude-interactive"
```

Run Claude Code in interactive mode with network access:

```bash
inner run -p claude-interactive -w ~/my-project
```

Run a claude-one-shot agent task (non-interactive, returns when done):

```bash
inner run -p claude-one-shot -w ~/my-project --arg "add docstrings to all exported functions"
```

See [Examples](../examples/) for more patterns, and [Profiles](../profiles/) to understand or customize the sandbox configuration.
