---
sidebar_position: 30
---

# Tutorials

Rune lets you ship interactive, in-IDE tutorials written in a small
[Starlark](https://starlark-lang.org/) DSL. They render in a tile
docked to the right of the editor, walk the user through a sequence of
steps, and move on only when the user actually does what each step
asks: runs a command, opens a file, answers a prompt.

If you maintain a Rune extension or just want to onboard your
teammates into your repo's conventions, a tutorial is often a better
first-run experience than a README; it runs *inside* the IDE, with
the user's real keybindings and theme, and it advances when the user
makes progress rather than when they remember to switch back to the
docs.

## What a tutorial looks like

Here is a tiny one:

```python
def run():
    wait_command(
        title   = "Welcome",
        command = "wopen",
        text    = "Open a workspace with `<cmd>wopen <path>`.")

tutorial(id="hello", title="Hello, Rune", version="1", entry=run)
```

The tile shows the step: its title and the instructions. Nothing the
user types dismisses it. As soon as the user actually dispatches the
`wopen` command, the entry function continues — and since there is
nothing after it, the tutorial finishes.

## The tutorial tile

A running tutorial is a pane along the right edge of the screen, framed
like a window and lined up with the workspace's windows: it starts and
ends on the same rows they do, below the tab bar and above the
workspaces bar. The workspaces are laid out beside it, not underneath,
so nothing the lesson draws ever covers the code, the terminal, or the
command prompt. It sits *outside* every workspace's window manager: the
window commands a lesson asks for (`windowcloseall`, `windowmove`,
`windowtogglemaximize`, ...) act on the workspace's windows and never on
the tile, so a lesson survives the very commands it teaches, and
switching workspaces keeps it in place.

- The body is the current step: its title as a heading, then its
  markdown. Scroll it with the mouse wheel or the scroll bar.
- Three buttons sit on the tile's last row: **Back** shows the step
  before the one on screen, **Skip** resolves the current step and
  moves on, and **Stop** ends the lesson.
- **Back** re-reads, it does not rewind. Nothing about the lesson
  moves: the current step stays armed while an earlier one is on
  screen, so doing what it asks (or pressing **Skip**) brings the tile
  back to it. **Back** on the oldest step returns to the current one.
- The middle button says what it does: while an earlier screen is on
  show it reads **Next** and pages forward towards the current step,
  since skipping a step you are not looking at would be a surprise. It
  reads **Skip** again once the current step is back on screen.
- The last step does not take the tile away. A lesson that reaches its
  end leaves a closing screen up so the copy you were working through
  stays in front of you; **Stop** closes it, and **Back** still pages
  through the whole lesson from there.
- The lesson never takes the keyboard. Whatever you type goes to the
  workspace, so the editor, a terminal and the command prompt answer it
  even while the tile is the pane in focus. The tile is read with the
  mouse, and the **Back**, **Skip** and **Stop** buttons are clicked.
- A step that asks a question is the one exception, since the lesson is
  waiting on the answer: `<left>` and `<right>` (or `<ctrl-h>` and
  `<ctrl-l>`) walk its options and `<enter>` takes the highlighted one.
  Clicking an option works too. `<esc>` is left to the workspace, so a
  modal editor still leaves insert mode with it.
- Clicking the tile focuses it and draws the focus frame; a click
  elsewhere hands focus back. The window commands never reach it:
  `windowfocus`, `windowclose` and `tabclose` act on the workspace's
  windows whether or not the tile has focus.
- Only **Stop** or the `tutorial stop` command ends the tutorial.
- The tile takes a quarter of the screen. On a screen too narrow to
  leave the workspace usable it is not shown, but the lesson keeps
  running and reappears once there is room.

## Running a tutorial

Tutorials are dispatched from Rune's [Command
Prompt](../learn/command-prompt.md). Open the prompt with the user's
configured command-prompt key (`<cmd>` in this guide) and run:

- `tutorial start <name>`: start the tutorial called `<name>`.
- `tutorial stop`: stop the running tutorial.

Tab-completion works for the subcommand and for `<name>` after
`start`. Type `tutorial start<TAB>` to see the available
tutorials. If the user types an unknown name, Rune prints
`unknown tutorial "<name>"`.

## Registering a tutorial

Tutorials are registered through the `tutorials` block of a Rune
config. Each entry maps a name to a path to a `.star` file. The name
is what shows up after the `tutorial` command (and in completion);
the file is read once at IDE startup and parsed into a runnable
tutorial.

```yaml tab
tutorials:
  onboarding: docs/onboarding.star
  release-checklist: docs/release.star
```

```python tab
"tutorials": {
    "onboarding":        "docs/onboarding.star",
    "release-checklist": "docs/release.star",
},
```

Parse errors are surfaced as non-fatal config errors; a typo never
takes the IDE down, it just makes that one tutorial unavailable
until you fix it.

Where you add the `tutorials.<your-tutorial>` block depends on who the tutorial is for.

### Ship a tutorial with a repository

Drop a workspace-local config at `.rune/config.yaml` (or
`.rune/config.star`) in the repository root and add a `tutorials`
block that points at `.star` files committed alongside it:

```yaml tab
# .rune/config.yaml
tutorials:
  onboarding: .rune/tutorials/onboarding.star
```

```python tab
# .rune/config.star
config["tutorials"] = {
    "onboarding": ".rune/tutorials/onboarding.star",
}
```

Anyone who opens the workspace gets `tutorial basics` available
automatically, with no per-developer setup, no extra installs. Paths are
resolved against the workspace root, so the same configuration works
for local checkouts and remote SSH workspaces.

This is the right place for a tutorial that walks new contributors
through *your* repository: the build steps that aren't obvious, the
two commands you wish everyone ran before opening a PR, the layout
of the codebase. The tutorial ships with the code and stays in sync
with it through normal commits and reviews.

### Ship a tutorial with an extension

A Rune extension package can ship its own `config.star` (or
`config.yaml`) at the top of its bundle. When the user installs the
extension, Rune shows them the diff your config makes against their
resolved config, asks for permission, and on allow merges your
additions into `~/.rune/config.yaml`. Subsequent IDE starts pick the
merged config up like any other user config.

This is the mechanism extensions use to "register" a tutorial: drop
the tutorial file inside the package and add a `tutorials` entry to
the package's bundled config. Use the package-version environment
variables (`RUNE_DATADIR`, `RUNE_PKG_ID`, `RUNE_PKG_VERSION`) to
point at the file from inside the package layout; they expand to
the install location at apply time:

```python title="config.star"
# Ship this file inside your extension bundle.

if "tutorials" not in config:
    config["tutorials"] = {}

config["tutorials"]["my-extension"] = (
    RUNE_DATADIR + "/pkg/" + RUNE_PKG_ID + "/" +
    RUNE_PKG_VERSION + "/tutorials/onboarding.star"
)
```

The `if "tutorials" not in config` guard is the conventional pattern
for extension configs: write additively so you never clobber a key
the user already set or a tutorial another extension contributed.

After install the user has a working `tutorial my-extension` and no
per-user setup steps. When you ship a new version of the extension
the same flow re-runs; Rune again shows the diff, asks for
permission, and persists the result.

This is the right place for a tutorial that walks the user through
*your extension*: what commands it exposes, what config it
understands, how to turn on optional features, and what each
shortcut does.

## DSL reference

Every tutorial is one Starlark file that calls `tutorial(...)`
exactly once with an entry function. The entry function runs on its
own goroutine and uses ordinary Starlark control flow
(`if`/`elif`/`else`, `for`, `while`, `def`, comprehensions, string
formatting) on top of the builtins below. Blocking builtins return
real values, so authors can branch and loop on user input inline.

### `tutorial(entry, id?, title?, version?)`

- `id` *(optional)*: stable identifier. Defaults to the name the
  tutorial is registered under.
- `title` *(optional)*: human-readable title. Defaults to the name.
- `version` *(optional)*: your own version string, surfaced via
  `Tutorial.Version()`. Use whatever scheme suits you.
- `entry` *(required)*: a 0-argument callable. The runtime invokes
  it when the user runs `tutorial start <name>`. Returning normally,
  calling `exit()`, or letting `cancel_on_dismiss` short-circuit
  ends the tutorial.

### `command_key()`

Returns the user's configured command-prompt key (default `:`).
Useful when you want to embed it inside step text without
hard-coding it:

```python
ck = command_key()
welcome = "Press `" + ck + "` to open the command prompt."
```

The `text` of a `wait_*` step gets this for free: the literal token
`<cmd>` is replaced with the command key at render time.

### `editor_mode()`

Returns the user's resolved editor mode: `"modal"`, `"standard"`, or
`"emacs"`. When the workspace is set to exo, this resolves to the
exo fallback so a tutorial always sees a concrete mode. Use it to
adjust prose where the modes differ conceptually:

```python
if editor_mode() == "modal":
    note = "Press `<esc>` to return to normal mode first."
else:
    note = ""
```

### `config_path()`

Returns the file this session reads its user configuration from. The
data directory is a launch flag, so never hardcode a path when your
copy points the user at their config:

```python
text = "Bindings live under `command.key_bindings` in `" + config_path() + "`."
```

### `key_for(command, *args)`

Returns the key spec the user has bound to `command` (with optional
arguments), or `""` when nothing is bound. The lookup reflects the
user's actual `command.key_bindings`, so it tracks custom rebindings
rather than defaults. When an arguments-qualified lookup misses, it
falls back to the binding for the bare command. Use it to show a key
hint only when one exists:

```python
def keyhint(cmd, *args):
    k = key_for(cmd, *args)
    return (" Default key: `" + k + "`.") if k else ""

text = "Split the workspace with `windownew`." + keyhint("windownew")
```

### `command_exists(command)`

Returns whether `command` is registered right now. Use it to branch on
commands that come from an installable package rather than the core
binary; the lookup is live, so it flips to `True` as soon as the user
installs the package mid-tutorial:

```python
if not command_exists("searchfile"):
    teach_install_fuzzy_search()
```

Prefer this over `key_for` for availability checks: a command can be
bound in a preset but not installed, and installed but unbound.

### `workspace_open()`

Returns whether a project workspace is open in the focused slot.
It is `False` when the focused slot still shows the home workspace,
`True` once a project workspace is attached. Use it to require a
workspace before a tutorial that only makes sense inside one:

```python
if not workspace_open():
    fail("Open a workspace first, then run this tutorial again.")
    return
```

### `exit()` and `cancel_on_dismiss(result)`

`exit()` ends the entry function immediately. It works from any
depth (helper functions, loops, nested branches) and is cleaner
than threading a return value back up.

`cancel_on_dismiss(result)` is sugar for "if the user dismissed
this prompt, exit; otherwise return the result unchanged." It works
with anything that has a `.selected` boolean attribute; both
`choice` and `confirm` results qualify:

```python
pick = cancel_on_dismiss(choice(
    message="Where to start?",
    options=["Open workspace", "Edit a file"]))
# pick.selected is always True here.
```

### Level constants

`notify(level=...)` accepts the module-level constants `error`,
`warn`, `info`, and `success` instead of magic integers.

### Blocking UI builtins

Each blocking builtin is one **step**: it paints the tile and pauses
the entry function until the step's milestone is met. There is no
"press Enter to continue" screen. A step's copy stays up, unchanged,
until the user does what it asks, presses **Skip**, or ends the
lesson. Cancellation (**Stop**, `tutorial stop`, closing the tile, or
the IDE shutting down) unwinds the function cleanly.

Because the milestone is the only way forward, put everything the
user needs to read for a step into that step's `text`. An
introduction belongs in the first step's copy, above its instruction;
a transition belongs in the copy of the step it leads into.

The `text` of every step is markdown: headings, bold, italics, lists,
inline code, fenced blocks. The literal token `<cmd>` expands to the
user's command-prompt key at render time.

#### `wait_command(command, title=None, text=None)`

Pause until the user dispatches a specific Rune command from the
command prompt (or its bound key). This is the step type that makes
tutorials feel interactive; it advances when the user *does the
thing*.

A failed dispatch leaves the step armed with its copy unchanged: the
command prompt already reports the error, and rewriting the tile
mid-step would move the instructions the user is reading.

- `command`: the command name to wait for. Matches both the
  user-typed name and any alias that expands to it, so if your user
  has `w` aliased to `wopen`, both forms advance the step. You can
  qualify it with arguments — `"! git log"` — when the lesson asks for
  a specific invocation: matching still uses the command name alone,
  but a skip resolves with those arguments, and the generated hint
  only offers a key when one is bound to *that* invocation. Without
  the arguments, a step asking for `! git log` would advertise the key
  bound to bare `!`, which opens the companion terminal instead.
- `title` *(optional)*: the step's heading. Reuse a title across
  consecutive steps that belong to one lesson ("focus up, now focus
  left") so they read as a continuation.
- `text` *(optional)*: the step's copy. Say what the user is about to
  learn and exactly what to do, with `<cmd>` for the prompt key and
  `key_for(...)` for their real binding. Without it the tile shows a
  generated one-liner ("Run `edit` from the command prompt (`:`), or
  press `<key>`.") followed by the command's manual, which is fine for
  a scratch lesson but rarely teaches anything.

`wait_command` returns a `command_result` with:

- `.name`: the matched command name.
- `.args`: a tuple of positional argument strings the user passed
  to the command. Guard reads of it — a skipped step resolves with
  the arguments the spec named, which is none here. See [Writing
  skip-safe lessons](#writing-skip-safe-lessons).

```python
def run():
    wait_command(
        title   = "Open a file",
        command = "edit",
        text    = ("Files open in the focused window. Open one with "
                   "`<cmd>edit <file>`; the completer lists the "
                   "workspace's files as you type."))

tutorial(entry=run)
```

#### `wait_event(event, uri=None, text=None, title=None)`

Pause until the editor dispatches a named event, for example when the
user opens a file. It is the right tool for steps where the user must
interact with a window of their own, like a fuzzy finder or a file
picker: the user types to filter, navigates the results, and opens
one, all while the tile keeps the instructions up.

- `event` *(required)*: the editor event-type name to wait for. The
  name is matched against the event Rune dispatches; an unknown name
  simply never matches. The most useful one is `"open"` (a file was
  opened). The full set is: `open`, `close`, `flush`, `create`,
  `change`, `remove`, `rename`, `edit`, `scroll`, `focus`, `unfocus`,
  `cursor`, `selection`, `hidden`, `visible`.
- `uri` *(optional)*: narrows the match to events whose URI contains
  this string. Without it any event of the right type resolves the
  step, which is usually wrong for a high-traffic event like `flush`.
- `text` *(optional)*: markdown shown in the tile while the step is
  armed. Describe the interaction the user should perform in the
  focused window. `<cmd>` expands to the command-prompt key.
- `title` *(optional)*: the step's heading.

By default `wait_event` matches purely on the event-type name, so any
open (for instance) resolves the step. Pass `uri` to also require the
event's URI to contain a given substring — for example
`wait_event(event="flush", uri="config")` waits for a write to the
user's configuration file rather than any buffer save.

The canonical use is a two-step "open the finder, then drive it" flow.
First wait for the command that opens the finder, then hand control to
the user with `wait_event`:

```python
def teach_file_search():
    # 1. Get the finder open. wait_command resolves when the user
    #    dispatches `searchfile`; the finder takes focus.
    wait_command(
        title   = "Search files",
        command = "searchfile",
        text    = ("`searchfile` opens a fuzzy finder over every file "
                   "in the workspace. Open it with `<cmd>searchfile`."))

    # 2. Now the user types to filter, moves the selection up and down,
    #    and presses <enter> to open a result. The finder has focus,
    #    so those keys never touch the tile; the step resolves on the
    #    file-open event.
    wait_event(
        event = "open",
        title = "Search files",
        text  = ("Type a few characters to filter, move the "
                 "selection with the arrow keys (or `<ctrl-j>` / "
                 "`<ctrl-k>`), then press `<enter>` to open a file."))
```

##### Key handling

The lesson never receives a keystroke: everything the user types goes to
the workspace. A step resolves from what the IDE *observes* instead:

| Builtin | What it does with keys |
|---|---|
| `wait_command` / `wait_shell` | Resolve from command/shell *dispatch*, never keystrokes. |
| `wait_event` | Resolves from an editor *event*, never keystrokes. |
| `confirm` / `choice` | Resolve when the user clicks an option in the tile. |

`<enter>`, `<space>`, and `<esc>` are never "continue": a step ends on
its milestone, on **Skip**, or not at all.

#### `wait_shell(args, title=None, text=None)`

Pause until the user runs a specific command *inside* Rune's companion
console. Use it to guide the user through console workflows such as
installing a package or signing in to a model provider.

`wait_shell` is exclusively about commands run *within* the console, so
it only advances on console activity. It does not cover *opening* the
console. Opening it is just running the `console` command from the
command prompt, which you wait for with
`wait_command(command="console")`; explain what the console is in that
step's copy.

- `args` *(required)*: a non-empty list of argument tokens the console
  command must contain, for example `["pkg", "install", "rune-agent"]`.
  Matching is **containment**, not exact equality: every token in
  `args` must appear among the command's arguments, in any position,
  so completion, aliases, and extra flags still match.
- `title` *(optional)*: the step's heading.
- `text` *(optional)*: the step's copy. Without it the tile shows
  "Run `pkg install rune-agent` in Rune's console."

Like `wait_command`, `wait_shell` returns a `command_result` whose
`.args` is the full tuple of arguments the user actually ran (handy
when `args` matched loosely and you want the exact tokens).

```python
def teach_agent():
    # First, get the user into the companion console; that is just the
    # `console` command run from the command prompt.
    wait_command(
        title   = "Set up the agent",
        command = "console",
        text    = ("Packages are installed from Rune's console, a "
                   "durable tab with its own REPL. Open it with "
                   "`<cmd>console`."))

    # Then wait for them to install the agent package inside it.
    wait_shell(
        title = "Set up the agent",
        args=["pkg", "install", "rune-agent"],
        text=("In the console, run `pkg install rune-agent`. "
              "The install takes a few seconds."))

tutorial(entry=run)
```

#### `confirm(message)`

Shows a Yes / No prompt in the tile, answered by clicking an option.
Returns `True` on Yes and `False` on No. Use it to branch the tutorial
on the user's answer:

```python
if confirm("Want to learn how to open a file next?"):
    teach_edit()
```

#### `choice(message, options)`

Shows a prompt with the given options in the tile. Returns a
`choice_result` with:

- `.value`: the selected option string (`""` on dismissal).
- `.index`: the 0-based option index (`-1` on dismissal).
- `.selected`: `True` if the user picked an option, `False` on
  dismissal.

```python
pick = choice(
    message="Where to start?",
    options=["Open a workspace", "Edit a file", "Done"])
if pick.value == "Open a workspace":
    ...
elif pick.value == "Edit a file":
    ...
```

### Side-effect builtins

These do not require user interaction; they execute their work
synchronously and return immediately so the entry function keeps
running.

#### `notify(message, level=info)`

Shows a system notification. The default `level` is `info`. Use
`success` for confirmations that something worked, `warn` for
expected errors, `error` for unexpected failures.

Notifications interrupt the user, so keep them for what the tile
cannot say: a lesson that has to skip a section, or a command that ran
with the wrong arguments. Reaching a milestone is not news — the tile
moving on to the next step already says so.

#### `open_file(uri)`

Opens `uri` in the focused editor. Returns when the editor has
finished opening; if the editor reports an error the runtime
surfaces it as a notification.

#### `highlight_window(anchor)`

Currently a stub. Renders nothing and returns immediately; the
anchor-based highlight is on the roadmap.

## Patterns

### Branch on user input

Use `choice` or `confirm` and branch with `if`/`elif`/`else`:

```python
def run():
    pick = cancel_on_dismiss(choice(
        message="Where do you want to start?",
        options=["Open a workspace", "Edit a file", "Done"]))

    if pick.value == "Open a workspace":
        teach_workspace()
    elif pick.value == "Edit a file":
        teach_edit()
    # else "Done", fall through.
```

### Loop over a checklist

Walk the user through a list of related tasks with a `for` loop:

```python
def run():
    for path in ["README.md", "Makefile", "main.go"]:
        wait_command(
            command = "edit",
            text    = "Open `" + path + "` next: `<cmd>edit " + path + "`.")
```

### Compose with helpers

Tutorials get long; factor out repeated sequences into `def`:

```python
def teach_edit():
    wait_command(
        title   = "Open a file",
        command = "edit",
        text    = "Open any file with `<cmd>edit <file>`.")

def run():
    if confirm("Want to learn how to open a file?"):
        teach_edit()
```

### Bail out cleanly

- `return` from `entry` ends the tutorial normally.
- `exit()` ends it from any depth (handy in deeply nested helpers).
- `cancel_on_dismiss(...)` ends it when the user dismisses a prompt.
- `fail("...")` ends it with an error notification.

### One step, one milestone

There is no builtin that shows a page and waits for Enter, on
purpose: a step the user can click through teaches nothing, and a
"read this, then do that" pair leaves the instructions gone by the
time they are needed. Fold the reading into the step that needs it:

```python
intro_md = """\
A **workspace** is a project root: its files, its terminals, its
language servers. Rune keeps up to nine of them open at once.
"""

wait_command(
    title   = "Open a workspace",
    command = "wopen",
    text    = intro_md + "\nOpen one now with `<cmd>wopen <path>`.")
```

The copy stays on screen for as long as the user needs it, and the
step advances only when they actually open a workspace. A wrap-up that
asks for nothing has nowhere to live: end the lesson instead of
announcing it.

For workflows that happen inside Rune's companion console (installing
a package, signing in to a model provider), reach for `wait_shell`
rather than `wait_command(command="console")`. It matches the console
command's argument tokens directly.

### Use `text` to teach syntax

If your command takes arguments, say so up front. The generated hint
is the command's manual, which is generic; `text` can be specific:

```python
wait_command(
    command = "edit",
    text    = ("`<cmd>edit` needs a `<file>` argument. Use the "
               "auto-completer (Tab / arrow keys) to pick a file, "
               "or type a path inside the workspace."))
```

The hint is markdown and can run several lines. It stays put for the
whole step: a failed dispatch never rewrites it, so the user is not
reading a moving target while they retype the command.

### Writing skip-safe lessons

The tile's **Skip** button resolves whatever step is armed, so every
lesson has to survive being skipped from end to end.

A skipped `wait_command` or `wait_shell` reports the action the step
asked for, so the script sees what it told the user to run. That works
as long as the spec names its arguments:

```python
# Skip-safe: the skip resolves with name "windownew", args ("right",).
r = wait_command(command="windownew right")
wait_command(
    title   = "Close the " + r.args[0] + " split",
    command = "windowclose")
```

A `wait_command` with no arguments in its spec is the one case a skip
cannot reproduce, because the live path resolves with whatever the
user typed. Guard those reads:

```python
edit = wait_command(command="edit")
name = edit.args[0] if len(edit.args) else "the file you opened"
```

Every other builtin either discards its response or already defines a
dismissal, so skipping one is always safe: `confirm` resolves as `No`,
`choice` as dismissed, and `wait_event` simply advances.

### Drive a focused window (finders, pickers)

Some steps need the user to interact with a window they have focused:
typing into a fuzzy finder, walking its results, and opening one. Use
a `wait_command` to open the window, then a `wait_event` to let the
user drive it: keys always go to the workspace, so the finder receives
every one of them and the step resolves on the editor event:

```python
def teach_text_search():
    wait_command(
        title   = "Search file contents",
        command = "searchtext",
        text    = ("`searchtext` searches the contents of every file "
                   "in the workspace. Open it with `<cmd>searchtext`."))
    wait_event(
        event = "open",
        title = "Search file contents",
        text  = ("Type to filter, navigate with the arrow keys, then "
                 "press `<enter>` to jump to the line."))
```

Guidance like "press `<esc>` to cancel" goes into the step's text; a
tutorial cannot observe keystrokes, only what they cause.

## Errors and recovery

- Parse errors are surfaced as workspace notifications when Rune
  starts. The offending tutorial is skipped; the rest continue to
  work.
- A failed `open_file` (no editor, bad URI) shows the error as
  a notification and the entry function continues with the next
  builtin.
- A failed `wait_command` or `wait_shell` does *not* advance. The
  IDE's command prompt already shows the error, and the step stays
  armed with its copy unchanged so the user can fix and retry.
- A `wait_event` step stays armed until a matching event is observed
  (the named type, plus the `uri` substring when one is given). A
  non-matching event keeps it waiting.
- A runtime error in the entry function (or anywhere it calls)
  surfaces as an error notification and ends the tutorial.
- `fail("message")` ends the tutorial with an error notification.
- The user can always press **Stop** in the tile or run the `tutorial
  stop` command. **Skip** resolves a single step without ending the
  lesson.
- A lesson written against a DSL an older Rune had (`floating_window`,
  `markdown`, `wait_key`, or the `on_error`, `alignment` and `offset`
  arguments) still loads: it does not break the config or the other
  tutorials in it. Running it notifies that the tutorial is not
  supported by this version of Rune and to upgrade the package that
  provides it.

## See also

- [Command Prompt](../learn/command-prompt.md): how `tutorial`,
  `wopen`, `edit`, and other commands are dispatched.
- [Key syntax](../learn/key-syntax.md): the syntax `key_for` returns,
  for quoting bindings in step copy.
- [Config reference](../config.md): where the `tutorials` block
  lives.
