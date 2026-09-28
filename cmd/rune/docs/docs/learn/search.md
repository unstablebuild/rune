---
sidebar_position: 4
---

# Search

Rune gives you several ways to find code, each suited to a different
question. This guide covers the structural search built on tree-sitter
(jump to a definition in the current file, or across the workspace) and
the fuzzy finders for files and symbols. It also points you at the two
neighboring tools: semantic symbol search for Tier 1 languages, and
the location picker for turning any command's output into a jump list.

At a glance:

| You want to | Command | Preset keybinding |
| --- | --- | --- |
| [Jump to a function in the current file](#jump-within-the-current-file) | `jumptoast` | <KeyBinding command={'echo {prompt}jumptoast<space>locals.scm<space>local.definition.method' + String.fromCharCode(124) + 'local.definition.function<space>'} /> |
| [Jump to a variable in the current file](#jump-within-the-current-file) | `jumptoast` | <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>" /> |
| [Jump to a type in the current file](#jump-within-the-current-file) | `jumptoast` | <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.type<space>" /> |
| [Find a function definition anywhere in the workspace](#search-across-the-workspace) | `searchfunc` | <KeyBinding command="searchfunc" /> |
| [Find a variable definition anywhere in the workspace](#search-across-the-workspace) | `searchvar` | <KeyBinding command="searchvar" /> |
| [Find a type definition anywhere in the workspace](#search-across-the-workspace) | `searchtype` | <KeyBinding command="searchtype" /> |
| [Search a file by name](#search-files) | `searchfile` | <KeyBinding command="searchfile" /> |
| [Search file contents across the workspace](#search-text) | `searchtext` | <KeyBinding command="searchtext" /> |
| [Jump to an arbitrary symbol's definition](#semantic-symbol-search) | `lsp definition <symbol>` | <KeyBinding command="echo {prompt}lsp<space>definition<space>" /> |
| [Open an arbitrary symbol's documentation](#semantic-symbol-search) | `lsp hover <symbol>` | <KeyBinding command="echo {prompt}lsp<space>hover<space>" /> |
| [List an arbitrary symbol's references](#semantic-symbol-search) | `lsp references <symbol>` | <KeyBinding command="echo {prompt}lsp<space>references<space>" /> |
| [Turn any `grep`/`rg` output into a jump list](#turn-command-output-into-a-jump-list) | `locationpicker <program>` | (define your own) |
| [Search a terminal's scrollback](#search-a-terminals-scrollback) | (built into the terminal) | `<meta-f>` |

## Structural search and navigation

Every [language package](../languages/supported.md) ships a tree-sitter
grammar and query files, so Rune has a real parse tree for the file
instead of plain text. Two commands query that tree using tree-sitter
patterns:

- **`jumptoast`** jumps the cursor to a node in the **current file**.
  Its syntax is `jumptoast <query> <captures> <name>`, where `<query>`
  is a query file (such as `locals.scm`), `<captures>` is one or more
  capture names joined by `|`, and `<name>` selects the matching line.
- **`searchast`** runs the same kind of query across **every file in the
  workspace** and presents the matches in a fuzzy picker. Its syntax is
  `searchast <query> <captures>`.

The editor presets wire the common forms to keys. The tables below follow the
preset selected at the top of the page.

### Jump within the current file

The `jumptoast` bindings prefill the prompt with the command and leave
it open, so you can fuzzy search the functions, variables, or types in
the file and jump to the one you pick:

| Key | Jumps to |
| --- | --- |
| <KeyBinding command={'echo {prompt}jumptoast<space>locals.scm<space>local.definition.method' + String.fromCharCode(124) + 'local.definition.function<space>'} /> | a function or method |
| <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>" /> | a variable |
| <KeyBinding command="echo {prompt}jumptoast<space>locals.scm<space>local.definition.type<space>" /> | a type |

### Search across the workspace

To search every file, use the `searchast` aliases. You can type each alias in
the command prompt and bind it yourself when the selected preset leaves it
unbound:

| Key | Alias | Searches for |
| --- | --- | --- |
| <KeyBinding command="searchfunc" /> | `searchfunc` | functions and methods |
| <KeyBinding command="searchvar" /> | `searchvar` | variables |
| <KeyBinding command="searchtype" /> | `searchtype` | types |

They open a fuzzy picker over every matching definition in the
workspace; selecting a result jumps you straight to it. All of these
query `locals.scm` filtered by the
`local.definition.function`/`.method`, `local.definition.var`, and
`local.definition.type` captures.

A language package supplies the query files these commands read, so
structural search lights up for any installed language. See
[Supported Languages](../languages/supported.md) for the full list and
how packages install.

## The fuzzy file and symbol finders

The file finder (`searchfile`) and symbol finder are floating surfaces:
they appear on top of your windows, take focus while open, and get out
of the way when you are done, without disturbing the windows underneath.
Type to fuzzy-filter, move through the matches, and press `<enter>` to
open the selection in the window you were last working in.

The fuzzy match is deterministic, so the shortest sequence that singles
out a name always ranks it first. Spend a few seconds finding that
sequence once and it becomes reflex, the same way the
[command prompt's fuzzy finder](./command-prompt.md#build-muscle-memory-with-the-fuzzy-finder)
works for commands.

### Search files

`searchfile` (<KeyBinding command="searchfile" />) opens the file finder: type to fuzzy-filter
files by name across the workspace and press `<enter>` to open the match.

### Search text

`searchtext` (<KeyBinding command="searchtext" />) searches file contents across the workspace and
presents the matches in a fuzzy picker, jumping you to the line you pick.

## Semantic symbol search

For [Tier 1 languages](../languages/supported.md#tier-1-languages)
such as [Go](../languages/go.md), Rune adds language-server symbol
search on top of the structural tools. Rune's
[symbol index](../languages/symbol-index.md) first locates a qualified name,
then Rune queries the language server from that source position. This lets you
jump to a definition, list references, or find implementations of a symbol
**by name** with full code intelligence. See
[Query any symbol by name](../languages/intelligence.md#query-any-symbol-by-name)
for the commands and bindings.

## Turn command output into a jump list

When the thing you want to find is best expressed as a shell command,
the [location picker](./location-picker.md) runs any program that prints
`path:line:col` locations (`grep`, `git grep`, `rg`, `awk`, or your own
script) and presents every line as a fuzzy-filterable entry with a live
preview. It is the bridge between Rune's structured search and the
text-search tools you already know.

## Search a terminal's scrollback

Terminals get their own find, independent of everything else on this page.
`<meta-f>` opens a floating find box over the focused terminal and searches
its entire scrollback, not just the visible screen. `<enter>` or `<meta-f>`
again jumps to the next match, and `<esc>` or `<ctrl-c>` closes the box and
leaves the current match selected, so a follow-up copy shortcut copies it.
It works whether or not the terminal is in [modal mode](./vim-editor.md).
There is no replace: a shell has no buffer to write into.

### Search configuration

`find_key` is the only part of the terminal's find box you can configure;
the rest of its appearance always follows
[`editor.standard.search`](./standard-editor.md#search-configuration). Leave
it unset and the terminal follows whatever `find_key` you configured there.

```yaml tab
terminal:
  search:
    find_key: "<meta-f>"
```

```python tab
"terminal": {
    "search": {
        "find_key": "<meta-f>",
    },
},
```

The [emacs preset](./emacs-editor.md#incremental-search) rebinds it to
`<ctrl-s>`, isearch-forward's key, so it shares muscle memory with the
editor's own incremental search.
