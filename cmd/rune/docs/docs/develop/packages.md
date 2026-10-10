---
sidebar_position: 13
---

# Packages

A package is the unit Rune installs with `pkg install`: a config overlay
that wires something into Rune, plus whatever that thing needs to run. It is
the shared delivery format underneath [extensions](./extensions.md),
which communicate with Rune through the [SDK](./sdk/index.md),
[plugins](./plugins.md), which wrap command-line tools, and
[themes](../learn/themes.md#share-your-theme).

## Distributing from a git repository

You distribute your own package by pushing it to a **public git
repository** and telling users to install it by its repository ID, which is
the repository's host and path:

```
pkg install github.com/<owner>/<repo>
```

The ID works for any git host Rune can clone over HTTPS, so GitHub, GitLab,
Bitbucket, and self-hosted servers all install the same way. The host has to
carry a dot, and the path needs at least two segments, which also covers
nested groups:

```
pkg install gitlab.com/<group>/<subgroup>/<repo>
pkg install git.example.com/<owner>/<repo>
```

Rune clones the repository, reads a `config.yaml` at its root as the
[config overlay](#the-config-overlay), and installs anything the overlay
[requires](#requiring-other-packages). An extension runs from source through
the matching language toolchain; a theme-only package needs no toolchain. The
repository must be public: Rune clones anonymously over HTTPS.

### What the repository must contain

A git package is a normal repository with a `config.yaml` at its root. An
extension package also needs its source files:

- A **non-empty `config.yaml`** overlay with the settings to merge into the
  user's config. This can be a theme under `gui.themes` (see
  [Share your theme](../learn/themes.md#share-your-theme)), aliases under
  `command.aliases`, or any other Rune configuration. If it declares extensions,
  see [Registering an extension](#registering-an-extension). Every extension's
  `path` must point at something the runner can start: a Go package directory,
  a `.py` file, or a `.rs` file (see
  [source and package extensions](./extensions.md#source-and-package-extensions)).
  Point `path` back into the checkout with
  `$RUNE_DATADIR/lib/$RUNE_PKG_ID`, which resolves to your repository's files
  on disk.
- For extensions, their **sources** and project manifest, so the toolchain
  can build and run them: `go.mod` (and `go.sum`) for Go, `pyproject.toml`
  for Python, `Cargo.toml` for Rust. Config-only packages need no sources.

For extensions, a `requirements:` list naming the language package (`python`,
`go`, or `rust`) belongs in the overlay so `pkg install` provisions the
toolchain first. Rune verifies the package while cloning and aborts the
install if `config.yaml` is missing or empty,
or points an extension entrypoint at something the runner cannot start.

### Example: a Go extension repository

A minimal Go extension repository:

```text title="<host>/<owner>/<repo>"
.
├── go.mod                      # module and its dependencies
├── go.sum                      # pinned dependency checksums
├── main.go                     # entry point
└── config.yaml
```

```yaml title="config.yaml"
requirements:
  - go
extensions:
  <extension_id>:
    path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID'
```

```text title="go.mod"
module github.com/<owner>/<repo>

go 1.25

require github.com/unstablebuild/rune-go-sdk v0.0.107
```

On install, the `go` requirement provides the toolchain, and Rune runs the
package as `go -C <package-directory> run .`. The configured directory must
contain `go.mod`. Commit `go.sum`: Rune runs the module in read-only module
mode, so every dependency must already be pinned there. See the
[Go SDK guide](./sdk/go.md) for writing the extension itself, and the
[Python](./sdk/python.md) guide for the equivalent layout in that language.

### Versions: latest, tags, and commits

A git package's version is a point in the repository's history. Users select
it as the optional third argument to `pkg install`:

| `pkg install <host>/<owner>/<repo> <version>` | Installs |
| --- | --- |
| (no version) or `latest` | The tip of the default branch. Recorded as the short commit SHA, so a later commit shows up as an update. |
| a **tag** (for example `v1.2.0`) | That tag's commit, pinned. |
| a **commit SHA** | That exact commit, pinned. |

Tag your releases and users can pin to them; `pkg update-check` and
`pkg update-all` compare against the default branch's tip, so an install
that tracks `latest` picks up new commits automatically. You do not publish
or register anything: pushing a commit or tag is the release.

## What `pkg install` does

Packages are installed from the [Rune console](../learn/console.md) with
[`pkg install <name>`](../learn/console.md#installing-packages-with-pkg).
When a user installs one, Rune:

1. **Installs required packages first.** If the package's config declares a
   top-level `requirements:` list, Rune resolves each named package to its
   latest version and installs any that are not already installed, config
   merge and environment included, before the package itself. If a requirement
   cannot be installed, the whole install is aborted.
2. **Puts executables on the `PATH`.** Every executable the package ships is
   copied into Rune's shared binary directory, which is always first on the
   `PATH` of Rune's terminals and of anything Rune launches. The tools are
   available immediately, with no separate step.
3. **Merges the config overlay.** The package's config is deep-merged into
   the user's config: keys the package introduces are added, including new
   leaves under a map the user already has. A package only needs to carry the
   settings that connect it to Rune, and it will not disturb the rest of the
   user's configuration. A value the user has already customized is left as
   is, so the package never clobbers a user setting silently. The one
   exception is a value the package derives from `$RUNE_PKG_VERSION`: when
   that changes across versions, Rune shows the user the diff and asks before
   applying it. The `requirements` key itself is package metadata, not
   config: it is stripped from the merge and never appears in the user's
   config.

This is the same on a git install and a first-party archive install: the
difference is only where the files come from. A git-hosted extension usually
has no executables of its own (step 2 is a no-op for it); its toolchain comes
from a required language package. The layout the install produces on disk is
described under [the on-disk package format](#the-on-disk-package-format).

:::info[The shared binary directory is always on PATH]
The overlay does not need a `PATH` entry for a bundled executable. Rune
places its shared binary directory first on the `PATH` at startup, so a
bundled binary is reachable as soon as it is installed. That holds in Rune's
terminals too: they put the directory back in front after the shell's startup
files run, even when one of them resets `PATH`. Only set `gui.env` in the
overlay when a tool needs extra environment variables of its own, not to
expose the binary.
:::

## The config overlay

Every package carries a `config.yaml` (or `config.star`) at its root: the
**config overlay**. In a git package it is the repository's `config.yaml`;
in a first-party archive it is the file at the top of the tarball. Either
way, Rune deep-merges it into the user's [config](../config.md) at install
time (as described [above](#what-pkg-install-does)) and it is where a package
wires itself into Rune: registering an extension, requiring other packages,
and pointing at its own files. If both `config.yaml` and `config.star` are
present, `config.yaml` wins.

### Referencing the install location

If a value in the overlay must point back into the installed package, use
these variables:

| Variable | Expands to |
| --- | --- |
| `$RUNE_DATADIR` | The Rune data directory on the machine that uses the value. |
| `$RUNE_PKG_ID` | The package's name. |
| `$RUNE_PKG_VERSION` | The installed version. |

`$RUNE_PKG_ID` and `$RUNE_PKG_VERSION` are expanded when the package is
installed. `$RUNE_DATADIR` is kept as written in the user's config and
resolved on the machine that uses the value, when it starts a command or
applies an environment. One config therefore serves local, `ssh://` and
`rune://` workspaces: in a remote workspace, an extension entrypoint, a
debugger command or a `gui.env` value names the data directory on the remote
machine, where the package is installed. A config written by an earlier
version of Rune may hold the expanded path instead; the next install or update
of the package writes `$RUNE_DATADIR` back without asking.

A package's own files are reachable at `$RUNE_DATADIR/lib/$RUNE_PKG_ID`,
which resolves to the version currently in use: the cloned repository for a
git package, or the extracted archive for a first-party one. Point at those
files by joining onto it. For example, an overlay can set an
environment variable to a directory the package ships, or name a bundled
asset by its path:

```yaml title="config.yaml"
gui:
  env:
    # a directory bundled in the package, e.g. a toolchain root
    <TOOL_HOME>: '$RUNE_DATADIR/lib/$RUNE_PKG_ID'
extensions:
  <pkg>:
    config:
      # a single bundled file
      asset_path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID/<data-file>'
```

A plugin that only puts a CLI on the `PATH` usually does not need these
variables at all, since the overlay can refer to the tool by name.

### Requiring other packages

A package that depends on another Rune package declares it in a top-level
`requirements:` list in its config overlay. Each entry is a package name,
the same name users type after `pkg install`:

```yaml title="config.yaml"
requirements:
  - go
extensions:
  <extension_id>:
    path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID'
```

At install time, each requirement is resolved to its latest version and
installed in full before the declaring package, so anything the requirement
contributes (executables on the `PATH`, environment variables from its
overlay) is in place by the time the declaring package starts anything.
A requirement that is already installed is left at its installed version.
If a requirement fails to install, the declaring package's install is
aborted.

### Registering an extension

An extension package registers itself through the config overlay. Add an
entry under `extensions.<extension_id>` with a `path` to its entrypoint and,
optionally, a `config` map with defaults:

```yaml title="config.yaml"
extensions:
  <extension_id>:
    path: '$RUNE_DATADIR/lib/$RUNE_PKG_ID'
    config:
      <setting>: <default>
      <group>:
        <setting>: <default>
```

Rune starts what `path` names and passes the `config` block to the extension
as its `config.Config`. For a git package, `path` names a Go package directory,
a `.py` file, or a `.rs` file in the checkout, which Rune runs through the
required language toolchain. The example above points at a Go package at the
repository root. A first-party archive that ships a compiled binary instead
points `path` at that executable (for example `/bin/<extension_binary>`).
Including useful defaults makes the extension's configurable surface visible
in the user's config immediately after install, and the user can edit those
values like any other config. See the [Extensions guide](./extensions.md) for
the SDK side of writing the extension process, and
[source and package extensions](./extensions.md#source-and-package-extensions)
for how each entrypoint is run.

### Registering a tutorial

A package can also ship an in-IDE [tutorial](./tutorials.md) and register it
from the overlay. Include the tutorial's `.star` file in the package and add
a `tutorials.<name>` entry that points at it with the install-location
variables:

```yaml title="config.yaml"
tutorials:
  <name>: '$RUNE_DATADIR/lib/$RUNE_PKG_ID/<tutorial>.star'
```

After install, the tutorial is available like any other. See the
[Tutorials guide](./tutorials.md) for how to author one and register it
additively.

## The on-disk package format

Whichever way a package is delivered, Rune installs it into the same layout
under the user's data directory (`$RUNE_DATADIR`, by default `~/.rune`). You
do not assemble this by hand for a git package (Rune builds it from your
clone), but understanding it explains where `$RUNE_DATADIR/lib/$RUNE_PKG_ID`
points and how bundled executables reach the `PATH`. In the trees below,
`<pkg>` is the package's name and `<version>` is the installed version.

A first-party package is a gzipped tar archive holding up to three kinds of
content:

```text title="<pkg>.tar.gz"
.
├── bin/                        # executables, copied onto PATH
│   ├── <tool>
│   └── <tool2>
├── lib/                        # payload, kept in place
│   ├── <shared-library>
│   └── <data-file>
└── config.yaml                 # config overlay, merged into the user config
```

Installing it, or cloning a git repository, produces this layout:

```text title="~/.rune/"
.
├── bin/                            # package executables, copied here by base name
│   ├── <tool>                      #   (this directory is always first on PATH)
│   └── <tool2>
├── pkg/
│   └── <pkg>/
│       └── <version>/              # the archive extracted, or the repo cloned, verbatim
│           ├── bin/
│           ├── lib/
│           └── config.yaml
├── lib/
│   └── <pkg> -> pkg/<pkg>/<version>/   # symlink to the in-use version
│                                       #   ($RUNE_DATADIR/lib/$RUNE_PKG_ID)
└── config.yaml                     # user config, with the overlay merged in
```

Three kinds of content, three destinations:

- **Executables.** Any non-hidden file with an execute bit set is copied
  into Rune's shared binary directory by its base name, and that directory is
  always first on the `PATH`. A `bin/` directory is the convention, not a
  requirement, and the file can be a compiled binary or a script with a
  shebang. Because names are taken by base name, keep each executable's file
  name unique. A git-hosted extension usually ships no executables at all: it
  runs from source through a required language toolchain.
- **Everything else (the payload).** All other files stay in place under
  `pkg/<pkg>/<version>/`, reachable through
  [`$RUNE_DATADIR/lib/$RUNE_PKG_ID`](#referencing-the-install-location). For a
  git package this is the whole checked-out repository, including the
  extension's sources; for a first-party archive it is data files, shared
  libraries, or a bundled toolchain.
- **The config overlay.** The [`config.yaml`](#the-config-overlay) at the
  root, merged into the user's config at install time.

### Example: a first-party plugin (ripgrep)

A [plugin](./plugins.md) that wraps a command-line tool is the simplest
first-party package: one binary plus an overlay that points a Rune feature at
it. Rune's own [ripgrep](https://github.com/BurntSushi/ripgrep) (`rg`) plugin
backs [fuzzy search](../learn/search.md): the archive carries the `rg` binary
under `bin/`, and the overlay wires the two commands fuzzy search reads:

```yaml title="config.yaml"
extensions:
  fuzzy_search:
    config:
      file:
        command: rg -l ""
      line:
        command: rg --color never -n --no-heading --max-columns 500 ""
```

It refers to `rg` by name because the binary is already on the `PATH`.
Everything else in the user's `extensions` and `fuzzy_search` configuration
is left untouched by the merge. A user installs it by name from the
[console](../learn/console.md):

```
pkg install rg
```

The [ripgrep package](https://github.com/unstablebuild/rune-plugin-rg) is a
complete, working example of this format.

:::info[This format is internal]
Third-party developers do not build or host these archives. They are how
Unstable Build ships first-party packages (language runtimes, the Rune Agent,
`runectl`, plugins like the one above), installed by a bare name. To
distribute your own extension, [use a git repository](#distributing-from-a-git-repository).
:::

## See also

- [Plugins](./plugins.md): package a CLI so a Rune feature can use
  it.
- [Extensions](./extensions.md): package a program that communicates with
  Rune through the SDK.
- [Console](../learn/console.md): install and manage packages with `pkg`.
- [Config](../config.md): how Rune configuration is structured and merged.
