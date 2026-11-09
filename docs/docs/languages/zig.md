---
sidebar_position: 7
sidebar_label: Zig (beta)
title: Zig
---

# Zig <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

Rune ships [Tier 1](./supported.md#support-tiers) Zig support: code intelligence through
[zls](https://zigtools.org/), build-on-save diagnostics powered by the
Zig build system, zls code actions on the
[command prompt](../learn/command-prompt.md), and a dedicated
[Rune console](../learn/console.md) command for driving the `zig`
toolchain from the editor.

## Setup

There is no setup. The Zig extension locates `zls` and `zig` on the
workspace host automatically, checking the bundled install first and
falling back to well-known locations (`~/.rune/bin`,
`/opt/homebrew/bin`, `/usr/local/bin`) and your shell's PATH.

If you want to use your own binaries, point Rune at them in
`config.yaml`:

```yaml tab
extensions:
  zig:
    config:
      lsp_path: "/path/to/zls"
      zig_path: "/path/to/zig"
```

## How Rune finds your Zig project

Zig projects are built by the Zig build system. A project's root is the
folder that contains its `build.zig` build script (and, for packages,
the `build.zig.zon` manifest). If a project does not have one yet,
running `zig init` in a terminal at the project root creates both.

Rune uses those files to decide where a Zig project starts and to root
the language server there:

- **Workspace root.** If the folder you open as your workspace contains
  a `build.zig` or `build.zig.zon`, or simply has `.zig` files at the
  top level, the language server starts right away, rooted at the
  workspace.
- **Nested projects.** A `build.zig` deeper in the workspace works too.
  When you open a `.zig` file, Rune walks up from that file to the
  nearest enclosing folder with a `build.zig` or `build.zig.zon` and
  starts a language server rooted at that folder. In a repository with
  several Zig projects, each project gets its own language server the
  first time you open one of its files, and that server is reused for
  every other file in the project.

A `.zig` file with no `build.zig` in any folder between it and the
workspace root gets no code intelligence, because there is no project
to root a server at. Run `zig init` at the project root and reopen the
file. The exception is a workspace whose own root qualified (it has
`build.zig`, `build.zig.zon`, or top-level `.zig` files): the
workspace-root server already covers every file under it.

:::info
Nested-project discovery is driven by opens. Creating a `build.zig`
from a terminal or another tool does not bring a server up on its own;
opening a `.zig` file inside that project does.
:::

Working across several projects in one repository? See the
[Monorepos](./monorepo.md) guide for how per-project discovery and
workspace-wide search fit together.

## Code intelligence: the `lsp` command

The cross-language `lsp` command drives code intelligence for Zig the
same way it works everywhere: hover, definition, references,
completion, rename, formatting, and diagnostics, all with the same
default key bindings. See
[the `lsp` command](./intelligence.md#code-intelligence-the-lsp-command)
for the full table. zls does not implement go-to-implementation, code
lenses, range formatting, or call hierarchy; those `lsp` subcommands
report no results for Zig files.

### Language server logs

Rune starts zls with stderr logging enabled and captures the output. Run
`process status` in the [Rune console](../learn/console.md#managing-processes-with-process)
to find the zls PID, then run
`process stdio <pid>` to view its logs. This also works after zls exits; use
`process audit` to find its previous PID. See
[Troubleshoot](../troubleshoot.md#inspect-any-process-rune-starts)
for details.

The default log level is `info`. Configure it with `debug.log_level`:

```yaml tab
extensions:
  zig:
    config:
      debug:
        log_level: debug
```

Accepted values are `err`, `warn`, `info`, and `debug`.

## Build-on-save diagnostics

Rune sends document changes and saves to zls for every Zig file. zls uses those
notifications to surface syntax and semantic errors from its own analysis as
you type. This immediate diagnostic path is always active and does not depend
on a build step.

Some compiler errors require the complete project, including type errors across
files. For those, zls can additionally invoke the Zig build system after a save
and report the compiler's results as diagnostics. This is the separate
**build-on-save** path.

Build-on-save turns on automatically when your `build.zig` declares a
step named `check`. The conventional shape compiles your code without
installing anything:

```zig
const exe_check = b.addExecutable(.{
    .name = "check",
    .root_module = b.createModule(.{
        .root_source_file = b.path("src/main.zig"),
        .target = target,
        .optimize = optimize,
    }),
});
const check = b.step("check", "Typecheck the project");
check.dependOn(&exe_check.step);
```

When your `build.zig` has no `check` step, ordinary zls diagnostics continue to
work. Rune posts a one-time hint explaining only that the additional
full-project compiler pass is inactive. The hint appears once per project that
has a `build.zig` and no explicit `build_on_save` setting. You can add the
conventional `check` step or control that compiler pass explicitly:

```yaml tab
extensions:
  zig:
    config:
      build_on_save: true
      build_on_save_args: ["-Dtarget=wasm32-wasi"]
```

`build_on_save: true` runs the build after saves even without a `check`
step, falling back to the project's `install` step; `false` turns the feature
off. `build_on_save_args` customizes the arguments passed to `zig build`.

## zls code actions: the `zig` command

The command prompt has its own `zig` command for the code actions zls
offers on the current file. Open the
[command prompt](../learn/command-prompt.md) and enter
`zig <subcommand>`. When several actions apply, Rune opens a picker.

This command is separate from the `zig` console command for the
toolchain described below.

| Command | What it does |
| --- | --- |
| `zig list` | List every code action zls offers for the current file and apply the one you choose. |
| `zig quickfix` | Apply a quick fix for a compile error in the current file. |
| `zig refactor` | Convert the string literal at the cursor to or from its multiline form. |
| `zig organize-imports` | Sort and deduplicate the file's `@import` declarations. |
| `zig fix-all` | Apply every quick fix zls offers for the current file at once. |

zls builds these from its own analysis of the whole file, so apart from
`zig refactor` they are file-wide rather than cursor-scoped. It answers
with no actions at all while the file has a syntax error, and never for
`.zon` files.

## Driving the toolchain: the console `zig` command

The `zig` command drives the Zig toolchain from the editor. It is a
[Rune console](../learn/console.md) command: open the console and run
it as `zig <subcommand>`, or submit a one-off from the
[command prompt](../learn/command-prompt.md) with
`console zig <subcommand>`. Arguments after the subcommand are passed
through to `zig`:

| Command | What it does |
| --- | --- |
| `zig build [<step>] [<args>]` | Build the project from `build.zig`. |
| `zig test [<file>] [<args>]` | Run the project's tests. |
| `zig run [<file>] [<args>]` | Build and run an executable. |
| `zig fmt [<paths>]` | Format Zig sources in place. |
| `zig version` | Show the zig compiler version. |
| `zig reload` | Restart the language server. |
| `zig help [<subcommand>]` | Show the command's usage, or one subcommand's details. |

`zig reload` reinitializes every language server the workspace has
brought up, for toolchain changes made outside the editor.

Subcommands run at the workspace root, not in the folder of the file
you have open, so in a repository with nested `build.zig` projects pass
the project path explicitly or run `zig` from a terminal in that
folder. If no `zig` binary is found, Rune reports it and asks you to
set `extensions.zig.config.zig_path`.

## Debugging

Zig compiles to native code with DWARF debug info, so an LLDB-based
adapter such as `lldb-dap` debugs a Zig binary the same way it debugs a
C or Rust one: breakpoints, stepping, stack traces, and variable
inspection all behave as expected. Build with a debug-friendly mode
(the default `Debug` optimize mode) and point a launch configuration at
the emitted binary.

Rune does not ship a preconfigured adapter for Zig, so register one
under `debugger.zig` in your config before starting a session. See the
[Debugger](../learn/debugger.md) guide for how debug sessions work and
how to register an adapter.
