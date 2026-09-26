# Key remapping

`gui.key_mapping` rewrites one key combination into another **before** it
reaches the editor. It is useful when the operating system swallows a key, when
the window in focus claims a key you would rather bind to a command, or when a
physical key never produces a usable keystroke on its own. The classic example
is **CapsLock**.

This feature applies to the Rune GUI only. Both sides of a mapping use the same
grammar as command bindings; see [Key combination syntax](./key-syntax.md) for
the full reference.

## How it works

`gui.key_mapping` is a table of `source → target` entries. When you press a key
whose combination matches a `source`, Rune delivers the `target` combination
instead. The match is exact, including modifiers: `<ctrl-left>` only matches
Ctrl held with the Left arrow, not a bare Left arrow.

```yaml
gui:
  key_mapping:
    "<capslock>": "<esc>"
```

```python
config["gui"]["key_mapping"] = {"<capslock>": "<esc>"}
```

The example above makes CapsLock behave as Escape inside Rune. Targets can be
printable characters or named keys, so `"<numlock>": "0"` and
`"<ctrl-left>": "<home>"` are both valid.

## When a command binding doesn't fire

The window in focus receives every key first. If it acts on that key, the event
stops there and `command.key_bindings` never runs. This is deliberate: it keeps
a shell, a full-screen program, and an editor in insert mode completely
transparent to typing. Any combination Rune claimed for itself globally could
never be typed into them again.

A binding therefore only fires on a combination the focused window declines. A
terminal is the strictest case, because it forwards almost everything to the
shell: plain characters, and every `ctrl` and `shift` combination. It leaves
alone only combinations that include `alt` or `meta`, which is why Rune's
shipped bindings live there.

So this binding opens the file explorer from an editor, but does nothing in a
terminal, where `<ctrl-e>` belongs to the shell:

```yaml tab
command:
  key_bindings:
    "<ctrl-e>": "fexplorer"
```

```python tab
config["command"]["key_bindings"]["<ctrl-e>"] = "fexplorer"
```

`gui.key_mapping` solves this without giving up the key you want to press.
The remap runs before the focused window ever sees the event, so you can press
one combination and have Rune route on another. Map your combination to one the
terminal declines, then bind the command to that target:

```yaml tab
gui:
  key_mapping:
    "<ctrl-e>": "<alt-meta-e>"
command:
  key_bindings:
    "<alt-meta-e>": "fexplorer"
```

```python tab
config["gui"]["key_mapping"]["<ctrl-e>"] = "<alt-meta-e>"
config["command"]["key_bindings"]["<alt-meta-e>"] = "fexplorer"
```

`<ctrl-e>` now opens the file explorer everywhere, terminals included. What
reaches the terminal is `<alt-meta-e>`, which it does not claim, so the binding
runs.

Two things to keep in mind:

- The source combination is spent. Nothing in Rune receives `<ctrl-e>` after
  this, so the shell can no longer use it either. That is usually the point, but
  pick a combination you do not need to send through.
- Remap individual combinations, not a bare modifier. `"<ctrl>": "<meta>"`
  rewrites the `ctrl` bit on every key, so `<ctrl-c>` would become `<meta-c>`
  and you would lose the ability to interrupt a running command.

## Physical keys you can remap

Some physical keys do not normally produce a keystroke Rune can act on. They are
available **only** as remap sources and targets, and never do anything on their
own unless you map them here:

| Source | Physical key |
| --- | --- |
| `<capslock>` | Caps Lock |
| `<numlock>` | Num Lock |
| `<scrolllock>` | Scroll Lock |
| `<menu>` | Menu / Context-menu key |

If one of these keys is not listed in `gui.key_mapping`, pressing it does
nothing.

## Keys that cannot be remapped

A few keys are never delivered when pressed and cannot be used as a mapping
source or target, because no standard terminal escape sequence exists for them:

- Function keys above F12 (F13 and up).
- Print Screen.
- Pause / Break.

Pressing these in Rune has no effect, and listing them in `gui.key_mapping` does
nothing.

## Why remap CapsLock here instead of in the OS?

Remapping CapsLock to Escape at the OS level changes the key *symbol*, but
Rune's GUI reads physical keys directly and may not see that substitution.
Mapping `<capslock>` in `gui.key_mapping` is reliable because it runs inside
Rune, after the physical key is read.

More generally, a keystroke can be intercepted before it reaches Rune by:

- **macOS**: Keyboard Shortcuts, Mission Control, Spotlight, or Accessibility
  settings.
- **Linux**: the desktop environment, the window manager / compositor, or the
  input-method framework.

`gui.key_mapping` cannot recover a keystroke that one of those layers consumes
entirely, but it does let you re-purpose keys that *do* reach Rune, including
physical keys Rune would otherwise ignore.

## Why Alt on Linux

On macOS the shipped presets put Rune's own commands on `<meta>`, the Command
key. On Linux, Super belongs to the desktop: GNOME, KDE, and most tiling window
managers bind Super chords for launchers, workspaces, and tiling, and consume
them before Rune sees them. The Linux presets therefore bind nothing to
`<meta>` and use `alt` combinations instead. A focused terminal declines every
`alt` combination, so these chords reach Rune from a shell just as Command does
on macOS.

- App-wide commands take the familiar Linux `ctrl` shortcut plus `alt`, since
  plain `ctrl` belongs to the program in a terminal: `<ctrl-alt-q>` quits,
  `<ctrl-alt-=>` / `<ctrl-alt-->` change the font size, and `<ctrl-alt>` digits
  pick workspaces.
- The vim and Helix presets focus windows with `<alt>` and `h` `j` `k` `l`,
  and keep the rest of the layout on that same `<ctrl-alt>` layer: `h` `j` `k`
  `l` resize, the brackets step through tabs, and `` ` `` searches tabs.
  `<ctrl-shift-alt>` moves windows, tabs, and workspaces.
- The Standard preset keeps its `<alt>` IJKL window layer, which works the
  same on both platforms.
- The Emacs preset puts Rune's layer on `<alt-shift>`, because `<alt>` is
  Emacs Meta, and moves with `<ctrl-shift-alt>`.

The presets keep common desktop chords free: `ctrl-alt` with F1–F12 (virtual
consoles), `ctrl-alt` and `ctrl-shift-alt` with the arrow keys (workspace
switching), `<ctrl-alt-t>`, `<ctrl-alt-d>`, `<ctrl-alt-delete>`,
`<ctrl-alt-backspace>`, `alt` with F1–F12, and `<alt-tab>`, `<alt-space>`, and
`<alt-esc>`. The one exception is `<ctrl-alt-l>`: the vim and Helix presets use
it to widen the focused window, and KDE, Cinnamon, Xfce, and MATE lock the
screen with it. Desktops differ, so if yours claims a chord a preset uses,
[move the binding](./command-prompt.md#move-a-binding-to-another-key) or remap
the key here.

Rune still reads Super as `meta`, so if your desktop leaves it alone you can
bind commands to `<meta-...>` chords yourself.

## Linux: Super, Meta, and Hyper

On Linux, Rune reads four modifiers: `ctrl`, `shift`, `alt`, and `meta`. `meta`
is the Super key, the one most keyboards label with the Windows logo, so a
Super combination is written `<meta-...>` and pressing Super registers as
`meta`. This is expected, not a lost keypress. The shipped Linux presets leave
`meta` unbound; see [Why Alt on Linux](#why-alt-on-linux).

If your xkb layout defines the historical Meta or Hyper modifiers on separate
keys (for example Hyper on Caps Lock), Rune does not yet see them as their own
modifiers, so pressing them does not register as a modifier. `gui.key_mapping`
cannot conjure a modifier Rune never receives, but you can still put such a key
to work by remapping it to something Rune does read. For example, send Escape
from the Caps Lock key:

```yaml
gui:
  key_mapping:
    "<capslock>": "<esc>"
```

See [Key combination syntax](./key-syntax.md#modifiers) for the full modifier
model on Linux.

## Notes

- The mapping is read once at startup. Edit your config and restart Rune to
  pick up changes.
- Invalid entries (an unparseable source or target, or a non-string target) are
  skipped and reported as a notification; the remaining valid entries still
  load.
- Remapping happens before key bindings are resolved, so
  [bind commands](./command-prompt.md#bind-a-command-to-a-key) under
  `command.key_bindings` to the **target** key, not the source. For example,
  with `"<capslock>": "<esc>"`, a binding on `<esc>` fires when you press
  CapsLock; a binding on `<capslock>` never matches, because Rune has already
  rewritten it to `<esc>` by then.

## See also

- [Key combination syntax](./key-syntax.md): the grammar for both sides of a
  mapping.
- [Bind a command to a key](./command-prompt.md#bind-a-command-to-a-key): how
  `command.key_bindings` maps keys to commands.
- [Configuration](../config.md): where `rune.star` / the YAML config live and
  how to open them.
