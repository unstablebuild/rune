---
sidebar_position: 1
---

# Command Prompt

One prompt drives the whole editor. If it isn't typing into a buffer,
it's a command: open a file, split a window, run a build, jump to a
definition. Each one is a single thing you can type, bind to a key, or
chain into an alias.

The payoff is speed that compounds. The fuzzy finder reaches any
command from the shortest sequence that singles it out, and commands
are named in consistent themed groups, so those sequences are
predictable and settle into muscle memory. You don't burn a key binding
on every action; you learn one prompt and it keeps getting faster.

This page covers how to open the prompt, how Rune parses what you type,
how to drive the fuzzy finder, and how to fire shell commands at will.

## Open the command prompt

The first-run key-binding preset chooses the activation key. For the preset
selected at the top of this page, it is <CommandPromptKey />.

Press that key from any editor or buffer and a one-line prompt opens at the
bottom of the screen. Type the command name, then any arguments separated by
spaces, then `<enter>`:

```
edit /etc/hosts
split
tabclose
```

Press `<esc>` or `<ctrl-c>` to dismiss the prompt without running
anything. Press `<tab>` while typing to complete the command name;
press it again to cycle through suggestions.

The key is configurable under `command.key`, overriding the preset value:

```yaml tab
command:
  key: "<meta-p>"
```

```python tab
"command": {
    "key": "<meta-p>",
},
```

The choice of key matters more than it looks. In the standard and Emacs
editors a bare printable key would be inserted into the buffer, so the prompt
needs a modified combination. In `exo` (exoeditor) mode there is no safe
default at all: the prompt key has to be one your guest editor will leave for
Rune. Pick a modified combination such as `<shift-meta-p>`.
See the [command-prompt configuration](../config.md#command-prompt) for
the full rules and recommended combinations.

## Arguments and quoting

Rune splits the line on whitespace into a command name and its
arguments. Two characters change that behaviour:

| Construct | Meaning |
| --- | --- |
| `'foo bar'` | One literal argument, no escapes. |
| `"foo bar"` | One argument; `\"`, `\\`, and `\<newline>` are escapes. |
| `\<space>` | A literal space inside an otherwise unquoted argument. |

So these all dispatch the same single-argument `edit` command:

```
edit "my file.go"
edit 'my file.go'
edit my\ file.go
```

Everything else (`$`, `*`, `?`, `|`, `&`, `(`, `)`, `<`, `>`) passes
through verbatim. The prompt does **not** glob and does **not**
interpret shell operators. That's intentional: each command decides
what its arguments mean.

If you need a `*.go` glob or a real pipe, hand the line to a shell
with `!` or `!!` (see below).

## Fixing the line

The prompt accepts the shell's editing keys for the end of the line:

| Key | What it does |
| --- | --- |
| `<backspace>` | Delete the previous character. On an empty line, close the prompt. |
| `<ctrl-w>` | Delete the previous word. |
| `<ctrl-backspace>`, `<alt-backspace>` | Delete the previous word. |
| `<ctrl-u>` | Clear the whole line. |

A "word" ends at anything that is not a letter, digit or `_`, so
`<ctrl-w>` walks back one path component at a time while you are
completing a file argument:

```
edit internal/handler/command/prompt.go
edit internal/handler/command/          <- <ctrl-w>
edit internal/handler/                  <- <ctrl-w>
```

Deleting past a completed argument unwinds the completion for it, so
the suggestion list follows the line back. Unlike `<backspace>`,
neither `<ctrl-w>` nor `<ctrl-u>` closes the prompt when the line runs
out.

Not all terminals distinguish `<ctrl-backspace>`; many send `^H`
instead, which the prompt treats the same way. For anything more than
trailing edits, use edit mode below.

## Edit the prompt with `<shift-esc>`

The prompt line is fine for short commands, but editing a long or
intricate one a character at a time is tedious. Press `<shift-esc>` to
drop the prompt into **edit mode**: the line you have typed so far
becomes a buffer you edit with a full Rune editor, with cursor motions,
word jumps, selection, yank and paste, undo, and auto-pairing all
available.

The editor follows your `editor.mode`: `vim` mode gives you
[modal editing](./vim-editor.md) with normal-mode motions and text objects,
`helix` mode gives you the [Helix editor](./helix-editor.md) and its
selection-first grammar, `standard` mode gives you the
[standard editor](./standard-editor.md), and `emacs` mode gives you the
[Emacs editor](./emacs-editor.md). In `exo` mode the prompt cannot host your
external editor, so it uses your
[fallback editor](./exoeditor.md#fallback-editor) (`editor.exo.fallback`),
which may be modal, Helix, standard, or Emacs. Either way you are editing the
command itself, not a file, so the buffer is the single line that will be
dispatched.

While edit mode is active, completion, history, and the manual are
suspended so your keystrokes go straight to the editor. To leave it:

| Key | What it does |
| --- | --- |
| `<shift-esc>` | Exit edit mode and return to the normal prompt. |
| `<tab>` | Exit edit mode and return to the normal prompt. |
| `<ctrl-c>` | Exit edit mode and return to the normal prompt. |
| `<enter>` | Exit edit mode and dispatch the command. |

On exit the edited line is replayed through the prompt as if you had
pasted it, so completions and argument parsing are recomputed from the
final text. This makes `<shift-esc>` the fast way to fix the middle of a
long command: jump to the word, change it with a motion, and press
`<enter>`.

## Variables

Rune expands `$VAR` placeholders against the current editor state when
a command runs. So this works:

```
edit $FILE_DIR/README.md
```

`$FILE_DIR` resolves to the directory of the focused file. The full
set of variables (`$FILE`, `$LINE`, `$WORD`, `$WORKSPACE_PATH`,
`$1..$9`, and POSIX operators like `${VAR:-default}` and
`${FILE%.go}_test.go`) lives in
[Aliases](./aliases.md).

## Run shell commands with `!` and `!!`

Two built-in commands hand the rest of the line to a real shell:

- `! <body>` opens a floating terminal that runs `body` through your
  `$SHELL` and shows its live output. Close the floater with `<esc>`
  or let the command exit on its own.
- `!! <body>` runs `body` headlessly through Rune's built-in shell
  interpreter. Stdout is discarded; stderr surfaces only if the
  command fails. Use this for side effects whose output you don't need
  to see.

Both forms accept pipelines, redirections, command substitutions, and
variable references; they're real shells:

```
! rg --color=always TODO
!! git fetch origin
! echo "$(date) on $(hostname)"
!! mkdir -p .rune/cache
```

Both forms run on whichever host the focused workspace lives on, so
the same `! ls` works in a local workspace and in one mounted over
SSH; Rune routes the command to the workspace's executor.

## Bind a command to a key

Once a command (built-in or alias) does what you want at the
prompt, bind it to a key so you don't have to type it:

```yaml tab
command:
  aliases:
    fmt: "!! gofmt -w $FILE"
  key_bindings:
    "<ctrl-f>": fmt
```

```python tab
"command": {
    "aliases": {
        "fmt": "!! gofmt -w $FILE",
    },
    "key_bindings": {
        "<ctrl-f>": "fmt",
    },
},
```

The binding dispatches the `fmt` alias directly. See
[Key combination syntax](./key-syntax.md) for the full grammar of
`<ctrl-f>`-style tokens.

A binding only fires if the window in focus does not claim the key first, so
`<ctrl-f>` above runs in an editor but not in a terminal, where it belongs to
the shell. See [When a command binding doesn't
fire](./key-mapping.md#when-a-command-binding-doesnt-fire) for how to keep the
key you want to press and still have the binding run everywhere.

## Move a binding to another key

`command.key_bindings` is keyed by the key combination, not by the command, and
your config is merged onto the shipped bindings. Binding a command to a new key
therefore *adds* a second way to run it: the original key stays bound. This
surprises people who rebind a key that their keyboard layout needs for typing.
On a French layout, for example, `<alt-shift-l>` types `|`, but the standard
preset binds it to `windowmove right`, so the pipe character never reaches the
editor.

To free the key, "reset" it by binding it to the empty string, and bind the
command to the key you actually want:

```yaml tab
command:
  key_bindings:
    "<alt-shift-l>": ""              # reset the original binding
    "<alt-shift-right>": "windowmove right"
```

```python tab
config["command"]["key_bindings"]["<alt-shift-l>"] = ""
config["command"]["key_bindings"]["<alt-shift-right>"] = "windowmove right"
```

An empty value is an explicit unbind: the entry stays in the map, but Rune
dispatches nothing for it. Deleting the key from your own config is not the
same thing, because the shipped binding underneath still applies.

## Replay keys as macros with `echo`

Some workflows can't be expressed as a single command and its
arguments; they need a small sequence of keystrokes, possibly opening
the prompt and pre-filling it. The `echo` command replays a sequence of
keys back into the event loop exactly as if you had typed them. This
turns an alias or key binding into a reusable macro.

The sequence uses the same [key syntax](./key-syntax.md) as bindings:
plain characters stand for themselves, and non-character keys are
spelled in angle brackets such as `<space>`, `<enter>`, and `<esc>`.

```yaml tab
command:
  aliases:
    # Open the prompt and type `edit `, leaving the cursor ready
    # for you to fuzzy-search a file to open in a new tab.
    tabnew: "echo {prompt}edit<space>"
```

```python tab
"aliases": {
    "tabnew": "echo {prompt}edit<space>",
},
```

### The `{prompt}` instruction

`{prompt}` opens the command prompt without assuming which key is bound
to it. Because the [activation key is configurable](#open-the-command-prompt),
hard-coding `:` would break for anyone using a different key; `{prompt}`
always opens the prompt regardless of `command.key`. Everything after it
in the sequence is typed into the freshly opened prompt:

```yaml tab
command:
  aliases:
    # Pre-fill the prompt with an LSP hover command, ready to run.
    hover: "echo {prompt}lsp<space>hover<space>"
```

```python tab
"aliases": {
    "hover": "echo {prompt}lsp<space>hover<space>",
},
```

Leaving the trailing `<space>` (and omitting `<enter>`) stops with the
command staged but not yet dispatched, so you can supply an argument or
review it before pressing `<enter>` yourself.

### The `{wait}` instruction

`{wait}` pauses the replay until the prompt's auto-completer has
finished populating its list. Interleave it when a later keystroke
depends on the completion results being ready, for example, selecting
the first match after typing a query:

```yaml tab
command:
  aliases:
    focusworkspace: "echo {prompt}workspacefocus{wait}<space>"
```

```python tab
"aliases": {
    "focusworkspace": "echo {prompt}workspacefocus{wait}<space>",
},
```

### Registers

`{register}X` expands the contents of register `X` (the same registers
the editor yanks into) as further key sequence, so you can replay a
recorded macro by name. Register expansion is recursive and guards
against cycles, and you cannot echo a register while it is actively
being recorded.

In the [vim editor](./vim-editor.md#macros), normal mode records a
macro into a register with `q{reg}` and replays it with `@{reg}` (for
example `qa` … `q` to record, `@a` to play). That recording is stored in
the same register, so `{register}` replays exactly those keys.

For example, record a macro into register `a` with `qa ... q`, then wrap
it in an alias so the recorded keys replay every time you run it:

```yaml tab
command:
  aliases:
    # Replay the keys recorded into register `a`.
    replaya: "echo {register}a"
```

```python tab
"aliases": {
    "replaya": "echo {register}a",
},
```

You can interleave registers with literal keys and the other
instructions, for example `echo {prompt}edit<space>{register}a<enter>`
to open the prompt and replay register `a` as the argument.

## Build muscle memory with the fuzzy finder

The prompt's filter is a fuzzy finder, and you can lean on it instead
of binding a key for everything. Say you want a new terminal tab but
haven't bound `terminalnewtab` under `command.key_bindings`. Open the
prompt, type enough letters to single out the command, and press
`<enter>`:

```
ternt<enter>
```

The fuzzy search is deterministic: `ternt` always yields
`terminalnewtab` at the top of the list, so `ternt<tab>` resolves to
it every time. The first time you reach for a command, spend 30
seconds finding the shortest sequence that ranks it first. From then
on you type that sequence on reflex, faster than hunting through a
menu, and without spending a key binding.

This is why built-in commands share consistent prefixes, or "themes":
`terminal*`, `tab*`, `window*`, and so on. The grouping is deliberate,
so once you know the theme you can guess the prefix and let the fuzzy
finder do the rest.

One snag: install an extension and a new command may suddenly answer
the sequence your fingers have memorized, because it fuzzy-matches
with a higher rank. The fix is an alias named exactly after your
memorized sequence, pointing at the command you mean:

```yaml tab
command:
  aliases:
    ternt: "terminalnewtab"
```

```python tab
"aliases": {
    "ternt": "terminalnewtab",
},
```

Now `ternt<enter>` always dispatches `terminalnewtab`, no matter what
else a fresh extension adds to the list.

## Re-run a previous command

Press <KeyBinding command="history" /> or run the `history` command to fill the prompt with
every line you've previously dispatched, newest first. The prompt now
fuzzy-searches that history instead of the command list, and because
each entry is the full line (command plus its arguments) you can find
and re-run a complex command without retyping it. Type to narrow the
list, press `<tab>` to move through the matches, and `<enter>` to run
the highlighted entry.

History persists across restarts and is capped at the most recent
entries. Both the key bound to the `history` command and the cap are
configurable:

```yaml tab
command:
  history_key: "<meta-r>"
  max_history: 20000
```

```python tab
"command": {
    "history_key": "<meta-r>",
    "max_history": 20000,
},
```

## See also

- [Aliases](./aliases.md): define your own commands, plus
  `$FILE`, `$1`, `${VAR:-default}`, and the rest of the expansion grammar.
- [Tasks](./tasks.md): long-running commands pinned as tabs or
  status bars instead of one-shot floaters.
- [Key combination syntax](./key-syntax.md): the spelling of
  bindings like `<ctrl-r>` and `<m-enter>`.
- [Exoeditor](./exoeditor.md): wire a guest terminal editor into
  the same command machinery.
