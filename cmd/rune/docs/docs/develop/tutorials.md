---
sidebar_position: 30
---

# Tutorials

Rune lets you ship interactive, in-IDE tutorials written in a small
[Starlark](https://starlark-lang.org/) DSL. They render as an overlay on top of the editor, walk
the user through a sequence of steps, and can wait for the user to
actually run a Rune command before moving on.

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
    floating_window(title="Welcome", text="Press `<enter>` to continue.")
    ws = wait_command(command="wopen")
    notify(level=success, message="Opened workspace: " + ws.args[0])

tutorial(id="hello", title="Hello, Rune", version="1", entry=run)
```

Three things happen when the user runs this:

1. A centered framed window says hello. The user presses Enter,
   Space, or Esc to dismiss.
2. The overlay waits at a small bottom-right hint. As soon as the
   user actually dispatches the `wopen` command, the tutorial
   captures the first argument and the entry function continues.
3. A success notification fires using the workspace path the user
   supplied, and the tutorial finishes.

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

The `on_error` hint of `wait_command` gets this for free: the
literal token `<cmd>` is replaced with the command key at render
time.

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

Each blocking builtin pauses the entry function until the user
responds. Cancellation (`<esc>` on a prompt, `tutorial stop`, or
the IDE shutting down) unwinds the function cleanly.

#### `floating_window(text, title=None, alignment=None, offset=None, allow_keys=None, dismiss_keys=None)`

A framed window with markdown content. Blocks until the user
presses `<enter>`,
`<space>`, or `<esc>` to dismiss. By default the window is centered
on the screen; `alignment` and `offset` let you pin it to a corner
  or edge instead.

- `text`: markdown body. Standard markdown: headings, bold,
  italics, lists, inline code, fenced blocks.
- `title` *(optional)*: single-line title rendered at the top of
  the window. Setting a title turns the top frame edge into an
  inverse-video status bar that also shows `Step N` (the current
  visible-content step number) on the right.
- `alignment` *(optional)*: where on the screen the window
  anchors. One of:

  | Value | Position |
  |---|---|
  | `"center"` (default) | Centered vertically and horizontally |
  | `"top"` | Top edge, horizontally centered |
  | `"bottom"` | Bottom edge, horizontally centered |
  | `"left"` | Left edge, vertically centered |
  | `"right"` | Right edge, vertically centered |
  | `"top-left"` | Top-left corner |
  | `"top-right"` | Top-right corner |
  | `"bottom-left"` | Bottom-left corner |
  | `"bottom-right"` | Bottom-right corner |

- `offset` *(optional)*: a `(x, y)` tuple of integers that nudges
  the window away from the anchor. On a left/right edge `x` shifts
  inward (toward the center). On a top/bottom edge `y` shifts
  inward. On the centered axis the offset adds directly: positive
  values move right or down, negative values move left or up.
  Offsets are clamped so the window always stays fully on screen.

- `allow_keys` *(optional)*: list of key specs (e.g.
  `["<meta-1>", "<meta-2>"]`) that the window does **not** swallow.
  Matching events fall through to the IDE root without dismissing
  the window, so the user can act on the very bindings they're
  reading about. Use this when the text says "Press X to do Y" and
  pressing X should actually do Y while leaving the window open.

- `dismiss_keys` *(optional)*: list of key specs that dismiss the
  window **and** fall through to the IDE root. Use this for
  read-then-act flows: the window says "Press `:` to open the
  command prompt", the user presses `:`, the window resolves, and
  the IDE root sees the `:` so the prompt opens. The next step
  (typically `wait_command`) sees the prompt the user just opened.

Examples:

```python
# Centered welcome (the default).
floating_window(title="Welcome", text="...")

# Tucked into the top-right, 2 cells in and 1 cell down.
floating_window(
    title     = "Tip",
    text      = "Try `<cmd>edit` to open a file.",
    alignment = "top-right",
    offset    = (2, 1))

# Pinned to the bottom edge, slightly above the status bar.
floating_window(
    text      = "Press `<enter>` to continue.",
    alignment = "bottom",
    offset    = (0, 2))

# Welcome screen that lets the user actually try meta-1..meta-9 and
# that advances when they press the command-prompt key.
ck = command_key()
floating_window(
    title = "Welcome",
    text  = "Press `<meta-1>`..`<meta-9>` to switch slots, or "
            "`" + ck + "` to open the command prompt.",
    allow_keys   = ["<meta-1>", "<meta-2>", "<meta-3>",
                    "<meta-4>", "<meta-5>", "<meta-6>",
                    "<meta-7>", "<meta-8>", "<meta-9>"],
    dismiss_keys = [ck])
```

Best for the introduction step, transitions ("now let's look at
X"), and anything where the user needs to read a paragraph before
moving on. Use `alignment` and `offset` when you want to keep the
code or output behind the window visible, for instance pinning the
window to a corner so the user can still see their cursor.

#### `markdown(text)`

A plain top-left banner. Lighter than `floating_window`; no
frame, no centering. Advances on `<enter>`, `<space>`, or `<esc>`.
Use for short prompts where a framed window would feel heavy.

#### `wait_key(key)`

Pause the tutorial until the user presses a specific key. A framed
hint box appears at the top-center showing `Press <key> to continue.`

- `key`: a key spec in the same syntax used elsewhere in Rune
  (`<ctrl-c>`, `<enter>`, `<f1>`, `<a-tab>`, plain `q` for the
  letter `q`, etc.). See the [key syntax
  reference](../learn/key-syntax.md).

#### `wait_command(command, on_error=None, title=None, alignment=None, text=None)`

Pause until the user dispatches a specific Rune command from the
command prompt. This is the step type that makes tutorials feel
interactive; it advances when the user *does the thing* rather
than when they press a key.

While the step is armed, Rune renders a framed hint box at the
top-center that shows the awaited command and inlines its manual
(name, synopsis, summary), plus the key bound to it when there is
one. On a failed dispatch the box switches to the `on_error` hint and
stays armed.

- `command`: the command name to wait for. Matches both the
  user-typed name and any alias that expands to it, so if your user
  has `w` aliased to `wopen`, both forms advance the step. You can
  qualify it with arguments — `"! git log"` — when the lesson asks for
  a specific invocation: matching still uses the command name alone,
  but the hint names the full invocation and only offers a key when
  one is bound to *that* invocation. Without the arguments, a step
  asking for `! git log` would advertise the key bound to bare `!`,
  which opens the companion terminal instead.
- `on_error` *(optional)*: replacement hint shown after the user
  attempts the command and it fails (for example, missing
  arguments). The hint is markdown; the token `<cmd>` is replaced
  with the command-prompt key. The step stays armed so the user can
  fix and try again.
- `title` *(optional)*: a status-bar title rendered on the hint box,
  matching the `floating_window` title treatment. Use the same title
  as the preceding `floating_window` so the waiting step reads as a
  continuation of the step the user just read.
- `alignment` *(optional)*: where the hint box anchors, using the same
  keywords as `floating_window`. Hint boxes default to the bottom of
  the screen; pass the same alignment you gave the preceding
  `floating_window` when a step needs to leave part of the layout
  visible, so the tutorial does not jump from one edge to the other.
- `text` *(optional)*: markdown shown in the hint box instead of the
  command's manual. Use it when a lesson asks for the same command
  twice in a row — "focus left, now focus right" — where the manual
  cannot say which half is still due.

`wait_command` returns a `command_result` with:

- `.name`: the matched command name.
- `.args`: a tuple of positional argument strings the user passed
  to the command.

```python
def run():
    floating_window(text="Open a file with `<cmd>edit`.")
    edit = wait_command(command="edit")
    notify(level=success, message="You opened " + edit.args[0])

tutorial(entry=run)
```

#### `wait_event(event, uri=None, text=None, title=None, on_error=None, alignment=None)`

Pause until the editor dispatches a named event, for example when the
user opens a file. Unlike the other waiting builtins, `wait_event`
never resolves from a keystroke: it passes **every** key straight
through to whatever window is focused and advances only when the host
observes the matching editor event.

That property is what makes it the right tool for steps where the user
must interact with a *focused window of their own*, like a fuzzy finder
or a file picker: the user types to filter, navigates the results, and
opens one, all while the tutorial hint stays up. A `floating_window`
cannot host that interaction because it swallows the very keys the
finder needs (see [Key handling](#key-handling-in-waiting-steps)).

- `event` *(required)*: the editor event-type name to wait for. The
  name is matched against the event Rune dispatches; an unknown name
  simply never matches. The most useful one is `"open"` (a file was
  opened). The full set is: `open`, `close`, `flush`, `create`,
  `change`, `remove`, `rename`, `edit`, `scroll`, `focus`, `unfocus`,
  `cursor`, `selection`, `hidden`, `visible`.
- `uri` *(optional)*: narrows the match to events whose URI contains
  this string. Without it any event of the right type resolves the
  step, which is usually wrong for a high-traffic event like `flush`.
- `text` *(optional)*: markdown shown in the hint box while the step
  is armed. Use it to describe the interaction the user should perform
  in the focused window.
- `title` *(optional)*: a status-bar title on the hint box, matching
  the `floating_window` title treatment.
- `on_error` *(optional)*: replacement hint markdown; `<cmd>` expands
  to the command-prompt key.
- `alignment` *(optional)*: where the hint box anchors, using the same
  keywords as `floating_window`.

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
    floating_window(title="Search files",
                    text="Open the file finder with `<cmd>searchfile`.")
    wait_command(command="searchfile")

    # 2. Now the user types to filter, moves the selection up and down,
    #    and presses <enter> to open a result. wait_event passes all of
    #    those keys through to the focused finder and resolves on the
    #    file-open event.
    wait_event(
        event    = "open",
        title    = "Find and open a file",
        text     = ("Type a few characters to filter, move the "
                    "selection with the arrow keys (or `<ctrl-j>` / "
                    "`<ctrl-k>`), then press `<enter>` to open a file."),
        on_error = ("Type to filter, move with the arrow keys, then "
                    "press `<enter>` to open a result."))
    notify(level=success, message="You opened a file from the finder.")
```

##### Key handling in waiting steps

The difference between `floating_window` and the `wait_*` builtins
comes down to which keys reach the focused window underneath the hint:

| Builtin | What it does with keys |
|---|---|
| `floating_window` | **Swallows** every key except its `allow_keys` / `dismiss_keys`. Good for "read this paragraph", bad for "type into the thing behind me". |
| `wait_key` | Resolves on the one key it waits for; others are swallowed. |
| `wait_command` / `wait_shell` | Resolve from command/shell *dispatch*, never keystrokes, so they pass all keys through. |
| `wait_event` | Resolves from an editor *event*, never keystrokes, so it passes all keys through. |

So whenever a step needs the user to type into or navigate a window
they have focused, that step must be a `wait_command`, `wait_shell`, or
`wait_event`, not a `floating_window`. Note also that `<enter>`,
`<space>`, and `<esc>` are reserved by `floating_window` for
auto-advance and can never be passed through as `dismiss_keys`.

#### `wait_shell(args, on_error=None, title=None, text=None)`

Pause until the user runs a specific command *inside* Rune's companion
console. Use it to guide the user through console workflows such as
installing a package or signing in to a model provider.

`wait_shell` is exclusively about commands run *within* the console, so
it words its own hint accordingly and only advances on console activity.
It does not cover *opening* the console. Opening it is just running the
`console` command from the command prompt, which you wait for with
`wait_command(command="console")`.

- `args` *(required)*: a non-empty list of argument tokens the console
  command must contain, for example `["pkg", "install", "rune-agent"]`.
  Matching is **containment**, not exact equality: every token in
  `args` must appear among the command's arguments, in any position,
  so completion, aliases, and extra flags still match.
- `on_error` *(optional)*: replacement hint shown after a failed
  console dispatch. Same semantics as `wait_command`'s `on_error`: it
  is markdown, `<cmd>` expands to the command-prompt key, and the
  step stays armed so the user can fix and retry.
- `title` *(optional)*: a status-bar title rendered on the hint box,
  same as `wait_command`'s `title`.
- `text` *(optional)*: the step's own instruction, rendered under the
  hint's opener. Prefer it over a preceding `floating_window`: the
  console is focused at this point, so a page would swallow the very
  keys the user needs to type. A failed dispatch replaces it with
  `on_error`.

Like `wait_command`, `wait_shell` returns a `command_result` whose
`.args` is the full tuple of arguments the user actually ran (handy
when `args` matched loosely and you want the exact tokens).

```python
def teach_agent():
    # First, get the user into the companion console; that is just the
    # `console` command run from the command prompt.
    floating_window(text="Open Rune's companion console with `<cmd>console`.")
    wait_command(command="console")

    # Then wait for them to install the agent package inside it.
    wait_shell(
        args=["pkg", "install", "rune-agent"],
        text="Type it and press Enter; the install takes a few seconds.",
        on_error="In the companion console, run `pkg install rune-agent`.")
    notify(level=success, message="Rune Agent installed.")

tutorial(entry=run)
```

#### `confirm(message)`

Opens a real Yes / No prompt. Returns `True` on Yes, `False` on No
or dismissal. Use it to branch the tutorial on the user's answer:

```python
if confirm("Want to learn how to open a file next?"):
    teach_edit()
```

#### `choice(message, options)`

Opens a real prompt with the given options. Returns a
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
        floating_window(text="Open `" + path + "` next.")
        wait_command(command="edit")
```

### Compose with helpers

Tutorials get long; factor out repeated sequences into `def`:

```python
def teach_edit():
    floating_window(title="Open a file", text="Use `<cmd>edit`.")
    r = wait_command(command="edit")
    notify(level=success, message="You opened " + r.args[0])

def run():
    floating_window(title="Welcome", text="...")
    if confirm("Want to learn how to open a file?"):
        teach_edit()
```

### Bail out cleanly

- `return` from `entry` ends the tutorial normally.
- `exit()` ends it from any depth (handy in deeply nested helpers).
- `cancel_on_dismiss(...)` ends it when the user dismisses a prompt.
- `fail("...")` ends it with an error notification.

### Wait for a real action, not a keystroke

Whenever you can, reach for `wait_command` instead of
`floating_window` followed by `wait_key`:

```python
# Yes
floating_window(text="Open a workspace with `<cmd>wopen`.")
wait_command(command="wopen")

# Less so
floating_window(text="Open a workspace yourself, then press <enter>.")
wait_key(key="<enter>")
```

The first form advances only when the user actually opens a
workspace; the second only checks that they pressed Enter.

For workflows that happen inside Rune's companion console (installing
a package, signing in to a model provider), reach for `wait_shell`
rather than `wait_command(command="console")`. It words its hint for the
console and matches the console command's argument tokens directly.

### Use `on_error` to teach syntax

If your command takes arguments, give the user a friendly nudge
when they get it wrong. The default hint is generic; an `on_error`
hint can be specific:

```python
wait_command(
    command  = "edit",
    on_error = ("`<cmd>edit` needs a `<file>` argument. Use the "
                "auto-completer (Tab / arrow keys) to pick a file, "
                "or type a path inside the workspace."))
```

The hint is markdown and can run several lines.

### Drive a focused window (finders, pickers)

Some steps need the user to interact with a window they have focused:
typing into a fuzzy finder, walking its results, and opening one. A
`floating_window` can't host this: it swallows the keys the finder
needs. Use a `wait_command` to open the window, then a `wait_event` to
let the user drive it, since `wait_event` passes every key through and
resolves on the editor event instead of a keystroke:

```python
def teach_text_search():
    floating_window(title="Search file contents",
                    text="Open the text finder with `<cmd>searchtext`.")
    wait_command(command="searchtext")
    wait_event(
        event = "open",
        title = "Search file contents",
        text  = ("Type to filter, navigate with the arrow keys, then "
                 "press `<enter>` to jump to the line."))
    notify(level=success, message="You searched file contents.")
```

`<enter>`, `<space>`, and `<esc>` are reserved auto-advance keys for
`floating_window`, so fold guidance like "press `<esc>` to cancel"
into the hint text rather than trying to observe those keys with a
`floating_window` step.

## Errors and recovery

- Parse errors are surfaced as workspace notifications when Rune
  starts. The offending tutorial is skipped; the rest continue to
  work.
- A failed `open_file` (no editor, bad URI) shows the error as
  a notification and the entry function continues with the next
  builtin.
- A failed `wait_command` or `wait_shell` does *not* advance. The
  IDE's command prompt already shows the error, and the step stays
  armed with the `on_error` hint (when supplied) so the user can fix
  and retry.
- A `wait_event` step stays armed until a matching event is observed
  (the named type, plus the `uri` substring when one is given). A
  non-matching event keeps it waiting, and any key the user presses
  passes straight through to the focused window rather than resolving
  the step.
- A runtime error in the entry function (or anywhere it calls)
  surfaces as an error notification and ends the tutorial.
- `fail("message")` ends the tutorial with an error notification.
- The user can always run the `tutorial stop` command to dismiss the
  overlay. Step windows and prompts also carry a `Skip`
  button that stops the tutorial the same way.

## See also

- [Command Prompt](../learn/command-prompt.md): how `tutorial`,
  `wopen`, `edit`, and other commands are dispatched.
- [Key syntax](../learn/key-syntax.md): the syntax accepted by
  `wait_key`.
- [Config reference](../config.md): where the `tutorials` block
  lives.
