---
sidebar_position: 9
---

# Monorepos

A monorepo is a single repository that holds many projects, often in
several languages, side by side. Rune was built with monorepos in mind:
search and navigation stream results as they are found and scale to
millions of files, and code intelligence roots a separate language
server at each project it discovers rather than trying to load the
whole tree at once.

This guide explains how that works across every language, and the few
habits that keep a large workspace fast.

## How Rune discovers projects

When you open a workspace, Rune does not assume the folder you opened is
a single project. Each language extension looks for that language's
**project markers**, the manifest files that mark where a project
begins:

| Language | Project markers |
| --- | --- |
| [Go](./go.md) | `go.mod`, `go.sum`, `go.work` |
| Rust | `Cargo.toml` |
| Python | `pyproject.toml`, `requirements.txt` (or `.lock`, `.in`), `.venv` |
| [TypeScript and JavaScript](./typescript.md) | `tsconfig.json`, `jsconfig.json`, `package.json` |

Discovery happens in two ways:

- **At the workspace root.** If the folder you opened carries a marker,
  its language server starts right away, rooted at the workspace.
- **On demand, as you open files.** When you open a source file, the
  extension walks up from that file to the nearest enclosing folder that
  carries a marker for its language, and starts a language server rooted
  at that folder. Each project gets its own server the first time you
  open one of its files, and that server is reused for every other file
  in the same project.

So in a monorepo with, say, a Go service, a Rust crate, and a Python
worker, you end up with one language server per project, each scoped to
its own subtree. Opening a Go file in one service never pulls the Rust
crate or the Python worker into that server's view.

TypeScript and JavaScript differ within their own projects: one server
serves every `tsconfig.json` below its root, so Rune roots it at the
outermost folder with a marker. The packages of a JavaScript workspace
share that server, and find references and rename cross between them.

A source file with no marker in any folder between it and the workspace
root gets no code intelligence, because there is no project to root a
server at. This is deliberate: a stray script deep in the tree does not
silently spin up a language server over a whole subtree. Add the
appropriate manifest (`go mod init`, `cargo init`, or a
`pyproject.toml`) at the project root and reopen the file.
TypeScript and JavaScript are the exception: their language server
checks a file with no project as an inferred project, so such a file is
served from the workspace root.

## Search and navigation scale with the repo

Code intelligence is scoped per project, but **search is not**: the
fuzzy file finder, content search, and structural search all range over
the entire workspace, so you can jump anywhere in the monorepo from a
single prompt.

They stay fast on large trees because they **stream**. Rune walks the
workspace with several workers in parallel and feeds results into the
fuzzy picker as they are found, so the list starts filling immediately
instead of waiting for a full scan to finish. The walker has been tuned
against a tree of several million files; you can keep typing to narrow
the results while the scan is still running, and cancel it at any time
with `<ctrl-c>`.

The walk skips what you would expect: directories ignored by
`.gitignore` (such as `node_modules`, `target`, `.venv`, and build
output) are not traversed, so vendored and generated files do not drown
out your own code or slow the scan.

For the commands and keybindings, see the [Search](../learn/search.md)
guide. Its main entry points in a monorepo are:

- `searchfile` (<KeyBinding command="searchfile" />): fuzzy-find any file by name across every
  project.
- `searchtext` (<KeyBinding command="searchtext" />): fuzzy-search file contents across the
  workspace.
- `searchast` (run `searchfunc`, `searchvar`, or `searchtype` from the command
  prompt): find a function, type, or
  variable definition anywhere in the workspace using structural Tree-sitter
  queries.

### Very large workspaces

`searchtext` uses a built-in scanner by default. On an exceptionally
large workspace, you can point it at an external tool like
[ripgrep](https://github.com/BurntSushi/ripgrep) or the silver searcher
instead, either per invocation or through the extension's `command`
config key:

```
searchtext rg --color never -n --no-heading --max-columns 500 ""
```

## Keeping a big workspace fast

The single most effective habit: **open your workspace at the root of
the project you are working in, not at the top of the whole monorepo.**

Rune scopes a language server to the project it discovers, but the file
and text search still range over everything under the workspace root.
If you open the entire monorepo, every search sees every project. If you
open just the subproject you are editing (for example, the one Go
module or the one Rust crate), search is naturally narrowed to it, and
the language server has less surrounding context to consider.

A few more tips:

- **Keep `.gitignore` accurate.** Because the walker skips ignored
  directories, adding generated output, dependency caches, and vendor
  folders to `.gitignore` directly shrinks what every search has to
  scan. This is the highest-leverage tuning for a large repo.
- **Let discovery do its job.** You do not need to pre-open or configure
  each project. Opening one of its files is enough to bring its language
  server up, scoped to that project.
- **Mind root-level markers.** If the workspace root itself carries a
  marker (a top-level `go.mod`, `Cargo.toml`, `pyproject.toml`, or
  `.venv`), a language server is rooted at the whole workspace and takes
  the whole tree as its project. That is exactly what you want for a
  single-project repo, and usually not what you want when the root is
  just a container for many subprojects. In that case, prefer opening
  the subproject directly, or keep the shared markers out of the very
  top level.

## What this looks like in practice

Say your repository is laid out like this:

```
my-monorepo/
  services/
    api/            go.mod        (a Go service)
    billing/        go.mod        (another Go service)
  crates/
    engine/         Cargo.toml    (a Rust crate)
  workers/
    ingest/         pyproject.toml (a Python project)
  scripts/
    deploy.py                     (a loose script, no project)
```

Opening `my-monorepo` as your workspace and then editing files gives
you:

- Opening `services/api/main.go` starts a Go language server rooted at
  `services/api`. Opening a file in `services/billing` starts a second,
  independent Go server rooted there.
- Opening `crates/engine/src/lib.rs` starts rust-analyzer rooted at
  `crates/engine`.
- Opening `workers/ingest/app.py` prepares that project's Python
  environment and starts a language server rooted at `workers/ingest`.
- Opening `scripts/deploy.py` gets no language server, because there is
  no Python marker between it and the workspace root. If you want code
  intelligence there, give `scripts/` a `pyproject.toml`.
- `searchfile`, `searchtext`, and `searchast` see all of it, streaming
  results from every project into one picker.

If you spend the day in `services/api`, opening the workspace at
`services/api` instead of `my-monorepo` keeps searches scoped to that
service while everything above still works exactly the same way.

:::note[GOPATH projects]
Go's pre-modules GOPATH layout has no `go.mod` marker and is not
supported for code intelligence. Run `go mod init <name>` at each Go
project's root to convert it. See the [Go guide](./go.md) for details.
:::
