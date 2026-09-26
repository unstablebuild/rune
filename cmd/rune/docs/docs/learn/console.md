---
sidebar_position: 2
---

# Rune Console

The Rune console is a read-evaluate-print loop for Rune's built-in commands.
These are the commands best run interactively: ones whose output is worth
streaming line by line, that report progress as they work, or that update
global Rune state. From a single prompt you install packages, sign in to
model providers, manage local models, and drive the
[debugger](./debugger.md).

When you type a command that is not a Rune built-in, the console runs it as an
ordinary command on your `PATH`, just like a regular terminal. That means you
can pipe the output of a Rune built-in into the usual POSIX utilities to trim
and filter it, exactly as you would in a normal shell.

It is not a terminal emulator (those are separate, opened with
`terminalnew`). The console is a durable tab with its own prompt: a small
read-evaluate-print loop wired into the same machinery as Rune's
[command prompt](./command-prompt.md), so every command you can run there is
also available here, with output that scrolls above the prompt.

## Opening the console

Open the console from the [command prompt](./command-prompt.md) with:

```
console
```

The first time you run it, Rune opens a durable **console** tab anchored at
the current workspace's root. Running `console` again focuses the same tab,
so there is one console per workspace.

You can also run a single command without dropping into the prompt by
passing it as arguments:

```
console pkg install rune-agent
```

This opens (or focuses) the console tab and submits the line for you. If a
command is already running in that console, Rune interrupts it with
`<ctrl-c>` first.

There is no default key binding for `console`; bind one yourself if you use
it often.

<Platform when="darwin">

For example, to mirror the companion terminal's `<shift-meta-enter>`, bind the
console to `<shift-alt-enter>`:

```yaml tab
command:
  key_bindings:
    "<shift-alt-enter>": "console"
```

```python tab
config["command"]["key_bindings"]["<shift-alt-enter>"] = "console"
```

</Platform>
<Platform when="linux">

The presets already use `<alt-shift-enter>` for the companion terminal, so
pick another chord, such as `<ctrl-shift-alt-enter>`:

```yaml tab
command:
  key_bindings:
    "<ctrl-shift-alt-enter>": "console"
```

```python tab
config["command"]["key_bindings"]["<ctrl-shift-alt-enter>"] = "console"
```

</Platform>

## What you can run

The console prompt accepts three kinds of input, all on the same line:

1. **Rune commands** for managing the IDE, such as `pkg`, `models`, and
   `debugger`.
2. **External programs** on your `PATH`, run by Rune's built-in,
   in-process shell interpreter.
3. **Full shell syntax** around either of the above: pipes, `;`, `&&` and
   `||`, redirections, `$VARIABLES`, and command substitution with
   `$(...)`.

So `pkg install runectl` runs a Rune command, `ls -la | grep test` runs a
real pipeline, and the two share one prompt and one history.

Every command's working directory is the workspace root, so relative paths
resolve against your project rather than wherever Rune was launched from.
On a remote (`ssh://`) workspace, external programs run on the remote host.

### Built-in commands

Run `help` to list everything the current console offers, or `help <command>`
for one command's details. The exact set depends on which extensions are
installed in the workspace, but the core commands are:

| Command | What it does |
| --- | --- |
| `help` | Show available commands, or details for one command. |
| `login` | Authenticate your Rune client. |
| `logout` | Log out from the current session so you can login with a different account. |
| `pkg <command> [<args>]` | Install and manage packages from Rune's official distribution. |
| `models <command> [<args>]` | Inspect and manage LLM providers and local models. |
| `agent <command> [<args>]` | Inspect and manage [Rune Agent](../agent/intro.md) models, chats, tools, skills, and configuration (available once the `rune-agent` package is installed). |
| `debugger <subcommand> [...]` | Control a [debug session](./debugger.md) via the Debug Adapter Protocol. Start with `debugger initialize <langID>`, then `launch` or `attach`, then `configured`. |
| `extensions (status\|info\|start\|stop\|restart\|logs) ...` | Manage workspace extensions. |
| `authorizer (list\|revoke) [<permission>]` | Manage persisted plugin authorizer decisions. |
| `lsp <subcommand> [<args>...]` | Language Server Protocol commands. |
| `process` | Process management: inspect and control processes Rune tracks. |
| `network <subcommand>` | Manage this machine's membership in your [private network](./network.md) of Rune instances. |

## Installing packages with `pkg`

Packages are how you add Rune Agent, `runectl`, and other components.
`pkg` installs them from Rune's official distribution, and it installs
third-party extensions straight from a **public git repository** by its ID:

| Command | What it does |
| --- | --- |
| `pkg install [--lang] <package> [<version>]` | Install a package by name from Rune's distribution, or a git-hosted extension by its `<host>/<owner>/<repo>` ID (GitHub, GitLab, Bitbucket, or a self-hosted server). With no version, installs or updates to the latest; a git package also accepts a tag or commit SHA as the version. Any executables it ships are added to your `PATH` for terminal sessions, and become usable immediately, with no separate `use` step. `--lang` lists and installs language (runtime) packages, which are otherwise hidden from completion. |
| `pkg remove <package> [<version>]` | Remove a package. With a version, removes only that version; otherwise removes all versions. |
| `pkg use <package> <version>` | Switch to an installed version (downloading it first if needed). |
| `pkg current <package>` | Print the version currently in use. |
| `pkg describe <package> [<version>]` | Show a package's notes and the notes for a release version in formatted markdown. With no version, describes the latest. |
| `pkg update-all` | Upgrade all installed packages to the latest version. |
| `pkg update-check` | Check for available updates across installed packages. |

For example, to install Rune's agent and its command-line companion:

```
pkg install rune-agent
pkg install runectl
```

To install a third-party extension published in a git repository, use its
repository ID, optionally pinned to a tag:

```
pkg install github.com/<owner>/<repo>
pkg install gitlab.com/<group>/<subgroup>/<repo>
pkg install github.com/<owner>/<repo> v1.2.0
```

See [Packages](../develop/packages.md#distributing-from-a-git-repository) for how these
are built and versioned.

## Managing models with `models`

The `models` command signs you in to model providers and manages locally
cached models. See the [Providers](../agent/providers.md) and
[Local Models](../agent/local-models.md) guides for the full walkthrough;
in short:

```
models providers openai add work    # store an OpenAI API key
models providers codex login        # sign in to a ChatGPT subscription
models local list                   # list locally cached models
```

## Editing, history, and completion

The console input line is edited with the same [editor](./vim-editor.md)
you use everywhere else in Rune, so your motions and text objects work
while composing a command. The most useful keys:

| Key | Action |
| --- | --- |
| `<enter>` | Run the current line. |
| `<tab>` | Complete the command or argument under the cursor. |
| `<ctrl-r>` | Fuzzy-search previous commands. |
| `<up>` / `<down>` | Step through command history. |
| `<ctrl-l>` | Clear the output above the prompt. |
| `<ctrl-c>` | Interrupt the running command. |

When a command name or argument has more than one possible completion,
`<tab>` opens a fuzzy-search overlay: keep typing to filter, move with
`<up>` and `<down>` (or `<ctrl-j>` and `<ctrl-k>`), accept with `<enter>`
or `<tab>`, and cancel with `<esc>`. Completion covers Rune commands and
their subcommands, programs on your `PATH`, and file paths for the
commands that take them.

History is per console and persists across restarts. You can tune it in
`rune.star`:

```yaml tab
console:
  max_history: 2000          # commands to remember
  modal_start_insert: true   # open the prompt in insert mode (modal editor)
  prompt: "rune> "           # input-line prefix (defaults to "> " when empty)
```

```python tab
config["console"] = {
    "max_history": 2000,         # commands to remember
    "modal_start_insert": True,  # open the prompt in insert mode (modal editor)
    "prompt": "rune> ",          # input-line prefix (defaults to "> " when empty)
}
```

## Managing processes with `process`

Rune tracks every process it spawns, not just the ones you start from the
console. That includes the programs extensions and Rune's own internals run in
the background, such as language servers and the debug adapter. The
`process` command lets you inspect and control all of them without leaving
Rune:

| Command | What it does |
| --- | --- |
| `process status` | List running processes. |
| `process audit` | List all processes, including those that have exited. |
| `process tree` | Show the parent-to-child process tree. |
| `process info <pid>` | Show detailed information for one process. |
| `process stdio <pid>` | Show the process's standard output and standard error destinations and captured output. |
| `process signal <pid> [<n>]` | Send a signal (default `SIGTERM`). |
| `process stop <pid>` | Gracefully stop a process, escalating to `SIGKILL` if needed. |

On a remote workspace these act on the remote host. Sensitive values in a
process's environment are redacted in `process info`.

## Managing extensions with `extensions`

Extensions are programs that bundle the Rune [SDK](../develop/sdk/index.md) to
integrate with Rune at a deeper level. The `extensions` command lets you see
their status and control their lifecycle:

| Command | What it does |
| --- | --- |
| `extensions status` | Show the status of every workspace extension. |
| `extensions info <id>` | Show detailed information for one extension. |
| `extensions start <id> <cmdAndArgs> [--config <json>]` | Start a workspace extension. |
| `extensions stop <id>` | Stop a running workspace extension. |
| `extensions restart <id>` | Restart a known workspace extension. |
| `extensions logs <id> [--tail <N>]` | Show an extension's captured stderr logs, optionally limited to the last `N` lines. |

## Reviewing plugin permissions with `authorizer`

When an extension or plugin (i.e. `runectl`) asks for a permission, Rune can
remember your answer so it does not prompt again. The `authorizer` command
lets you review and undo those persisted decisions:

| Command | What it does |
| --- | --- |
| `authorizer list` | List persisted plugin permission decisions. |
| `authorizer revoke <permission>` | Revoke a persisted decision, so the plugin is prompted again next time. |

## See also

- [Command Prompt](./command-prompt.md): the prompt the console is built on.
- [Debugger](./debugger.md): the `debugger` command in depth.
- [Providers](../agent/providers.md) and [Local Models](../agent/local-models.md): the `models` command.
- [Rune Agent](../agent/intro.md): installed with `pkg install rune-agent`.
