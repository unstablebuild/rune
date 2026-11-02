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
| [Search the workspace for a regular expression](#search-with-a-regular-expression) | `searchregex <pattern>` | (define your own) |
| [Find code by what it does](#search-by-meaning) | `searchsemantic <query>` | (define your own) |
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

## Search with a regular expression

`searchregex` searches every file of the workspace for a regular
expression and floats the matching lines in a picker, one per line, the
way `grep` lists them:

```
searchregex 'func\s+New\w*\('
```

The preview above the list shows the focused line in its file with its
matches emphasized. The status bar at the bottom counts the matching
lines and their files, says how long the search took and how it was
asked for, and says when it stopped at the limit or when the index was
not ready and it read every file. On its right it says which of its
file's matches the focused line is, and where. A red `ERROR` label
replaces `REGEXP` when the search failed. Type to narrow the results
down and press `<enter>` to jump to the match.

Flags go anywhere in the pattern:

| Flag | Does |
| --- | --- |
| `-i`, `--ignore-case` | ignores case |
| `-F`, `--fixed-strings` | searches for the pattern as plain text |
| `-r <dir>`, `--root <dir>` | searches only in this folder; repeat it for several |
| `-n <limit>`, `--limit <limit>` | lists at most this many lines (5000 by default) |
| `--` | ends the flags, so the rest is the pattern even if it starts with `-` |

Patterns use the RE2 regular expression syntax. `^` and `$` match at the
start and end of every line, and a match never spans lines. The command
prompt reads a backslash outside quotes as an escape and joins the words
of a pattern with single spaces, so single-quote a pattern that has
backslashes or runs of spaces.

`searchregex` skips binary files and files larger than 64 MB, and like
the rest of workspace search it leaves out what git ignores, hidden
folders, and the folders in `search.excluded_folders`. As in ripgrep,
that only applies below a root: `searchregex -r .github runs-on` searches
the hidden `.github` folder because `-r` names it.

Rune keeps a trigram index of each workspace, so a search reads only the
files that may match and answers in milliseconds even in large
repositories. Rune builds the index in the background when you open the
workspace and keeps it up to date as files change. Until the index is
ready, or with `search.regex_enabled` off, `searchregex` reads every file. See
[Workspace search](../agent/local-models.md#workspace-search) for the
`search` settings.

The same index answers the agent's `search_content` tool, and
[`runectl search regexp`](../develop/runectl.md) from a Rune terminal:

```
runectl search regexp -g '*.go' 'func\s+New\w*\('
```

## Search by meaning

Semantic indexing is off by default. Enable `search.semantic_enabled`
(`true` in YAML, `True` in Starlark) and restart Rune before using
`searchsemantic`. Regexp and symbol indexing are unaffected by the semantic
setting, and semantic indexing does not require regexp indexing.

`searchsemantic` finds code by describing what it does, so you do not
have to guess the words it uses:

```
searchsemantic where is the retry backoff computed
```

The results float in a picker, best first, each at the line it matched.
The preview above the list highlights the result's line and emphasizes
the query's words in the stretch of code that matched. The status bar at
the bottom says how the result ranked: its score, how far its meaning
stood out (`z`) and how close it came (`cos`), its rank by words, and
whether it trails far behind the best ones, with the lines that matched
on the right. Type to narrow the results down and press `<enter>` to
open one.

When the embedding model cannot run, for instance because the local
model server failed to load it, `searchsemantic` searches the
workspace's trigram index for the query's words instead. The status bar
then shows a red `ERROR` label, and Rune's log says why the model could
not run.

Flags go anywhere in the query:

| Flag | Does |
| --- | --- |
| `-r <dir>`, `--root <dir>` | searches only in this folder; repeat it for several |
| `-n <limit>`, `--limit <limit>` | lists at most this many results (20 by default) |
| `--meaning=false` | searches by words alone |
| `--prefix-last` | also matches the last word as a prefix |
| `--` | ends the flags, so the rest is the query even if it starts with `-` |

### Semantic-search requirements

Only **searching by meaning** needs an embedding model and `llama-server`.
Text, regex, filename, structural and language-server symbol searches do
not need them. Neither does `searchsemantic --meaning=false`, which
searches by words alone.

By default, Rune uses [EmbeddingGemma 2](https://huggingface.co/google/embeddinggemma-2)
through `ggml-org/embeddinggemma-2-GGUF:Q8_0`. It loads only the 270M-parameter
text model, not the full 740M-parameter model's vision and audio encoders.
Embedding runs on the machine running Rune, including for SSH workspaces.

- **Setup:** when the server or model is missing, `searchsemantic` offers
  to install the `llama-server` package and download the model. It also
  offers to upgrade an installed server that is too old. Say yes, and
  Rune tells you when the command is ready. Once installed, embedding
  needs no API key or network connection.
- **CPU or GPU:** a dedicated GPU is not required. The packaged server
  supports macOS 13.3+ on Apple Silicon or Intel, with Metal acceleration,
  and Linux on x86-64 or arm64 with glibc 2.35+. See
  [Server requirements](../agent/local-models.md#server-requirements)
  for platform details and custom builds.
- **Disk:** the current [Q8_0 download](https://huggingface.co/ggml-org/embeddinggemma-2-GGUF)
  is about **865 MB**: 310 MB of text weights plus a 555 MB media projector.
  Rune downloads both, but does not load the projector for code search.
  Allow extra space for the server package and workspace indexes.
- **Memory:** model-file size is not a RAM requirement. Inference buffers,
  indexing and the editor need additional memory; a local chat model uses
  a separate server and adds its own footprint. Google does not specify a
  fixed minimum RAM or VRAM, and Rune has no measured minimum for this
  workload. The embedding server starts on demand and stops after being
  idle.

Rune adds Google's code-retrieval and document prefixes automatically,
uses mean pooling, and L2-normalizes the full 768-dimensional vectors.
You do not need to add a task prefix to your query. The model supports
8,192 tokens, but Rune currently uses a **2,048-token embedding context**
and indexes code in smaller chunks rather than embedding whole files.

:::caution[Inference precision]
Google requires **BF16 or FP32 activations**, not FP16, which can produce
invalid or silently degraded embeddings. `Q8_0` describes weight
quantization, not activation precision. Rune relies on llama.cpp's
backend kernels, some of which use FP16 intermediates, so it does not
currently guarantee BF16/FP32-only inference across backends. See the
[model card's precision guidance](https://huggingface.co/google/embeddinggemma-2#4-numerical-precision).
:::

See [Workspace search](../agent/local-models.md#workspace-search) for what
is indexed and how to configure it.

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
