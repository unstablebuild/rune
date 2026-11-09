---
sidebar_position: 6
sidebar_label: Rust (beta)
title: Rust
---

# Rust <span className="badge badge--secondary" style={{fontSize: '0.5em', verticalAlign: 'middle'}}>Beta</span>

Rune ships [Tier 1](./supported.md#support-tiers) Rust support: code intelligence through
[rust-analyzer](https://rust-analyzer.github.io/), a toolchain that
installs itself through [rustup](https://rustup.rs/) and is updated
from the editor, and dedicated editor commands for Rust analysis and
toolchain management.

## Setup

There is no setup. The Rust extension bundles rust-analyzer and rustup.
The first time you open a Rust project on a machine with no toolchain,
Rune installs the stable toolchain together with the `rust-src`,
`clippy`, and `rustfmt` components, reporting progress through
notifications. Once installed, `cargo`, `rustc`, `rustfmt`, and
`cargo-clippy` are on the PATH of every terminal inside Rune.

Toolchain setup is best-effort: if it fails, rust-analyzer still starts
with whatever toolchain the host already has. Later toolchain updates
are explicit, through [the console `rust` command](#managing-the-toolchain-the-console-rust-command).

If you want to use your own language server binary, point Rune at it in
`config.yaml`:

```yaml tab
extensions:
  rust:
    config:
      lsp_path: "/path/to/your/binary"
```

## How Rune finds your Rust project

Rust code is organized into **crates**, built by Cargo, Rust's build
tool. A crate's root is the folder that contains its `Cargo.toml`
manifest, which names the crate and records its dependencies. If a
project does not have one yet, running `cargo init` in a terminal at
the project root creates it.

Rune uses that manifest to decide where a Rust project starts and to
root the language server there:

- **Workspace root.** If the folder you open as your workspace contains
  a `Cargo.toml`, or simply has `.rs` files at the top level, the
  language server starts right away, rooted at the workspace.
- **Nested projects.** A `Cargo.toml` deeper in the workspace works
  too. When you open a `.rs` file, Rune walks up from that file to the
  nearest enclosing folder with a `Cargo.toml` and starts a language
  server rooted at that folder. In a monorepo with several crates, each
  crate gets its own language server the first time you open one of its
  files, and that server is reused for every other file in the crate.

A `.rs` file with no `Cargo.toml` in any folder between it and the
workspace root gets no code intelligence, because there is no project
to root a server at. Run `cargo init` at the project root and reopen
the file.

:::info
Nested-crate discovery is driven by opens. If an external tool, like Rune's agent wants
to run semantic queries for a crate you have never opened, and no enclosing crate
already has a server running, that crate's server does not start until you open one of
its files, so code intelligence there stays unavailable until then. Opening any `.rs`
file in the crate brings the server up. Crates that belong to a Cargo workspace are
already covered by the workspace-root server, so this only affects standalone crates
with no enclosing workspace.
:::

Working across several crates in one repository? See the
[Monorepos](./monorepo.md) guide for how per-crate discovery and
workspace-wide search fit together.

## Code intelligence: the `lsp` command

The cross-language `lsp` command drives code intelligence for Rust
the same way it works everywhere: hover, definition, references,
implementation, completion, rename, formatting, and diagnostics, all
with the same default key bindings. See
[the `lsp` command](./intelligence.md#code-intelligence-the-lsp-command) for the
full table.

Rune configures rust-analyzer to use
[clippy](https://doc.rust-lang.org/clippy/), Rust's linter, as its
check-on-save command: each time you save a file, rust-analyzer runs
`cargo clippy` and reports the findings as diagnostics, so lint
warnings show up alongside compiler errors as you work.

That check-on-save pass is on top of rust-analyzer's own semantic
diagnostics, which are pulled as you type and do not wait for a save.

### Language server logs

rust-analyzer writes logs to stderr, where Rune captures them. Run
`process status` in the console to find the rust-analyzer PID, then run
`process stdio <pid>` to view its logs. This also works after rust-analyzer
exits; use `process audit` to find its previous PID. See
[Troubleshoot](../troubleshoot.md#inspect-any-process-rune-starts)
for details.

Rune sets rust-analyzer's `RA_LOG` filter to `info` by default. Override it with
`debug.log_level`:

```yaml tab
extensions:
  rust:
    config:
      debug:
        log_level: "rust_analyzer=debug"
```

The value uses rust-analyzer's native `RA_LOG` filter syntax. A simple level
such as `debug` applies globally, while a directive such as
`rust_analyzer=debug` limits verbose logging to that module.

## Rust-analyzer tools: the `rust` command

The command prompt has its own `rust` command for Rust-specific analysis,
refactoring, navigation, and workspace tools. Open the
[command prompt](../learn/command-prompt.md) and enter
`rust <subcommand>`. Most tools act on the cursor or current selection.
When several assists or targets apply, Rune opens a picker; longer output
opens in a scrollable, read-only view.

This command is separate from the `rust` console command for rustup
described below.

| Command | What it does | Availability |
| --- | --- | --- |
| `rust list` | List every assist available at the cursor or selection and apply the one you choose. | Default |
| `rust extract` | Extract selected code into an available target, such as a variable, constant, function, module, or type. | Default |
| `rust inline` | Inline an applicable call, local variable, type alias, or macro at the cursor. | Default |
| `rust rewrite` | List in-place rewrites, such as conversions, inversions, and reorderings, available at the cursor. | Default |
| `rust refactor` | List every refactoring available at the cursor or selection and apply the one you choose. | Default |
| `rust quickfix` | Apply a quick fix for the diagnostic at the cursor. | Default |
| `rust organize-imports` | Merge and sort `use` declarations in the current file. | Default |
| `rust status` | Show rust-analyzer's analysis status, scoped to the current crate when a file is open. | Default |
| `rust syntax-tree` | Show the syntax tree for the current file. | Default |
| `rust hir` | Show the high-level intermediate representation (HIR) for the item at the cursor. | Default |
| `rust mir` | Show the mid-level intermediate representation (MIR) for the function at the cursor. | Default |
| `rust interpret` | Const-evaluate the function at the cursor and show the result. | Default |
| `rust file-text` | Show rust-analyzer's current view of the file's text. | Default |
| `rust item-tree` | Show the item tree for the current file. | Default |
| `rust expand-macro` | Expand the macro invocation at the cursor. | Default |
| `rust memory-usage` | Show rust-analyzer's memory usage report. | Opt-in |
| `rust crate-graph` | Show the crate dependency graph in DOT format. | Default |
| `rust dependencies` | List the crates and versions in the workspace dependency graph. | Default |
| `rust related-tests` | List tests related to the symbol at the cursor. | Default |
| `rust recursive-memory-layout` | Show the recursive memory layout, including sizes and offsets, for the type at the cursor. | Default |
| `rust failed-obligations` | Show failed trait obligations for the item at the cursor. | Default |
| `rust diagnostics` | List rust-analyzer diagnostics for the current file, including compiler details when available. | Default |
| `rust reload-workspace` | Reload the Cargo workspace in rust-analyzer. | Default |
| `rust rebuild-proc-macros` | Rebuild the workspace's procedural macros. | Default |
| `rust run-flycheck` | Start the configured flycheck, such as `cargo check` or Clippy. | Default |
| `rust clear-flycheck` | Clear flycheck diagnostics. | Default |
| `rust cancel-flycheck` | Cancel a running flycheck. | Default |
| `rust parent-module` | Open the parent module of the current file. | Experimental |
| `rust child-modules` | Open a child module declared at the cursor. | Experimental |
| `rust open-cargo-toml` | Open the `Cargo.toml` that owns the current file. | Experimental |
| `rust external-docs` | Show the documentation URL for the symbol at the cursor. | Experimental |
| `rust join-lines` | Join the selected lines, or the cursor line, with rust-analyzer's formatting-aware edit. | Experimental |
| `rust matching-brace` | Move the cursor to the matching brace. | Experimental |
| `rust on-enter` | Apply rust-analyzer's smart-enter edit at the cursor. | Experimental |
| `rust move-item-up` | Move the item at the cursor up. | Experimental |
| `rust move-item-down` | Move the item at the cursor down. | Experimental |
| `rust ssr "<pattern> ==>> <replacement>"` | Apply a structural search-and-replace query across the workspace. | Experimental |
| `rust runnables` | List the runnable targets available at the cursor and jump to the one you choose. Use `rust run` to run one. | Experimental |
| `rust run` | Run a target at the cursor and show its output, opening a picker when several apply. | Experimental |
| `rust type` | Show type information for the selection, or hover information at the cursor when nothing is selected. | Experimental |
| `rust symbols <query> [deps]` | Search workspace types; add `deps` or `dependencies` to include dependencies. | Experimental |
| `rust hover` | Show rendered hover documentation and any attached actions, such as running a target or opening a type or implementation. | Experimental |
| `rust eval-predicate <predicate>` | Evaluate a trait predicate in the context of the item at the cursor. | Experimental |

The experimental tools expose rust-analyzer operations that are less
stable and are disabled by default. Enable all of them in `config.yaml`:

```yaml tab
extensions:
  rust:
    config:
      experimental: true
```

`rust memory-usage` is opt-in for a different reason: the released
rust-analyzer build rejects the request, so the command is only
registered when you ask for it and point `lsp_path` at a build compiled
with its `dhat` feature.

```yaml tab
extensions:
  rust:
    config:
      debug:
        memory_usage: true
```

## Managing the toolchain: the console `rust` command

The `rust` command drives rustup from the editor. It is a
[Rune console](../learn/console.md) command: open the console and run
it as `rust <subcommand>`, or submit a one-off from the
[command prompt](../learn/command-prompt.md) with
`console rust <subcommand>`. Arguments after the subcommand are passed
through to rustup:

| Command | What it does |
| --- | --- |
| `rust show` | Show the active and installed toolchains. |
| `rust toolchain <args>` | Install, list, or remove toolchains. |
| `rust default <toolchain>` | Set the default toolchain. |
| `rust component <args>` | Add, remove, or list toolchain components. |
| `rust target <args>` | Add, remove, or list cross-compilation targets. |
| `rust update [<toolchain>]` | Update Rust toolchains. |
| `rust check` | Check for updates to Rust toolchains. |
| `rust override <args>` | Manage per-directory toolchain overrides. |
| `rust which <binary>` | Show the path to a toolchain binary. |
| `rust run <toolchain> <command>` | Run a command with a given toolchain. |
| `rust doc [<args>]` | Open the Rust documentation. |
| `rust self <args>` | Manage the rustup installation. |
| `rust reload` | Re-probe the toolchain and restart the language server. |
| `rust help [<subcommand>]` | Show the command's usage, or one subcommand's details. |

Commands that can change the active toolchain (`default`, `toolchain`,
`target`, `component`, and `update`) reload the language server
automatically afterwards, so code intelligence tracks the new
toolchain without a manual step. `rust reload` does the same thing on
demand, for changes made outside the editor: it re-probes the toolchain
sysroot and reinitializes every language server the workspace has
brought up. It does not install or update toolchain binaries; use
`rust update` for that.

The console command needs the managed toolchain, so it reports
`no managed Rust toolchain: CARGO_HOME is not set` when `CARGO_HOME` is
missing from the environment Rune runs in. Rune also skips the
toolchain install in that case and leaves rust-analyzer to the host's
own Rust installation.

## Debugging

The Rust package ships `lldb-dap`, an LLDB-based debug adapter for
native code. See the [Debugger](../learn/debugger.md) guide for how
debug sessions work and how to register an adapter.
