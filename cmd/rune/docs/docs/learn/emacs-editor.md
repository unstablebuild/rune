---
sidebar_position: 25
---

# Emacs Editor

An Emacs-style cheatsheet for Rune's built-in Emacs editor. It combines
familiar point, mark, and kill-ring editing, incremental search, and numeric
arguments with Rune commands for files, windows, tabs, search, and language
intelligence.

## Preset

Choose **emacs** when Rune first asks which key bindings you want. For an
existing configuration, the editor itself is selected with:

```yaml tab
editor:
  mode: "emacs"
```

```python tab
config["editor"]["mode"] = "emacs"
```

The setup preset also installs `<alt-x>`, the `<ctrl-x>` command family, and
the command-layer unbindings that leave Emacs editing keys available to the
editor. Changing only `editor.mode` does not replace command bindings already
in your config. If you maintain those bindings yourself, merge changes into
the existing `command` map rather than adding a second top-level `command`
key.

See the [key syntax](./key-syntax.md) reference for the notation used on this
page.

## Alt is Emacs Meta

Rune keeps the Emacs editing layer and Rune's window-management layer on
different modifiers:

- `<alt>` is the Emacs Meta modifier. It is Option on macOS and Alt on Linux.
- `<meta>` is Rune's window and workspace modifier. It is Command on macOS and
  Super on Linux. On Linux, first-run setup lets you move it to `<ctrl-super>`
  or `<alt-super>` if your desktop keeps Super chords for itself. Change it
  later with [`gui.meta_key`](./key-syntax.md#what-meta-means). `<alt>` is not
  offered, because Alt is already Emacs Meta.

For example, Emacs `forward-word` is `<alt-f>`, while Rune window focus uses
<KeyBinding command="windowfocus left" chord="<meta-b>" /> and <KeyBinding command="windowfocus right" chord="<meta-f>" />. An Emacs control-plus-Meta command such as
`forward-sexp` is `<ctrl-alt-f>`.

## Editing-layer conventions

The editor keeps the established GNU meaning for the single-modifier `<ctrl>`
and `<alt>` letter chords it implements. Rune adds convenient modified-arrow
bindings, plus commands on combinations such as `<ctrl-shift>`. There are no
CUA aliases: `<ctrl-z>` is not undo and `<ctrl-c>` is not copy, because GNU
reserves them for `suspend-frame` and the major-mode prefix map.

Rune's own editor features mostly live on `<ctrl-shift>` combinations and
modified arrow keys. Rune's window, workspace, and system-clipboard commands
live on `<meta>`, which is a separate layer and not an editing layer.

## Where Emacs editing applies

The same built-in editor powers file buffers and Rune-owned editable surfaces
that select Emacs mode, including the command prompt's edit mode. Read-only
views keep their own navigation, and the Emacs preset leaves terminals
modeless, so a program running in a terminal receives its own input and owns
its own editing behavior.

## Remapping keys

Most Emacs editing and point-motion keys on this page are built into the
editor itself, not Rune commands, so they cannot be rebound through the
`command` map. To swap the physical keys that trigger them, use the GUI's
[`gui.key_mapping`](./key-mapping.md) property, which rewrites one key
combination into another before it reaches the editor.

## Message bar appearance

The Emacs editor uses an overlaid message bar as its echo area. Search and
prompt text appears there, while transient states such as `ISEARCH`, `QUERY`,
`GOTO`, `ZAP`, `ARG`, `QUOTE`, and `MACRO` appear in the status field when the
status bar is visible.

Configure the base attributes and echo-area layout under
`editor.emacs.message_bar`:

```yaml tab
editor:
  emacs:
    message_bar:
      attr: {fg: default, bg: gray}
      layout: '░▒▓█ {{ .Message | fg "white" }} '
```

```python tab
config["editor"]["emacs"]["message_bar"] = {
    "attr": attr(fg = "default", bg = "gray"),
    "layout": '░▒▓█ {{ .Message | fg "white" }} ',
}
```

`attr` supplies the base attributes for the echo area. The status bar's own
layout controls the transient-state label. `layout` controls only the
echo-area message. It is a Go template with exactly one `.Message` component
and supports the `bg`, `fg`, `bold`, `italic`, `underline`, `strikethrough`,
`reverse`, `blink`, and `dim` styling operators used by status-bar layouts.
Styling applied to `.Message` overrides the corresponding base `attr` values.

Search matches in the buffer use `editor.emacs.search_attr`:

```yaml tab
editor:
  emacs:
    search_attr: {fg: grey, bg: yellow}
```

```python tab
config["editor"]["emacs"]["search_attr"] = attr(
    fg = "grey",
    bg = "yellow",
)
```

## Emacs keybindings preset

When configuring Rune for the first time, we offer the ability to choose one of three
presets. If you choose the emacs preset, Rune keeps layout commands away from the
single-modifier `<ctrl>` and `<alt>` key combinations used by Emacs editing. Window
directions reuse Emacs's PNBF vocabulary: `p` is previous/up, `n` next/down, `b`
backward/left, and `f` forward/right. Hold `<meta>`, which means Command on macOS and
Super on Linux, with those letters to affect Rune windows instead of moving through text.

![Rune Emacs editor preset default keybindings](https://assets.rune.build/images/emacs-editor-keyboard-cheatsheet-v8.svg)

The most important defaults are:

| Key | Action |
| --- | --- |
| <KeyBinding command="windowfocus up" chord="<meta-p>" /> <KeyBinding command="windowfocus left" chord="<meta-b>" /> <KeyBinding command="windowfocus down" chord="<meta-n>" /> <KeyBinding command="windowfocus right" chord="<meta-f>" /> | focus a neighboring window up, left, down, or right |
| <KeyBinding command="windowfocus other" chord="<ctrl-x>o" /> | focus the other window, following GNU's other-window |
| <KeyBinding command="windowmove up" chord="<shift-meta-p>" /> <KeyBinding command="windowmove left" chord="<shift-meta-b>" /> <KeyBinding command="windowmove down" chord="<shift-meta-n>" /> <KeyBinding command="windowmove right" chord="<shift-meta-f>" /> | move the current window up, left, down, or right |
| <KeyBinding command="windowresize increase height" /> / <KeyBinding command="windowresize decrease width" /> / <KeyBinding command="windowresize decrease height" /> / <KeyBinding command="windowresize increase width" /> | make the window taller, narrower, shorter, or wider |
| <KeyBinding command="windownew down" chord="<meta-d>" /> | split below |
| <KeyBinding command="windownew right" chord="<meta-r>" /> | split right |
| <KeyBinding command="windowclose" chord="<meta-k>" /> | close the current window |
| <KeyBinding command="windowcloseall" chord="<shift-meta-k>" /> | keep the current window and close every other window |
| <KeyBinding command="windowtogglemaximize" chord="<meta-m>" /> | expand or restore the current window |
| <KeyBinding command="windowdefaultsplit h" /> | make below the default split direction |
| <KeyBinding command="windowdefaultsplit v" /> | make right the default split direction |
| <KeyBinding command="tabnext" /> / <KeyBinding command="tabprevious" /> | next or previous tab |
| <KeyBinding command="tabclose" chord="<meta-w>" /> | close the current tab |
| <KeyBinding command="tabmove right" chord="<shift-meta-]>" /> <KeyBinding command="tabmove left" chord="<shift-meta-[>" /> | move the current tab right or left |
| <KeyBinding command="terminalneworsplit" chord="<meta-enter>" /> | open a terminal, or split when the current window already contains tabs or a terminal |
| ``<meta-`>`` | search workspaces |
| <KeyBinding command="workspacefocus 1" chord="<meta-1>" /> through <KeyBinding command="workspacefocus 9" chord="<meta-9>" /> | focus workspace 1 through 9 |
| <KeyBinding command="workspacemove 1" chord="<shift-meta-1>" /> through <KeyBinding command="workspacemove 9" chord="<shift-meta-9>" /> | swap the current workspace with an existing position 1 through 9 |

GNU's <KeyBinding command="windowclose" chord="<ctrl-x>0" />, <KeyBinding command="windowcloseall" chord="<ctrl-x>1" />, <KeyBinding command="windownew down" chord="<ctrl-x>2" />, <KeyBinding command="windownew right" chord="<ctrl-x>3" />, and <KeyBinding command="windowfocus other" chord="<ctrl-x>o" /> are
bound to the same window commands, but a focused terminal sends `<ctrl-x>` to
the program running inside it, so the `<meta>` layer covers all of them too:
the PNBF directions reach the same windows <KeyBinding command="windowfocus other" chord="<ctrl-x>o" /> toggles between. The
`<meta>` spelling is the one that works from editors, terminals, explorers,
and every other Rune surface, and it is what the cheatsheet and the command
prompt advertise.

On the `<meta>` layer, `<shift>` has one meaning per family: it moves rather
than focuses for layout, it prompts for a name for language commands, and it
reverses direction for anything that steps through a list.

See [Layout Management](./layout-management.md) for the complete window and
workspace system.

## Point motion

### Characters and lines

| Key | Motion |
| --- | --- |
| `<left>` `<ctrl-b>` | one character left |
| `<right>` `<ctrl-f>` | one character right |
| `<up>` `<ctrl-p>` | one line up |
| `<down>` `<ctrl-n>` | one line down |
| `<home>` `<ctrl-a>` | start of line |
| `<end>` `<ctrl-e>` | end of line |
| `<alt-m>` | first nonblank character on the line |

Left and right motion stop at line boundaries. Use line motion to cross to
the preceding or following line.

### Words, buffers, and expressions

| Key | Motion |
| --- | --- |
| `<alt-b>` `<alt-left>` | previous word start |
| `<alt-f>` `<alt-right>` | current or next word end |
| `<alt-shift-,>` | first line of the buffer, preserving the column where possible |
| `<alt-shift-.>` | last line of the buffer, preserving the column where possible |
| `<ctrl-alt-f>` | forward across the next bracketed expression |
| `<ctrl-alt-b>` | backward across the preceding bracketed expression |
| `<ctrl-alt-u>` | up and out to the opening bracket of the enclosing pair |
| `<ctrl-alt-d>` | down into the next opening bracket |
| `<ctrl-alt-a>` | opening bracket of the innermost enclosing block |
| `<ctrl-alt-e>` | just after the closing bracket of the innermost enclosing block |
| `<ctrl-alt-h>` | mark the innermost enclosing block as the region |
| `<ctrl-alt-k>` | kill the next bracketed expression |

Rune's expression motions recognize balanced `()`, `[]`, and `{}`. Forward
motion and killing scan for the next opening bracket, while backward motion
scans for the preceding closing bracket. They do not treat a bare word,
string, or reader form as a complete expression.

`<alt-shift-,>` and `<alt-shift-.>` are the physical key combinations behind
GNU Emacs's beginning-of-buffer and end-of-buffer commands. Rune currently
moves vertically to the first or last line and retains the current column when
that line is long enough, rather than forcing point to the first or final
buffer position.

The definition-named GNU chords are bracket-based approximations in Rune.
`<ctrl-alt-a>`, `<ctrl-alt-e>`, and `<ctrl-alt-h>` operate on the innermost
enclosing balanced pair. They do not use language definitions or fold
information.

### Sentences and paragraphs

| Key | Motion |
| --- | --- |
| `<alt-a>` | backward to the sentence start |
| `<alt-e>` | forward to the sentence end |
| `<alt-shift-[>` | backward to the previous paragraph |
| `<alt-shift-]>` | forward to the next paragraph |

Sentence motion follows the GNU Emacs defaults: a sentence ends at `.`, `?`,
`!`, `…`, or `‽`, optionally followed by closing quotes or brackets, and then
the end of a line, a tab, or two spaces. A single space only ends a sentence
when it reaches the end of the line, so abbreviations such as `Mr. Smith` do
not break the motion. Sentences never cross a blank line except when the
current paragraph has none left.

### Pages and scrolling

| Key | Action |
| --- | --- |
| `<pgup>` | move point one viewport up |
| `<pgdn>` | move point one viewport down |
| `<ctrl-v>` | page down |
| `<alt-v>` | page up |
| `<ctrl-l>` | center the current line |
| `<ctrl-alt-up>` | scroll the view up one line |
| `<ctrl-alt-down>` | scroll the view down one line |

With a [numeric argument](#numeric-arguments), `<ctrl-v>` and `<alt-v>`
scroll that many lines instead of a page.

## Selection, mark, and region

Shift with a basic motion creates or extends an ordinary selection:

| Key | Action |
| --- | --- |
| `<shift-left>` `<shift-right>` | extend selection by one character |
| `<shift-up>` `<shift-down>` | extend selection by one line |
| `<shift-home>` `<shift-end>` | extend to the start or end of the line |
| `<shift-pgup>` `<shift-pgdn>` | extend by one viewport |
| `<esc>` | clear selection and search highlighting |

Mark and region commands use the most recently set mark:

| Key | Action |
| --- | --- |
| `<ctrl-space>` | set mark at point |
| `<alt-w>` | copy from mark to point |
| `<ctrl-w>` | kill from mark to point |
| `<ctrl-u><ctrl-space>` | jump to the most recent mark and pop it |
| <KeyBinding command="exchangepointandmark" chord="<ctrl-x><ctrl-x>" /> | exchange point and the most recent mark |
| `<ctrl-g>` | quit a pending prompt or argument; otherwise clear selection, search, and marks |

`<alt-w>` leaves point and the buffer unchanged, and a shift-selection counts
as an active region for both. `<ctrl-w>` kills the region into the clipboard;
when it uses an explicit mark, it pops that mark. Each `<ctrl-space>` pushes a
mark, and repeating `<ctrl-u><ctrl-space>` walks back through older marks, like
the GNU Emacs mark ring.

### Syntactic selection

| Key | Action |
| --- | --- |
| `<ctrl-=>` | expand selection to the next syntactic boundary |
| `<ctrl-shift-w>` | shrink syntactic selection |
| `<ctrl-shift-m>` | select enclosing `()`, `{}`, or `[]` |

Expanding and shrinking a syntactic selection depends on language support for
the current buffer. Selecting an enclosing pair is bracket-based and works
without it. These are Rune additions.

## Editing

### Typing, indentation, and undo

| Key | Action |
| --- | --- |
| any printable key | insert text; replace the active selection |
| `<space>` | insert a space |
| `<enter>` `<ctrl-m>` | insert an indented newline; honor an empty auto-pair |
| `<tab>` `<ctrl-i>` | indent selection or line; otherwise insert one indentation unit |
| `<shift-tab>` | outdent selection or line |
| `<backspace>` `<ctrl-h>` | delete backward |
| `<delete>` `<ctrl-d>` | delete at point |
| `<ctrl-/>` `<ctrl-_>` <KeyBinding command="undo prefix" chord="<ctrl-x>u" /> | undo |
| `<ctrl-shift-/>` `<ctrl-alt-/>` `<ctrl-alt-_>` | redo |
| `<ctrl-q>` | quoted insert: insert the literal form of the next character-producing key |

The [Emacs preset](#preset) enables auto-pairing. Typing `(`, `[`, `{`, `"`, or
`'` inserts its matching close. `<backspace>` between an empty pair removes
both characters, and typing a matching closer immediately before its
auto-inserted copy moves over it. `<enter>` between `{}` opens an indented blank
line; other pairs receive a normal indented newline.

Undo follows the GNU one-undo-per-command rule: a command that performs
several internal edits, such as `<alt-shift-6>`, `<alt-t>`, or a whole
query-replace session, reverts in a single step. Consecutive typed
characters group into one undo of up to twenty characters, and runs of
`<backspace>` or `<ctrl-d>` group the same way. <KeyBinding command="undo prefix" chord="<ctrl-x>u" /> is the two-key
undo alias.

To redo the GNU way, run any non-undo command, such as the `<ctrl-f>` point
motion, to end the current undo sequence, then undo again. The explicit redo
keys invoke GNU Emacs 28's `undo-redo`. `<ctrl-z>` is not undo: GNU reserves
it for `suspend-frame`.

`<ctrl-q>` is GNU `quoted-insert`. The character represented by the next key
is inserted instead of running its command, so `<ctrl-q><tab>` inserts a tab
character and `<ctrl-q><ctrl-k>` inserts a literal control code rather than
killing the line. Meta chords and named keys without a literal character do
nothing. A [numeric argument](#numeric-arguments) repeats the character, so
`<ctrl-u>3<ctrl-q><tab>` inserts three tabs in one undoable step.

ASCII defines `<ctrl-m>` as `RET` and `<ctrl-i>` as `TAB`, and Rune follows
that in both the GUI and a terminal, so either spelling of those two keys
does the same thing.

### Transposing

| Key | Action |
| --- | --- |
| `<ctrl-t>` | transpose the characters around point; at the end of a line, swap the last two |
| `<alt-t>` | transpose the words around point and move past them |

Both follow their GNU semantics: repeating `<ctrl-t>` drags a character
forward, and `<alt-t>` before the first word of the buffer does nothing.

### Newlines and line operations

| Key | Action |
| --- | --- |
| `<ctrl-o>` | open a line and leave point before the new newline |
| `<ctrl-j>` | insert a newline and indent |
| `<ctrl-enter>` | insert a blank line below |
| `<ctrl-shift-enter>` | insert a blank line above |
| `<alt-shift-6>` | join the current line onto the previous one |
| `<alt-up>` `<alt-down>` | move the line or selection up or down |
| `<alt-shift-up>` `<alt-shift-down>` | duplicate the line or selection up or down |

`<alt-shift-6>` is the physical key combination behind the Emacs
`delete-indentation` command. It removes horizontal whitespace around the
join, then inserts one space unless the join is at a buffer edge or directly
beside a recognized bracket.

Moving or duplicating lines uses Rune's shared clipboard internally, so either
operation replaces its newest entry.

### Killing and whitespace

| Key | Action |
| --- | --- |
| `<ctrl-k>` | kill to the end of the line; at the end of a line, kill the newline |
| `<ctrl-shift-backspace>` | kill the entire line, including its newline when present |
| `<alt-d>` `<alt-delete>` | kill forward through the current or next word |
| `<alt-backspace>` | kill backward to the previous word start |
| `<alt-k>` | kill forward to the sentence end |
| `<alt-z>` | zap: read a character, then kill through its next occurrence |
| `<alt-space>` | collapse whitespace around point to one space |
| `<alt-\\>` | delete spaces and tabs around point |

Kill commands save the removed text to Rune's clipboard, and consecutive
kills accumulate into a single entry: forward kills append, backward kills
prepend, and any other command ends the chain. Two `<ctrl-k>` in a row kill
a line and its newline as one yankable unit, and `<alt-d><alt-d>` collects
two words.

`<alt-z>` prompts for a character in the echo area and kills from point
through its next occurrence, across lines if needed. When the character is
not found, nothing is killed. `<ctrl-g>` cancels the prompt.

`<alt-\\>` leaves point where the horizontal whitespace began. It does not
cross a line boundary.

### Copy, yank, and clipboard history

| Key | Action |
| --- | --- |
| `<alt-w>` | copy the region: mark to point, or the active selection |
| `<ctrl-y>` | yank from Rune's clipboard |
| `<alt-y>` | replace the last yank with an older clipboard-history entry |

Rune's shared [clipboard history](./clipboard.md) plays the role of the GNU
Emacs kill ring: kill commands such as `<ctrl-k>`, `<ctrl-w>`, `<alt-d>`,
`<alt-k>`, and `<alt-z>` all save their text there, alongside `<alt-w>`
copies. `<alt-y>` only runs directly after a yank, and it walks
the history from newest to oldest, wrapping around at the end. With a
numeric argument, `<ctrl-u>N<ctrl-y>` yanks the Nth most recent entry and
`<ctrl-u><ctrl-y>` yanks while leaving point in front of the inserted text.

Like GNU, `<alt-w>` and `<ctrl-w>` act on the mark-to-point region, and a
shift-selection counts as an active region. `<alt-w>` leaves point where it
was. `<ctrl-c>` is not copy: GNU reserves it as the major-mode prefix, and
Rune places its own clipboard commands on <KeyBinding command="clipboardcopy" chord="<meta-c>" /> and <KeyBinding command="clipboardpaste" chord="<meta-v>" /> instead.

### Words, paragraphs, and comments

| Key | Action |
| --- | --- |
| `<alt-u>` | uppercase the current or next word |
| `<alt-l>` | lowercase the current or next word |
| `<alt-c>` | capitalize the current or next word |
| `<alt-q>` | fill the paragraph at Rune's ruler column |
| `<alt-;>` | toggle the language's line comment |

Emacs word commands treat Unicode letters and digits as word constituents;
underscores and other punctuation separate words. Line comments require
comment syntax for the current language.

## Search and replace

### Incremental search

| Key | Action |
| --- | --- |
| `<ctrl-s>` | start an incremental search forward |
| `<ctrl-r>` | start an incremental search backward |
| printable keys | extend the query; point follows the nearest match live |
| `<ctrl-s>` `<ctrl-r>` while searching | next or previous match; with an empty query, resume the last search |
| `<backspace>` | remove the last query character |
| `<enter>` `<esc>` | accept the search, leaving point at the match |
| `<ctrl-g>` | abort the search and return to where it began |

Like GNU Emacs, a forward search leaves point at the end of the match and a
backward search at its start, and repeating past the last match wraps
around the buffer. Any other editing or motion key accepts the search and
then runs normally. The match highlight survives accepting; press `<esc>`
or `<ctrl-g>` to clear it. `<ctrl-s><ctrl-s>` from an empty prompt repeats
the most recently accepted search.

A focused terminal reuses `<ctrl-s>` too: it opens a find box over the
terminal's scrollback instead of the buffer-local incremental search
described above. See [Search](./search.md#search-a-terminals-scrollback)
for how it behaves there. The emacs preset moves it off the default
`<meta-f>` so terminal search shares the same muscle memory, which also
frees <KeyBinding command="windowfocus right" chord="<meta-f>" /> for `windowfocus right`.

### Query replace

`<alt-shift-5>` starts query-replace: it prompts for the search text, then
the replacement, then stops on each match:

| Key | Action |
| --- | --- |
| `y` `<space>` | replace this match and continue |
| `n` `<backspace>` `<delete>` | skip this match |
| `!` | replace this and every remaining match |
| `.` | replace this match and stop |
| `q` `<enter>` `<esc>` | stop |
| `<ctrl-g>` | quit |

The search starts at point and stops at the end of the buffer without
wrapping. Its prompts support typing, `<space>`, `<backspace>`, submission with
`<enter>`, and cancellation with `<esc>` or `<ctrl-g>`, but not the full Emacs
minibuffer keymap. The whole session, including a `!` replace-all, reverts with
a single undo.

### Go to line

`<alt-g>g` (also `<alt-g><alt-g>`) prompts for a one-based line number and
moves point there, clamped to the buffer. It uses the same minimal prompt
editing described for query replace.

### Workspace search

The shipped preset follows GNU's search-map convention for workspace text
search and uses familiar GUI find-result keys afterward:

| Key | Action |
| --- | --- |
| <KeyBinding command="searchtext" chord="<alt-s>o" /> | search text across the workspace, following GNU `occur` |
| <KeyBinding command="jumptolocation next search" chord="<meta-g>" /> | next workspace-search result |
| <KeyBinding command="jumptolocation prev search" chord="<shift-meta-g>" /> | previous workspace-search result |

See the [Search](./search.md) tools for everything beyond the current
buffer.

## Numeric arguments

Most motion and editing commands accept a GNU-style repeat count entered
before the command:

| Key | Action |
| --- | --- |
| `<ctrl-u>` | four; repeat to multiply, so `<ctrl-u><ctrl-u>` is sixteen |
| `<ctrl-u>` digits | an explicit count, such as `<ctrl-u>12<alt-f>` |
| `<alt-0>` through `<alt-9>` | digit argument, such as `<alt-5><ctrl-f>` |
| `<alt-->` | negative argument |
| `<ctrl-g>` | cancel the pending argument |

`<ctrl-u>8x` inserts eight `x` characters, `<ctrl-u><ctrl-n>` moves four
lines down, and `<alt-2><alt-0><ctrl-f>` moves twenty characters forward.
Counted kills accumulate into a single clipboard entry, and a counted
command reverts with a single undo. Pressing `<ctrl-u>` after typing digits
ends the count, so `<ctrl-u>5<ctrl-u>7` inserts 77777.

A negative argument runs supported directional commands in the opposite direction:
`<alt--><alt-f>` moves back a word, `<alt-->3<ctrl-d>` deletes three
characters backward, and `<alt--><alt-e>` moves back a sentence.
`<ctrl-->` also enters a negative argument in the GUI; a terminal folds it
onto the same key as `<ctrl-/>`, so use `<alt-->` in a terminal. A command
without a reverse mapping does nothing when given a negative argument.

Some commands give the argument its GNU-specific meaning instead of a plain
repeat:

| Key | Action |
| --- | --- |
| `<ctrl-u><ctrl-space>` | jump to the most recent mark and pop it |
| `<ctrl-u>N<ctrl-k>` | kill through N following newlines; zero kills to the line start; negative kills backward to preceding line ends |
| `<ctrl-u><ctrl-y>` | yank, leaving point before the inserted text |
| `<ctrl-u>N<ctrl-y>` | yank the Nth most recent clipboard entry |
| `<ctrl-u>N<alt-y>` | rotate the last yank N entries back |
| `<ctrl-u>N<ctrl-v>` `<ctrl-u>N<alt-v>` | scroll N lines instead of a page |
| `<ctrl-u>N<alt-z>` | zap through the Nth occurrence; negative zaps backward |
| `<ctrl-u>N<ctrl-q>` | insert the quoted character N times |
| `<alt--><alt-k>` | kill backward to the sentence start |
| `<alt--><alt-u>` `<alt--><alt-l>` `<alt--><alt-c>` | change the case of the previous words without moving point |

## Folds and hidden text

| Key | Action |
| --- | --- |
| `<ctrl-shift-z>` | toggle the fold at point |
| `<ctrl-shift-a>` | toggle all folds |
| `<ctrl-shift-h>` | hide the active selection |
| `<ctrl-shift-v>` | reveal hidden text |

Fold commands depend on syntactic fold information for the current buffer.
They are Rune additions on `<ctrl-shift>` combinations. `<ctrl-alt-h>` keeps
GNU's `mark-defun` purpose through Rune's bracket-based approximation, and
`<ctrl-alt-v>` remains unbound because Rune has no other-window scrolling.

## Keyboard macros

| Key | Action |
| --- | --- |
| `<f3>` | start recording into the unnamed clipboard register |
| `<f4>` | stop recording, or play the recording once |

Rune implements the basic GNU `<f3>` and `<f4>` keyboard-macro workflow. It
records the key events Rune receives, so playback can include editor and
command-layer actions. It does not implement GNU's macro counter behavior or
`<ctrl-x>(` and `<ctrl-x>)` sequences. See the
[Clipboard](./clipboard.md) guide for how shared registers fit into editing
workflows.

## Command Prompt

`<alt-x>` opens Rune's [Command Prompt](./command-prompt.md). Start typing a
command name to fuzzy-filter all available commands, then press `<enter>` to
run it. This is the Rune counterpart to `execute-extended-command`, with IDE,
workspace, language, agent, and terminal commands in the same prompt.

<KeyBinding command="history" chord="<alt-shift-x>" /> toggles between the command list and the history, mirroring
GNU's `M-X` as the sibling of `M-x`.

## IDE commands

These bindings are installed by Rune's full Emacs setup preset. `<alt>` and
`<ctrl>` carry only what GNU Emacs itself defines. Everything Rune-only lives
on `<meta>`, the host layer, which is Command on macOS and Super on Linux.

### Files and session

| Key | Action |
| --- | --- |
| <KeyBinding command="write" chord="<ctrl-x><ctrl-s>" /> | save the current buffer |
| <KeyBinding command="writeall" chord="<ctrl-x>s" /> | save all modified buffers |
| <KeyBinding command="searchfile" chord="<ctrl-x><ctrl-f>" /> | search for a workspace file |
| <KeyBinding command="quit" chord="<ctrl-x><ctrl-c>" /> | quit Rune |

The `<ctrl-x>` sequences are Rune command bindings, not an open-ended Emacs
prefix map. A focused terminal consumes them, so <KeyBinding command="quit" chord="<meta-q>" /> also quits.
Quitting does not implicitly run save-all. Save first when you want to keep
all modified buffers.

### Explorer and current-file symbols

| Key | Action |
| --- | --- |
| <KeyBinding command="fexplorer" chord="<meta-o>" /> | toggle the workspace file explorer, following GNU Dired |
| <KeyBinding command={'echo {prompt}jumptoast<space>locals.scm<space>local.definition.method' + String.fromCharCode(124) + 'local.definition.function<space>'} chord="<meta-j>" /> | jump to a function or method in the current file |
| <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.type<space>" chord="<meta-s>" /> | jump to a type in the current file |
| <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>" chord="<meta-x>" /> | jump to a variable in the current file |

These sit on the host `<meta>` layer rather than `<ctrl-x>` because a focused
terminal consumes every `<ctrl-x>` chord as input for the program running
inside it before Rune sees it. GNU has no chord to inherit for the symbol
jumps: `M-g i` (imenu) would be the natural home, but the editor owns the
`M-g` goto-map.

### Cursor history and mark

| Key | Action |
| --- | --- |
| <KeyBinding command="cursorhistory prev" chord="<alt-,>" /> | previous cursor-history location, following GNU xref back |
| <KeyBinding command="cursorhistory next" chord="<ctrl-alt-,>" /> | next cursor-history location, following GNU xref forward |
| <KeyBinding command="exchangepointandmark" chord="<ctrl-x><ctrl-x>" /> | exchange point and mark |

### Language intelligence and changes

| Key | Action |
| --- | --- |
| <KeyBinding command="lsp definition" /> | go to definition, following GNU xref |
| <KeyBinding command="lsp references" /> | find references, following GNU xref |
| <KeyBinding command="lsp implementation" /> | go to implementation |
| <KeyBinding command="lsp hover" /> | show hover and type information |
| <KeyBinding command="lsp format" /> | format the current buffer, using GNU's indent-region chord |
| <KeyBinding command="lsp complete" /> | request language completion |
| <KeyBinding command="echo {prompt}lsp<space>definition<space>" /> | find a definition by name, following GNU xref apropos |
| <KeyBinding command="echo {prompt}lsp<space>references<space>" /> | find references by name |
| <KeyBinding command="echo {prompt}lsp<space>implementation<space>" /> | find implementations by name |
| <KeyBinding command="echo {prompt}lsp<space>hover<space>" /> | show hover information by name |
| <KeyBinding command="lspnextdiagnostic" /> / <KeyBinding command="lspprevdiagnostic" /> | next or previous diagnostic |
| <KeyBinding command="lsp diagnostics" /> | open the diagnostics picker |
| <KeyBinding command="gitprevchange" /> | previous Git change |
| <KeyBinding command="gitnextchange" /> | next Git change |

Language commands depend on the language support available in the current
workspace.
<Platform when="darwin">The F-keys repeat the diagnostics and Git commands, but
a focused terminal sends bare F-keys to the program running inside it.</Platform>
`<f3>` and `<f4>` stay unbound at the command layer because the editor owns them
as GNU's [keyboard-macro](#keyboard-macros) keys.

Rune does not assign unrelated commands to GNU's `<ctrl-x>n`, `<ctrl-x>p`,
`<ctrl-x>r`, `<ctrl-x>t`, or `<ctrl-x>e` families. This keeps those familiar
namespaces clear as Rune's built-in Emacs support grows.

### Bookmarks

| Key | Action |
| --- | --- |
| <KeyBinding command="locationtoggle bookmark" /> | toggle a bookmark at point |
| <KeyBinding command="locationdeleteall bookmark" /> | delete all bookmarks |
| <KeyBinding command="jumptolocation next bookmark" /> | next bookmark |
| <KeyBinding command="jumptolocation previous bookmark" /> | previous bookmark |
| <Platform when="darwin"><KeyBinding command="locationhighlight bookmark" /></Platform><Platform when="linux">unbound; run `locationhighlight bookmark`</Platform> | highlight bookmarks |

Bookmarks stay on `<meta>` plus an F-key so a focused terminal cannot eat
them. GNU's own `C-x r m`, `C-x r b`, and `C-x r l` are three-chord sequences.

## Differences from GNU Emacs

Rune's Emacs editor targets familiar editing muscle memory without embedding
the complete GNU Emacs command system. The main differences are:

- Implemented GNU chords keep their familiar purpose, although structural
  commands use the approximations described below. Rune's own editing
  features mostly use `<ctrl-shift>` combinations and modified arrow keys.
- `<alt>` is Emacs Meta, while `<meta>` is reserved for Rune layouts.
- Window focus uses `<meta>` plus PNBF. Adding `<shift>` moves window content.
  <Platform when="darwin">Host-Meta arrows resize.</Platform><Platform when="linux">Adding
  `<ctrl-alt>` resizes, echoing GNU's C-M-p/b/n/f motions, and GNU's <KeyBinding command="windowresize increase height" chord="<ctrl-x>^" />,
  <KeyBinding command="windowresize decrease width" chord="<ctrl-x>{" />, <KeyBinding command="windowresize increase width" chord="<ctrl-x>}" /> and <KeyBinding command="windowresize reset" chord="<ctrl-x>+" /> work too. Resize stays off `<meta>`
  arrows, which Linux desktops take for window snapping.</Platform>
- Window creation and lifecycle are reachable through GNU's `<ctrl-x>` number
  family and through direct host-Meta chords, because a focused terminal eats
  `<ctrl-x>`.
- Xref navigation keeps GNU's <KeyBinding command="lsp definition" chord="<alt-.>" />, <KeyBinding command="lsp references" chord="<alt-shift-/>" />, <KeyBinding command="cursorhistory prev" chord="<alt-,>" />,
  <KeyBinding command="cursorhistory next" chord="<ctrl-alt-,>" />, and <KeyBinding command="echo {prompt}lsp<space>definition<space>" chord="<ctrl-alt-.>" /> meanings. Rune-only language actions live
  on the separate `<meta>` host layer.
- `<ctrl-c>`, `<ctrl-z>`, and `<ctrl-m>` keep their GNU meanings, so they are
  not copy, undo, and goto-matching-bracket. `<ctrl-c>` is unbound because
  Rune has no major-mode prefix map, `<ctrl-z>` is unbound because Rune has
  nothing to suspend, and `<ctrl-m>` inserts a newline like `RET`.
- `<ctrl-x>` contains the specific Rune command sequences listed above, not a
  general Emacs prefix keymap. Each one duplicates a `<meta>` chord.
- The kill ring is Rune's shared clipboard history, so kills and copies are
  visible to the rest of Rune and to other buffers.
- Expression motion is bracket-based and does not move across bare atoms.
- `<ctrl-alt-h>` marks the enclosing bracketed block rather than a true defun,
  and `<ctrl-alt-v>` is unbound because Rune has no other-window scrolling.
- `<alt-shift-,>` and `<alt-shift-.>` move to the first and last lines while
  preserving the column where possible, rather than to the absolute buffer
  beginning and end.
- Named register commands, rectangle commands, and recursive editing are not
  implemented. Keyboard macros use the unnamed clipboard register internally.
- Keyboard macros use the basic `<f3>` and `<f4>` workflow rather than the
  `<ctrl-x>(` and `<ctrl-x>)` sequences, and do not implement macro counters.

If you need a complete Emacs environment rather than Rune's built-in keymap,
run Emacs inside Rune with [Exoeditor](./exoeditor.md).
