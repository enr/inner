---
title: Getting Started
description: Install inner and run your first sandboxed session
weight: 1
---



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

{{< download-linux >}}

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

On the first invocation `inner` initializes `~/.config/inner/` and installs the built-in profiles:

```
~/.config/inner/
├── config.toml        # global configuration (default_profile = "shell")
├── profiles/          # sandbox profiles (TOML)
│   ├── shell.toml
│   ├── shell-oneshot.toml
│   ├── claude-interactive.toml
│   ├── claude-one-shot.toml
│   ├── gemini-interactive.toml
│   ├── gemini-one-shot.toml
│   └── cursor-interactive.toml
├── directives/
└── logs/              # run records and output logs
```

More specific profiles (Podman, language toolchains, OpenCode, OpenClaw) are
available as [contrib profiles](/inner/profiles/#contrib-profiles), installed on
demand.

## Environment Check

Run `inner doctor` to verify that your environment is ready:

```bash
inner doctor
```

Example output:

```
[ok]  bwrap: v0.9.0 (/usr/bin/bwrap)
[ok]  user namespaces: enabled
[ok]  terminal injection (TIOCSTI): blocked by the kernel (dev.tty.legacy_tiocsti = 0)
[ok]  abstract unix sockets: host sockets blocked for network = full sandboxes (Landlock ABI 6)
[ok]  profiles: 7 profile(s) in /home/alice/.config/inner/profiles
[ok]  logs dir: /home/alice/.config/inner/logs
[ok]  claude auth: OAuth credentials present (~/.claude/.credentials.json)
[ok]  claude: /home/alice/.local/bin/claude
[??]  GEMINI_API_KEY: not set (required for Gemini agents)
[??]  gemini: not found in PATH (required for Gemini agent profiles)
[ok]  display: wayland (wayland-1)

2 warning(s) — no errors
```

`[!!]` is an error and must be fixed before running anything; `[??]` is a
warning, often about a tool you do not use.

Agent CLIs authenticate with their own login. For Claude Code, run `claude`
once on the host and sign in: the `claude` capability copies the credentials
into each sandbox. The built-in agent profiles do not forward
`ANTHROPIC_API_KEY`; to use an API key instead, add it to `[env] inherit` in
your profile.

## Quick Start

Start an interactive shell in the default sandbox (`shell` profile by default):

```bash
inner run
```

To change the default profile, edit `~/.config/inner/config.toml`:

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

See [Examples](/inner/examples/) for more patterns, and [Profiles](/inner/profiles/) to understand or customize the sandbox configuration.
