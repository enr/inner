---
title: Aliases
description: Define short names for frequently used inner commands
weight: 4
---

# Aliases

Aliases let you define short names for frequently used `inner` commands directly in your config file. They work like git aliases: the alias name is expanded into the configured command before argument parsing.

## Defining aliases

Aliases are declared in the `[aliases]` table of `~/.inner/config.toml` (global) or `.inner/config.toml` (local, directory-level):

```toml
[aliases]
review  = "run --profile code-review"
chat    = "run --profile claude-interactive"
oneshot = "run --profile claude-one-shot"
```

## Using aliases

Once defined, the alias name works as a top-level `inner` subcommand:

```bash
inner review                      # → inner run --profile code-review
inner chat                        # → inner run --profile claude-interactive
inner oneshot -- fix the bug      # → inner run --profile claude-one-shot -- fix the bug
```

Any flags or extra arguments you pass after the alias name are appended to the expansion:

```bash
inner review --network            # → inner run --profile code-review --network
inner review -- "add unit tests"  # → inner run --profile code-review -- add unit tests
```

## Global vs local aliases

Aliases follow the same global/local precedence as the rest of the config:

- `~/.inner/config.toml` — global, applies everywhere
- `.inner/config.toml` — local (directory-level), overrides the global config for that directory

When the same alias key appears in both files, the **local value wins**.

```toml
# ~/.inner/config.toml
[aliases]
review = "run --profile code-review"   # used everywhere

# /my-project/.inner/config.toml
[aliases]
review = "run --profile my-project-review"  # used only inside /my-project
```

## Precedence rules

- **Built-in commands are never shadowed.** If an alias name collides with an existing `inner` subcommand (e.g. `run`, `profile`, `config`), the built-in takes precedence and the alias is ignored.
- Aliases are expanded **one level only** — aliases cannot expand into other aliases.

## Limitations

- Alias values are split on whitespace using `strings.Fields`. Quoted arguments with spaces (e.g. `--arg "hello world"`) are not supported in the alias definition; pass them directly on the command line after the alias name.
- Aliases cannot invoke external commands (no `!`-prefix like git). They must expand to an `inner` subcommand.
