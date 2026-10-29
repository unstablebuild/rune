---
sidebar_position: 15
---

# runectl

`runectl` is Rune's command-line companion: a small CLI that runs as an
external process and communicates with a running Rune workspace as a
plugin. It is the fast path to integrating with Rune. Where a full
[extension](./sdk/index.md) is the right tool when you want to ship UI, register
commands, or run a long-lived process inside Rune, `runectl` lets a shell
script, a Makefile target, an editor hook, or a one-off command reach
into the workspace without writing any code.

Because it is just a CLI that reads and writes plain text, it composes
with the rest of your environment: pipe its output through `jq`, `grep`,
`fzf`, or anything else, and pipe other tools' output back into it.

## Install

`runectl` is distributed as a Rune package. Install it from the
[Rune console](../learn/console.md) with:

```
pkg install runectl
```

(or `console pkg install runectl` from the command prompt). Once installed,
`runectl` is on your `PATH` inside Rune's terminals.

## How it finds the workspace

Commands you run from a Rune terminal target the right workspace
automatically, with no flags or configuration. That is also why hooks and
scripts spawned by tools running inside Rune just work.

## What you can do

Run `runectl help` to see every command, or `runectl <command> --help`
for a specific one. The main groups:

| Command | What it does |
| --- | --- |
| `runectl notify <level> <message>` | Post a Rune notification (`error`, `warn`, `info`, `success`). |
| `runectl open <uri>` | Open a resource in the workspace. |
| `runectl uri <path>` | Resolve a filesystem path to a workspace URI. |
| `runectl datadir` | Print the extension data directory. |
| `runectl screenshot` | Take a GUI screenshot and print its absolute path. |
| `runectl exec -- <cmd> [args...]` | Run a command through the workspace executor. |
| `runectl editor ...` | Read and edit buffer text, cursors, and location lists. |
| `runectl wm ...` | Window-manager actions: focus, split, float, close. |
| `runectl lsp ...` | Language-server queries: definitions, references, diagnostics, rename, symbols, and more. |
| `runectl syntax ...` | Tree-sitter searches and structural queries across the workspace. |
| `runectl storage ...` | Create, read, update, and delete extension documents. |
| `runectl llm ...` | Talk to the workspace's model router. |
| `runectl search ...` | Search the workspace's files through its index, by regular expression. |
| `runectl mcp` | Start an MCP server over stdio exposing Rune's syntax and LSP tools. |
| `runectl status <state> [message]` | Report program status to the terminal (OSC 7501): `idle`, `working`, `done`, `blocked`, `error`, or `clear`. |
| `runectl status wrap -- <cmd> [args...]` | Run any command and report its progress and outcome to the terminal, with no changes to the command. |

Most commands accept a `-F`/`--format` flag to choose the output format:
`table` (the default, human-readable), `json` (for piping into other
tools), or a Go template for custom shaping.

## Screenshots

```sh
image=$(runectl screenshot)
```

`runectl screenshot` saves Rune's rendered GUI to a PNG in
`$RUNE_DATADIR/screenshots/` and prints only its absolute path, followed by
a newline. It waits up to five seconds for a new complete image. Errors go
to stderr with a nonzero exit status and no path on stdout.

creenshots require GUI mode. TUI sessions safely discard the request, and
`runectl` times out without creating an image. The capture contains Rune's
output, not native window borders or the desktop behind it. Requests sharing
a data directory should run sequentially: simultaneous requests can observe
the same new file.

## Program status

`runectl status` speaks the [Program Status
Protocol](../learn/terminal/program-status.md)
(OSC 7501): a program tells the terminal what it is doing, and Rune shows
it on the tab, on the workspace bar, and as a notification when the tab
is not in focus. Every new message a program reports (`runectl status
<state> <message>`) comes through as a notification, even without a state
change. Programs that speak the protocol themselves need no help from
`runectl`. See the [Program Status
Protocol](../learn/terminal/program-status.md) guide for how Rune shows each state,
when a status clears, and how to configure it.

### Wrap any command

`runectl status wrap` gives any command a status, with no changes to the
command and no integration work. Put it in front of a build, a test run,
a deploy, or a download, then go do something else: the tab spins while
the command runs, and when it ends the tab turns green or red and Rune
notifies you, even if you are in another workspace.

```
runectl status wrap -- cargo build --release
```

The command runs exactly as it would without `wrap`: it keeps your
terminal, so prompts, colours, and progress bars work as usual, and
`Ctrl-C` and other signals reach it. `wrap` reports `working` when the
command starts and the outcome when it ends, then exits with the
command's own status, so scripts, `&&` chains, and CI steps behave the
same with or without it:

| When the command | `wrap` reports | and exits with |
| --- | --- | --- |
| exits 0 | `done` | 0 |
| exits with status N | `error`, `exit status N` | N |
| is killed by a signal, e.g. `Ctrl-C` | `error`, e.g. `signal: interrupt` | 128 + the signal number |
| cannot be started | `error`, with the reason | 1 |

`--app` names the program and `--msg` sets the message shown while it
runs and when it is done; it defaults to the command line:

```
runectl status wrap --app terraform --msg "apply production" -- terraform apply
```

**Make it the default.** An alias wraps a tool every time you run it from
your shell, with nothing else to remember:

```sh
alias make='runectl status wrap --app make -- make'
alias cargo='runectl status wrap --app cargo -- cargo'
```

**Hook it into a Makefile.** Wrap the long targets so everyone who runs
them gets a spinner while they run and a result when they finish:

```make
test:
	runectl status wrap --app make --msg "go test" -- go test ./...
```

**Hook it into git.** A `pre-push` hook can flag a slow check while it
runs and leave a `done` or `error` record behind for you to find:

```sh
#!/bin/sh
runectl status wrap --app git --msg "pre-push checks" -- make lint test
```

### Report status yourself

The first argument is the state, the optional second one a message:

```
runectl status working "Installing updates"
runectl status blocked --kind permission "Deploy to production?"
runectl status done "Upgraded 12 packages"
runectl status error "rsync failed"
runectl status idle
runectl status clear
```

`--app` names the program (`cargo`, `terraform`), `--title` labels the
record, `--progress 0-100` adds a percentage to `working` or `blocked`,
and `--id build/test` addresses one of several records so a tool can
report a tree of tasks. `clear` removes the addressed record and
everything beneath it, or every record when no `--id` is given.

**Hook it into an agent.** Agents that expose lifecycle hooks can report
when they stop to ask for permission, and when they are done, so you can
let them run in the background and come back when they need you:

```sh
# permission hook
runectl status blocked --kind permission --app my-agent "Allow running tests?"
# stop hook
runectl status done --app my-agent "Finished implementing the feature"
```

### See it in action

`demo` plays the examples from the specification so you can see how Rune
renders each state, with progress running from 0 to 100% while working
(`--progress-duration`, 8 seconds by default):

```
runectl status demo --scenario deploy
```

`runectl status query` checks whether the terminal supports the protocol,
exiting with status 1 when it does not.

## Composing with POSIX tools

The point of `runectl` is to get things out of Rune and into your
environment, and back. The examples below mix and match the command
groups to show the range.

**Notify on a long build, from outside Rune.** Kick off a build in a
plain terminal and have the workspace tab light up when it lands, even if
you have wandered off to another workspace:

```
make build && runectl notify success "build done" || runectl notify error "build failed"
```

**Code-review the buffer you are staring at.** Pull the live text of an
open editor with `editor print` and send it to the workspace's model
router (`llm message` takes the prompt as an argument), then read the
review back in your terminal:

```
runectl llm message gemini/gemini-3.5-flash "Review this Go file for bugs and edge cases: $(runectl editor print ./convert.go)"
```

**Grep the workspace without walking it.** `search regexp` answers from
the index Rune keeps of the workspace, so it returns as fast in a large
monorepo as in a small project. It prints each line as `grep -n` does,
and takes ripgrep's flags for case (`-i`), literal text (`-Q`), file
globs (`-g`), and matches per file (`-m`):

```
runectl search regexp -g '*.go' -m 1 'func \w+Retry'
```

Hidden, ignored, and excluded folders are skipped unless `--root` names
them or a folder that holds them, as in ripgrep:

```
runectl search regexp -r .github runs-on
```

**Turn diagnostics into a commit gate.** Count workspace errors and fail
a pre-commit hook if any remain:

```
errors=$(runectl lsp workspace-diagnostics --format json | jq '[.[] | select(.severity == "error")] | length')
[ "$errors" -eq 0 ] || { runectl notify error "$errors errors, commit blocked"; exit 1; }
```

**Ask the language server about a symbol by name.** The `lsp`
subcommands accept a bare symbol, so you do not need to hunt down a file,
line, and column. Read a type's hover documentation:

```
runectl lsp hover term.Writer
```

```go
type Writer interface { // size=16 (0x10)
	// Context returns the current context of the Writer.
	// This context can be used by tui.Components in combination
	// with term.Interrupter.Interrupt(context.Context) to disambiguate
	// regular calls to Draw from interrupt-driven calls to Draw.
	Context() context.Context
	// SetCell sets the contents of the given cell location.  If
	// the coordinates are out of range, then the operation is ignored.
	SetCell(Coordinates, Cell)
	// UnionAttributes computes the set union between a and b,
	// that is overrides a set of attributes at the given coordinates
	// that contain all the bit flags set in a, b or both, and uses the color
	// defined in b or if not set, uses the color in a.
	UnionAttributes(Coordinates, Attributes)
}
```

Locate where it is defined, list every reference, or find its
implementations the same way. Each prints the matching locations as
`<uri> <start-line>:<start-col>-<end-line>:<end-col>`, one per line:

```
runectl lsp definition term.Writer
runectl lsp references term.Writer
runectl lsp implementation term.Writer
```

Since those are just locations, pipe one into `runectl open` to open it
as a tab in the workspace:

```
runectl lsp definition term.Writer | head -n1 | cut -d' ' -f1 | xargs runectl open
```

**Drive the layout from a script.** Split the focused window and open a
file beside your code, using `wm focus` to target the active window:

```
runectl wm split "$(runectl wm focus)" ./docs/CHANGELOG.md
```

**Run a build in the workspace's environment, not your shell's.** Use the
workspace executor so the command sees the same tools and environment
Rune does:

```
runectl exec -- go test ./...
```

Because every command is line- and JSON-friendly, you can drop `runectl`
into the same pipelines you already use for everything else.
