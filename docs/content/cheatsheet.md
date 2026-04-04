---
title: Cheat Sheet
description: Quick reference for inner commands and common tasks
weight: 7
---

# Cheat Sheet

## Health & Environment

| Task | Command |
|------|---------|
| Check requirements & setup | `inner doctor` |
| View current version | `inner version` |
| Initialize/Repair `~/.inner` | `inner init` |
| Reset built-in profiles | `inner reset` |

## Profiles

| Task | Command |
|------|---------|
| List all profiles | `inner profile list` |
| List with paths & shadowed | `inner profile list -w` |
| View profile TOML | `inner profile show <name>` |
| Explain capabilities/mounts | `inner profile show <name> --explain` |
| Create from template | `inner profile new <name>` |
| Edit in `$EDITOR` | `inner profile edit <name>` |
| Clone a profile | `inner profile clone <src> <dest>` |
| Validate all profiles | `inner profile validate --all` |
| Install from URL | `inner profile install <url>` |

## Running Sandboxes

| Task | Command |
|------|---------|
| Start default (shell) | `inner run` |
| Run specific profile | `inner run -p <name>` |
| Mount project read-write | `inner run -w /path/to/project` |
| Preview bwrap command | `inner run --dry-run` |
| Set a timeout (seconds) | `inner run --timeout 300` |
| Add extra mount | `inner run -m /host:/sandbox:ro` |
| Inject environment var | `inner run -e KEY=VAL` |
| Override entrypoint | `inner run --entrypoint /bin/zsh` |
| Disable network | `inner run --no-network` |

## Agents & One-Shots

| Task | Command |
|------|---------|
| Claude Interactive | `inner run -p claude-interactive -w .` |
| Claude One-Shot | `inner run -p claude-one-shot --arg "refactor this"` |
| Gemini Interactive | `inner run -p gemini-interactive -w .` |
| Pass prompt from file | `inner run -p claude-one-shot --args-file prompt.md` |
| Pass args via `--` | `inner run -p claude -- --model claude-3-5-sonnet` |

## Security Verification

| Task | Command |
|------|---------|
| Verify profile security | `inner verify -p <name>` |
| Show TOML fix suggestions | `inner verify -p <name> --suggest` |

## Logs

| Task | Command |
|------|---------|
| List recent runs | `inner log list` |
| View specific run log | `inner log show <run-id>` |
| Clean logs (default 30d) | `inner log clean` |
| Clean older than 7 days | `inner log clean --older-than 7` |

## Configuration

| Task | Command |
|------|---------|
| View merged config | `inner config show` |
| Edit global config | `inner config edit` |
| Edit local (.inner/) config | `inner config edit --local` |

---

## Pro-Tips

### Ad-hoc inspection
Run a plain shell with your project mounted to see what the agent sees:
```bash
inner run -p shell -w .
```

### Temporary Profiles
You can run a profile directly from a URL without installing it:
```bash
inner run -p https://example.com/custom.toml
```

### Quick Aliases
Define a shortcut in `~/.inner/config.toml`:
```toml
[aliases]
test = "run --profile claude-one-shot --network"
```
Then use it as: `inner test -- "run npm test and fix failures"`
