---
sidebar_position: 2
---

# Config

## Where the config lives

Your personal Rune config lives at `~/.config/rune/config.yaml` or
`~/.config/rune/config.star` (`$XDG_CONFIG_HOME/rune/` when
`XDG_CONFIG_HOME` is set): one file, in one of two formats. An existing
`~/.rune` install keeps its config there instead, and `-c` or `-d` always
overrides the resolution. You never have to create it by hand: the first
time you open Rune, the bootstrap writes `config.yaml` with a preset of
your choosing for you.

A workspace can also define its own `.rune/config.yaml` at the repository
root, which is layered on top of your personal config, when you open that workspace.

A workspace config can run programs on your machine, through extensions,
language servers, aliases or the terminal shell, so it only applies once you
trust it. The first time you open a workspace that has one, Rune shows you the
file and asks:

![Workspace trust prompt](https://assets.rune.build/images/docs-rune/config/workspace-trust.png)

- **trust** applies it now and every time you open this workspace, until the
  file changes.
- **trust once** applies it until you quit Rune.
- **ignore** opens the workspace with only your personal config until you quit
  Rune. Closing the prompt does the same.

Rune asks again whenever the file changes, whether you edited it yourself or
pulled a change. A workspace config larger than 64 KiB is never applied.

To change your mind, find the decision with `authorizer list` in the
[console](./learn/console.md#reviewing-permissions-and-trust-with-authorizer),
revoke it with `authorizer revoke <decision>`, and run `workspacereload` in
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

## Including other files

A config file can pull in other config files, so settings can be split by
topic, shared between machines, or installed by a package. In YAML, list them
under a top-level `include` key; in Starlark, call `include()`:

```yaml tab
include:
  - $RUNE_DATADIR/etc/rune-agent.yaml
  - ~/dotfiles/rune/theme.yaml
  - keys.star
editor:
  mode: vim
```

```python tab
include("$RUNE_DATADIR/etc/rune-agent.yaml")
include("~/dotfiles/rune/theme.yaml")
include("keys.star")
config["editor"]["mode"] = "vim"
```

Rune reads the files in the order listed, each on top of Rune's defaults and
the files before it, and then the including file's own settings on top of
them all, so a later file wins over an earlier one and your own file always
wins over what it includes. An included file can be YAML or Starlark whatever
the including file is, and can include files of its own. A Starlark file sees
in `config` what was set before it ran. In Starlark, `include()` takes effect
where it is called: settings assigned above the call are overridden by the
file, settings assigned below it override the file.

A path is resolved from the directory of the file that includes it; `~` is
your home directory and `$RUNE_DATADIR` is Rune's data directory. A file that
does not exist is reported and skipped, so an `include` line can be commented
out and back in without breaking the rest of the file. A file that includes
itself, directly or through another, is an error.

Two keys know about the layer they are set in. A `gui.env` variable that
refers to itself, such as `PATH: /opt/tool/bin:$PATH`, is chained onto the
value of the layer below it rather than replacing it, so every file can add
its own entries. And `include` itself is read from each file, not merged:
a workspace's `.rune/config.yaml` cannot include files.

[Installed packages](./develop/packages.md#the-config-overlay) keep their
settings in `$RUNE_DATADIR/etc/<package>.yaml` and add an `include` line for
it to your config, which is why that key appears there after a `pkg install`.
Put your own settings in your config, where they override the package's,
rather than in the file under `etc/`, which Rune rewrites when the package
changes.

## Applying config changes

Rune watches your config file and the files it includes. When you save one,
Rune reads the configuration again and applies what it can without a reload:
`gui.env` variables, the appearance keys `themereload` covers, `sounds`, `acp`
agents, newly added extensions and newly added tutorials. A notification then
lists which of your changes are in effect and which are not yet. The rest
takes a reload or a restart.

`workspacereload` closes the focused workspace and opens it again. The open
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
- `sounds`
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
  scroll multiplier, sound volume, key mapping, quick menu, app icon,
  `gui.env`. `runectl screenshot` applies the themes, sound volume, quick menu
  and app icon without one, and themes and `gui.env` variables added by a
  package install are available right away (see below).
- `log.output_path` and `log.level`
- `models.*` (the LLM router), `notifications.auto_close`, `telemetry.*`
- `animations.*`, `workspace.home`, clipboard and macro settings
- the configuration of the home screen shown when no workspace is open

Some `gui.*` values can be changed for the current session with commands such
as `guitheme`, `guifont`, `guifontsize`, `guiopacity` and `guivolume`. Those
changes are transient: write the corresponding key to your config file to make
them permanent.

You do not have to quit to try a `gui.*` change, though: `guiwindownew` spawns
a second Rune process in its own OS window, and that process reads the config
file from scratch. The new window comes up with your edited `gui.*` settings
while the current one keeps running, which makes it a convenient way to test
fonts, themes and window settings. Everything else in the restart list is
per-process too, so the new window picks those up as well.

Installing a package applies its config the same way a save does: `gui.env`
variables, the keys `themereload` covers, `sounds`, `acp` agents, newly added
extensions and newly added tutorials take effect live, and a notification
lists which keys are in effect now and which need a restart.

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

See the [Standard Editor cheatsheet](./learn/standard-editor.md) for
the exact keystrokes and the full binding table.

### Emacs

An Emacs-style editor with point motion, marks and regions, word and
paragraph commands, clipboard-history yank-pop, and a command family
under `<ctrl-x>`. Pick this if your muscle memory comes from Emacs but you
want Rune's native editor and IDE integrations.

See the [Emacs Editor cheatsheet](./learn/emacs-editor.md) for the complete
binding tables and the differences from GNU Emacs.

The first-run Emacs choice installs command bindings in addition to selecting
`editor.mode`. If you switch an existing custom config by hand, changing only
the mode does not replace your `command.key_bindings` map.

### Helix

A [Helix](https://helix-editor.com)-style editor with an inverted modal
grammar: a motion first selects text, then an operator acts on
that selection.

Pick this if your muscle memory comes from Helix, or if you like modal editing
but want the selection to be visible before you act on it.

See the [Helix Editor cheatsheet](./learn/helix-editor.md) for the exact
key combinations and what is or isn't supported.

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
your favorite editor, or you want to use an editor Rune does not
ship as a built-in.

For the list of what's supported in `exo` mode (file watching, LSP, search integration,
...), see the [Exoeditor](./learn/exoeditor.md) guide.

Exoeditor has four fallback choices, picked with `editor.exo.fallback`. The fallback is
what Rune uses for buffers whose URI is not a real file path, anything that is not
`file://`, `ssh://` or `rune://`. For example, Rune's file explorer is an in-memory
editable buffer, so launching the external editor is not possible. Picking the right
fallback keeps those buffers feeling like the editor you chose:

For ready-to-copy `exo` configs (Vim, Neovim, Helix, Nano, and more), see the
[examples in the Exoeditor guide](./learn/exoeditor.md#examples).

## Syntax Highlighting

If you don't like the default syntax highlights, you can always override the default
config by adding a `editor.highlights` section in your config. The attributes applied to
each Tree-sitter capture name, are merged key by key onto Rune's defaults. See the [Syntax
Highlighting](./appearance/syntax-highlighting.md) guide, or check the default values in
[Rune's builtin
config](https://github.com/unstablebuild/rune/tree/main/cmd/rune/rune.star).

  ```yaml tab
  editor:
    highlights:
      function:
        fg: blue
      comment:
        fg: silver
        flags: italic
      # function, function.builtin, function.method, type, property, 
      # variable, operator, keyword, string, escape, number, 
      # constant.builtin, comment, markup.heading, markup.raw, 
      # markup.raw.delimiter, markup.link, markup.link.url,
      # markup.link.label, markup.link.text, markup.list,
      # markup.quote, markup.strong, markup.italic, markup.strikethrough,
      # markup.underline, markup.math, text.title, text.literal,
      # text.uri, text.reference, text.emphasis, text.strong
  ```

  ```python tab
  config["editor"]["highlights"]["comment"] = {"fg": "silver", "flags": "italic"}
  ```

## Command Prompt

`command.key` is the [key combination](./learn/key-syntax.md) that opens the command
prompt. Based on the preset selected at the top of this page, it is <CommandPromptKey />.
If you use a different preset, switch it above and the rest of this guide will show
you the key bindings according to your preset.

## Animations

These keys set the animations Rune plays and the shaders they play. See
[Animations](./appearance/animations.md) for each of them, and for the shaders
available:

- `animations.intro`: the intro of the built-in logo when Rune starts.
- `animations.loading_workspace` and `animations.open_workspace`: while a
  workspace loads and once it opens.
- `animations.command_prompt`: over the command prompt while it is open.
- `animations.active_content_tab` and `animations.active_workspace_tab`: over
  tabs whose program is working.
- `animations.screensaver`: once Rune has had no input for a while.
- `animations.cursor`: over the window when the cursor jumps (GUI only).

## GUI

- `gui.default_theme` / `gui.themes`: the active color theme and the catalog
  of available themes. See [Themes](./appearance/themes.md).
- `gui.sound_volume`: the volume of the GUI's [sounds](./appearance/sounds.md),
  from `0` (mute) to `1` (the default). Values outside that range are clamped.

  ```yaml tab
  gui:
    sound_volume: 0.5
  ```

  ```python tab
  config["gui"]["sound_volume"] = 0.5
  ```

### Remap keys

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

### Choose your meta key (Linux only)

- `gui.meta_key`: the physical key or keys that `<meta>` stands for in Rune's
  key binding syntax. The value can be one of the following:

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

### Environment Variables

- `gui.env`: environment variables Rune sets for itself and everything it
  starts: terminals, extensions, language servers, debuggers and tasks. A
  value may reference other variables, such as `$HOME`, and `$RUNE_DATADIR`
  names the Rune data directory of the machine that applies it. Each machine
  applies the config stored on it, so in a remote workspace the variables come
  from the resolved Rune config on the remote machine. The remote machine never
  applies the `gui.env` of a project's `.rune/config.yaml`, for security reasons.

  `PATH` extends the existing `PATH` instead of replacing it: the directories
  listed before `$PATH` go in front of it and the ones after it go at the end.
  Rune's shared binary directory, `$RUNE_DATADIR/bin`, always comes first.

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

### Choose the app's icon

- `gui.app_icon`: the path of a GIF, JPEG, PNG or WebP image that Rune shows as its icon
while it runs, in place of the Rune logo. A square image at least 256 pixels wide looks
best; other shapes are centered on a transparent square. Images wider or taller than 8192
pixels are rejected.

  - On Linux the image becomes the window icon. GNOME and KDE Plasma show the
    icon of Rune's launcher instead, so also set the `Icon=` line of
    `~/.local/share/applications/rune.desktop` to the absolute path of the
    image. Rune's installer writes that file; with the Debian package, copy
    `/usr/share/applications/rune.desktop` there first. Upgrades from within
    Rune keep the edit, but running the installer again replaces the file.
  - On macOS the image replaces the icon in the Dock and the Command-Tab
    switcher. Finder, Launchpad and the Dock tile shown while Rune is not
    running keep the bundled icon.

```yaml tab
gui:
  app_icon: ~/.rune/icon.png
```

```python tab
config["gui"]["app_icon"] = "~/.rune/icon.png"
```

## Links

- `meta_open_url`: how an `http://` or `https://` link opens when you click it
  in a markdown tab, a hover, a workspace notice, the cheatsheet or a Rune
  Agent reply, or click it with `<meta>` held in the GUI. It takes one of:

  - `"system"`, the default: open the link in your system browser.
  - `"clipboard"`: copy the link to the clipboard.
  - a Rune command, written like an [alias](./learn/aliases.md) body, that runs
    with `$URL` set to the link.

  ```yaml tab
  meta_open_url: "browser $URL" # or "clipboard", or "! open $URL"
  ```

  ```python tab
  config["meta_open_url"] = "browser $URL" # or "clipboard", or "! open $URL"
  ```
