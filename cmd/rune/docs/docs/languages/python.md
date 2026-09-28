---
sidebar_position: 5
---

# Python

Rune ships [Tier 1](./supported.md#support-tiers) Python support: code intelligence through the
[ty](https://github.com/astral-sh/ty) language server, formatting
through [ruff](https://github.com/astral-sh/ruff), and project
environments managed end to end with
[uv](https://github.com/astral-sh/uv). The extension bundles all three
tools, so there is nothing to install. You do not even need Python on
the machine: Rune installs and manages its own interpreter, separate
from any system Python.

## Setup

There is no setup. Open a workspace with a Python project in it and
Rune prepares everything: an interpreter, a virtual environment with
your dependencies, and a running language server. Progress is reported
through notifications as it happens.

The interpreter Rune installs is also linked as `python` and `python3`
on the PATH of every terminal inside Rune, so scripts and tools you run
by hand resolve the same interpreter the editor uses.

## How Rune finds your Python project

Rune decides where a Python project starts by looking for one of these
markers in a folder:

- `pyproject.toml`, the standard Python project manifest
- `requirements.txt`, `requirements.lock`, or `requirements.in`
- a `.venv` directory (an existing virtual environment)

How the language server starts depends on where the markers are:

- **Workspace root.** If the folder you open as your workspace carries a
  marker, or simply contains `.py` files at the top level, the language
  server starts right away, rooted at the workspace.
- **Nested projects.** Markers deeper in the workspace work too. When
  a `.py` file is opened, created, or changed, Rune walks up from that
  file to the nearest enclosing folder that carries a marker and starts
  a language server rooted at that folder. Because a change counts, an
  external tool like Rune's agent editing a file brings the project up
  even with no editor tab open. In a monorepo with several Python
  projects, each project gets its own language server the first time one
  of its files is touched, and that server is reused for every other
  file in the project.

A `.py` file with no marker in any folder between it and the workspace
root gets no code intelligence, because there is no project to root a
server at. Run `python init` from the
[command prompt](../learn/command-prompt.md) (or add a
`pyproject.toml` yourself): creating the marker lets the next edit or
open of a `.py` under it discover the project, no reload required.

:::info
A nested project's language server starts the first time one of its `.py` files is
opened, created, or edited. Code intelligence, such as diagnostics, hover, or
go-to-definition, only works once that server is running. So a request for a nested
project that has not been opened or edited yet, for example Rune's agent inspecting a
file before it touches it, may come back empty. Opening or editing any `.py` in the
project starts the server, and requests work from then on. Projects nested inside a
workspace-root Python project are already served by the root server, so this only
affects standalone projects deeper in the workspace tree.
:::

Working across several projects in one repository? See the
[Monorepos](./monorepo.md) guide for how per-project discovery and
workspace-wide search fit together.

## Automatic environments

When Rune brings a project up, it first makes sure its managed
interpreter is present, installing it on first run. It then prepares
the environment to match how the project declares its dependencies:

| Project shape | What Rune does |
| --- | --- |
| `pyproject.toml` | Syncs the environment with the project and its lockfile (`uv sync`). |
| `requirements.txt` (or `.lock`, `.in`) | Creates a `.venv` if missing and installs the requirements into it. |
| `.venv` only | Reuses your existing virtual environment as is. |
| Bare `.py` files at the workspace root | The managed interpreter alone; no environment is created. |

Environment setup is best-effort: if a sync fails (say, a broken
dependency), Rune warns you and still starts the language server, so
code intelligence keeps working while you fix the environment.

## Who manages the environment

Rune asks before it touches a project's environment. The first time it
brings a Python project up, it shows the project's path and offers two
answers:

- **Let Rune manage this environment** — Rune installs its managed
  interpreter, creates and syncs the `.venv`, and keeps the debugger
  pointed at it, as described above.
- **I manage it myself** — Rune leaves your conda env, pyenv shims,
  hand-rolled `.venv` or system interpreter completely alone. Code
  intelligence still works: ty and ruff are bundled with Rune and start
  either way.

The answer is remembered per project, so a monorepo can have Rune
manage one project while you drive another yourself. Dismissing the
question with `Esc` leaves the environment alone for the session and
asks again next time.

Change your mind at any point from the console:

- `python enable` hands the current project back to Rune and prepares
  the environment right away. Reload the workspace afterwards so the
  language server picks up the new interpreter.
- `python disable` stops Rune from managing the current project.
- `python status` shows the project root, who manages it, and the
  detected project shape.

All three accept an optional path, so you can point them at another
project in the same workspace. While a project is unmanaged, the other
`python` subcommands refuse to run against it and tell you to run
`python enable` first.

## Code intelligence: the `lsp` command

The cross-language `lsp` command drives code intelligence for Python
the same way it works everywhere: hover, definition, references,
implementation, completion, rename, formatting, and diagnostics, all
with the same default key bindings. See
[the `lsp` command](./intelligence.md#code-intelligence-the-lsp-command) for the
full table. Formatting requests are served by ruff; everything else is
served by ty.

### Language server logs

ty and ruff write logs to stderr, where Rune captures them. Run
`process status` in the console to find their PIDs, then run
`process stdio <pid>` to view a server's logs. This also works after a server
exits; use `process audit` to find its previous PID. See
[Troubleshoot](../troubleshoot.md#inspect-any-process-rune-starts)
for details.

ty uses `info` logging by default. Configure the level for the built-in Python
servers with `debug.log_level`:

```yaml tab
extensions:
  python:
    config:
      debug:
        log_level: debug
```

Accepted values are `trace`, `debug`, `info`, `warn`, and `error`. ty receives
the selected level directly. For Rune's default ruff formatting server,
`debug` and `trace` also enable ruff's verbose logging. Custom commands and
alternate commands are left unchanged.

## Managing interpreters and dependencies: the `python` command

The `python` command, powered by uv, manages interpreters and
dependencies without leaving the editor. It is a
[Rune console](../learn/console.md) command: open the console and run
it as `python <subcommand>`, or submit a one-off from the
[command prompt](../learn/command-prompt.md) with
`console python <subcommand>`:

| Command | What it does |
| --- | --- |
| `python install [<version>]` | Install a Python interpreter version. |
| `python list` | List available and installed Python versions. |
| `python find` | Find an installed Python interpreter. |
| `python pin <version>` | Pin the project to a Python version. |
| `python uninstall <version>` | Uninstall a Python interpreter version. |
| `python init [<path>]` | Initialize a new project (creates `pyproject.toml`). |
| `python add <package>...` | Add dependencies to the project. |
| `python remove <package>...` | Remove dependencies from the project. |
| `python sync` | Sync the environment with the project lockfile. |
| `python lock` | Update the project lockfile. |
| `python tree` | Display the project dependency tree. |
| `python build` | Build the project into distributable archives. |
| `python run <command> [<args>]` | Run a command in the project environment. |
| `python tool <args>` | Run and manage tools provided by Python packages. |
| `python pip <args>` | Manage packages with a pip-compatible interface. |
| `python venv [<path>]` | Create a virtual environment. |
| `python cache <args>` | Manage the uv cache. |
| `python self <args>` | Manage the uv executable. |
| `python enable [<path>]` | Let Rune manage the project's environment. |
| `python disable [<path>]` | Stop Rune from managing the project's environment. |
| `python status [<path>]` | Show who manages the project's environment. |

## Bringing nested projects up on edits

By default Rune discovers nested projects on opens, creates, and
out-of-band changes, so a project comes up the first time any of its
files is touched, including edits the agent writes with no editor tab
open. In a very large monorepo with many Python projects, you can
restrict discovery to editor opens only by setting `watch_events` to
`false` in `config.yaml`:

```yaml tab
extensions:
  python:
    config:
      watch_events: false
```

With this off, a nested project only comes up when you open one of its
`.py` files in the editor; edits made without an open tab no longer
trigger bring-up.

## Using a different language server

To replace the built-in servers, set the `command` and
`alternate_commands` keys in `config.yaml`:

```yaml tab
extensions:
  python:
    config:
      command: "/path/to/your/server"
      alternate_commands:
        textDocument/formatting: "/path/to/your/formatter server"
```

`command` replaces ty as the main language server.
`alternate_commands` routes individual LSP methods to a different
server; by default ruff serves `textDocument/formatting` and
`textDocument/rangeFormatting`. Overriding `command` without supplying
`alternate_commands` drops the ruff routes, so your server handles
everything, including formatting. The method names are the request
names defined by the
[Language Server Protocol specification](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/),
which lists every accepted method.

## Diagnostic scope

By default Rune reports Python diagnostics only for the files you have
open. To check every file in the project and surface problems in files
you have not opened yet, set `diagnostic_mode` to `workspace` in
`config.yaml`:

```yaml tab
extensions:
  python:
    config:
      diagnostic_mode: workspace
```

Accepted values are `off`, `openFilesOnly` (the default), and
`workspace`. Workspace mode re-checks the whole project each time
diagnostics are gathered, so it is more expensive on large
repositories and is off by default. This setting only changes
project-wide reporting: per-file checks, such as the agent verifying a
file it just edited, already cover unopened files regardless of the
mode.
