---
sidebar_position: 9
sidebar_label: C and C++ (beta)
title: C and C++
---

# C and C++ <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

Rune ships [Tier 1](./supported.md#support-tiers) C and C++ support: code
intelligence through [clangd](https://clangd.llvm.org/), clangd's
refactorings and clang-tidy fixes on the
[command prompt](../learn/command-prompt.md), a dedicated
[Rune console](../learn/console.md) command for inspecting and steering
the language server, and native debugging with `lldb-dap`.

## Setup

There is no setup. The C/C++ extension locates `clangd` on the workspace
host automatically, checking the bundled install first and falling back
to well-known LLVM locations (`~/.rune/bin`, Homebrew's `llvm`, the
`/usr/lib/llvm-*` packages, `/usr/local/bin`, `/usr/bin`, the Xcode
Command Line Tools) and your shell's PATH. An LLVM installed beside the
system toolchain wins over the toolchain's own `clangd`, which is often
older.

If you want to use your own binary, point Rune at it in `config.yaml`:

```yaml tab
extensions:
  ccpp:
    config:
      lsp_path: "/path/to/clangd"
```

One `clangd` serves every C and C++ file in the workspace, including
headers. Rune starts it the first time you open a `.c`, `.cpp`, `.h` or
other C/C++ file, rooted at the workspace, and reuses it for every other
file.

## How clangd finds your compile flags

clangd gives accurate answers only when it knows how each file is
compiled: include paths, defines, language standard. It reads those
from a **compilation database**, a `compile_commands.json` written by
your build system, or from a hand-written `compile_flags.txt`.

### Generating the compilation database

When you open a C or C++ file whose project has no compilation database,
Rune offers to generate one with the project's build system:

- **Yes** generates it now.
- **Always** generates it now, and from then on generates the database of
  any project in the workspace that opens without one, without asking.
  `ccpp compdb auto off` makes Rune ask again.
- **No** leaves the project alone until Rune restarts.

Rune asks first because generating runs the project's build scripts.
Like every command an extension runs, each one also goes through Rune's
command permissions. `ccpp compdb generate` generates the database any
time, which also refreshes it after the build changes. Once the database
is written, Rune restarts clangd so files already open pick it up.

| Build system | What Rune runs | Writes |
| --- | --- | --- |
| CMake | `cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON` | `build/compile_commands.json` |
| Meson | `meson setup build` | `build/compile_commands.json` |
| Autotools | `autoreconf -i` and `./configure` when the project is not yet configured, then `bear --append -- make` | `compile_commands.json` |
| Make | `make -nBw \| bear parse-sh` | `compile_commands.json` |

[Bear](https://github.com/rizsotto/Bear), which ships with the C/C++
package, writes the database for Autotools and Make projects:

- For **Make**, Rune asks make to print the commands a full build runs
  without running them, and Bear turns that output into the database, so
  nothing is compiled. A build that hides its commands or generates
  sources first leaves the database empty; record a real build instead
  by running `bear -- make` in a terminal.
- For **Autotools**, Rune builds the project while Bear records each
  compiler call. Automake's makefiles do not survive a dry run: it would
  rerun `configure` and clear the header dependencies make tracks.
  Rebuilding keeps the entries of files that did not need compiling
  again. A project built before it had a database compiles nothing, so
  the database comes out empty; run `make clean`, then
  `ccpp compdb generate`.

Rune does not generate a database for Bazel projects. Add the
[`hedron_compile_commands`](https://github.com/hedronvision/bazel-compile-commands-extractor)
extractor and run `bazel run @hedron_compile_commands//:refresh_all`.
A folder with no build system needs a hand-written
`compile_flags.txt`, with one compiler flag per line.

### Where clangd looks

For each file, clangd walks up from the file's folder and uses the first
`compile_commands.json` or `compile_flags.txt` it finds, looking in each
ancestor and in that ancestor's `build/` subfolder. Files no database
covers get a bare compile, which is enough for syntax but misses
project includes and defines. Give those files a baseline with
`fallback_flags`:

```yaml tab
extensions:
  ccpp:
    config:
      fallback_flags: ["-std=c++20", "-Iinclude"]
```

When your build writes the database somewhere clangd does not look, say
`out/Debug/`, tell Rune where it is. The `ccpp compdb use` console
command described below pins a directory for the current workspace and
remembers the choice across restarts; the `compile_commands_dir` setting
does the same declaratively, relative to the workspace root:

```yaml tab
extensions:
  ccpp:
    config:
      compile_commands_dir: "out/Debug"
```

`ccpp compdb use` takes precedence over `compile_commands_dir`, which
takes precedence over clangd's own lookup.

## Code intelligence: the `lsp` command

The cross-language `lsp` command drives code intelligence for C and C++
the same way it works everywhere: hover, definition, declaration,
references, completion, rename, formatting with clang-format,
diagnostics, inlay hints, and call hierarchy, all with the same default
key bindings. See
[the `lsp` command](./intelligence.md#code-intelligence-the-lsp-command)
for the full table.

Diagnostics come from clang itself and, for files a compilation database
covers, from clang-tidy. Rename and references work across translation
units once clangd's background index has seen the project, which it
builds from the compilation database on first start.

### Language server logs

Rune starts clangd with stderr logging enabled and captures the output.
Run `process status` in the
[Rune console](../learn/console.md#managing-processes-with-process) to
find the clangd PID, then run `process stdio <pid>` to view its logs.
This also works after clangd exits; use `process audit` to find its
previous PID. See
[Troubleshoot](../troubleshoot.md#inspect-any-process-rune-starts) for
details.

The default log level is `error`. Configure it with `debug.log_level`:

```yaml tab
extensions:
  ccpp:
    config:
      debug:
        log_level: verbose
```

Accepted values are `error`, `info`, and `verbose`. At `info` and above
clangd logs every request and reply.

### Extra clangd flags

Pass additional flags to clangd with `args`. The most common one is
`--query-driver`, which lets clangd ask a cross compiler for its
built-in include paths:

```yaml tab
extensions:
  ccpp:
    config:
      args: ["--query-driver=/opt/toolchain/bin/arm-none-eabi-*"]
```

Rune always runs clangd with background indexing, clang-tidy, and
include-what-you-use header insertion; `args` adds to those defaults.

## Code actions: the `c` and `cpp` commands

The command prompt has `c` and `cpp` commands for the code actions and
custom requests clangd offers on the current file. Open the
[command prompt](../learn/command-prompt.md) and enter `c <subcommand>`
or `cpp <subcommand>`. Both drive the same clangd; `cpp` adds the
refactorings clangd only offers to C++ code. When several actions apply,
Rune opens a picker.

clangd offers its refactorings for the exact selection or cursor
position they apply to, and none while the file has a parse error. When
a subcommand finds nothing, Rune tells you where to put the cursor or
what to select.

### Available in both `c` and `cpp`

| Command | What it does |
| --- | --- |
| `c switch` | Open the header of the current source file, or the source of the current header. |
| `c list` | List every code action clangd offers at the cursor or selection and apply the chosen one. |
| `c quickfix` | Apply a fix for the diagnostic at the cursor, including clang-tidy fixes. |
| `c refactor` | Apply one of the refactorings clangd offers at the cursor or selection. |
| `c extract-function` | Extract the selected statements into a new function. |
| `c extract-variable` | Extract the selected expression into a new variable. |
| `c define-inline` | Move the out-of-line function body at the cursor to its declaration. |
| `c define-outline` | Move the function body at the cursor out of line. |
| `c expand-macro` | Replace the macro invocation at the cursor with its expansion. |
| `c populate-switch` | Add the missing enum cases to the switch at the cursor. |
| `c swap-operands` | Swap the operands of the binary expression at the cursor. |
| `c ast` | Show the clang AST of the selection, or of the node at the cursor. |
| `c symbol-info` | Show the name, container and USR of the symbol at the cursor. |
| `c memory-usage` | Show what clangd holds in memory, largest first. |

### C++ only

| Command | What it does |
| --- | --- |
| `cpp add-using` | Add a using-declaration for the qualified name at the cursor and drop the qualifier. |
| `cpp expand-auto` | Replace the `auto` at the cursor with the type it deduces to. |
| `cpp raw-string` | Convert the string literal at the cursor to a raw string literal. |
| `cpp remove-using-namespace` | Remove the using-directive at the cursor and qualify the names it covered. |
| `cpp scopify-enum` | Convert the enum at the cursor to a scoped enum and qualify its uses. |
| `cpp special-members` | Declare the implicit copy and move members of the class at the cursor. |
| `cpp memberwise-constructor` | Define a constructor initializing every member of the class at the cursor. |
| `cpp override-pure-virtuals` | Override the pure virtual methods the class at the cursor inherits. |

`c ast`, `c symbol-info` and `c memory-usage` open their results in a
floating window; the rest edit the buffer.

## Steering the server: the console `ccpp` command

The `ccpp` command inspects and steers clangd from the editor. It is a
[Rune console](../learn/console.md) command: open the console and run it
as `ccpp <subcommand>`, or submit a one-off from the
[command prompt](../learn/command-prompt.md) with
`console ccpp <subcommand>`.

| Command | What it does |
| --- | --- |
| `ccpp status` | Show the clangd in use, whether it is running, and the compilation database in effect for the focused file. |
| `ccpp compdb` | List the compilation databases of the focused file's project, marking the active one. |
| `ccpp compdb use <dir>` | Make clangd read `compile_commands.json` from `<dir>` and restart it. |
| `ccpp compdb clear` | Drop the compilation database override and restart clangd. |
| `ccpp compdb generate` | Generate `compile_commands.json` for the focused file's project with its build system and restart clangd. |
| `ccpp compdb auto [on \| off]` | Show or set whether a project that opens without `compile_commands.json` gets one without asking. |
| `ccpp restart` | Restart the clangd language server. |
| `ccpp version` | Show the clangd version. |
| `ccpp help [<subcommand>]` | Show the command's usage, or one subcommand's details. |

`ccpp compdb` searches the focused file's project root, its build
folders (`build*`, `builddir`, `_build`, `out`, `cmake-build-*`) and
their direct children, so CMake multi-config trees such as `out/Debug`
and `out/Release` show up side by side. Pick one with
`ccpp compdb use <dir>`; the choice is saved for the workspace and
survives restarts. When a project has no database at all, `ccpp compdb`
and `ccpp status` show the command `ccpp compdb generate` runs for it,
or, for Bazel and folders with no build system, how to write one
yourself.

A project's root, for `ccpp status` and `ccpp compdb`, is found from
the build files above the focused file: `CMakeLists.txt`,
`meson.build`, `MODULE.bazel` and `BUILD.bazel`, the Autotools
`configure.ac`, `configure`, `Makefile.am` and `Makefile.in`,
`Makefile` and `GNUmakefile`, as well as clangd's own
`compile_commands.json`, `compile_flags.txt` and `.clangd`. Build files
nest, one per source folder in a recursive Make or CMake tree, so the
root is the outermost folder built by the same system as the nearest
build file, while a nested project of another build system keeps its
own root. This only scopes the search for databases; clangd itself is
always rooted at the workspace.

## Debugging

The C/C++ package ships `lldb-dap`, an LLDB-based debug adapter for
native code, and registers it for both `c` and `cpp`, so there is
nothing to configure. Build with debug info (`-g`) and without
optimization (`-O0`) so every local keeps its debug entry, then point a
launch at the compiled binary: breakpoints, stepping, stack traces, and
variable inspection behave as expected, and standard library containers
such as `std::vector` render through LLDB's formatters.

See the [Debugger](../learn/debugger.md) guide for how debug sessions
work.
