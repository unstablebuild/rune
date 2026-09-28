---
sidebar_position: 30
sidebar_label: Exoeditor (beta)
title: Exoeditor
---

# Exoeditor <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

Rune ships with four built-in editors: [`vim`](./vim-editor.md),
[`helix`](./helix-editor.md), [`standard`](./standard-editor.md)
(conventional), and [`emacs`](./emacs-editor.md). You can also run any terminal editor, including
Vim, Neovim, Helix, Kakoune, Micro, Emacs `-nw`, and Nano, inside a
Rune-managed tab by setting `editor.mode = "exo"`.

In `exo` mode Rune becomes an an IDE host wrapped around the terminal editor you already use. Your editor, the **exoeditor**, owns keystrokes, modes, and rendering. Rune, the **exohost**, owns the workspaces, windows, search, language intelligence, AI, and the debugger.

## Why run your editor inside Rune?

Because you get a production-grade environment wrapped around the editor you already love:

- **Your editor is the editor, everywhere.** When an extension, a code-search result, a diagnostic, or a stack frame opens a file, it opens in your configured editor, rather than one of the builtin editors. A fallback editor is configured to edit in-memory buffers like the [file explorer's buffer](./file-explorer.md).
- **Semantic features work like the built-ins.** Rune tracks your cursor position inside the external editor, so go to definition, find references, find implementations, hover, signature help, diagnostics and renames behave just like they do in `vim`/`standard`; no LSP setup required on your editor side.
- **Drop the plugins.** Once the IDE handles file pickers, LSP, search, debuggers, git, and tasks, you can strip your editor's config back to motions, colors, and in-file key bindings. A sane division of modifiers is encouraged: Your editor owns `<ctrl>`/`<alt>` modifiers, and Rune owns `<meta>`.

## How it fits together

In `exo` mode the responsibilities split like this:

- **The guest editor owns editing.** It receives every keystroke, manages its own modes, undo, registers, autocompletion popups, and writes the file to disk itself. Rune does not intercept edits or maintain its own cursor.
- **The exo host owns the workspace.** Tabs, splits, the [Command Prompt](./command-prompt.md), aliases, key bindings, tasks, language intelligence, search, diagnostics, the debugger, and AI all run as usual.
- **The exo host mirrors the file on disk.** After your editor saves, Rune reloads the on-disk contents into its buffer mirror and clears the dirty marker on the tab. Pseudo-URIs that don't live on disk (like the file explorer) run in a built-in editor instead; pick which one with [`editor.exo.fallback`](../config.md#exoeditor).
- **The exo host asks your editor to move the cursor.** When something like "go to definition" needs to jump, Rune renders your configured `goto` template (e.g. `<esc>:{line}<enter>{col}|`) and injects the keystrokes into the guest editor. Position read-back is heuristic: Rune infers the cursor from the rendered grid, so highly customized status lines or popups can occasionally throw it off.

### What you forfeit

`exo` swaps a tightly-coupled editor for a loosely-coupled one. A few features the built-in editors give you for free do not work the same way:

- **Auto-reload on external changes.** If an agent edit, formatter, or git operation rewrites the file while you have it open, Rune notices but does not force-reload the guest editor; only editors with their own file-change detection (Vim's `autoread`, Helix's reload, …) will pick it up. A stale buffer that you then save will overwrite the newer contents.
- **Autosave.** Saves go through your editor's own write path; Rune cannot autosave on focus loss or cancel an in-flight write.
- **In-editor highlights.** Syntax highlights, diagnostic squiggles, and inline hints come from your editor's configuration, not from Rune's renderer. Rune-rendered highlights are controlled by the beta [`editor.exo.experimental_highlights`](#experimental-highlights-beta) setting.

## Configuration

Set `editor.mode` to `"exo"` and configure the `exo` block:

```yaml tab
editor:
  mode: exo
  exo:
    command: 'vim "+call cursor({line}, {col})" {file}'
    goto: "<esc>:{line}<enter>{col}|"
    quit: "<esc>:qa!<enter>"
    fallback: vim
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": 'vim "+call cursor({line}, {col})" {file}',
        "goto":    "<esc>:{line}<enter>{col}|",
        "quit":    "<esc>:qa!<enter>",
        "fallback": "vim",
    },
},
```

### Substitutions

The `command` template is the argv Rune executes inside a vte to open a file. Available substitutions:

- `{file}`: absolute path to the file to open (required).
- `{line}`: 1-based line number for the initial cursor.
- `{col}`: 1-based column number for the initial cursor.

`goto` is a [key sequence](./key-syntax.md) that, when injected into the vte, makes the editor place its cursor at `{line}`/`{col}`. The same `{line}`/`{col}` substitutions are recognised inside the sequence. Leave it empty to disable `:goto` and mouse click-to-line for this editor.

### Quit sequence

`quit` is a [key sequence](./key-syntax.md) that Rune injects into the embedded editor when it closes the tab, so the external process exits cleanly instead of being left running or killed abruptly. Use a **force-quit** that discards any unsaved state; Rune has already mirrored saved contents to disk, and a confirmation prompt would otherwise hang the close. Leave it empty to let Rune terminate the process directly.

### Command prompt key

In `exo` mode you set [`command.key`](../config.md#command-prompt) yourself, and a good default is `<shift-meta-p>`, because almost every terminal editor already binds `:` for something of its own. Modal editors (Vim, Neovim, Helix, Kakoune) consume `:` for their command-line mode, and naturally modeless editors (Nano, Emacs, Micro, …) treat `:` as text to insert. In both cases the editor swallows the keystroke and the [Command Prompt](./command-prompt.md) never opens, so a modified combination the editor will not capture works best:

```yaml tab
command:
  key: "<shift-meta-p>"
```

```python tab
"command": {
    "key": "<shift-meta-p>",
},
```

Pick any modifier combination the external editor does not interpret. A bare `:` is never a suitable command-prompt key in `exo` mode.

### Fallback editor

Not every URI Rune opens lives on disk. The file explorer is served from a `memory://` pseudo-URI (`memory:///fexplorer`) that an external terminal editor cannot meaningfully open. The general rule is "anything not `file://` or `ssh://`", but today the file explorer is the single concrete pseudo-URI Rune ships with. In `exo` mode Rune routes those URIs to a built-in Rune editor instead.

`editor.exo.fallback` selects which built-in editor handles them. Valid values
are `"vim"`, `"helix"` (Helix's selection-first grammar), `"standard"`
(conventional, the default), and `"emacs"`. The deprecated `"modal"` and
`"modeless"` values are aliases for `"vim"` and `"standard"`.
The workspace as a whole is still considered externally managed (auto-save,
file-watcher reloads, and writes to disk all stay disabled); only the editing
surface for these IDE-owned URIs changes.

```yaml tab
exo:
  command: 'vim "+call cursor({line}, {col})" {file}'
  goto: "<esc>:{line}<enter>{col}|"
  fallback: standard
```

```python tab
"exo": {
    "command":  'vim "+call cursor({line}, {col})" {file}',
    "goto":     "<esc>:{line}<enter>{col}|",
    "fallback": "standard",
},
```

If `fallback` is set to an unrecognised value, Rune falls back to `"standard"`
so the IDE still boots.

### Experimental highlights (beta)

`editor.exo.experimental_highlights` lets Rune paint its own location-list attributes (syntax highlights, LSP diagnostics, and debugger highlights) directly on top of the guest editor's output. With it enabled, `exo` behaves almost like a built-in editor for visual feedback: the same colors you would see in `vim`/`standard` appear over your editor's viewport.

:::caution[Experimental]
Rune infers the file-to-cell mapping from the rendered grid, so unusual editor chromes, status lines, or popups can throw the overlay off. Disable it if you see misaligned highlights.
:::

```yaml tab
exo:
  command: 'vim "+call cursor({line}, {col})" {file}'
  goto: "<esc>:{line}<enter>{col}|"
  experimental_highlights: true
```

```python tab
"exo": {
    "command":             'vim "+call cursor({line}, {col})" {file}',
    "goto":                "<esc>:{line}<enter>{col}|",
    "experimental_highlights": True,
},
```

When enabled, Rune suppresses the guest editor's own text attributes for the file body and replaces them with its own. The editor's chrome (status line, gutter, command line) is left alone. Defaults to `False`.

### Examples

Each example below sets [`editor.exo.fallback`](#fallback-editor) to the
built-in editor that best matches the guest editor's muscle memory: `vim`
for the vi-family editors, `emacs` for Emacs, and `standard` for everything
else. It only affects IDE-owned surfaces like the file explorer; pick whichever
built-in you would rather use there.

Vim / Neovim:

```yaml tab
editor:
  mode: exo
  exo:
    command: 'vim "+call cursor({line}, {col})" {file}'
    goto: "<esc>:{line}<enter>{col}|"
    quit: "<esc>:qa!<enter>"
    fallback: vim
command:
  key: "<shift-meta-p>"
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": 'vim "+call cursor({line}, {col})" {file}',
        "goto":    "<esc>:{line}<enter>{col}|",
        "quit":    "<esc>:qa!<enter>",
        "fallback": "vim",
    },
},
"command": {
    "key": "<shift-meta-p>",
},
```

Helix:

```yaml tab
editor:
  mode: exo
  exo:
    command: "hx {file}:{line}:{col}"
    goto: "<esc>:goto<space>{line}<enter>"
    quit: "<esc>:q!<enter>"
    fallback: standard
command:
  key: "<shift-meta-p>"
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": "hx {file}:{line}:{col}",
        "goto":    "<esc>:goto<space>{line}<enter>",
        "quit":    "<esc>:q!<enter>",
        "fallback": "standard",
    },
},
"command": {
    "key": "<shift-meta-p>",
},
```

Kakoune:

```yaml tab
editor:
  mode: exo
  exo:
    command: "kak {file} +{line}:{col}"
    goto: "<esc>:edit<space>-existing<space>{file}<space>{line}<space>{col}<enter>"
    quit: "<esc>:q!<enter>"
    fallback: standard
command:
  key: "<shift-meta-p>"
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": "kak {file} +{line}:{col}",
        "goto":    "<esc>:edit<space>-existing<space>{file}<space>{line}<space>{col}<enter>",
        "quit":    "<esc>:q!<enter>",
        "fallback": "standard",
    },
},
"command": {
    "key": "<shift-meta-p>",
},
```

Micro:

```yaml tab
editor:
  mode: exo
  exo:
    command: "micro {file}:{line}:{col}"
    goto: "<ctrl-l>{line}:{col}<enter>"
    quit: "<ctrl-q>"
    fallback: standard
command:
  key: "<shift-meta-p>"
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": "micro {file}:{line}:{col}",
        "goto":    "<ctrl-l>{line}:{col}<enter>",
        "quit":    "<ctrl-q>",
        "fallback": "standard",
    },
},
"command": {
    "key": "<shift-meta-p>",
},
```

Emacs (no window system):

```yaml tab
editor:
  mode: exo
  exo:
    command: "emacs -nw +{line}:{col} {file}"
    goto: "<alt-x>goto-line<enter>{line}<enter>"
    quit: "<alt-x>kill-emacs<enter>"
    fallback: emacs
command:
  key: "<shift-meta-p>"
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": "emacs -nw +{line}:{col} {file}",
        "goto":    "<alt-x>goto-line<enter>{line}<enter>",
        "quit":    "<alt-x>kill-emacs<enter>",
        "fallback": "emacs",
    },
},
"command": {
    "key": "<shift-meta-p>",
},
```

Nano:

```yaml tab
editor:
  mode: exo
  exo:
    command: "nano +{line},{col} {file}"
    goto: "<ctrl-_>{line},{col}<enter>"
    quit: "<ctrl-x>n"
    fallback: standard
command:
  key: "<shift-meta-p>"
```

```python tab
"editor": {
    "mode": "exo",
    "exo": {
        "command": "nano +{line},{col} {file}",
        "goto":    "<ctrl-_>{line},{col}<enter>",
        "quit":    "<ctrl-x>n",
        "fallback": "standard",
    },
},
"command": {
    "key": "<shift-meta-p>",
},
```

If `editor.exo.command` is missing or does not contain `{file}`, Rune logs a config error and falls back to `mode = "vim"` so the IDE still boots. An invalid `goto` template is blanked, leaving click-to-line as a nop.

:::note
The `quit` sequences above force-discard so the editor exits without a save prompt. Vim/Neovim's `:qa!` quits all windows, while Helix and Kakoune's `:q!` close the single buffer. Micro's `<ctrl-q>` and Nano's `<ctrl-x>n` will still prompt if the buffer is dirty in ways Rune hasn't mirrored; the trailing `n` answers Nano's "Save modified buffer?" prompt with "No". Adjust the sequence if you have rebound these keys in your editor.
:::

## What you give up

Handing editing off to a guest TUI editor means forfeiting the integration points Rune's built-in editors provide:

- **Syntax highlighting** is whatever your editor draws, unless you use the beta [`editor.exo.experimental_highlights`](#experimental-highlights-beta) overlay.
- **Diagnostic highlights** from LSP are not rendered in your editor's viewport, unless you use the beta [`editor.exo.experimental_highlights`](#experimental-highlights-beta) overlay.
- **Incremental edit dispatching to extensions** is gone; extensions no longer see edits as you type.
- **Auto-saving is off.** Saving is delegated to the guest editor; Rune commands like `:write` return an error. `editor.auto_save` is silently ignored in `exo` mode.
- **Auto-reload on external changes is off.** When something outside your editor (an agent, `git`, another process) rewrites the file on disk, Rune does not push that change into the guest editor. If your TUI editor does not detect and reload changed files itself, you will keep editing a stale buffer and may overwrite the new contents on save.

## What you keep

- Your editor, your keybindings, your plugins, your workflow, untouched.
- Rune as a **resource opener**: commands and extensions that open files (jump-to-definition, file picker, agent-opened files) route through your editor.
- A live, read-only mirror of the file on disk. Rune watches the file and reloads the buffer on Write/Rename, so LSP, syntax, and status-bar subscribers stay in sync.
- The Rune window manager, tabs, terminals, agents, tasks, clipboard, and `runectl` all continue to work as usual.
- `<esc>` reaches the guest editor unchanged (the vte runs in non-modal mode).

## When to use it

Pick `exo` if your current terminal editor setup is non-negotiable and you want Rune as the workspace shell around it. For the deepest integration (semantic highlights, inline diagnostics, incremental edits) stick with `vim` or `standard`.
