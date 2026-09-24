---
sidebar_position: 15
---

# Helix Editor

A cheatsheet for Rune's [Helix](https://helix-editor.com)-style editor.
Helix inverts the vi grammar: a motion first **selects** the text you mean,
then an operator acts on that selection. You press `w` then `d` to delete a
word, not `dw`, and you always see what an operator is about to touch.

The `:` command mode lives at the [Command Prompt](./command-prompt.md):
same prompt, broader scope.

## Preset

Enable it in `config.yaml`:

```yaml tab
editor:
  mode: "helix"
```

## Where the helix editor applies

Setting `mode: "helix"` does more than turn your file buffers modal; the
same editor powers prompts and output surfaces throughout Rune, so the
motions and operators on this page work everywhere:

- **The console.** Rune's [console](./console.md) uses it as the prompt
  editor, so you compose command lines with the full selection grammar.
- **The agent dialogue.** In [Rune Agent](../agent/intro.md), the dialogue
  prompt becomes a helix editor.
- **Terminals.** Terminals gain a modal mode: enter it to edit the prompt
  line, and treat the whole scrollback as a navigable buffer.
- **Task and command output.** When a [task](./tasks.md) finishes, or a
  one-shot plugin command (`! command`) completes, its output opens in a
  helix editor so you can scroll and navigate the results.

## Selections come first

Every range is an **anchor** and a **head**, and it is never empty: in normal
mode the caret is itself a one-cell selection. A motion in normal mode drops a
fresh anchor and moves the head; in select mode (`v`) the anchor survives, so
motions grow and flip the range.

```text
foo bar baz        w      selects "foo " with the caret on the space
foo bar baz        w d    deletes "foo ", leaving "bar baz"
foo bar baz        v w w  selects "foo bar " (select mode keeps the anchor)
```

Rune's helix editor is **single-selection**. Helix's multiple-selection
commands are deliberately left unbound rather than approximated, so they stay
free for your own `command.key_bindings`: `s`, `S`, `C`, `A-C`, `A-s`,
`A-minus`, `A-_`, `K`, `A-K`, `,`, `A-,`, `&`, `(`, `)`, `A-(`, `A-)`, `A-I`,
`A-a`. The syntax-tree sibling and parent motions (`A-n`, `A-p`, `A-e`,
`A-b`, `A-left`, `A-right`) are unbound for the same reason.

## Modes

| Mode | Enter | Leave |
| --- | --- | --- |
| Normal | `<esc>` | — |
| Select | `v` | `v` or `<esc>` |
| Insert | `i` `I` `a` `A` `o` `O` `c` | `<esc>` |
| Goto | `g` | any key (one shot) |
| Match | `m` | any key (one shot) |
| View | `z` | any key (one shot) |
| View (sticky) | `Z` | `<esc>` |
| Bracket | `[` or `]` | any key (one shot) |
| Replace | `r` | any key (one shot) |

Most operators drop select mode when they run, as Helix does. `J`, `A-J` and
`=` deliberately keep it.

## Counts

Type digits before a motion or operator to repeat it: `3w` walks three words,
`2x` selects two lines, `3p` pastes the register three times as one run.

## Motions

### Character and line

| Key | Motion |
| --- | --- |
| `h` `l` / `<left>` `<right>` | Left / right one cell |
| `j` `k` / `<down>` `<up>` | Down / up one line |
| `<home>` / `g h` | Line start |
| `<end>` / `g l` | Line end |
| `g s` | First non-blank on the line |
| `g \|` | Column *n* (with a count) |

### Word

| Key | Motion |
| --- | --- |
| `w` / `b` | Next word start / previous word start |
| `e` | Next word end |
| `W` / `B` / `E` | Same, treating punctuation as word material |

Word motions are a port of Helix's own `word_move`, so repeated presses walk
forward exactly as Helix does, line endings re-anchor the selection onto the
next word, and a motion that cannot move leaves the range untouched.

### Find character on line

| Key | Motion |
| --- | --- |
| `f{char}` / `F{char}` | Select forward / backward through `{char}` |
| `t{char}` / `T{char}` | Select forward / backward up to `{char}` |
| `A-.` | Repeat the last `f` / `t` / `F` / `T` |

A miss leaves the selection alone. `<space>` and `<tab>` are accepted as the
target character.

### File, paragraph and brace

| Key | Motion |
| --- | --- |
| `g g` | First line (or line *n* with a count) |
| `g e` | Last line |
| `{n}G` | Line *n*. Bare `G` is a no-op, as in Helix |
| `] p` / `[ p` | Next / previous paragraph |
| `m m` | Matching bracket |
| `g t` / `g c` / `g b` | Window top / centre / bottom |
| `g k` / `g j` | Line up / down keeping the column |
| `g .` | Last modification |

### Search

| Key | Motion |
| --- | --- |
| `/` / `?` | Search forward / backward; `<enter>` jumps to and selects the first match |
| `n` / `N` | Next / previous match |
| `*` | Search for the selection, anchored at word boundaries |
| `A-*` | Search for the selection verbatim |

### Jumps

| Key | Action |
| --- | --- |
| `g w` | Label every visible word, then jump to the one whose two-character label you type |
| `C-s` | Push the current selection onto the jumplist |
| `C-o` / `C-i` or `<tab>` | Jump backward / forward |

`G`, `g \|`, `g .` and a confirmed search push the position they left, so
`C-o` walks back to it.

`g w` only labels words of two or more word characters, and never the word the
caret is already on. Labels are ordered outward from the caret, so the nearest
targets get the shortest ones. Any key that is not part of a live label cancels.
In select mode `g w` extends the selection to the label instead of replacing it.

## Scrolling

| Key | Action |
| --- | --- |
| `C-f` / `C-b`, `<pgdn>` / `<pgup>` | Page down / up |
| `C-d` / `C-u` | Half page down / up |
| `z j` / `z k`, `C-e` / `C-y` | Scroll one line down / up |
| `z z` or `z c` | Centre the caret |
| `z t` / `z b` | Put the caret at the top / bottom |

`z` is one shot; `Z` keeps view mode until `<esc>`. Inside view mode, `/`,
`?`, `n` and `N` search, and `<space>` / `<backspace>` page by half screens.

## Changing the selection

| Key | Action |
| --- | --- |
| `v` | Toggle select mode |
| `;` | Collapse the selection onto the caret |
| `A-;` | Flip anchor and head |
| `A-:` | Force the selection forward |
| `x` | Select the whole line; repeats extend downwards |
| `X` | Snap the selection out to whole lines |
| `A-x` | Drop the partially covered first and last lines |
| `%` | Select the whole file |
| `_` | Trim whitespace off both ends |
| `A-o` / `A-<up>` | Expand to the enclosing syntax node |
| `A-i` / `A-<down>` | Shrink to the child syntax node |

## Text objects

`m i {object}` selects the inside, `m a {object}` includes the delimiters.

| Object | Selects |
| --- | --- |
| `w` / `W` | Word / word including punctuation |
| `p` | Paragraph |
| `m` | The closest enclosing bracket pair |
| `(` `)` `{` `}` `[` `]` `<` `>` | That pair |
| `"` `'` `` ` `` | That quote |

Rune also accepts `s` for a sentence and the vi aliases `b`, `B` and `t`;
Helix leaves those keys for its tree-sitter objects, which need a syntax
service Rune's editor does not own.

## Operators

An operator acts on the current selection.

| Key | Action |
| --- | --- |
| `d` / `A-d` | Delete, with / without yanking |
| `c` / `A-c` | Change, with / without yanking |
| `y` | Yank |
| `p` / `P` | Paste after / before the selection |
| `R` | Replace the selection with the register |
| `r{char}` | Replace every character in the selection |
| `~` | Toggle case |
| `` ` `` / ``A-` `` | Lowercase / uppercase |
| `J` / `A-J` | Join lines / join and select the separator |
| `>` / `<` | Indent / unindent |
| `=` | Reindent to the syntax target |
| `C-c` | Toggle line comments |
| `C-a` / `C-x` | Increment / decrement the number |
| `u` / `U` | Undo / redo |
| `A-u` / `A-U` | Undo / redo, taking a count |

`c` on a whole-line selection opens a fresh blank line instead of pulling the
next line up, so `x c` leaves you a line to type on — the same behaviour as
Helix's `only_whole_lines` branch.

### Surround

| Key | Action |
| --- | --- |
| `m s{char}` | Wrap the selection in `{char}` |
| `m r{from}{to}` | Replace the surrounding `{from}` pair with `{to}` |
| `m d{char}` | Delete the surrounding `{char}` pair |

### Adding lines

| Key | Action |
| --- | --- |
| `] <space>` / `[ <space>` | Add a blank line below / above without leaving normal mode |

## Insert mode

| Key | Enters insert |
| --- | --- |
| `i` / `a` | Before / after the selection |
| `I` / `A` | At the first non-blank / at the line end |
| `o` / `O` | On a new line below / above |
| `c` | After deleting the selection |

`i` is the one entry that leaves a non-empty range behind, so `<esc>` keeps
the caret where it already was rather than pulling it back onto the text you
just typed. Every other entry lands on the last character inserted. This
matches Helix, where only `a` sets `restore_cursor`.

While inserting:

| Key | Action |
| --- | --- |
| `<esc>` | Back to normal mode |
| `C-s` | Commit an undo checkpoint |
| `C-r{reg}` | Insert a register |
| `C-w` / `A-<backspace>` | Delete the word before the caret |
| `A-d` / `A-<del>` | Delete the word after the caret |
| `C-u` | Kill to the line start |
| `C-k` | Kill to the line end, joining when already there |
| `C-h` / `<backspace>` | Delete backwards, dedenting a whole level inside indentation |
| `C-d` / `<del>` | Delete forwards |
| `C-j` / `<enter>` | Insert a newline |
| `<tab>` / `<shift-tab>` | Smart indent / literal indent |
| Arrows, `<pgup>` / `<pgdn>`, `<home>` / `<end>` | Move the caret |

## Registers

`"{reg}` picks the register the next operator uses.

| Register | Holds |
| --- | --- |
| `a`–`z` | Named. `A`–`Z` appends instead of replacing |
| `"` | The unnamed register: every yank and delete |
| `0` | The last yank |
| `-` | The last small (non-linewise) delete |
| `_` | The black hole: writes are discarded |
| `.` | The last inserted text |
| `/` | The last search pattern |
| `%` | The current file path |
| `+` / `*` | The system clipboard |

Helix clears a pending register on the very next key, so the idiom is
`w"ay` — motion, register, operator. Rune keeps the register until an
operator consumes it, which makes `"ay` work too.

## Macros

| Key | Action |
| --- | --- |
| `Q` | Start recording into the selected register; press again to stop |
| `q` | Replay the selected register |
| `"{reg}Q` / `"{reg}q` | Record into / replay register `{reg}` |

A recorded macro is a plain register, so you can bind it to a key or wrap it
in an alias with the [command prompt](./command-prompt.md)'s `echo`
`{register}` instruction.

## Windows, tabs and pickers

The helix preset follows Helix's own leader conventions. `<space>` and
`<ctrl-w>` are free in normal mode and taken in insert mode, which is exactly
the gate these menus need.

### The `<space>` menu

| Key | Action |
| --- | --- |
| `<space>f` | Open a file |
| `<space>e` | Toggle the file explorer |
| `<space>b` | Search open tabs |
| `<space>j` | Previous cursor position |
| `<space>/` | Search the workspace |
| `<space>k` | Hover |
| `<space>r` | Rename the symbol |
| `<space>h` | References to the symbol |
| `<space>d` | Diagnostics |
| `<space>g` | Next Git change |
| `<space>y` / `<space>p` | Copy to / paste from the system clipboard |
| `<space>?` | Command history |
| `<space>s` / `<space>t` / `<space>v` | Jump to a function / type / variable |

### The `<ctrl-w>` window menu

| Key | Action |
| --- | --- |
| `<ctrl-w>h` `j` `k` `l` | Focus the window left / down / up / right |
| `<ctrl-w>H` `J` `K` `L` | Move the window left / down / up / right |
| `<ctrl-w>w` | Focus the other window |
| `<ctrl-w>s` / `<ctrl-w>v` | Split horizontally / vertically |
| `<ctrl-w>q` / `<ctrl-w>o` | Close this window / close the others |

Helix reaches the same menu through `<space>w`, but a Rune key sequence is
exactly two keys, so only the `<ctrl-w>` spelling survives the translation.

See [Layout Management](./layout-management.md) for the complete layout
system, and [Key Mapping](./key-mapping.md) to remap the physical keys that
drive the editor grammar itself.

## What's different from Helix

- **Single selection.** Phase one ships one selection; the multi-selection
  keys are listed above and stay unbound.
- **No tree-sitter objects.** `m i f`, `m i t`, `m i a`, `m i c` and the
  sibling motions need a syntax service the editor does not own. `A-o` /
  `A-i` expansion still works because Rune supplies it.
- **`:` is Rune's prompt.** There is no separate Helix command mode; `:`
  opens the global [command prompt](./command-prompt.md), so `:w`, `:q` and
  friends are Rune commands.
- **No pickers inside the editor.** `<space>f`, `<space>b` and the rest are
  Rune commands bound by the preset, not editor-internal pickers.
- **Extras.** `.` repeats the last insert, and `C-e` / `C-y` scroll by a
  line. Helix leaves those keys unbound.
- **Registers live longer.** See [Registers](#registers).

## What's not here

Shell piping (`\|`, `!`, `$`), `C-z` suspend, LSP-driven `g d` / `g r` / `g i`
navigation and the `[`/`]` diagnostic and syntax jumps are not bound by the
editor. Use the [command prompt](./command-prompt.md) and the preset's
`<space>` menu for the ones Rune provides.
