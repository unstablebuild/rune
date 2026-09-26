---
sidebar_position: 20
---

# Standard Editor

Rune's standard editor follows the modeless conventions used by Sublime Text,
VS Code, Zed, IntelliJ, and native text controls. Typing inserts text, arrow
keys move the caret, and the ordinary `<shift>` movement keys extend the
selection through the same movement. Semantic-selection shortcuts are listed
separately below.

## Preset

Enable it in `config.yaml`:

```yaml tab
editor:
  mode: "standard"
```

The preset adapts application shortcuts to the operating system, and the keys
on this page follow the platform chosen with the Preset button at the top.

<Platform when="darwin">

`<meta>` is Command and `<alt>` is Option. Where a table lists two keys, the
Standard editor accepts both.

</Platform>
<Platform when="linux">

Common application shortcuts use `<ctrl>`, and Rune binds nothing to Super,
which belongs to the desktop. Rune's own commands use `<alt>` and `<ctrl-alt>`;
see [Why Alt on Linux](./key-mapping.md#why-alt-on-linux). The Standard editor
ignores every chord that carries `<meta>` or `<ctrl-alt>`, so the few editing
chords that exist only on Command elsewhere move to `<ctrl>`, `<ctrl-shift>`,
or `<alt>`.

</Platform>

Every key on this page uses Rune's [key syntax](./key-syntax.md).

## Remapping keys

Most editing and movement keys are built into the editor, not Rune commands,
so they cannot be rebound through `command.key_bindings`. To swap the physical
keys that trigger them, use [`gui.key_mapping`](./key-mapping.md), which rewrites
a key combination before it reaches the editor.

Application and layout shortcuts are Rune commands and can be changed through
`command.key_bindings`. The editor receives ordinary keys before the command
layer, so a command binding does not replace a key that the standard editor
already handles. Choose an unused chord when remapping a command.

## Windows and tabs

### Standard keybindings preset

When configuring Rune for the first time, we offer the ability to choose one of three
presets. If you choose the standard preset, Rune keeps familiar text-editing conventions
while putting windows, tabs, Git changes, and diagnostics on consistent keyboard layers,
powered by adding a second arrow key cluster at the `ijkl` keys.

<Platform when="darwin">

![Rune Standard editor preset default keybindings](https://assets.rune.build/images/standard-editor-keyboard-cheatsheet-v6.svg)

</Platform>

The preset uses a single `<alt>` layer, so a binding is guessable from the
key alone:

- `<alt>` plus `ijkl` points up, left, down, and right.
- `<alt>` plus a layout letter acts on a window or a tab.
- `<alt>` plus a code letter asks the language server about the caret.
- Adding `<shift>` turns focus into movement, or asks for a symbol by name.
- Adding <Platform when="darwin">`<meta>`</Platform><Platform when="linux">`<ctrl-shift>`</Platform>
  to the `ijkl` cluster resizes instead of focusing.

<Platform when="darwin">

No application binding in this preset needs three modifiers.

</Platform>

The tables below provide a copyable reference for the preset bindings:

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Focus a window | `<alt-i>` / `<alt-j>` / `<alt-k>` / `<alt-l>` |
| Move a window | `<alt-shift-i>` / `<alt-shift-j>` / `<alt-shift-k>` / `<alt-shift-l>` |
| Resize a window | `<alt-meta-i>` / `<alt-meta-j>` / `<alt-meta-k>` / `<alt-meta-l>` |
| Restore the default size | `<shift-meta-backspace>` |
| Maximize / minimize the size | `<shift-meta-+>` / `<shift-meta-->` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Focus a window | `<alt-i>` / `<alt-j>` / `<alt-k>` / `<alt-l>` |
| Move a window | `<alt-shift-i>` / `<alt-shift-j>` / `<alt-shift-k>` / `<alt-shift-l>` |
| Resize a window | `<ctrl-shift-alt-i>` / `<ctrl-shift-alt-j>` / `<ctrl-shift-alt-k>` / `<ctrl-shift-alt-l>` |
| Restore the default size | `<ctrl-shift-alt-backspace>` |
| Maximize / minimize the size | `<ctrl-shift-alt-=>` / `<ctrl-shift-alt-->` |

</Platform>

The directions are up, left, down, and right; for resizing, that is taller,
narrower, shorter, and wider.

Common window actions stay on the same `<alt>` layer:

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Split the focused window | `<alt-n>` |
| Open a terminal in place or in a new split | `<alt-enter>` |
| Run a program with `!` | `<shift-meta-enter>` |
| Close the focused window | `<alt-q>` |
| Close every window | `<alt-shift-q>` |
| Toggle maximization of the focused window | `<alt-m>` |
| Set horizontal / vertical split orientation | `<alt-h>` / `<alt-v>` |
| Prefill `windowconverttab` in the command prompt | `<alt-shift-enter>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Split the focused window | `<alt-n>` |
| Open a terminal in place or in a new split | `<alt-enter>` |
| Run a program with `!` | `<alt-shift-enter>` |
| Close the focused window | `<alt-q>` |
| Close every window | `<alt-shift-q>` |
| Toggle maximization of the focused window | `<alt-m>` |
| Set horizontal / vertical split orientation | `<alt-h>` / `<alt-v>` |
| Prefill `windowconverttab` in the command prompt | `<ctrl-alt-enter>` |

</Platform>

Tabs use brackets for left and right. Add `<shift>` to reorder the current
tab:

| Action | Key |
| --- | --- |
| Close tab | `<alt-w>` |
| Previous / next tab | `<alt-[>` / `<alt-]>` |
| Move tab left / right | `<alt-shift-[>` / `<alt-shift-]>` |
| Focus tab 1 to 9 | `<alt-1>` … `<alt-9>` |
| Move the current tab to position 1 to 9 | `<alt-shift-1>` … `<alt-shift-9>` |
| Search open tabs | ``<alt-`>`` |

New tab is a platform-native shortcut rather than an `<alt>` binding:
<Platform when="darwin">`<meta-t>`. `<meta-w>` is also kept as a native
close-tab alias.</Platform><Platform when="linux">`<ctrl-n>`.</Platform>

Workspaces sit one level above tabs:

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Focus workspace 1 to 9 | `<meta-1>` … `<meta-9>` |
| Move the focused window to workspace 1 to 9 | `<shift-meta-1>` … `<shift-meta-9>` |
| Search workspaces | ``<meta-`>`` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Focus workspace 1 to 9 | `<ctrl-alt-1>` … `<ctrl-alt-9>` |
| Move the focused window to workspace 1 to 9 | `<ctrl-shift-alt-1>` … `<ctrl-shift-alt-9>` |
| Search workspaces | ``<ctrl-alt-`>`` |

</Platform>

See [Layout Management](./layout-management.md) for the complete layout system.

Common navigation commands avoid keys owned by the editor:

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Toggle the file explorer | `<shift-meta-e>` |
| Previous / next cursor position | `<alt-,>` / `<alt-.>` |
| Open the cheatsheet | `<alt-/>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Toggle the file explorer | `<ctrl-shift-e>` |
| Previous / next cursor position | `<alt-,>` / `<alt-.>` |
| Open the cheatsheet | `<alt-/>` |

</Platform>

Code-intelligence commands share the same `<alt>` layer and are listed under
[Language intelligence](#language-intelligence-diagnostics-and-git-changes).

## Selection

Add `<shift>` to a movement to select through the same destination. Releasing
`<shift>` and moving the caret clears the selection. Pressing `<esc>` also
clears the selection. `<ctrl-shift-left>` and `<ctrl-shift-right>` are semantic
selection commands rather than word-selection variants.

<Platform when="darwin">

| Movement | Key |
| --- | --- |
| Select one character | `<shift-left>` / `<shift-right>` |
| Select one line vertically | `<shift-up>` / `<shift-down>` |
| Select by word | `<alt-shift-left>` / `<alt-shift-right>` |
| Select to line boundary | `<shift-meta-left>` / `<shift-meta-right>` |
| Select to document boundary | `<shift-meta-up>` / `<shift-meta-down>` |
| Select to previous / next paragraph | `<ctrl-shift-up>` / `<ctrl-shift-down>` |

</Platform>
<Platform when="linux">

| Movement | Key |
| --- | --- |
| Select one character | `<shift-left>` / `<shift-right>` |
| Select one line vertically | `<shift-up>` / `<shift-down>` |
| Select by word | `<alt-shift-left>` / `<alt-shift-right>` |
| Select to line boundary | `<shift-home>` / `<shift-end>` |
| Select to document boundary | `<ctrl-shift-home>` / `<ctrl-shift-end>` |
| Select to previous / next paragraph | `<ctrl-shift-up>` / `<ctrl-shift-down>` |

</Platform>

Line-start movements include leading whitespace. For example,
<Platform when="darwin">`<shift-meta-left>`</Platform><Platform when="linux">`<shift-home>`</Platform>
selects all the way to column zero, not only to the first non-blank character.

### Selection commands

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Select all | `<meta-a>` or `<ctrl-a>` |
| Select current line; repeat to extend | `<meta-l>` |
| Select next occurrence | `<meta-d>` or `<ctrl-d>` |
| Select previous occurrence | `<ctrl-meta-d>` |
| Select indentation level | `<shift-meta-j>` |
| Expand semantic selection | `<ctrl-w>` or `<shift-meta-space>` |
| Shrink semantic selection | `<ctrl-shift-w>` |
| Shrink / expand semantic selection | `<ctrl-shift-left>` / `<ctrl-shift-right>` |
| Select enclosing `()`, `{}`, or `[]` | `<ctrl-shift-m>` |
| Undo / redo a selection change | `<meta-u>` / `<shift-meta-u>` |
| Clear selection | `<esc>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Select all | `<ctrl-a>` |
| Select current line; repeat to extend | `<ctrl-shift-l>` |
| Select next occurrence | `<ctrl-d>` |
| Select previous occurrence | `<ctrl-shift-d>` |
| Select indentation level | `<ctrl-shift-i>` |
| Expand semantic selection | `<ctrl-w>` |
| Shrink semantic selection | `<ctrl-shift-w>` |
| Shrink / expand semantic selection | `<ctrl-shift-left>` / `<ctrl-shift-right>` |
| Select enclosing `()`, `{}`, or `[]` | `<ctrl-shift-m>` |
| Undo / redo a selection change | `<ctrl-u>` / `<ctrl-shift-u>` |
| Clear selection | `<esc>` |

</Platform>

Semantic selection depends on language support for the current buffer.

## Cursor movement

### Common movement

| Key | Movement |
| --- | --- |
| `<left>` / `<right>` | one character |
| `<up>` / `<down>` | one line |
| `<home>` / `<end>` | start / end of line |
| `<pgup>` / `<pgdn>` | one viewport up / down |

### Platform movement

<Platform when="darwin">

| Movement | Key |
| --- | --- |
| Previous word start | `<alt-left>` |
| Next word end | `<alt-right>` |
| Previous / next paragraph | `<ctrl-up>` / `<ctrl-down>` |
| Start of line | `<meta-left>` |
| End of line | `<meta-right>` |
| Start of document | `<meta-up>` |
| End of document | `<meta-down>` |
| Jump to matching delimiter | `<ctrl-m>` |

</Platform>
<Platform when="linux">

| Movement | Key |
| --- | --- |
| Previous word start | `<ctrl-left>` |
| Next word end | `<ctrl-right>` |
| Previous / next paragraph | `<ctrl-up>` / `<ctrl-down>` |
| Start of line | `<home>` |
| End of line | `<end>` |
| Start of document | `<ctrl-home>` |
| End of document | `<ctrl-end>` |
| Jump to matching delimiter | `<ctrl-m>` |

</Platform>

<Platform when="darwin">

`<meta-left>` always means the actual start of the line. Rune keeps the
Meta-arrow combinations available to the editor rather than using them for
window focus or movement.

</Platform>

### Scrolling

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Move one viewport | `<pgup>` / `<pgdn>` |
| Select one viewport | `<shift-pgup>` / `<shift-pgdn>` |
| Center the current line | `<ctrl-l>` |
| Scroll one line without moving the caret | `<ctrl-alt-up>` / `<ctrl-alt-down>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Move one viewport | `<pgup>` / `<pgdn>` |
| Select one viewport | `<shift-pgup>` / `<shift-pgdn>` |
| Center the current line | `<ctrl-l>` |
| Scroll one line without moving the caret | `<alt-pgup>` / `<alt-pgdn>` |

</Platform>

## Editing

### Typing and deletion

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Insert text | any printable key |
| Newline | `<enter>` |
| Indent / outdent | `<tab>` / `<shift-tab>` |
| Delete left / right | `<backspace>` / `<delete>` |
| Delete previous word | `<alt-backspace>` or `<ctrl-backspace>` |
| Delete next word | `<alt-delete>` or `<ctrl-delete>` |
| Delete to start of line | `<meta-backspace>` |
| Delete to end of line | `<meta-delete>` |
| Cut to end of line | `<ctrl-k>` |
| Delete current line | `<shift-meta-k>` |
| Transpose characters around the caret | `<ctrl-t>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Insert text | any printable key |
| Newline | `<enter>` |
| Indent / outdent | `<tab>` / `<shift-tab>` |
| Delete left / right | `<backspace>` / `<delete>` |
| Delete previous word | `<ctrl-backspace>` or `<alt-backspace>` |
| Delete next word | `<ctrl-delete>` or `<alt-delete>` |
| Delete to start of line | `<ctrl-shift-backspace>` |
| Delete to end of line | `<ctrl-shift-delete>` |
| Cut to end of line | `<ctrl-k><ctrl-k>` |
| Delete current line | `<ctrl-shift-k>` |
| Transpose characters around the caret | `<ctrl-t>` |

</Platform>

Word deletion follows the caret direction: Backspace deletes the word to the
left, and Delete removes the word to the right. At the end of a line,
cutting to the end of the line cuts the newline and joins the following line.
<Platform when="linux">`<ctrl-k>` starts the [`<ctrl-k>` prefix](#legacy-prefix), so
cutting to the end of the line takes it twice.</Platform>

### Clipboard

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Copy | `<meta-c>` or `<ctrl-c>` |
| Cut | `<meta-x>` or `<ctrl-x>` |
| Paste | `<meta-v>` or `<ctrl-v>` |
| Paste and reindent | `<shift-meta-v>` or `<ctrl-shift-v>` |
| Paste from clipboard history | `<alt-meta-v>` or `<meta-k><meta-v>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Copy | `<ctrl-c>` |
| Cut | `<ctrl-x>` |
| Paste | `<ctrl-v>` |
| Paste and reindent | `<ctrl-shift-v>` |
| Paste from clipboard history | `<ctrl-k><ctrl-v>` |

</Platform>

Copy and cut use the current line when there is no selection. Copy leaves the
buffer unchanged; cut removes the line. A later paste preserves the line-wise
clipboard behavior. After pasting, repeat paste from clipboard history to
replace that paste with progressively older clipboard-history entries.

### Undo and redo

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Undo | `<meta-z>` or `<ctrl-z>` |
| Redo | `<shift-meta-z>`, `<meta-y>`, `<ctrl-shift-z>`, or `<ctrl-y>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Undo | `<ctrl-z>` |
| Redo | `<ctrl-shift-z>` or `<ctrl-y>` |

</Platform>

### Line operations

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Insert line below | `<ctrl-enter>` |
| Insert line above | `<ctrl-shift-enter>` |
| Move line up / down | `<alt-up>` / `<alt-down>` |
| Duplicate line up / down | `<alt-shift-up>` / `<alt-shift-down>` |
| Duplicate line below | `<shift-meta-d>` |
| Join with next line | `<meta-j>` or `<ctrl-shift-j>` |
| Indent line | `<meta-]>` or `<ctrl-]>` |
| Outdent line | `<meta-[>` or `<ctrl-[>` |
| Toggle line comment | `<meta-/>` or `<ctrl-/>` |
| Toggle block comment | `<alt-meta-/>` |
| Reflow paragraph at the ruler | `<alt-meta-q>` |
| Toggle soft wrap | `<alt-z>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Insert line below | `<ctrl-enter>` |
| Insert line above | `<ctrl-shift-enter>` |
| Move line up / down | `<alt-up>` / `<alt-down>` |
| Duplicate line up / down | `<alt-shift-up>` / `<alt-shift-down>` |
| Duplicate line below | `<alt-shift-down>` |
| Join with next line | `<ctrl-shift-j>` |
| Indent line | `<ctrl-]>` |
| Outdent line | `<ctrl-[>` |
| Toggle line comment | `<ctrl-/>` |
| Toggle block comment | `<ctrl-shift-/>` |
| Reflow paragraph at the ruler | `<ctrl-shift-g>` |
| Toggle soft wrap | `<alt-z>` |

</Platform>

## Search

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Find in the current buffer | `<meta-f>` or `<ctrl-f>` |
| Open replace | `<meta-r>` |
| Upgrade an open find to replace | `<meta-r>` |
| Next match while open | `<enter>`, `<meta-f>`, or `<ctrl-f>` |
| Cycle between find and replacement inputs | `<tab>` / `<shift-tab>` |
| Close and leave the active match selected | `<esc>` |
| Next / previous match after closing | `<ctrl-->` / `<ctrl-shift-->` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Find in the current buffer | `<ctrl-f>` |
| Open replace | `<ctrl-h>` |
| Upgrade an open find to replace | `<ctrl-h>` |
| Next match while open | `<enter>` or `<ctrl-f>` |
| Cycle between find and replacement inputs | `<tab>` / `<shift-tab>` |
| Close and leave the active match selected | `<esc>` |
| Next / previous match after closing | `<ctrl-->` / `<ctrl-shift-->` |

</Platform>

Find opens a floating widget and selects the active match in the buffer as you
type. In replace mode, **Replace** changes the active occurrence and advances
to the next one, while **All** replaces every occurrence. These buttons are
mouse-only; `<enter>` always advances to the next match. The input fields use
Standard editing behavior, including drag selection and double-click word
selection. Search is case-sensitive, does not match across line boundaries, and
wraps from the last match to the first. Rune starts from the caret and retains
the last query for the next search. Closing an empty search restores the caret
to its starting position instead of leaving a match selected.

### Search configuration

Configure the widget under `editor.standard.search`. `find_key` and
`replace_key` open find and replace; the example shows their defaults:

<Platform when="darwin">

```yaml tab
editor:
  standard:
    search:
      find_key: "<meta-f>"
      replace_key: "<meta-r>"
```

```python tab
config["editor"]["standard"]["search"]["find_key"] = "<meta-f>"
config["editor"]["standard"]["search"]["replace_key"] = "<meta-r>"
```

</Platform>
<Platform when="linux">

```yaml tab
editor:
  standard:
    search:
      find_key: "<ctrl-f>"
      replace_key: "<ctrl-h>"
```

```python tab
config["editor"]["standard"]["search"]["find_key"] = "<ctrl-f>"
config["editor"]["standard"]["search"]["replace_key"] = "<ctrl-h>"
```

</Platform>

The remaining keys style the widget; the example shows the default visual
attributes:

```yaml tab
editor:
  standard:
    search:
      attr: { fg: default, bg: default }
      input_attr: { fg: default, bg: default }
      placeholder_attr: { fg: gray, bg: default }
      frame_attr: { fg: gray, bg: default }
      focus_frame_attr: { fg: silver, bg: default }
      button_attr: { fg: default, bg: gray }
      button_hover_attr: { fg: default, bg: blue }
      match_attr: { fg: grey, bg: yellow }
      current_match_attr: { fg: default, bg: default }
```

```python tab
config["editor"]["standard"]["search"].update({
    "attr": attr(fg = "default", bg = "default"),
    "input_attr": attr(fg = "default", bg = "default"),
    "placeholder_attr": attr(fg = "gray", bg = "default"),
    "frame_attr": attr(fg = "gray", bg = "default"),
    "focus_frame_attr": attr(fg = "silver", bg = "default"),
    "button_attr": attr(fg = "default", bg = "gray"),
    "button_hover_attr": attr(fg = "default", bg = "blue"),
    "match_attr": attr(fg = "grey", bg = "yellow"),
    "current_match_attr": attr(fg = "default", bg = "default"),
})
```

`attr` styles the widget background. The input, placeholder, frame, focused
frame, button, hovered button, inactive match, and active match can each be
styled independently with their corresponding keys. Attribute colors accept
Rune's named colors and hex values. The optional `status_attr` styles the
legacy status-bar Find prompt used only if Rune cannot open the floating Find
window; it is unset by default.

## Language intelligence, diagnostics, and Git changes

<Platform when="darwin">

In-file navigation uses a `<ctrl-meta>` IJKL layer. The vertical `i` / `k`
pair visits Git changes, while the horizontal `j` / `l` pair visits
diagnostics:

</Platform>
<Platform when="linux">

The vertical `i` / `k` pair on `<ctrl-alt>` visits Git changes. Diagnostics sit
on `<f8>`, as in VS Code, since several desktops lock the screen on
`<ctrl-alt-l>`:

</Platform>

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Previous Git change | `<ctrl-meta-i>` |
| Next Git change | `<ctrl-meta-k>` |
| Previous diagnostic | `<ctrl-meta-j>` |
| Next diagnostic | `<ctrl-meta-l>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Previous Git change | `<ctrl-alt-i>` |
| Next Git change | `<ctrl-alt-k>` |
| Previous diagnostic | `<shift-f8>` |
| Next diagnostic | `<f8>` |

</Platform>

Everything else the language server offers sits on a plain `<alt>` letter.
Each binding acts on the symbol under the caret; adding `<shift>` opens the
Command Prompt with that command ready for a symbol name instead:

| Action | Key | By name |
| --- | --- | --- |
| Go to definition | `<alt-d>` | `<alt-shift-d>` |
| Find references | `<alt-r>` | `<alt-shift-r>` |
| Find implementations | `<alt-p>` | `<alt-shift-p>` |
| Show hover and type information | `<alt-t>` | `<alt-shift-t>` |
| Go to declaration | `<alt-c>` | `<alt-shift-c>` |
| Go to type definition | `<alt-y>` | `<alt-shift-y>` |
| Show signature help | `<alt-g>` | |
| Format the buffer | `<alt-b>` | |
| List diagnostics | `<alt-e>` | |
| Complete at the caret | `<ctrl-space>` | |

Three more `<alt>` letters jump to a symbol declared in the current file by
name, using the language's tree-sitter `locals.scm` queries rather than the
language server:

| Action | Key |
| --- | --- |
| Jump to a function or method | `<alt-f>` |
| Jump to a type | `<alt-s>` |
| Jump to a variable | `<alt-x>` |

Symbol rename is available through the
[Command Prompt](./command-prompt.md) as `lsp rename`.

## Folds

<Platform when="darwin">

| Key | Action |
| --- | --- |
| `<ctrl-{>` | collapse the fold at the caret |
| `<ctrl-}>` | expand the fold at the caret |
| `<ctrl-shift-a>` | toggle all folds |
| `<alt-meta-[>` / `<alt-meta-]>` | collapse / expand the fold at the caret |
| `<meta-k><meta-j>` | expand all folds |
| `<meta-k><meta-1>` | collapse all folds |

</Platform>
<Platform when="linux">

| Key | Action |
| --- | --- |
| `<ctrl-{>` | collapse the fold at the caret |
| `<ctrl-}>` | expand the fold at the caret |
| `<ctrl-shift-a>` | toggle all folds |
| `<ctrl-k><ctrl-j>` | expand all folds |
| `<ctrl-k><ctrl-1>` | collapse all folds |

</Platform>

## Hidden lines

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Hide the lines covered by the selection | `<ctrl-alt-h>` |
| Reveal the hidden block at the caret | `<ctrl-alt-v>` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Hide the lines covered by the selection | `<ctrl-shift-h>` |
| Reveal the hidden block at the caret | `<ctrl-shift-r>` |

</Platform>

## Macros

| Key | Action |
| --- | --- |
| `<ctrl-q>` | start or stop recording to the unnamed register |
| `<ctrl-shift-q>` | play the recorded macro |

## Legacy prefix

<Platform when="darwin">

The standard editor retains a one-shot `<meta-k>` compatibility layer for a
small set of advanced operations:

| Sequence | Action |
| --- | --- |
| `<meta-k><meta-space>` | set mark at the caret |
| `<meta-k><meta-a>` | select from mark to caret |
| `<meta-k><meta-w>` | delete from mark to caret |
| `<meta-k><meta-x>` | swap caret and mark |
| `<meta-k><meta-g>` | clear mark |
| `<meta-k><meta-u>` | uppercase selection |
| `<meta-k><meta-l>` | lowercase selection |
| `<meta-k><meta-k>` | delete to end of line |
| `<meta-k><meta-backspace>` | delete to start of line |
| `<meta-k><meta-j>` | expand all folds |
| `<meta-k><meta-1>` | collapse all folds |
| `<meta-k><meta-v>` | paste from clipboard history |

This layer is optional compatibility, not the primary standard keymap.

</Platform>
<Platform when="linux">

The standard editor retains a one-shot `<ctrl-k>` compatibility layer for a
small set of advanced operations. The second key also takes `<ctrl>`, as in
VS Code's `Ctrl+K` chords:

| Sequence | Action |
| --- | --- |
| `<ctrl-k><ctrl-space>` | set mark at the caret |
| `<ctrl-k><ctrl-a>` | select from mark to caret |
| `<ctrl-k><ctrl-w>` | delete from mark to caret |
| `<ctrl-k><ctrl-x>` | swap caret and mark |
| `<ctrl-k><ctrl-g>` | clear mark |
| `<ctrl-k><ctrl-u>` | uppercase selection |
| `<ctrl-k><ctrl-l>` | lowercase selection |
| `<ctrl-k><ctrl-k>` | cut to end of line |
| `<ctrl-k><ctrl-backspace>` | delete to start of line |
| `<ctrl-k><ctrl-j>` | expand all folds |
| `<ctrl-k><ctrl-1>` | collapse all folds |
| `<ctrl-k><ctrl-v>` | paste from clipboard history |

This layer is optional compatibility, not the primary standard keymap. Normal
cut remains `<ctrl-x>`.

</Platform>

## Auto-pair and paste

When `auto_pair` is enabled, typing `(`, `[`, `{`, `"`, or `'` inserts the
matching close. Typing a supported closer immediately before the same existing
character moves over it instead of inserting a duplicate. `<backspace>` inside
a supported empty pair removes both characters. `<enter>` between `{}` splits
the braces across a blank line; other pairs receive a normal indented newline.

When no selection is active, bracketed terminal paste is inserted verbatim as
one text run. It does not add auto-paired closers or rewrite indentation.

## Command prompt and configuration

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Open the command prompt | `<shift-meta-p>` |
| Open Rune's configuration | `<meta-,>` |
| Command history | `<shift-meta-r>` |
| Quit | `<meta-q>` |
| Font size bigger / smaller | `<meta-=>` / `<meta-->` |

</Platform>
<Platform when="linux">

| Action | Key |
| --- | --- |
| Open the command prompt | `<ctrl-shift-p>` |
| Open Rune's configuration | `<ctrl-,>` |
| Command history | `<ctrl-alt-r>` |
| Quit | `<ctrl-alt-q>` |
| Font size bigger / smaller | `<ctrl-alt-=>` / `<ctrl-alt-->` |

</Platform>

See [Command Prompt](./command-prompt.md) for commands, aliases, completion,
and custom bindings.

## What's not here

The standard editor does not currently implement:

- Multi-cursor editing.
- An Emacs `<ctrl-x>` prefix layer.
- An editor-owned completion popup. Completion lives in language extensions;
  the Standard preset invokes it with `<ctrl-space>` when available.
- A dedicated goto-line key. Use the [Command Prompt](./command-prompt.md).

For complete Vim, Neovim, Helix, Emacs, or another editor's behavior, use
[Exoeditor](./exoeditor.md).
