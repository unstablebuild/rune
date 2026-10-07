---
sidebar_position: 8
sidebar_label: TypeScript (beta)
title: TypeScript and JavaScript
---

# TypeScript and JavaScript <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

Rune ships [Tier 1](./supported.md#support-tiers) TypeScript and
JavaScript support. Code intelligence comes from the native TypeScript 7
compiler, [typescript-go](https://github.com/microsoft/typescript-go),
which also serves the language server protocol. On top of that you get
tsgo's code actions on the
[command prompt](../learn/command-prompt.md) and a
[Rune console](../learn/console.md) command for type-checking,
installing dependencies, and running `package.json` scripts.

One language server serves every dialect: `.ts`, `.mts`,
`.cts`, `.tsx`, `.js`, `.mjs`, `.cjs`, and `.jsx`. Code intelligence
works across them, so going to a definition from a `.tsx` component
lands in the `.ts` module that declares it.

## Setup

There is no setup. The `typescript` package bundles the TypeScript 7
compiler. It needs no Node.js runtime, and installing it also installs
the `javascript` and `tsx` grammars. Rune picks the compiler for each
project in this order:

1. The `lsp_path` setting, when you set one.
2. The project's own TypeScript 7 or later, installed by its package
   manager under `node_modules`, including the isolated layouts of pnpm
   and bun. Rune looks in the project and in every folder above it up to
   the workspace root, so a monorepo's hoisted install is found too. This
   way the server checks your code with the same compiler your build
   uses.
3. The compiler from the `typescript` package.
4. A `tsgo` or `tsc` on the workspace host's PATH, as long as it is
   TypeScript 7 or later. Older `tsc` releases do not serve LSP.

To use a compiler of your own, point Rune at it in `config.yaml`:

```yaml tab
extensions:
  typescript:
    config:
      lsp_path: "/path/to/tsgo"
```

A project pinned to TypeScript 6 or older is still analyzed by the
TypeScript 7 server, with TypeScript 7 semantics. TypeScript 7 removed
`tsconfig.json` options that older releases accept, such as `baseUrl`,
`target` ES5 and `moduleResolution` node. When the project's
`node_modules` holds an older `typescript`, Rune warns once, and
[`ts check`](#the-console-ts-command) names each removed option.
`ts version` shows both versions when they differ.

### Dependencies

The language server works without `node_modules`. Imports of packages
that are not installed show up as unresolved-import diagnostics. Rune
never installs dependencies by itself, because package install scripts
run arbitrary code. When a project declares dependencies that are not
installed, Rune shows a one-time hint. For a project at the workspace
root, the hint points at [`ts install`](#the-console-ts-command). For a
nested project, it names the package manager command to run in that
project's folder.

## How Rune finds your project

A TypeScript or JavaScript project starts at the folder that holds its
`tsconfig.json`, `jsconfig.json`, or `package.json`. TypeScript finds
the nearest `tsconfig.json` for each file on its own, so one language
server covers every package below its root. Rune roots it at the
outermost of those folders, so the packages of a monorepo share one
server:

- **Workspace root.** The language server starts right away when the
  workspace you open has one of those files, or has TypeScript or
  JavaScript files at its top level.
- **Nested projects.** When the workspace root has no marker, opening a
  file starts a server at the outermost folder between the file and the
  workspace root that has one. A monorepo in a subfolder of the
  workspace gets one server for all of its packages.
- **Loose scripts.** A file with no marker in any folder between it and
  the workspace root is served from the workspace root.

Because the packages share a server, find references and rename reach
the other packages that import a symbol. The server only searches the
projects it has loaded, which happens when you open one of their files.

A TypeScript file that no `tsconfig.json` includes is checked as an
*inferred project*, with default compiler options: no `paths` aliases,
and no packages' types, such as `@types/node`. Unlike Go, Rust, and
Zig, such a file still gets code intelligence. Rune tells you once per
server when you open one, so its unresolved imports do not come as a
surprise. Add a `tsconfig.json` that includes it to fix them.

Working across several projects in one repository? See the
[Monorepos](./monorepo.md) guide.

## Code intelligence: the `lsp` command

The cross-language `lsp` command works for TypeScript and JavaScript
the same way it works everywhere: hover, definition, type definition,
implementation, references, completion, signature help, rename,
formatting, and diagnostics. See
[the `lsp` command](./intelligence.md#code-intelligence-the-lsp-command)
for the full table. TypeScript 7.0 does not implement go to
declaration, so `lsp declaration` reports no results.

## tsgo commands: the `ts` command

The command prompt has its own `ts` command for tsgo's code actions and
its extensions to the protocol. Open the
[command prompt](../learn/command-prompt.md) and enter
`ts <subcommand>`. This command is separate from the
[`ts` console command](#the-console-ts-command).

| Command | What it does |
| --- | --- |
| `ts organize-imports` | Remove unused imports, then sort and merge the rest. |
| `ts remove-unused-imports` | Remove unused imports and keep the order of the rest. |
| `ts sort-imports` | Sort and merge imports without removing any. |
| `ts fix-all` | Apply every fix tsgo can make on its own across the file, such as adding missing imports. |
| `ts quickfix` | Apply a fix for the diagnostic at the cursor. When several fixes apply, Rune opens a picker. |
| `ts list` | List every code action at the cursor or selection and apply the one you choose. |
| `ts source-definition` | Go to the JavaScript implementation of the symbol at the cursor, where `lsp definition` stops at its `.d.ts` declaration. |
| `ts open-tsconfig` | Open the `tsconfig.json` or `jsconfig.json` that owns the current file, or report that the file is in an inferred project. |

TypeScript 7.0 ships no refactorings yet. Its code actions are the
organize-imports family, fix-all, and quick fixes that add a missing
import, implement the members of an interface a class declares, or add
the type annotations `isolatedDeclarations` requires. `ts list` shows
whatever the server offers, so new actions appear there as TypeScript
releases them.

### Profiling the language server

If you work on tsgo itself, turn on its profiling commands:

```yaml tab
extensions:
  typescript:
    config:
      developer: true
```

| Command | What it does |
| --- | --- |
| `ts cpu-profile start [<dir>]` | Start profiling the CPU of the TypeScript language servers. |
| `ts cpu-profile stop` | Stop profiling and report where each server wrote its profile. |
| `ts heap-profile [<dir>]` | Save a heap profile of a TypeScript language server. |
| `ts alloc-profile [<dir>]` | Save an allocation profile of a TypeScript language server. |
| `ts gc` | Run the garbage collector of the TypeScript language servers. |

Profiles are written on the workspace host. They go to
`/tmp/rune-tsgo-profiles` unless you pass a directory; a relative
directory is resolved against the workspace root.

## The console `ts` command

The `ts` [Rune console](../learn/console.md) command type-checks,
installs, and runs scripts. Open the console and run it as
`ts <subcommand>`, or submit a one-off from the command prompt with
`console ts <subcommand>`.

| Command | What it does |
| --- | --- |
| `ts check [<project>]` | Type-check a project with the TypeScript 7 compiler. `<project>` is a folder or a config file. A folder without a `tsconfig.json` checks every project below it. |
| `ts install [<package>/] [<args>]` | Install dependencies with the package's package manager. |
| `ts run [<package>/] [<script> [<args>]]` | Run a `package.json` script. Without a script, list them. Press `<tab>` to complete packages and script names. |
| `ts test [<package>/] [<args>]` | Run the `package.json` `test` script. A bun package without one runs `bun test`, as `bun init` sets it up. |
| `ts restart` | Restart every TypeScript language server, for example after installing `typescript@7` into the project. |
| `ts version [<project>]` | Show the language server's compiler and version, and the project's own `typescript` version when it differs. |
| `ts help [<subcommand>]` | Show the command's usage, or one subcommand's details. |

Without a project or package, a subcommand works on the file you last
focused: `ts check` checks the nearest `tsconfig.json` above it, and the
others use the nearest `package.json`. With no file focused, they work
at the workspace root. A package argument must be `.` or contain a `/`,
such as `packages/api/`, so that it is not taken for a script name or a
test runner's argument. Projects and packages are relative to the
workspace root, and `<tab>` completes them.

A `tsconfig.json` with `references`, such as the one Vite creates, lists
no files of its own. `ts check` builds it with `tsc -b` so the
referenced projects are checked, and writes the build info and any
declarations they are configured to emit, as `tsc -b` in the project
would. Other projects are checked with `tsc --noEmit -p`.

`ts install`, `ts run`, and `ts test` pick the package manager from the
package's folder, or from the nearest folder above it that names one,
as a workspace root does for its packages. A folder names it with the
`packageManager` field of its `package.json`, or else with its lockfile:
`bun.lock` or `bun.lockb` for bun, `pnpm-lock.yaml` for pnpm,
`yarn.lock` for yarn, and `package-lock.json` for npm. Without either
anywhere, it is npm. Arguments after the script name reach the script.
With npm, Rune adds the `--` that npm needs for that.

Their output appears when the command exits, so run watch modes and
development servers from a terminal instead.

## Symbol index

The [symbol index](./symbol-index.md) resolves qualified names in
TypeScript and JavaScript without a running language server. A module
is named after its file, or after its folder for an `index.*` file. For
example, `geometry.Shape` names the `Shape` class in `geometry.ts`, and
`geometry.Shape.area` names its `area` method.

The index resolves:

- references through namespace imports (`import * as geometry`),
  `import x = require()` in TypeScript, and `require()` in JavaScript;
- classes, interfaces, type aliases, enums, and functions, including
  `const f = () => …`;
- methods of classes and abstract classes;
- names re-exported from `index.*` files, including renamed ones.

Some constructs are left to the language server, which resolves them
precisely. These are default imports bound to arbitrary local names,
tsconfig `paths` aliases, bare package names resolved from
`node_modules`, dynamic `import()`, `export *` chains through more than
one `index.*` file, declaration merging, and methods assigned on
prototypes. Leaving them unresolved avoids jumping to the wrong place.

## Debugging

Rune does not ship a JavaScript debug adapter yet. Support for
debugging Node.js programs is planned.
