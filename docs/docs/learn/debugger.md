---
sidebar_position: 35
---

# Debugger

Rune has a built-in source-level debugger. It runs inside your workspace,
from the [Rune console](./console.md), and uses Rune's own surfaces to show
you what is happening: breakpoints and the stopped line appear as markers
in the editor gutter, in-scope variables are shown inline next to the code,
and the call stack and expression results stream into the console.

The debugger speaks the **Debug Adapter Protocol (DAP)**, the same protocol
other IDEs use, so it is language-agnostic. Support starts with **Go** and
expands from there.

## Configure a debug adapter

For Go, there is nothing to configure. When you install the `go` package,
Rune sets up the debug adapter for you and installs
[Delve](https://github.com/go-delve/delve) (`dlv`) along with it, so the
debugger works out of the box.

For a language that does not ship a built-in adapter, you can register one
yourself under a `debugger` block, keyed by language ID:

```yaml tab
debugger:
  <langID>:
    command: "<adapter> --listen={addr}"
    adapter_id: "<adapter-id>"
```

```python tab
config["debugger"] = {
    "<langID>": {
        "command": "<adapter> --listen={addr}",
        "adapter_id": "<adapter-id>",
    },
}
```

- `command` is the adapter's launch command. Rune replaces `{addr}` with a
  free local address it picks for the session.
- `adapter_id` is optional and defaults to the language ID.

If you start a session for a language with no configured adapter, Rune
reports that none is configured.

## A debug session, start to finish

The debugger is driven by the `debugger` command in the
[Rune console](./console.md). A session follows a fixed lifecycle:
**initialize, launch or attach, set breakpoints, then signal that
configuration is done.**

```
debugger initialize go
debugger launch ./cmd/myprog
# move the cursor to a line and set a breakpoint (see below)
debugger configured
```

Step by step:

1. **`debugger initialize <langID>`** starts a session and launches the
   adapter for that language.
2. **`debugger launch <program> [args...]`** runs a program under the
   debugger, or **`debugger attach <pid>`** attaches to a running process.
   Repeat `-e KEY=VAL` to set environment variables for the debuggee, and
   use `--` before a program path or arguments that begin with `-`.
3. **Set breakpoints** while the session waits (see
   [Breakpoints](#breakpoints)).
4. **`debugger configured`** sends your breakpoints and lets the program
   run. It stops at the first breakpoint it hits.

From there you step through the code and inspect state, then end with
`debugger terminate`.

## Attaching to a listening adapter

Some debuggers run the debug adapter next to the program instead of letting
the editor spawn it. Python is the common case: start the program under
`debugpy` and it listens for a debugger to connect.

```
uv run --with debugpy==1.8.17 python -m debugpy --listen 127.0.0.1:5678 app.py
```

Then, from the console, attach to that endpoint in a single command, with no
`debugger initialize` first:

```
debugger attach python connect://127.0.0.1:5678
```

This creates the session against the adapter that is already listening and
sends the attach request. The language ID selects the adapter configuration,
so its `attach` template still applies; only the connection is replaced for
this session. Add the program path as a last argument when the adapter wants
to know it:

```
debugger attach python connect://127.0.0.1:5678 app.py
```

The endpoint belongs to a single session, so keep it out of
`debugger.<langID>.command` in `rune.star`. A command fixed to `connect://`
can no longer launch a program.

No session may be active when you run this form. If one is, end it with
`debugger terminate` first. From here the session continues as usual: set
breakpoints, then run `debugger configured`.

## Breakpoints

Breakpoints are set at the cursor, from the
[command prompt](./command-prompt.md), so you do not have to type line
numbers. Move the cursor to the line you want and run:

```
debugger set-breakpoint
```

This is a toggle: run it again on the same line to clear the breakpoint. A
red marker appears in the gutter for each breakpoint. Because this command
works from the command prompt, it is convenient to bind it to a key in
`rune.star`:

```yaml tab
command:
  key_bindings:
    "<ctrl-alt-b>": "debugger set-breakpoint"
```

```python tab
config["command"]["key_bindings"]["<ctrl-alt-b>"] = "debugger set-breakpoint"
```

Breakpoints are kept in memory as you set them and sent to the debugger
when you run `debugger configured`. You can keep toggling them after the
program is running, too. Rune sets breakpoints only on executable lines, so
if the cursor is on a blank line, a comment, or a lone closing brace, the
breakpoint moves to the next real statement and Rune tells you where it
landed.

## Controlling execution

Once the program stops, these commands move it forward. Each takes an
optional thread ID; with none, Rune uses the thread that last stopped.

| Command | What it does |
| --- | --- |
| `debugger continue` | Resume execution. |
| `debugger next` | Step over the current line. |
| `debugger step-in` | Step into a call. |
| `debugger step-out` | Step out of the current function. |
| `debugger pause` | Pause a running program. |
| `debugger reverse-continue` | Resume backward, for adapters that support reverse debugging. |
| `debugger step-back` | Step one line backward. |

Every time the program stops, Rune opens the source file at the stopped
line, marks that line in the gutter, and prints the current call stack into
the console.

## Inspecting state

| Command | What it does |
| --- | --- |
| `debugger stack-trace [<thread>]` | Show the call stack. |
| `debugger threads` | List threads. |
| `debugger scopes [<frame>]` | Show the scopes of a frame. |
| `debugger variables [<ref>]` | Show variables in a scope (the current frame's top scope by default). |
| `debugger evaluate <expr>` | Evaluate an expression in the current frame. |
| `debugger set-variable <name> <expr>` | Set a variable in the current scope. |
| `debugger set-expression <expr> <value>` | Assign to an expression. |

When the program is stopped, Rune also shows each in-scope variable's value
inline next to its name in the source, so you can read the local state
without running a command. These inline values clear when you continue.

To navigate frames without changing execution, use `debugger jump forward`
and `debugger jump backward` from the command prompt: they move the cursor
up and down the captured call stack without issuing any step.

## Ending a session

End the session with:

```
debugger terminate
```

A session also ends on its own when the program exits or the adapter
disconnects. Either way, Rune removes the breakpoint, stopped-line, and
variable markers from the editor. To start over within the same session,
use `debugger restart`.

## Command reference

Run `debugger help` in the console for the full list. The subcommands group
into:

- **Session**: `initialize`, `launch`, `attach`, `configured`,
  `terminate`, `restart`.
- **Execution**: `continue`, `next`, `step-in`, `step-out`, `step-back`,
  `reverse-continue`, `pause`, `goto`.
- **Inspection**: `threads`, `stack-trace`, `scopes`, `variables`,
  `evaluate`, `set-variable`, `set-expression`, `modules`,
  `loaded-sources`, `disassemble`.
- **Breakpoints and navigation** (from the command prompt):
  `set-breakpoint`, `jump`.

Most subcommands run only inside the console; `set-breakpoint` and `jump`
also work from the command prompt because they act on the editor cursor.

## See also

- [Rune Console](./console.md): where the `debugger` command lives.
- [Command Prompt](./command-prompt.md): for `debugger set-breakpoint` and `debugger jump`.
- [Supported languages](../languages/supported.md): where debugging is available.
