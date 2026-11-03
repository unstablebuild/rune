---
sidebar_position: 1
---

# Tasks

A task in Rune is a long-running shell command that you launch from the
command prompt and keep around as a first-class window. Unlike a one-shot `!`
command, a task stays mounted in the workspace: you can stop it, re-run it,
scroll back through its output, and read it like any other buffer once it
exits.

Tasks are how you keep recurring side-channel work (builds, linters, test
suites, dev servers, file watchers, log tails) visible without giving up the
main editor area. The two commands that create them, `tasknew` and
`tasknewtab`, differ only in where the task lives once it has been created.

## When to reach for a task

- A command you re-run dozens of times a day (`go build ./...`, `npm run
  test`, `cargo check`, `pytest -q`). Bind it to a task and you stop retyping
  it.
- A command whose *status* matters more than its live output: a build that
  either passes or fails, a linter that has zero or N findings. `tasknew`
  surfaces that status in the minimized window's color so you can keep working
  and glance over only when it changes.
- A long-lived process you want pinned as a tab (a dev server, a `tail -F`,
  a REPL) where the output *is* the thing you want to see. `tasknewtab` puts
  it in a tab and color-codes the tab the same way.
- Anything you would otherwise leave running in a separate terminal multiplexer
  pane just to watch its exit code.

If you only need to run a command once and read its output, dispatch it with
`!` instead. Tasks are for commands that earn their keep by being re-run.

## `tasknew`: a minimized status window

```
:tasknew <name> <left|right> [filter] -- <cmd> [<args>...]
```

`tasknew` creates a task and minimizes it to the side of the screen you
choose. The minimized window shows the task's name and a color that reflects
the exit status of the last run:

| Color | Meaning |
| --- | --- |
| gray | Running. |
| green | Last run exited 0 (succeeded). |
| red | Last run exited non-zero (failed). |
| yellow | Cancelled by you (e.g. via `<ctrl-c>`). |

The status is governed by the exit code of the program the task runs, so any
command that follows the standard convention of "0 means success" works
without extra wiring.

`name` is the handle you use to refer to the task in `taskfocus` and
`taskclose`. `left` or `right` picks which side of the screen the minimized
window docks to. The optional `filter` is a comma-separated list of
`.gitignore`-style patterns (no spaces; it is a single shell token). When
supplied, Rune re-runs the task whenever a file in the workspace that matches
one of those patterns changes, handy for "rebuild on save" or "rerun tests
when the Go files change" workflows.

Everything after `--` is the command line. The first token is the program,
the rest are arguments.

### Examples

Watch a Go build on the right, re-running on every `.go` change:

```
:tasknew build right *.go -- go build ./...
```

Run the test suite once and pin its result to the left side:

```
:tasknew test left -- go test ./...
```

Lint a frontend project on every relevant edit:

```
:tasknew lint left *.ts,*.tsx,*.css -- npm run lint
```

After the command exits, the minimized window turns green or red, and you can
un-minimize it to read the full output without losing your place.

## `tasknewtab`: a live tab

```
:tasknewtab <name> [filter] -- <cmd> [<args>...]
```

`tasknewtab` creates the same task, but instead of leaving it minimized it
converts it into a regular tab in the focused window. Use this form when the
task's live output is what you want to look at: a dev server's request log,
a long-running test runner, a process you are actively debugging.

The tab itself is color-coded with the same scheme as the minimized window:

| Tab color | Meaning |
| --- | --- |
| gray | Running. |
| green | Exited successfully. |
| red | Exited with a non-zero status. |
| yellow | Cancelled. |

So even when the tab is not focused, its color tells you whether the
underlying process is still healthy.

### Example

Run the dev server as a live tab:

```
:tasknewtab devserver -- npm run dev
```

Run the test suite as a tab and rerun it whenever Go sources change:

```
:tasknewtab tests *.go -- go test ./...
```

## Driving a running task

Once a task is focused (whether through `taskfocus`, by un-minimizing it, or
because it lives in a tab) three key bindings drive its lifecycle:

- `<ctrl-r>` reloads the task. If it is still running, Rune cancels the
  current run and starts a new one. If it has already exited, Rune starts it
  again from scratch. This is the same primitive that the optional `filter`
  argument triggers automatically.
- `<ctrl-c>` stops the task. The tab turns yellow to mark it as cancelled,
  and the buffered output stays on screen so you can inspect what it printed
  before it was interrupted.
- `<ctrl-r>` after a `<ctrl-c>` starts the task again. A cancelled task is
  still a task; it just is not running right now.

## Reading the output after the task exits

When a task finishes (successfully, with an error, or because you cancelled
it) Rune installs the editor on top of the captured output. The output
buffer behaves like any other Rune buffer: you can scroll it, search it with
`/`, jump to a match, copy text out of it, and use it as the source for
`locationpicker`-style workflows. There is no separate "output viewer" to
learn; the editor you already know is the output viewer.

This is why tasks are a better fit than ad-hoc `!` invocations for anything
whose output you might want to grep through later: the moment the command
exits, its log is a buffer.

## Managing tasks

Tasks are identified by the `name` you give them at creation time, and that
name is what the rest of the task commands take as an argument:

- `taskfocus <name>` focuses an existing task; un-minimizes it if it was
  minimized, or switches to its tab if it was tabbed.
- `taskclose <name>` stops the task and removes its window.

If you call `tasknew` or `tasknewtab` with a name that is already in use,
Rune prompts you to replace the existing task rather than silently
overwriting it. This makes it safe to bind a task creation to a key without
worrying about leaking duplicates.

## A few patterns worth stealing

### Bind your build to one keystroke

Combine a task with a command alias and a key binding so that the build is
always one keystroke away, always re-runs on save, and always shows up on
the same side of the screen:

```yaml tab
command:
  aliases:
    build: "tasknew build right *.go -- go build ./..."
  key_bindings:
    "<ctrl-b>": "build"
```

```python tab
"command": {
    "aliases": {
        "build": "tasknew build right *.go -- go build ./...",
    },
    "key_bindings": {
        "<ctrl-b>": "build",
    },
},
```

Press `<ctrl-b>` once; from then on the build re-runs whenever a `.go` file
changes and the minimized window on the right is green or red at a glance.

### Mirror a dev server in a tab

```yaml tab
command:
  aliases:
    dev: "tasknewtab dev -- npm run dev"
```

```python tab
"aliases": {
    "dev": "tasknewtab dev -- npm run dev",
},
```

`dev` opens the server as a tab. If it crashes, the tab turns red; press
`<ctrl-r>` in the tab to restart it.

### Use a filter to scope a watcher

The optional filter argument is a comma-separated list of
[gitignore-style](https://git-scm.com/docs/gitignore) patterns. Only file
changes that match one of the patterns trigger a re-run, which keeps
unrelated edits (Markdown, vendored assets, generated files) from constantly
restarting your task. The list is a single shell token, so it cannot
contain spaces.

```
:tasknew unit right *.go,*.mod -- go test ./...
```

### Use `tasknew` and `tasknewtab` for different jobs

A reasonable split for a typical project:

- `tasknew` for the *gatekeepers*: build, lint, typecheck, unit tests.
  Their output matters only when they fail; the minimized window's color is
  what you watch the rest of the time.
- `tasknewtab` for the *observables*: dev server, log tail, REPL, e2e
  runner. Their output is the point; pin them in tabs and read them like
  any other buffer.

## See also

- [Command Variables](../aliases.md) for the `$FILE`, `$WORKSPACE`,
  and `$1` placeholders you can use inside a task's command line.
- [Key Syntax](../key-syntax.md) for the canonical spelling of bindings like
  `<ctrl-r>` and `<ctrl-c>`.
