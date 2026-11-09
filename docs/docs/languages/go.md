---
sidebar_position: 4
---

# Go

Rune ships [Tier 1](./supported.md#support-tiers) Go support: code intelligence through a built-in
language client, and a dedicated `go` command that drives refactors,
tests, and module management. This guide works through every Go feature
from the commands you type down to what each one does.

Rune also includes a built-in debugger, powered by (and with thanks to)
the [Delve](https://github.com/go-delve/dlv) community, that you can use
to debug your Go programs. One thing to know up front: you launch a
debug session by passing the **package** to run, not the path to
`main.go`. For the full walkthrough, see the [Debugger](../learn/debugger.md)
guide.

## Setup

The Go extension ships everything it needs; there is nothing to install
separately. Rune provides its own language server for Go, so support works
out of the box.

If you want to use your own language server binary, point Rune at it in
`config.yaml`:

```yaml tab
extensions:
  go:
    config:
      lsp_path: "/path/to/your/binary"
```

## How Rune finds your Go project

Go code is organized into **modules**. A module is a folder tree whose
root contains a `go.mod` file, a small manifest that names the project
and records its dependencies. If a project does not have one yet,
running `go mod init <name>` in a terminal at the project root creates
it.

Rune uses that manifest to decide where a Go project starts and to root
the language server there. Three marker files count: `go.mod`, `go.sum`,
and `go.work`.

- **Workspace root.** If the folder you open as your workspace contains
  one of the markers, the language server starts right away, rooted at
  the workspace.
- **Nested projects.** Markers deeper in the workspace work too. When
  you open a `.go` file, Rune walks up from that file to the nearest
  enclosing folder that carries a marker and starts a language server
  rooted at that folder. In a monorepo with several modules, each module
  gets its own language server the first time you open one of its files,
  and that server is reused for every other file in the module.

A `.go` file with no marker in any folder between it and the workspace
root gets no code intelligence, because there is no project to root a
server at. Create a `go.mod` with `go mod init` and reopen the file.

:::info
Nested-module discovery is driven by opens. If an external tool, like Rune's agent wants
to run semantic queries for a module you have never opened, and no enclosing module
already has a server running, that module's server does not start until you open one of
its files, so code intelligence there stays unavailable until then. Opening any `.go` file
in the module brings the server up. Modules that sit under a workspace-root module are
already covered by the root server, so this only affects standalone modules with no
enclosing root.
:::

Working across several modules in one repository? See the
[Monorepos](./monorepo.md) guide for how per-module discovery and
workspace-wide search fit together.

:::warning[GOPATH projects are not supported]
Before modules, Go located projects by their placement under a special
directory tree (`$GOPATH/src/...`) with no marker file in the project
itself. This legacy layout is known as **GOPATH mode**, and Rune's code
intelligence does not support it. If your project has no `go.mod`, run
`go mod init <module-name>` once at its root to convert it; everything
in this guide works from there.
:::

## Code intelligence

Go to definition, find references, diagnostics, hover, rename, and
formatting all run through the cross-language `lsp` command. For the full
command list, the query-by-name workflow, and the default keys, see the
[Code Intelligence](./intelligence.md) guide. The rest of this page
covers what is specific to Go.

### Language server logs

gopls writes its normal logs to stderr, where Rune captures them. Run
`lsp info go` in the console to find the gopls PID, then run
`process stdio <pid>` to view its logs. This also works after gopls exits. If
gopls is not running, `lsp status` shows why. See
[Troubleshoot](../troubleshoot.md#inspect-any-process-rune-starts)
for details.

Normal `info` logging is enabled by default. Set `debug.log_level` to increase
verbosity:

```yaml tab
extensions:
  go:
    config:
      debug:
        log_level: debug
```

Accepted values are `info`, `debug`, and `trace`. They map to gopls's normal,
verbose, and very verbose logging. The existing `debug.rpc_trace` option can
add the full JSON-RPC trace when protocol messages are needed.

Setting `debug.logfile` tells gopls to write its logs to that file instead of
stderr, so those logs do not appear in `process stdio`.

### Interesting queries

Some `lsp` queries are more powerful than they first appear. The
`implementation` query in particular is bidirectional; it answers "what
satisfies this?" *and* "what does this satisfy?" depending on where the
cursor is. In a language like Go where interface satisfaction is
implicit, this is the fastest way to discover the relationships the
compiler infers for you.

Place the cursor and run `lsp implementation` (or
<KeyBinding command="lsp implementation" />):

| Cursor on | You get |
| --- | --- |
| A **struct** (concrete type) | The interface(s) that the struct satisfies. |
| An **interface** | The struct(s) that satisfy the interface. |
| A **method on a struct** | The interface method(s) that this method implements, i.e. which interface(s) it is part of. |
| A **method on an interface** | The corresponding method on each struct that satisfies the interface. |

When a query has more than one answer, Rune opens a picker so you can jump
to any of the results; with a single answer it jumps straight there.

This makes implicit relationships explorable in both directions:

- "What interfaces does this type implement?" → cursor on the struct.
- "What types implement this interface?" → cursor on the interface.
- "Which interface contract does this method fulfill?" → cursor on the
  struct's method.
- "Where is this interface method actually implemented?" → cursor on the
  interface's method.

## Go-specific commands: the `go` command

The `go` command, powered by gopls, adds Go refactors, tests, and module
management on top of `lsp`. Run it as `go <subcommand>`. Most subcommands
act on the cursor position or the current selection; the command tells you
what to do when nothing applicable is under the cursor.

### Imports

| Command | What it does |
| --- | --- |
| `go organize-imports` | Remove unused imports, add missing ones, and sort them. |
| `go add-import` | Add a package import to the current file. |
| `go eliminate-dot-import` | Remove a dot import and qualify all references with the package name. |

### Tests and benchmarks

| Command | What it does |
| --- | --- |
| `go test [<name>]` | Run the test or benchmark. |
| `go add-test` | Generate a table-driven test for the function at the cursor. |

`go test` runs a single test or benchmark **by name**. You do not write a
regex or remember its line. Pass the exact function name and Rune runs
just that one:

```
go test TestNewServer
```

If the name starts with `Benchmark`, it is run as a benchmark instead.
Tab completion fuzzy-searches every `Test`, `Benchmark`, `Fuzz`, and
`Example` function in the **whole workspace**, so you can pull up any test
by name from anywhere and run it for a quick one-off check, without
opening its file first. With no argument at all, `go test` finds the test
or benchmark nearest the cursor and runs that one.

`go test` is the one Go command that executes through the real `go`
toolchain instead of the language server. The language server is still
used to locate the nearest test when you omit the name, but the run
itself happens in the file's package directory, streaming progress as
notifications.

### Refactorings

All of these act on the cursor or the current selection.

| Command | What it does |
| --- | --- |
| `go extract-function` | Replace selected statements with a call to a new function. |
| `go extract-method` | Like above, but a method on the same receiver. |
| `go extract-variable` | Replace the selected expression with a new local variable. |
| `go extract-variable-all` | Same, replacing every occurrence of the expression. |
| `go extract-constant` | Replace the selected constant expression with a named constant. |
| `go extract-constant-all` | Same, replacing every occurrence. |
| `go extract-to-new-file` | Move selected top-level declarations to a new file in the package. |
| `go inline-call` | Replace a function/method call with its body. |
| `go inline-variable` | Replace references to a local variable with its initializer. |
| `go invert-if` | Invert an if-else, negating the condition and swapping branches. |
| `go remove-unused-param` | Remove an unused parameter and update all callers. |
| `go move-param-left` | Move the parameter at the cursor one position left, updating callers. |
| `go move-param-right` | Move the parameter at the cursor one position right, updating callers. |
| `go fill-struct` | Fill missing fields of a struct literal with zero values or matching variables. |
| `go fill-switch` | Add the missing cases to a type or enum switch. |
| `go add-tags` | Add JSON struct tags to the fields of the enclosing struct. |
| `go remove-tags` | Clear struct tags on the fields of the enclosing struct. |
| `go change-quote` | Toggle a string literal between raw backtick and double-quote form. |
| `go split-lines` | Split arguments or composite-literal fields onto separate lines. |
| `go join-lines` | Join multi-line arguments or composite-literal fields onto one line. |

### Source actions

| Command | What it does |
| --- | --- |
| `go fix-all` | Apply every unambiguously safe fix in the file. |
| `go doc` | Browse documentation for the current package. |
| `go assembly` | Show the compiler's assembly for the function at the cursor. |
| `go free-symbols` | Report symbols used in the selection but defined outside it. |
| `go toggle-compiler-opt` | Toggle compiler optimization details (inlining, escape analysis) in diagnostics. |

### Module management

| Command | What it does |
| --- | --- |
| `go tidy` | Run `go mod tidy` so `go.mod` matches the source. |
| `go vendor` | Run `go mod vendor` to create or refresh the vendor directory. |
| `go upgrade-dependency` | Check for available upgrades of direct dependencies in `go.mod`. |
| `go vulncheck` | Run govulncheck for known vulnerabilities reachable by the code. |
| `go generate` | Run `go generate` for the `//go:generate` directive nearest the cursor. |
| `go regenerate-cgo` | Re-run cgo to regenerate Go declarations after editing C code. |

## Key bindings

The shared code-intelligence keys (`lsp` hover, definition, references,
implementation, rename, format, diagnostics, and completion) are listed
in the [Code Intelligence](./intelligence.md#preset-key-bindings) guide.
Go-specific commands are not bound by the editor presets. Run them from the
command prompt or add bindings to your existing `command.key_bindings` map.

Everything bound to a key is also available from the [command
prompt](../learn/command-prompt.md), so you can run any `lsp` or `go`
subcommand by name even when it has no binding.
