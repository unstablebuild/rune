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

The preset adapts application shortcuts to the operating system. Pick macOS or
Linux with the platform switch at the top of the page; every key on this page
follows that choice and uses Rune's [key syntax](./key-syntax.md).

<Platform when="darwin">

`<meta>` is Command and `<alt>` is Option. Editing keys use `<meta>`, with
`<ctrl>` twins for the common ones, and Rune's own commands sit on `<alt>`.

</Platform>

<Platform when="linux">

Editing keys use `<ctrl>`, as in Sublime Text and VS Code for Linux, and every
Rune command sits on `<meta>`. The editor handles no `<meta>` key, so Rune's
commands work from any buffer. First-run setup asks whether `<meta>` should be
Super or Alt. Change it later with
[`gui.meta_key`](./key-syntax.md#what-meta-means). With Alt, the editor's own
Alt keys, such as `<alt-left>`, still come first.

</Platform>

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

![Rune Standard editor preset default keybindings](https://assets.rune.build/images/standard-editor-keyboard-cheatsheet-v6.svg)

The preset uses a single layer,
<Platform when="darwin">`<alt>`</Platform><Platform when="linux">`<meta>`</Platform>,
so a binding is guessable from the key alone:

- The layer plus `ijkl` points up, left, down, and right.
- The layer plus a layout letter acts on a window or a tab.
- The layer plus a code letter asks the language server about the caret.
- Adding `<shift>` turns focus into movement, or asks for a symbol by name.

The tables below provide a copyable reference for the preset bindings:

| Action | Up / left / down / right |
| --- | --- |
| Focus a window | <KeyBinding command="windowfocus up" /> / <KeyBinding command="windowfocus left" /> / <KeyBinding command="windowfocus down" /> / <KeyBinding command="windowfocus right" /> |
| Move a window | <KeyBinding command="windowmove up" /> / <KeyBinding command="windowmove left" /> / <KeyBinding command="windowmove down" /> / <KeyBinding command="windowmove right" /> |

<Platform when="darwin">

Adding `<meta>` to the `ijkl` cluster resizes the window instead:
<KeyBinding command="windowresize increase height" /> makes it taller,
<KeyBinding command="windowresize decrease width" /> narrower,
<KeyBinding command="windowresize decrease height" /> shorter, and
<KeyBinding command="windowresize increase width" /> wider.

</Platform>

<Platform when="linux">

Adding `<ctrl-shift>` to the `ijkl` cluster resizes the window instead:
<KeyBinding command="windowresize increase height" /> makes it taller,
<KeyBinding command="windowresize decrease width" /> narrower,
<KeyBinding command="windowresize decrease height" /> shorter, and
<KeyBinding command="windowresize increase width" /> wider. Resize avoids
`<alt-meta>`, which becomes plain `<meta>` when `<meta>` is Alt, and `<meta>`
arrows, which desktops take under Super and the editor takes under Alt.

</Platform>

<KeyBinding command="windowresize reset" /> restores the default size, while
<KeyBinding command={["windowresize max width", "windowresize max height"]} />
and <KeyBinding command={["windowresize min width", "windowresize min height"]} />
maximize and minimize the focused window.

Common window actions stay on the same layer:

| Action | Key |
| --- | --- |
| Split the focused window | <KeyBinding command="windownew" /> |
| Open a terminal in place or in a new split | <KeyBinding command="terminalneworsplit" /> |
| Close the focused window | <KeyBinding command="windowclose" /> |
| Close every window | <KeyBinding command="windowcloseall" /> |
| Toggle maximization of the focused window | <KeyBinding command="windowtogglemaximize" /> |
| Set horizontal / vertical split orientation | <KeyBinding command="windowdefaultsplit h" /> / <KeyBinding command="windowdefaultsplit v" /> |
| Prefill `windowconverttab` in the command prompt | <KeyBinding command="echo {prompt}windowconverttab<space>" /> |

Tabs use brackets for left and right. Add `<shift>` to reorder the current
tab:

| Action | Key |
| --- | --- |
| New tab | <KeyBinding command="tabnew" /> |
| Close tab | <KeyBinding command="tabclose" /> |
| Previous / next tab | <KeyBinding command="tabprevious" /> / <KeyBinding command="tabnext" /> |
| Move tab left / right | <KeyBinding command="tabmove left" /> / <KeyBinding command="tabmove right" /> |
| Search open tabs | <KeyBinding command="tabsearch" /> |

<Platform when="darwin">

<KeyBinding command="tabfocus 1" /> … <KeyBinding command="tabfocus 9" />
focus tabs 1 to 9, and <KeyBinding command="tabmove 1" /> …
<KeyBinding command="tabmove 9" /> move the current tab to that position.

</Platform>

<KeyBinding command="workspacefocus 1" /> … <KeyBinding command="workspacefocus 9" />
focus workspaces 1 to 9, and <KeyBinding command="workspacemove 1" /> …
<KeyBinding command="workspacemove 9" /> reorder the current workspace. See
[Layout Management](./layout-management.md) for the complete layout system.

Common navigation commands avoid keys owned by the editor:

| Action | Key |
| --- | --- |
| Toggle the file explorer | <KeyBinding command="fexplorer" /> |
| Previous / next cursor position | <KeyBinding command="cursorhistory prev" /> / <KeyBinding command="cursorhistory next" /> |
| Open the cheatsheet | <KeyBinding command="cheatsheet" /> |

Code-intelligence commands share the same layer and are listed under
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

Line-start movements include leading whitespace. For example,
`<shift-meta-left>` selects all the way to column zero, not only to the first
non-blank character.

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

Line-start movements include leading whitespace. For example, `<shift-home>`
selects all the way to column zero, not only to the first non-blank character.

</Platform>

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
| Select current line; repeat to extend | `<ctrl-l>` |
| Select next occurrence | `<ctrl-d>` |
| Select previous occurrence | `<ctrl-alt-d>` |
| Select indentation level | `<ctrl-shift-j>` |
| Expand semantic selection | `<ctrl-w>` or `<ctrl-shift-space>` |
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

`<meta-left>` always means the actual start of the line. Rune keeps the
Meta-arrow combinations available to the editor rather than using them for
window focus or movement.

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

### Scrolling

| Action | Key |
| --- | --- |
| Move one viewport | `<pgup>` / `<pgdn>` |
| Select one viewport | `<shift-pgup>` / `<shift-pgdn>` |
| Center the current line | <Platform when="darwin">`<ctrl-l>`</Platform><Platform when="linux">`<ctrl-k><ctrl-c>`</Platform> |
| Scroll one line without moving the caret | `<ctrl-alt-up>` / `<ctrl-alt-down>` |

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

Word deletion follows the caret direction: Backspace deletes the word to the
left, and Delete removes the word to the right. At the end of a line,
`<ctrl-k>` cuts the newline and joins the following line.

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
| Delete current line | `<ctrl-shift-k>` |
| Transpose characters around the caret | `<ctrl-t>` |

Word deletion follows the caret direction: Backspace deletes the word to the
left, and Delete removes the word to the right. `<ctrl-k>` starts a
[chord](#the-meta-k--ctrl-k-prefix).

</Platform>

### Clipboard

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Copy | <KeyBinding command="clipboardcopy" chord="<meta-c>" /> or `<ctrl-c>` |
| Cut | `<meta-x>` or `<ctrl-x>` |
| Paste | <KeyBinding command="clipboardpaste" chord="<meta-v>" /> or `<ctrl-v>` |
| Paste and reindent | `<shift-meta-v>` or `<ctrl-shift-v>` |
| Paste from clipboard history | `<alt-meta-v>` |

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
clipboard behavior. After pasting, repeat the clipboard-history key to replace
that paste with progressively older clipboard-history entries.

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
| Duplicate line below | `<ctrl-shift-d>` |
| Join with next line | `<ctrl-j>` |
| Indent line | `<ctrl-]>` |
| Outdent line | `<ctrl-[>` |
| Toggle line comment | `<ctrl-/>` |
| Toggle block comment | `<ctrl-shift-/>` |
| Reflow paragraph at the ruler | `<ctrl-k><ctrl-q>` |
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
| Open replace | `<ctrl-r>` |
| Upgrade an open find to replace | `<ctrl-r>` |
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

Configure the widget under `editor.standard.search`. The examples show the
default keys and visual attributes; the Linux preset sets `find_key` to
`<ctrl-f>` and `replace_key` to `<ctrl-r>`:

```yaml tab
editor:
  standard:
    search:
      find_key: "<meta-f>"
      replace_key: "<meta-r>"
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
config["editor"]["standard"]["search"] = {
    "find_key": "<meta-f>",
    "replace_key": "<meta-r>",
    "attr": attr(fg = "default", bg = "default"),
    "input_attr": attr(fg = "default", bg = "default"),
    "placeholder_attr": attr(fg = "gray", bg = "default"),
    "frame_attr": attr(fg = "gray", bg = "default"),
    "focus_frame_attr": attr(fg = "silver", bg = "default"),
    "button_attr": attr(fg = "default", bg = "gray"),
    "button_hover_attr": attr(fg = "default", bg = "blue"),
    "match_attr": attr(fg = "grey", bg = "yellow"),
    "current_match_attr": attr(fg = "default", bg = "default"),
}
```

`attr` styles the widget background. The input, placeholder, frame, focused
frame, button, hovered button, inactive match, and active match can each be
styled independently with their corresponding keys. Attribute colors accept
Rune's named colors and hex values. The optional `status_attr` styles the
legacy status-bar Find prompt used only if Rune cannot open the floating Find
window; it is unset by default.

## Language intelligence, diagnostics, and Git changes

<Platform when="darwin">

In-file navigation steps through Git changes and diagnostics on a
`<ctrl-meta>` IJKL layer: the vertical `i` / `k` pair visits Git changes,
while the horizontal `j` / `l` pair visits diagnostics:

</Platform>

<Platform when="linux">

In-file navigation steps through Git changes and diagnostics. Period and comma
step forward and back through diagnostics, and with `<shift>` through Git
changes:

</Platform>

| Action | Key |
| --- | --- |
| Previous Git change | <KeyBinding command="gitprevchange" /> |
| Next Git change | <KeyBinding command="gitnextchange" /> |
| Previous diagnostic | <KeyBinding command="lspprevdiagnostic" /> |
| Next diagnostic | <KeyBinding command="lspnextdiagnostic" /> |

Everything else the language server offers sits on a plain letter of the
layer. Each binding acts on the symbol under the caret; adding `<shift>` opens
the Command Prompt with that command ready for a symbol name instead:

| Action | Key | By name |
| --- | --- | --- |
| Go to definition | <KeyBinding command="lsp definition" /> | <KeyBinding command="echo {prompt}lsp<space>definition<space>" /> |
| Find references | <KeyBinding command="lsp references" /> | <KeyBinding command="echo {prompt}lsp<space>references<space>" /> |
| Find implementations | <KeyBinding command="lsp implementation" /> | <KeyBinding command="echo {prompt}lsp<space>implementation<space>" /> |
| Show hover and type information | <KeyBinding command="lsp hover" /> | <KeyBinding command="echo {prompt}lsp<space>hover<space>" /> |
| Go to declaration | <KeyBinding command="lsp declaration" /> | <KeyBinding command="echo {prompt}lsp<space>declaration<space>" /> |
| Go to type definition | <KeyBinding command="lsp type-definition" /> | <KeyBinding command="echo {prompt}lsp<space>type-definition<space>" /> |
| Show signature help | <KeyBinding command="lsp signature-help" /> | |
| Format the buffer | <KeyBinding command="lsp format" /> | |
| List diagnostics | <KeyBinding command="lsp diagnostics" /> | |
| Complete at the caret | <KeyBinding command="lsp complete" /> | |

Three more keys jump to a symbol declared in the current file by name, using
the language's tree-sitter `locals.scm` queries rather than the language
server:

| Action | Key |
| --- | --- |
| Jump to a function or method | <KeyBinding command={'echo {prompt}jumptoast<space>locals.scm<space>local.definition.method' + String.fromCharCode(124) + 'local.definition.function<space>'} /> |
| Jump to a type | <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.type<space>" /> |
| Jump to a variable | <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>" /> |

Symbol rename is available through the
[Command Prompt](./command-prompt.md) as `lsp rename`.

## Folds

<Platform when="darwin">

| Action | Key |
| --- | --- |
| Collapse / expand the fold at the caret | `<ctrl-{>` / `<ctrl-}>` or `<alt-meta-[>` / `<alt-meta-]>` |
| Toggle all folds | `<ctrl-shift-a>` |
| Expand all folds | `<meta-k><meta-j>` |
| Collapse all folds | `<meta-k><meta-1>` |

</Platform>

<Platform when="linux">

| Action | Key |
| --- | --- |
| Collapse / expand the fold at the caret | `<ctrl-{>` / `<ctrl-}>` |
| Toggle all folds | `<ctrl-shift-a>` |
| Expand all folds | `<ctrl-k><ctrl-j>` |
| Collapse all folds | `<ctrl-k><ctrl-1>` |

</Platform>

## Hidden lines

| Key | Action |
| --- | --- |
| `<ctrl-alt-h>` | hide the lines covered by the selection |
| `<ctrl-alt-v>` | reveal the hidden block at the caret |

## Macros

| Key | Action |
| --- | --- |
| `<ctrl-q>` | start or stop recording to the unnamed register |
| `<ctrl-shift-q>` | play the recorded macro |

## The `<meta-k>` / `<ctrl-k>` prefix

<Platform when="darwin">

A one-shot prefix, `<meta-k>`, reaches a small set of advanced operations.
Hold the same modifier for the second key:

| Action | Key |
| --- | --- |
| Set mark at the caret | `<meta-k><meta-space>` |
| Select from mark to caret | `<meta-k><meta-a>` |
| Delete from mark to caret | `<meta-k><meta-w>` |
| Swap caret and mark | `<meta-k><meta-x>` |
| Clear mark | `<meta-k><meta-g>` |
| Uppercase selection | `<meta-k><meta-u>` |
| Lowercase selection | `<meta-k><meta-l>` |
| Delete to end of line | `<meta-k><meta-k>` |
| Delete to start of line | `<meta-k><meta-backspace>` |
| Expand all folds | `<meta-k><meta-j>` |
| Collapse all folds | `<meta-k><meta-1>` |

</Platform>

<Platform when="linux">

A one-shot prefix, `<ctrl-k>`, reaches a small set of advanced operations.
Hold the same modifier for the second key:

| Action | Key |
| --- | --- |
| Set mark at the caret | `<ctrl-k><ctrl-space>` |
| Select from mark to caret | `<ctrl-k><ctrl-a>` |
| Delete from mark to caret | `<ctrl-k><ctrl-w>` |
| Swap caret and mark | `<ctrl-k><ctrl-x>` |
| Clear mark | `<ctrl-k><ctrl-g>` |
| Uppercase selection | `<ctrl-k><ctrl-u>` |
| Lowercase selection | `<ctrl-k><ctrl-l>` |
| Delete to end of line | `<ctrl-k><ctrl-k>` |
| Delete to start of line | `<ctrl-k><ctrl-backspace>` |
| Expand all folds | `<ctrl-k><ctrl-j>` |
| Collapse all folds | `<ctrl-k><ctrl-1>` |
| Center the current line | `<ctrl-k><ctrl-c>` |
| Paste from clipboard history | `<ctrl-k><ctrl-v>` |
| Reflow paragraph at the ruler | `<ctrl-k><ctrl-q>` |

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

| Action | Key |
| --- | --- |
| Open the command prompt | <CommandPromptKey /> |
| Open Rune's configuration | <Platform when="darwin"><KeyBinding command="config" /></Platform><Platform when="linux">unbound; run `config`</Platform> |

See [Command Prompt](./command-prompt.md) for commands, aliases, completion,
and custom bindings.

## What's not here

The standard editor does not currently implement:

- Multi-cursor editing.
- An Emacs `<ctrl-x>` prefix layer.
- An editor-owned completion popup. Completion lives in language extensions;
  the Standard preset invokes it with
  <KeyBinding command="lsp complete" chord="<ctrl-space>" /> when available.
- A dedicated goto-line key. Use the [Command Prompt](./command-prompt.md).

For complete Vim, Neovim, Helix, Emacs, or another editor's behavior, use
[Exoeditor](./exoeditor.md).
