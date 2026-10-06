---
sidebar_position: 11
---

# Extensions

Extensions are programs that bundle the Rune [SDK](./sdk/index.md) to integrate
with a workspace at a deeper level than [runectl](./runectl.md): they can
register commands and key bindings, build persistent UIs, and run for as
long as the workspace is open. This guide explains how Rune
loads and runs them, how it authenticates and authorizes the access they
request, what the `extensions` key in your config expects, and how to
troubleshoot them.

If you only want a quick scripted integration from a shell, a Makefile, or
an editor hook, reach for [runectl](./runectl.md) instead. It is the same
integration surface without writing or installing an extension.

## What an extension is

An extension is a standalone executable. Rune launches it as a separate
process and talks to it over a private, local channel. Because it is its
own process, an extension can be written in any language with a Rune SDK,
ships and updates on its own schedule, and a crash in an extension never
takes the editor down with it.

Extensions are installed from the [Rune console](../learn/console.md) with
`pkg install <name>`, delivered as a [package](./packages.md) like
everything else Rune installs. Installing one drops its executable into your
Rune data directory and registers it under the `extensions` key of your
config.
Everything is delivered this way, including the extensions that Unstable
Build authors, such as [Go](../languages/go.md) language support, fuzzy search, and
the [Rune Agent](../agent/intro.md); there is no separate class of built-in
extension. In every case an extension ends up as an entry in your config
that names an executable to run and an optional block of configuration to
hand it.

## How extensions are loaded

When you open a workspace, Rune reads the `extensions` map from your
[config](../config.md) and starts each enabled entry. Loading happens per
workspace: every workspace you open gets its own set of extension
processes, so two workspaces never share extension state.

Each extension is launched, then Rune waits for it to finish announcing
itself: the commands, key bindings, and aliases it contributes are
registered before the extension is considered ready. From that point on the
extension is live for the lifetime of the workspace, and its commands appear
in the [command prompt](../learn/command-prompt.md) and console alongside
Rune's built-in ones.

:::info[Extensions run locally]
Extensions always run as local processes on the machine where you launched
Rune, even when the workspace itself is remote (for example over `ssh://`).
You own your extensions; they are not installed on, and do not run on, the
remote host. They reach the remote workspace through Rune, not the other way
around.
:::

## How extensions are run

Rune starts the extension's executable as a child process and connects to it
over a private channel that is local to your machine and scoped to a single
workspace. Nothing about that channel is exposed on the network, and other
programs on your machine cannot join it.

Through that channel the extension calls into the capabilities Rune exposes,
such as the editor, the window manager, the file system, language servers,
the syntax tree, notifications, storage, and the command executor. These are
the same capabilities surfaced on the command line by
[runectl](./runectl.md), which is itself a thin client over the same
surface. Each capability is gated by a permission, described below.

Rune captures the extension's output for diagnostics (see
[Troubleshooting](#troubleshooting)) and watches the process. The details of
the transport, the handshake, and how credentials are delivered are internal
to Rune and may change between releases; do not build against them. The
stable contract for authors is the [SDK](./sdk/index.md).

## Authentication and authorization

Rune treats every extension as untrusted code that has to ask before it can
touch anything. There are two layers.

### Authentication

When Rune launches an extension it hands that specific process the
credentials it needs to connect back to the workspace, over a private local
channel. A program that was not started by Rune cannot connect, and the
credentials are scoped to the one workspace and one process they were issued
for. You do not configure any of this; Rune sets it up for each launch.

### Authorization: the permission model

An extension declares the set of capabilities it needs, by name (the editor,
the file system, command execution, language servers, the debugger, the
model router, and so on). Rune enforces that declaration: a call into a
capability the extension never declared is rejected outright.

For the capabilities it did declare, **you** are the gatekeeper. The first
time an extension reaches for a sensitive capability, Rune prompts you and
remembers your answer. Each prompt offers four choices:

- **Allow once** for this session only.
- **Allow always** and stop asking.
- **Deny once**.
- **Deny always**.

Command execution is checked more closely than a one-time grant: Rune can
prompt based on what is actually being run, not just on the fact that the
extension may run commands at all.

You can review and undo the answers Rune has remembered from the
[console](../learn/console.md) with the `authorizer` command:

| Command | What it does |
| --- | --- |
| `authorizer list` | List the permission decisions Rune has persisted. |
| `authorizer revoke <permission>` | Forget a decision, so you are prompted again next time. |

Revoking a decision is the clean way to reset an extension you granted too
much, or to re-test a permission prompt.

### Verified publishers

Official packages Rune installs are signed. Rune ships a trust keyring and
refreshes it from the API at startup, so it can tell when an extension's
entrypoint belongs to a package signed by a **verified publisher**.

You can let verified publishers skip the permission prompts with two
top-level `authorizer` settings:

```yaml
authorizer:
  auto_authorize_verified: true
  auto_authorize_commands: true
```

- `auto_authorize_verified` (default `true`) grants an extension's
  non-command permissions automatically when it comes from a verified
  publisher, instead of prompting you.
- Command execution is held to a stricter rule. Rune auto-authorizes a
  workspace command **only when both** `auto_authorize_commands` **and**
  `auto_authorize_verified` apply and the extension is from a verified
  publisher. A verified publisher on its own never silences command
  prompts, and `auto_authorize_commands` on its own applies only to
  verified publishers — an unverified extension always prompts before it
  runs a command. This keeps command execution, the most sensitive
  capability, gated on provenance you can trust.

Set `auto_authorize_verified` to `false` if you want Rune to prompt for
verified publishers too.

## Configuring extensions

Extensions live under the top-level `extensions` key of your
[config](../config.md). It is a map from an extension's id to its settings.
Rune manages this map for you: `pkg install` adds the entry and sets `path`
to the installed executable, so you rarely write one by hand. What you do
tweak is the `config` block inside each entry, which tunes how that
extension behaves.

```yaml tab
extensions:
  <id>:
    path: "<executable to run>"
    config:
      # extension-specific settings
```

```python tab
"extensions": {
    "<id>": {
        "path": "<executable to run>",
        "config": {
            # extension-specific settings
        },
    },
}
```

Rune understands exactly two fields per entry:

| Field | Type | Meaning |
| --- | --- | --- |
| `path` | string | The executable Rune launches for this extension. May include arguments, and may reference `$RUNE_DATADIR` to point inside your Rune data directory. A `.py` or `.rs` path, or a Go package directory containing `go.mod`, runs through the matching language package's toolchain (see [Source and package extensions](#source-and-package-extensions)). |
| `config` | map | An opaque block handed verbatim to the extension. Rune does not interpret it; its shape is defined by the extension's author. |

Everything inside `config` is the extension's own contract. Consult that
extension's documentation for the keys it accepts. For example, the
[Go extension](../languages/go.md) accepts a custom language-server path:

```yaml tab
extensions:
  go:
    config:
      lsp_path: "/path/to/your/binary"
```

```python tab
"extensions": {
    "go": {
        "config": {
            "lsp_path": "/path/to/your/binary",
        },
    },
}
```

When you install an extension with `pkg install`, its entry is added for
you, usually with `path` already set to the installed binary under
`$RUNE_DATADIR`. In most cases you only ever touch the `config` block to
tune the extension's own behavior.

### Source and package extensions

An extension's `path` can name a source entrypoint or Go package directory
instead of a compiled binary. Rune runs it through the toolchain of the
matching language package, which must be installed. The package that ships
such an extension declares it in `requirements:` so it is installed first,
and a missing toolchain fails the extension start with a `pkg install` hint:

| Entrypoint | Runs as | Package |
| --- | --- | --- |
| `<file>.py` | `uv run` against the nearest `pyproject.toml` above the file | `python` |
| `<package-dir>` | `go -C <package-dir> run .`, using the package's `go.mod` and `go.sum` | [`go`](../languages/go.md) |
| `<file>.rs` | `cargo run` against the nearest `Cargo.toml` above the file | `rust` |

For Go, point `path` at the main package directory, not at `main.go`. The
directory must contain `go.mod`, and modules with dependencies must commit
`go.sum` so Rune can run them in read-only module mode.

A Python entrypoint lives inside a project: uv builds the extension's
environment from the `pyproject.toml` next to (or above) the script, so
its dependencies (such as `rune-sdk`) are declared there as in any
Python project. See
[the packages guide](./packages.md#distributing-from-a-git-repository) and the
[Python SDK guide](./sdk/python.md) for a complete example.

## Troubleshooting

Most extension problems are visible from the [Rune console](../learn/console.md)
with the `extensions` command, which manages the lifecycle of every
extension in the current workspace:

| Command | What it does |
| --- | --- |
| `extensions status` | List every workspace extension with its status, process id, and uptime. |
| `extensions info <id>` | Show detailed state for one extension. |
| `extensions start <id> <cmdAndArgs> [--config <json>]` | Start an extension. |
| `extensions stop <id>` | Stop a running extension. |
| `extensions restart <id>` | Restart a known extension. |
| `extensions logs <id> [--tail <N>]` | Show an extension's captured logs, optionally limited to the last `N` lines. |

A typical investigation:

1. **Is it running?** Run `extensions status`. If the extension is not
   listed or shows as stopped, its entry may be missing from your config or
   its `path` may not point at a real executable.
2. **Why did it stop or fail to start?** Run `extensions logs <id>`. Rune
   captures what the extension writes to its error stream, which is where
   most startup failures, missing-dependency errors, and crashes show up.
   `extensions info <id>` adds restart counts and other state.
3. **Try a clean restart.** `extensions restart <id>` relaunches it with the
   current config, which is the fastest way to pick up a config change or
   clear a wedged state.

### Permission prompts keep reappearing, or never appear

If an extension is repeatedly denied a capability it needs, you may have a
lingering **deny always** decision. List persisted decisions with
`authorizer list` and clear the relevant one with `authorizer revoke`, then
exercise the feature again to get a fresh prompt. Conversely, if an
extension behaves as though it cannot reach a capability and you are never
prompted, confirm the extension actually declares that capability; Rune
blocks calls into capabilities an extension never requested.

### Going deeper with logs

Beyond `extensions logs`, Rune folds extension diagnostics into its main log
at `$RUNE_DATADIR/debug.log` (`~/.local/share/rune/debug.log` by default).
Raising `log_level` in your [config](../config.md)
makes both Rune and the extensions it launches more verbose, which is useful
when a startup failure is not obvious from the captured output alone. See
the general [Troubleshooting](../troubleshoot.md) guide for more on Rune's
logs.

## See also

- [SDK](./sdk/index.md): build your own extension.
- [Packages](./packages.md): how an extension is bundled and distributed for `pkg install`.
- [runectl](./runectl.md): the same integration surface from the console, no extension required.
- [Console](../learn/console.md): the `extensions` and `authorizer` commands in context.
- [Config](../config.md): where the `extensions` key lives and how config is layered.
- [Tutorials](./tutorials.md): ship onboarding content alongside an extension.
