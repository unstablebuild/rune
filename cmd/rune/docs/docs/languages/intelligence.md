---
sidebar_position: 2
keywords:
  - code intelligence
  - go to definition
  - jump to definition
  - find references
  - find implementations
  - diagnostics
  - errors and warnings
  - hover
  - rename symbol
  - format
  - completion
  - lsp
  - language server
---

# Code Intelligence

Rune's code intelligence is cross-language. The same `lsp` command drives
**go to definition**, **find references**, **diagnostics**, hover docs,
rename, formatting, and completion for every
[Tier 1 language](./supported.md#tier-1-languages). The keys
and workflow below are identical no matter which language server is
running underneath.

Each language layers its own dedicated command on top for
language-specific refactors and tooling. This page covers the shared
`lsp` command; the per-language guides cover the rest.

## Code intelligence: the `lsp` command

`lsp` is the cross-language entry point for code intelligence. Run it as
`lsp <subcommand>`. Most subcommands act on the symbol under the cursor;
the navigation commands also accept an explicit symbol name as an
argument.

| Command | Argument | What it does | Preset key |
| --- | --- | --- | --- |
| `lsp hover` | `[<symbol>]` | Show docs, types, and signatures for the symbol | <KeyBinding command="lsp hover" /> |
| `lsp definition` | `[<symbol>]` | Jump to where the symbol is defined | <KeyBinding command="lsp definition" /> |
| `lsp declaration` | `[<symbol>]` | Jump to the symbol's declaration | <KeyBinding command="lsp declaration" /> |
| `lsp type-definition` | `[<symbol>]` | Jump to the symbol's type definition | <KeyBinding command="lsp type-definition" /> |
| `lsp implementation` | `[<symbol>]` | List implementations of the symbol | <KeyBinding command="lsp implementation" /> |
| `lsp references` | `[<symbol>]` | List every use of the symbol | <KeyBinding command="lsp references" /> |
| `lsp complete` |  | Show completions at the cursor | <KeyBinding command="lsp complete" /> |
| `lsp signature-help` |  | Show the signature of the call at the cursor | <KeyBinding command="lsp signature-help" /> |
| `lsp rename` |  | Rename the symbol at the cursor everywhere | <KeyBinding command="lsp rename" /> |
| `lsp format` |  | Format the file, or the selection if text is selected | <KeyBinding command="lsp format" /> |
| `lsp diagnostics` |  | Open a picker with every diagnostic in the file | <KeyBinding command="lsp diagnostics" /> |

### Go to definition

The everyday flow is to query whatever is under the cursor. Put the
cursor on a symbol and press <KeyBinding command="lsp definition" /> to jump to where it is defined; no
argument, no typing. The same cursor-driven shortcut exists for each
navigation query:

| Key | Command | Jumps to |
| --- | --- | --- |
| <KeyBinding command="lsp definition" /> | `lsp definition` | Where the symbol is defined |
| <KeyBinding command="lsp hover" /> | `lsp hover` | Its docs, type, and signature |
| <KeyBinding command="lsp implementation" /> | `lsp implementation` | What implements it (or what it implements) |
| <KeyBinding command="lsp references" /> | `lsp references` | Every use of it |

For `hover`, `definition`, `declaration`, `type-definition`,
`implementation`, and `references`, leaving the argument off always
targets the symbol you are sitting on. When you know the symbol's name
but not where it lives, use the by-name variant below instead.

### Query any symbol by name

This is one of the most powerful ways to move around a codebase. You can
query a symbol (a function, type, struct, interface, or method) **by name**,
without first having to find where it lives. Rune's [symbol index](./symbol-index.md)
resolves qualified names across the workspace before asking the language server
for the semantic result. If you remember what something is called but not where
it is defined, press <KeyBinding command="echo {prompt}lsp<space>definition<space>" />,
type its qualified name, and jump straight to its definition:

```
lsp definition server.NewServer
```

Each preset provides variants that pre-fill the prompt with the command and
leave it open for you to type a name:

| Query | Preset key |
| --- | --- |
| Hover | <KeyBinding command="echo {prompt}lsp<space>hover<space>" /> |
| Definition | <KeyBinding command="echo {prompt}lsp<space>definition<space>" /> |
| References | <KeyBinding command="echo {prompt}lsp<space>references<space>" /> |
| Implementation | <KeyBinding command="echo {prompt}lsp<space>implementation<space>" /> |

This is the counterpart to
[Go to definition](#go-to-definition) above: the plain keys act on the
cursor, while these keys act on a name.

### Diagnostics

`lsp diagnostics` (<KeyBinding command="lsp diagnostics" />) opens a location picker listing every
diagnostic the language server reports for the file. To step through them
without the picker, use:

| Key | Action |
| --- | --- |
| <KeyBinding command="lspnextdiagnostic" /> | Jump to the next diagnostic |
| <KeyBinding command="lspprevdiagnostic" /> | Jump to the previous diagnostic |

These are aliases for `jumptolocation next lsp-diagnostics` and
`jumptolocation previous lsp-diagnostics`.

### Highlighting occurrences

There is no command for this: Rune automatically highlights other
occurrences of the symbol under the cursor shortly after the cursor
settles. Move off the symbol and the highlight clears.

## Preset key bindings

The selected editor preset binds the most common code-intelligence
actions. See [Key syntax](../learn/key-syntax.md) for how the bindings are
written.

| Key | Command | Target |
| --- | --- | --- |
| <KeyBinding command="lsp hover" /> | `lsp hover` | Symbol under the cursor |
| <KeyBinding command="echo {prompt}lsp<space>hover<space>" /> | `lsp hover` | Symbol you query by name |
| <KeyBinding command="lsp definition" /> | `lsp definition` | Symbol under the cursor |
| <KeyBinding command="echo {prompt}lsp<space>definition<space>" /> | `lsp definition` | Symbol you query by name |
| <KeyBinding command="lsp references" /> | `lsp references` | Symbol under the cursor |
| <KeyBinding command="echo {prompt}lsp<space>references<space>" /> | `lsp references` | Symbol you query by name |
| <KeyBinding command="lsp implementation" /> | `lsp implementation` | Symbol under the cursor |
| <KeyBinding command="echo {prompt}lsp<space>implementation<space>" /> | `lsp implementation` | Symbol you query by name |
| <KeyBinding command="lsp rename" /> | `lsp rename` | Symbol under the cursor |
| <KeyBinding command="lsp format" /> | `lsp format` | The file, or the selection |
| <KeyBinding command="lsp diagnostics" /> | `lsp diagnostics` | The whole file |
| <KeyBinding command="lspnextdiagnostic" /> | Next diagnostic | The whole file |
| <KeyBinding command="lspprevdiagnostic" /> | Previous diagnostic | The whole file |
| <KeyBinding command="lsp complete" /> | `lsp complete` | The cursor position |

<Platform when="darwin">

:::note
`<ctrl-space>`, used for completion by the Vim and Standard
presets, is reserved for input source switching. To use it for completion,
go to System Settings → Keyboard → Keyboard Shortcuts → Input Sources and
uncheck both entries.
:::

</Platform>

Everything bound to a key is also available from the [command
prompt](../learn/command-prompt.md), so you can run any `lsp` subcommand
by name even when it has no binding.
