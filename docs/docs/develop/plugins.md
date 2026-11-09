---
sidebar_position: 12
---

# Plugins

A plugin is a command-line tool made available inside Rune. It is
just a program on the user's `PATH` plus a bit of configuration that points
a Rune feature at it. A plugin uses no [SDK](./sdk/index.md) and does not talk to
Rune's APIs: it is a standalone CLI, such as a faster search tool, a
formatter, or a linter, that a built-in feature or the user can call.

Plugins are the lightest way to extend Rune. When you want the opposite, a
program that communicates with Rune through the SDK to ship UI, register
commands, or stay alive for the length of a workspace, that is an
[extension](./extensions.md).

## How a plugin fits into Rune

A plugin is delivered as a [package](./packages.md): its executable is
placed on the user's `PATH`, and its configuration overlay is merged into
the user's Rune [config](../config.md) so the feature that uses the tool is
already pointed at it. The tool is available immediately after
`pkg install`, in Rune's terminals and to anything Rune launches, with no
manual setup.

Because the shared binary directory Rune installs into is always first on
the `PATH`, a plugin's command works everywhere a command works: typed in a
terminal, called by a Rune feature, or invoked from a script or hook.
[ripgrep](https://github.com/BurntSushi/ripgrep) packaged as a plugin, for
example, backs Rune's [fuzzy search](../learn/search.md) while also being an
ordinary `rg` on the `PATH`.

## Reaching back into the workspace with runectl

A plugin's CLI runs as a plain external process, so on its own it does not
know anything about the Rune workspace it was launched from. When you want a
plugin to interact with the workspace, compose it with
[runectl](./runectl.md), Rune's command-line companion. Because `runectl`
reads and writes plain text, it goes in either direction:

- **Call `runectl` from your tool.** Shell out to it as a subprocess to
  query or drive the workspace: resolve a path to a workspace URI, open a
  resource, read the focused buffer, post a notification, and more.
- **Feed `runectl` output into your tool.** Pipe a `runectl` command's
  output (for example as JSON) into your CLI for a quick integration.

`runectl` targets the right workspace automatically for anything launched
from inside Rune, so a plugin invoked there reaches the workspace with no
flags or configuration. See the [runectl guide](./runectl.md) for the full
command set and composition examples.

## Building and distributing a plugin

A plugin is distributed as a package, so building one, wiring its config
overlay, and publishing it are covered in the [Packages guide](./packages.md),
which walks through [ripgrep](https://github.com/unstablebuild/rune-plugin-rg)
as a complete, working example.

## See also

- [Packages](./packages.md): build and distribute a plugin as an installable
  package.
- [runectl](./runectl.md): let a plugin reach into the running workspace.
- [Extensions](./extensions.md): when you need to communicate with Rune
  through the SDK instead of packaging a plain CLI.
- [Console](../learn/console.md): install and manage packages with `pkg`.
