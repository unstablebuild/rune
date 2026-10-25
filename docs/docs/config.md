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

A workspace config can run programs on your machine, through extensions,
language servers, aliases or the terminal shell, so it only applies once you
trust it. The first time you open a workspace that has one, Rune shows you the
file and asks:

- **trust** applies it now and every time you open this workspace, until the
  file changes.
- **trust once** applies it until you quit Rune.
- **ignore** opens the workspace with only your personal config until you quit
  Rune. Closing the prompt does the same.

Rune asks again whenever the file changes, whether you edited it yourself or
pulled a change. A workspace config larger than 64 KiB is never applied.

To change your mind, find the decision with `authorizer list` in the
[console](./learn/console.md#reviewing-permissions-and-trust-with-authorizer),
revoke it with `authorizer revoke <decision>`, and run `:workspacereload` in
that workspace to be asked again.

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
config["editor"]["mode"] = "vim"

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
buffers, cursor positions, window layout and the extension tabs shown in it,
such as agent chats, are restored, but everything the workspace owns is
rebuilt from a fresh read of your config file and the workspace's
`.rune/config.yaml`, which asks for your trust again if it changed. This is
the fastest way to try out a change and covers most of the configuration
surface:

- `editor.*`: mode, auto-save, tabspaces, comments, syntax size limits
- `command.*`: prompt key, aliases, key bindings, history key, overlay
- `syntax.*`, icons, frame and tab attributes, wallpaper
- `terminal.*`, including the plugin bar and `terminal.clipboard`
- `extensions`: every extension process is stopped and started again
- `debugger.*` adapters and language-server settings
- `workspace.symbol_db` and extension auto-authorize settings
- `meta_open_url`

Only the focused workspace is reloaded; other workspace tabs keep running with
the configuration they were opened with.

### Restart

Everything that belongs to the process or to the GUI window is built once at
startup and needs a restart:

- all `gui.*` settings: fonts, themes, ligatures, opacity, blur,
  scroll multiplier, key mapping, quick menu, `gui.env`. Themes and
  `gui.env` variables added by a package install are available right away
  (see below).
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
`gui.env` variables, `gui.themes`, newly added extensions and newly added
tutorials take effect live. When the install finishes, the notification lists
which keys are in effect now and which need a restart.

## Editor Modes

Rune ships with four built-in editors and supports running any terminal
editor as another option. Pick one with `editor.mode`:

```yaml tab
editor:
  mode: "emacs" # or "standard", "vim", "helix", or "exo"
```

```python tab
"editor": {
    "mode": "emacs", # or "standard", "vim", "helix", or "exo"
},
```

The older `"modal"` and `"modeless"` values still work as deprecated aliases
for `"vim"` and `"standard"`, and the vim editor's settings section may still
be spelled `editor.modal` instead of `editor.vim`. Switch to the new names when
you next edit your config.

The five supported configurations are:

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
  replace directly with `<meta-r>` on macOS or `<ctrl-r>` on Linux, which also
  switch an open search to replace mode.
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

### Helix

A [Helix](https://helix-editor.com)-style editor with an inverted modal
grammar: a motion first selects the text you mean, then an operator acts on
that selection. You press `w` then `d` to delete a word, not `dw`, and you
always see what an operator is about to touch.

Pick this if your muscle memory comes from Helix, or if you like modal editing
but want the selection to be visible before you act on it.

See the [Helix Editor cheatsheet](./learn/helix-editor.md) for the exact
keystrokes and what is or isn't supported.

Multiple selections work as in Helix: `C` copies a selection onto the next
line, `s` selects every regex match, `,` drops back to one selection, and every
motion, operator and insert-mode keystroke acts on all of them. See
[Multiple selections](./learn/helix-editor.md#multiple-selections).

The first-run Helix choice installs command bindings in addition to selecting
`editor.mode`, including Helix's `<space>` leader menu and its `<ctrl-w>`
window menu. If you switch an existing custom config by hand, changing only
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

Exoeditor has four fallback choices, picked with `editor.exo.fallback`. The
fallback is what Rune uses for buffers whose URI is not a real file path,
anything that is not `file://` or `ssh://`. Today the single concrete example
that ships with Rune is the file explorer
(`memory:///fexplorer`), where launching the external editor doesn't
make sense. Picking the right fallback keeps those buffers feeling like
the editor you chose:

- `editor.exo.fallback: "vim"` keeps those buffers in the vim editor. Use it
  when your external editor is Vim or Neovim, so a keystroke like `j`/`k` keeps
  its vi meaning across the whole UI.
- `editor.exo.fallback: "helix"` keeps those buffers on Helix's
  selection-first grammar. Use it when your external editor is Helix, so a
  keystroke like `w`/`e` keeps its Helix meaning across the whole UI.
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

## Tab animations

`animations.active_content_tab` plays over content tabs whose program reports
it is [working](./learn/terminal/program-status.md), and
`animations.active_workspace_tab` over the workspace tabs that own one. Both
take the same keys:

- `enabled`: set to `false` to turn the effect off.
- `shader`: one of `blaze`, `burn`, `inferno`, `pulse` or `shine`.
- `fps`: how many times per second the effect redraws.
- `loop`: how long one loop of the effect lasts, such as `"2400ms"`. The
  flames of `blaze` and `inferno` take a few dozen loops to come round, so a
  shorter loop makes the fire faster.

Settings that only apply to one shader go under a key named after it, and are
only used while that shader is selected. All of them are optional:

| Shader | Setting | Effect |
| --- | --- | --- |
| `pulse`, `shine` | `color` | The colour the label is blended toward. Defaults to white. |
| `pulse`, `shine` | `intensity` | 0 to 1: how far the label is blended at the peak, where 1 reaches `color`. Subtle by default, so the label keeps its status colour. |
| `shine` | `band_width` | 0 to 1: how much of the label the band covers. Narrower reads as a glint, wider as a wash. |
| `shine` | `direction` | The way the band sweeps: `bottom_left_to_top_right` (default), `top_left_to_bottom_right`, `left_to_right`, `right_to_left`, `top_to_bottom` or `bottom_to_top`. |
| `blaze`, `inferno` | `color` | Turns the flames' red to this colour's hue, keeping their shading. Defaults to red; a grey also keeps them red. |
| `blaze`, `inferno` | `swap_red_blue` | Blue flames rather than red, which do not read as an error. Applied after `color`. |
| `burn` | `gradient` | The list of colours a character goes through as it burns. |
| `burn` | `duration` | 0 to 1: the part of a loop each character burns for. Shorter makes a narrower wave. |
| `burn` | `seed` | Picks another burn order. |
| `burn` | `origin` | 0 to 1: where across the label the wave starts, 0 being its left edge. `None` (the default) starts it at a random character. |
| `blaze`, `burn`, `inferno` | `animate_background` | `true` animates the cell background rather than only colouring the text: blaze and inferno set it on fire, and burn burns the label glyph by glyph. |

```yaml tab
animations:
  active_workspace_tab:
    shader: shine
    shine:
      color: green
      intensity: 0.8
      band_width: 0.4
```

```python tab
config["animations"]["active_workspace_tab"]["shine"] = {
    "color":      "green",
    "intensity":  0.8,
    "band_width": 0.4,
}
```

## Screensaver

`animations.screensaver` plays a shader once Rune has had no input for a while.
Each time it starts, it plays the next of the shaders you list, starting over
after the last one. The shader plays a pass at a time, over and over, until
input stops it. Each shader decides what it draws, including whether your
workspace still shows through.

- `shaders`: the shaders to play, by the names `:shaderrun` completes, such as
  `matrix`, `rain` or `inferno`. A single name, such as `shaders: inferno`,
  plays just that one. Empty by default, which means there is no screensaver.
- `idle_timeout`: how long Rune waits without input before it starts, such as
  `"10m"` (the default).
- `duration`: how long one pass lasts. Defaults to `"1m"`. The fires, `blaze`
  and `inferno`, keep their own pace, so a pass of one lasts as many whole
  cycles of the fire as it takes to cover `duration`: about 31s each for
  `blaze` and 100s for `inferno`.
- `time_shader`: how a pass plays the shader's animation:
  - `loop` (the default): from start to end.
  - `ping_pong`: to the end and back.
  - `reverse`: from end to start.
  - `accelerate`: starting slow and speeding up.
  - `decelerate`: starting fast and slowing down.
  - `boomerang`: to the end and back, slowing down at the end.
- `fps`: how many times per second it redraws. Defaults to 30.
- `enabled`: set to `false` to turn it off without removing your shaders.

Any key, mouse movement, click or paste stops it. That input is not swallowed:
it still reaches the editor, so the key that wakes Rune up is typed as usual.

```yaml tab
animations:
  screensaver:
    shaders: [matrix, rain, inferno]
    idle_timeout: 5m
    duration: 30s
    time_shader: ping_pong
```

```python tab
config["animations"]["screensaver"]["shaders"] = ["matrix", "rain", "inferno"]
config["animations"]["screensaver"]["idle_timeout"] = "5m"
config["animations"]["screensaver"]["duration"] = "30s"
config["animations"]["screensaver"]["time_shader"] = "ping_pong"
```

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

- `gui.meta_key`: the physical key or keys that `<meta>` stands for in Rune's
  own key specs: `command.key_bindings`, `command.key`,
  `command.history_key`, and the search and file explorer `*_key` settings.
  The editors, terminals and extensions still receive the key you actually
  pressed, so this is not a remap. The value is one of the following:

  | Value | `<meta-x>` means | Offered for |
  | --- | --- | --- |
  | `<super>` (default) | Super+X (Command+X on macOS) | every editor |
  | `<alt>` | Alt+X | Linux: vim, helix, standard |
  | `<ctrl-super>` | Ctrl+Super+X | Linux: emacs |
  | `<alt-super>` | Alt+Super+X | Linux: emacs |

  macOS accepts only `<super>`. First-run setup on Linux asks for this value
  because desktops often keep Super+digits, Super+L and Super+arrows for
  themselves. A value the editor does not offer is reported as a config error,
  and Rune falls back to `<super>`. If two bindings end up on the same chord
  (for example `<meta-x>` and an explicit `<alt-x>` under `<alt>`), Rune
  reports the clash and the spelling without `<meta>` wins. See
  [Key combination syntax](./learn/key-syntax.md#what-meta-means).

  ```yaml tab
  gui:
    meta_key: "<alt>"
  ```

  ```python tab
  config["gui"]["meta_key"] = "<alt>"
  ```

- `gui.env`: environment variables Rune sets for itself and everything it
  starts: terminals, extensions, language servers, debuggers and tasks. A
  value may reference other variables, such as `$HOME`, and `$RUNE_DATADIR`
  names the Rune data directory of the machine that applies it. Each machine
  applies the config stored on it, so in a remote workspace the variables come
  from `~/.rune/config.yaml` on the remote machine. The remote machine never
  applies the `gui.env` of a project's `.rune/config.yaml`, because it can't
  ask you to trust that file.

  `PATH` extends the existing `PATH` instead of replacing it: the directories
  listed before `$PATH` go in front of it and the ones after it go at the end.
  Rune's shared binary directory, `$RUNE_DATADIR/bin`, always comes first.

  Terminals running bash, zsh or fish apply `gui.env` again after the shell's
  startup files, so it wins over them: a variable set in both has the
  `gui.env` value, and Rune's `PATH` entries come first even when a startup
  file such as `/etc/profile` resets `PATH`. The rest of the `PATH` that your
  startup files built follows.

  ```yaml tab
  gui:
    env:
      EDITOR: vim
      PATH: '$HOME/.local/bin:$PATH'
  ```

  ```python tab
  config["gui"]["env"] = {
      "EDITOR": "vim",
    "PATH": "$HOME/.local/bin:$PATH",
  }
  ```

## Links

- `meta_open_url`: how an `http://` or `https://` link opens when you click it
  in a markdown tab, a hover, a workspace notice, the cheatsheet or a Rune
  Agent reply, or click it with `<meta>` held in the GUI. It takes one of:

  - `"system"`, the default: open the link in your system browser.
  - `"clipboard"`: copy the link to the clipboard.
  - a Rune command, written like an [alias](./learn/aliases.md) body, that runs
    with `$URL` set to the link.

  A link is untrusted input, so a command receives it in a form that cannot
  run anything. `$URL` always expands to a single argument. In `$URL`,
  characters a URL may not contain, as well as `'` and `$`, are
  percent-encoded, which leaves the link pointing at the same page. Inside a
  `!` or `!!` command, `$URL` is read as plain text whether you write it
  unquoted, in double quotes or in single quotes, and Rune refuses to run the
  command rather than let a link change it. A command that passes `$URL` on to
  a second shell, such as `sh -c`, `eval` or `ssh`, must quote it again there,
  since characters like `&` and `;` can appear in links.

  Clicking a link with another scheme with `<meta>` held in the GUI opens it
  in the system browser.

  ```yaml tab
  meta_open_url: "browser $URL" # or "clipboard", or "! open $URL"
  ```

  ```python tab
  config["meta_open_url"] = "browser $URL" # or "clipboard", or "! open $URL"
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
