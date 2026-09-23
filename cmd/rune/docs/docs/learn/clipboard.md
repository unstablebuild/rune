---
sidebar_position: 40
---

# Clipboard

Rune copies and pastes through your operating system's clipboard by
default, so text you yank in the editor is available to other
applications and vice versa. This is controlled by the top-level
`clipboard` setting, which defaults to `"system"`:

```yaml tab
clipboard: "system" # or "memory"
```

```python tab
"clipboard": "system", # or "memory"
```

## How the system clipboard works

Rune talks to the platform clipboard through the standard system
utilities. What it needs depends on your operating system:

| Platform | What Rune uses |
| --- | --- |
| macOS | `pbcopy` and `pbpaste` (ship with macOS) |
| Linux (Wayland) | `wl-copy` and `wl-paste` from `wl-clipboard` |
| Linux (X11) | `xclip`, or `xsel` |

On macOS the system clipboard works out of the box. On Linux Rune
shells out to one of the utilities above, so at least one must be
installed and on your `PATH`.

On a Wayland session Rune prefers `wl-clipboard`; on X11 it uses
`xclip` and falls back to `xsel`. Install whichever matches your
session, for example:

```bash
# Debian/Ubuntu, Wayland
sudo apt install wl-clipboard

# Debian/Ubuntu, X11
sudo apt install xclip   # or: sudo apt install xsel
```

## When the system clipboard is unavailable

If none of the expected utilities are present (a common case on a
headless or freshly installed Linux box), Rune cannot reach the system
clipboard. When that happens Rune keeps copy and paste working through
an internal in-memory clipboard so you are never stuck, and it shows a
`system clipboard: …` warning so you know the text did not leave Rune.

The fix is to install one of the utilities listed above for your
platform.

## Suppressing the warnings with `memory`

If you cannot or do not want to install a clipboard utility, set the
`clipboard` mode to `"memory"`:

```yaml tab
clipboard: "memory"
```

```python tab
"clipboard": "memory",
```

In `memory` mode Rune uses an in-memory clipboard that lives entirely
inside the editor. Copy and paste work between Rune's own buffers and
registers, but the contents never cross the boundary to other
applications. Because Rune never touches the system clipboard in this
mode, the `system clipboard: …` warnings are suppressed.

The `clipboard` setting lives alongside the rest of your editor
configuration; see [Config](../config.md) for where the file lives and
how to edit it.

## Highlighting in the terminal

Dragging across text in a terminal highlights it and copies it to the
clipboard, as do double-clicking a word and triple-clicking a line. A
highlight starts only once the pointer leaves the cell you pressed, so a
click (for example to focus the window) does not replace what you copied
before.

When the program running in the terminal tracks the mouse, as vim does
with `set mouse=a`, clicks and drags go to that program instead and the
terminal does not highlight or copy. Use the program's own copy command,
such as yanking to the `+` register in vim.

## See also

- [Vim editor](./vim-editor.md): registers and the `+` system clipboard register
- [Standard editor](./standard-editor.md): copy, cut, paste, and paste-history keys
- [Config](../config.md)
