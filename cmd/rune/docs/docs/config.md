---
sidebar_position: 2
---

# Config

## Where the config lives

Your personal Rune config lives at `~/.rune/config.yaml` or
`~/.rune/config.star`: one file, in one of two formats. You never have to
create it by hand: the first time you open Rune, the bootstrap offers
**standard**, **emacs**, and **vim** key bindings and writes
`~/.rune/config.yaml` for you. The choice is not permanent: change
[`editor.mode`](#editor-modes) later to switch editors.

A workspace can also carry its own `.rune/config.yaml` at the repository
root, which is layered on top of your personal config for that project.

## YAML or Starlark?

The two formats produce the same configuration; they differ in how you write
it.

**YAML** (`config.yaml`) is plain, declarative key/value config. Pick it when
you want the simplest possible file: you write keys and values, and Rune
merges them onto its built-in defaults.

**[Starlark](https://starlark-lang.org)** (`config.star`) is a Python-like
language with variables,
functions, conditionals (`if`/`elif`/`else`), and loops (`for`). Pick it when
you want to build your configuration programmatically.

The important difference is what your Starlark script starts with. Rune
predeclares a global `config` dict that is already populated with Rune's
built-in default configuration. Your script reads from and writes to it in
place:

```python title="config.star"
# `config` already holds Rune's defaults; mutate what you need.
config["editor"]["mode"] = "modal"

for key in ["<ctrl-h>", "<ctrl-l>"]:
    config["command"]["key_bindings"][key] = "tabnext"
```

Whatever `config` holds when the script finishes is merged onto Rune's
defaults, exactly as a YAML document would be. The script runs in a sandbox:
standard Starlark only, with no `load()`, no file I/O, and no environment
access. A `config.star` that binds nothing (for example, a file with only
comments) is treated as an empty overlay.

## Applying config changes

Rune does not watch the config file. Open it with `:config`, edit it, save it —
nothing happens until you reload. There are two levels of reload.

### `:workspacereload`

`:workspacereload` closes the focused workspace and opens it again. The open
buffers, cursor positions and window layout are restored, but everything the
workspace owns is rebuilt from a fresh read of your config file and the
workspace's `.rune/config.yaml`. This is the fastest way to try out a change
and covers most of the configuration surface:

- `editor.*`: mode, auto-save, tabspaces, comments, syntax size limits
- `command.*`: prompt key, aliases, key bindings, history key, overlay
- `syntax.*`, icons, frame and tab attributes, wallpaper
- `terminal.*`, including the plugin bar
- `extensions`: every extension process is stopped and started again
- `debugger.*` adapters and language-server settings
- `workspace.symbol_db` and extension auto-authorize settings

Only the focused workspace is reloaded; other workspace tabs keep running with
the configuration they were opened with.

### Restart

Everything that belongs to the process or to the GUI window is built once at
startup and needs a restart:

- all `gui.*` settings: fonts, themes, ligatures, opacity, blur,
  scroll multiplier, key mapping, quick menu, `gui.env`
- `log.output_path` and `log.level`
- `models.*` (the LLM router), `notifications.*`, `telemetry.*`
- `animations.*`, `workspace.home`, clipboard and macro settings
- the configuration of the home screen shown when no workspace is open

Some `gui.*` values can be changed for the current session with commands such
as `guitheme`, `guifont`, `guifontsize` and `guiopacity`. Those changes are
transient: write the corresponding key to your config file to make them
permanent.

You do not have to quit to try a `gui.*` change, though: `guiwindownew` spawns
a second Rune process in its own OS window, and that process reads the config
file from scratch. The new window comes up with your edited `gui.*` settings
while the current one keeps running, which makes it a convenient way to test
fonts, themes and window settings. Everything else in the restart list is
per-process too, so the new window picks those up as well.

Installing a package is the one case where config is applied without a reload:
`gui.env` variables, newly added extensions and newly added tutorials are
picked up live. If a package changes anything else, Rune tells you to restart.

## Editor Modes

Rune ships with three built-in editors and supports running any terminal
editor as another option. Pick one with `editor.mode`:

```yaml tab
editor:
  mode: "emacs" # or "standard", "modal", or "exo"
```

```python tab
"editor": {
    "mode": "emacs", # or "standard", "modal", or "exo"
},
```

The four supported configurations are:

### Vim

A vi-style editor with modes, motions, operators, text objects,
marks, registers, and macros. Pick this if your muscle memory comes
from Vim or Neovim.

See the [Vim Editor cheatsheet](./learn/vim-editor.md) for the
exact keystrokes and what is or isn't supported.

There is no ex command mode: in Rune the `:` prompt is the global
[command prompt](#command-prompt), not a vi-internal one, so
`:s/foo/bar/g`, `:g/pattern/`, and similar ex commands are not part
of the editor. The vim preset is not a 100% Vim/Neovim port either; if you
rely on vimscript or the long tail of plugins, use [Exoeditor](#exoeditor)
with `vim` or `nvim` instead.

### Standard

A platform-aware, modeless editor for users coming from VS Code, Sublime Text,
Zed, or IntelliJ. Typing always inserts text, ordinary arrow keys move the
caret, and adding `<shift>` selects through the same movement.

Broad strokes of what's supported:

- Native application shortcuts: `<meta-c>`/`<meta-v>`/`<meta-z>` on macOS and
  `<ctrl-c>`/`<ctrl-v>`/`<ctrl-z>` on Linux.
- Native movement: `<alt-left>`/`<alt-right>` and
  `<meta-left>`/`<meta-right>` line boundaries on macOS; `<ctrl-left>`/
  `<ctrl-right>` words and `<ctrl-home>`/`<ctrl-end>` document boundaries on
  Linux.
- Adding `<shift>` extends every movement, including word, line, and document
  jumps.
- Line operations: duplicate, move, kill, join, indent/outdent,
  toggle line/block comment, wrap paragraph.
- Semantic selection: expand/shrink, select line, select all,
  select-next-occurrence of word at cursor.
- Copy and cut use the current line when no text is selected.
- Search and replace via `<meta-f>` on macOS and `<ctrl-f>` on Linux. Open
  replace directly with `<meta-r>`, or switch an open search to replace mode
  with `<meta-r>`.
- Folds: toggle one, toggle all, collapse/expand range.
- Auto-pair for `()`, `[]`, `{}`, `"`, `'`.
- Macros recorded on the unnamed register via `<ctrl-q>`.

Not supported in the standard editor: multi-cursor editing, a `<ctrl-x>` Emacs
prefix, completion popup (lives in language extensions), and goto-line (use
the [command prompt](#command-prompt)).

See the [Standard Editor cheatsheet](./learn/standard-editor.md) for
the exact keystrokes and the full binding table.

### Emacs

An Emacs-style editor with point motion, marks and regions, word and
paragraph commands, clipboard-history yank-pop, and a Rune command family
under `<ctrl-x>`. Pick this if your muscle memory comes from Emacs but you
want Rune's native editor and IDE integration.

See the [Emacs Editor cheatsheet](./learn/emacs-editor.md) for the complete
binding tables and the differences from GNU Emacs.

The first-run Emacs choice installs command bindings in addition to selecting
`editor.mode`. If you switch an existing custom config by hand, changing only
the mode does not replace your `command.key_bindings` map.

### Exoeditor

Exoeditor mode (`editor.mode = "exo"`) runs a real terminal editor
(Vim, Neovim, Helix, Kakoune, Nano, Emacs `-nw`, ...) as an **external
editor** inside a Rune-managed file tab, with Rune acting as the
**host** around it. Use this when you want the last 1% of parity with
your editor of choice, or you want to use an editor Rune does not
ship as a built-in.

For the list of what does and does not work inside `exo` mode (file
watching, LSP, search integration, ...), see the
[Exoeditor](./learn/exoeditor.md) guide.

Exoeditor has three fallback choices, picked with `editor.exo.fallback`. The
fallback is what Rune uses for buffers whose URI is not a real file path,
anything that is not `file://` or `ssh://`. Today the single concrete example
that ships with Rune is the file explorer
(`memory:///fexplorer`), where launching the external editor doesn't
make sense. Picking the right fallback keeps those buffers feeling like
the editor you chose:

- `editor.exo.fallback: "modal"` keeps those buffers modal. Use it when your
  external editor is Vim or Neovim, so a keystroke like `j`/`k` keeps its vi
  meaning across the whole UI.
- `editor.exo.fallback: "standard"` keeps those buffers in the standard editor.
  Use it when your external editor is modeless (Nano, Micro, Kakoune, Emacs in its
  default bindings, ...), so a printable keystroke is always insertion, not a
  command.
- `editor.exo.fallback: "emacs"` gives those buffers Rune's built-in Emacs
  editing behavior. Use it when your external editor is Emacs and you want
  consistent point, mark, and region commands in Rune-owned buffers.

For ready-to-copy `exo` configs (Vim, Neovim, Helix, Nano, and more), see the
[examples in the Exoeditor guide](./learn/exoeditor.md#examples).

## Syntax Highlighting

- `editor.highlights`: the attributes applied to each Tree-sitter capture
  name, merged key by key onto Rune's defaults. See
  [Syntax Highlighting](./learn/syntax-highlighting.md).

  ```yaml tab
  editor:
    highlights:
      comment:
        fg: silver
        flags: italic
  ```

  ```python tab
  config["editor"]["highlights"]["comment"] = {"fg": "silver", "flags": "italic"}
  ```

## Command Prompt

- `command.key`: the [key combination](./learn/key-syntax.md) that opens the
  command prompt. The first-run preset writes a matching value. For the preset
  selected at the top of this page, it is <CommandPromptKey />.

  [Exoeditor](#exoeditor) is not part of the bootstrap flow, so it has no
  bootstrapped default; you set `command.key` yourself (see below).

  ```yaml tab
  command:
    key: ":"
  ```

  ```python tab
  "command": {
      "key": ":",
  },
  ```

  A bare printable key like `:` only works with [Vim](#vim)
  editing, where the editor distinguishes a normal mode from an insert
  mode and the command key is bound outside insert mode. With
  [Standard](#standard) editing the editor always treats the next
  character as text to insert, so pressing `:` would insert `:` at the
  cursor instead of opening the prompt; that is why the standard
  bootstrap rebinds `command.key` to `<shift-meta-p>`.

  In [Exoeditor](#exoeditor) mode the right `command.key` depends on your
  **external editor's** modality, not on the fallback. There is no
  bootstrapped default, so pick the value that fits your editor:

  - Modal external editors (Vim, Neovim, Helix, Kakoune) have a normal mode
    where `:` is free, so keep `command.key` at `:`.
  - Naturally modeless external editors (Micro, Emacs, Nano) treat `:` as
    text to insert, so a single-character `command.key` can never open the
    prompt; rebind it to a modified combination such as `<shift-meta-p>`.

  See the [Examples](./learn/exoeditor.md#examples) in the Exoeditor guide for
  the recommended `command.key` per editor.

## GUI

- `gui.default_theme` / `gui.themes`: the active color theme and the catalog
  of available themes. See [Themes](./learn/themes.md).
- `gui.key_mapping`: remap physical keys before they reach the editor (GUI
  only). Both sides use the [key combination syntax](./learn/key-syntax.md).
  This is the reliable way to make CapsLock act as Escape, and the only way to
  use keys such as CapsLock, NumLock, ScrollLock, and Menu, which otherwise do
  nothing. See [Key remapping](./learn/key-mapping.md).

  ```yaml tab
  gui:
    key_mapping:
      "<capslock>": "<esc>"
  ```

  ```python tab
  config["gui"]["key_mapping"] = {"<capslock>": "<esc>"}
  ```

## Telemetry

- `telemetry.enabled`: whether Rune reports anonymous usage data. It is `true`
  by default. See [Telemetry](./learn/telemetry.md) for the full list of what
  is reported and how to opt out.

  ```yaml tab
  telemetry:
    enabled: false
  ```

  ```python tab
  config["telemetry"]["enabled"] = False
  ```
