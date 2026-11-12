---
sidebar_position: 8
---

# Claude Code

This guide is about running Anthropic's standalone Claude Code CLI inside Rune.
If you want Rune Agent to use Anthropic models through your Claude Pro/Max
subscription, see [Providers › Claude](./providers.md#claude-sign-in-with-claude)
instead.

[Claude Code](https://www.anthropic.com/claude-code) is Anthropic's
third-party coding agent. It is **not** part of Rune Agent, but it runs
inside a Rune terminal like any other CLI. Recent versions report their
status to Rune automatically. With the **claude-code** package and its
Claude Code plugin, Claude Code also treats Rune as its IDE: it sees
what you select, reviews its edits with you in Rune, checks Rune's
diagnostics, and uses Rune's language servers.

This guide is about pointing the harness you already use at Rune's
capabilities. None of it is required to run Claude Code in Rune; it just
makes the two fit together. The last section goes the other way:
[using Claude Code skills and subagents in Rune
Agent](#use-claude-code-skills-in-rune-agent).

## See Claude's status automatically

Run `claude` in a Rune terminal. Claude Code **v2.1.295 or later** reports
whether it is working, waiting for your input, or done through the
[Program Status Protocol](../learn/terminal/program-status.md) (OSC 7501).
Rune displays that status on the terminal tab and the workspace bar, so
you can see which session needs attention while working elsewhere.

This needs **no hooks, Claude Code plugin, or `runectl` installation**.
Claude Code detects Rune's support for the protocol itself.

If no status appears, check `claude --version`, run directly in Rune
rather than inside tmux, and make sure
`CLAUDE_CODE_DISABLE_TERMINAL_TITLE` is not set to `1`. Claude Code's
background sessions do not report terminal status, even while attached.
See Anthropic's [terminal status
guide](https://code.claude.com/docs/en/terminal-config#see-session-status-in-your-terminal)
for these requirements.

## Connect Claude Code to Rune

Connecting takes a Rune package, which runs alongside each workspace,
and a Claude Code plugin, which teaches Claude Code to use it. It
requires Claude Code **v2.1.300 or later**.

### 1. Install the package

Install the package from Rune's [console](../learn/console.md):

```
pkg install claude-code
```

Each open workspace starts it, and Claude Code then finds the workspace
as an IDE named **Rune**. Until the plugin is connected, Rune shows a
dialog with the remaining steps; choose **Already installed** to dismiss
it for good. It also closes by itself the first time the plugin
connects.

### 2. Install the plugin

Start `claude` in a Rune terminal and run:

```
/plugin marketplace add unstablebuild/rune-claude-code
/plugin install rune@unstablebuild
```

The plugin is installed once for your user and works in every Rune
workspace.

### 3. Start Claude Code

Run `claude` from Rune's [command prompt](../learn/command-prompt.md).
It opens a terminal tab and starts Claude Code there, connected to the
workspace you ran it in. Options go to Claude Code, so `claude --resume`
picks a past session to resume.

Claude Code that you start yourself in a Rune terminal connects too: the
package turns on Claude Code's IDE auto-connect in Rune's terminals
opened after it is installed. When workspaces are nested, see [Several
workspaces](#several-workspaces).

To connect only when you ask, set the variable to `false` under
[`gui.env`](../config.md#gui) in your config. Sessions started with
`claude` from the command prompt still connect; in one you start
yourself, run `/ide` and pick Rune:

```yaml
gui:
  env:
    CLAUDE_CODE_AUTO_CONNECT_IDE: 'false'
```

### Check the connection

In a Claude Code session started from a Rune terminal:

- `/ide` lists **Rune** as connected.
- `/mcp` lists the `plugin:rune:rune` server as connected.

### What you get

- **Your selection as context.** Select code in Rune and Claude Code
  shows it beside the prompt and includes it with your next message.
  Run `claudesend` from Rune's [command
  prompt](../learn/command-prompt.md) to insert an `@`-mention of the
  current file, with the selected lines, into Claude Code's prompt.
- **Edits reviewed in Rune.** When Claude Code proposes an edit, Rune
  opens the change as a diff in a split beside your editor. Press `a`
  (or `y`) to accept it, `r` (or `n`) to reject it, or `e` to edit the
  proposal and save it to accept your version. `q` or `<esc>` closes the
  diff and leaves the answer to Claude Code's prompt in the terminal.
  Nothing is written to the file until you answer.
- **Diagnostics after edits.** Claude Code checks the diagnostics Rune's
  language servers report for each file it edits, and is told about any
  new problems its edit introduced.
- **Rune's language-server and syntax tools.** Claude Code can find
  definitions, references, implementations, symbols and diagnostics, and
  preview renames, through the same language servers Rune uses (the
  `lsp_*` and `syntax_*` tools). It can also open a file in Rune
  (`rune_open`), mark lines for you to look at (`rune_highlight`), and
  post a Rune notification (`rune_notify`). The plugin includes a skill
  that tells Claude Code when to prefer these over text search.
- **Symbols in the prompt.** Type a qualified name such as `config.Lo`,
  or `@` and the start of a package name, and Claude Code's prompt
  offers matching symbols from the workspace. An `@`-mention of a
  symbol, such as `@config.Load`, attaches the file that defines it.
- **References beside text search.** When Claude Code's Grep tool
  searches for an identifier, Rune adds the language server's references
  to that symbol to the result, so Claude Code can tell real uses from
  comments and other text.
- **A diagnostics pane.** Run `/rune-diagnostics` in Claude Code to open
  a pane listing the workspace's diagnostics; it refreshes after each
  turn, and `r` refreshes it on demand. It needs a language server that
  reports diagnostics for the whole workspace; otherwise the pane says
  so.

### Several workspaces

Each Rune workspace serves its own connection. A session started with
`claude` from the command prompt connects to the workspace you ran it
in. One you start yourself in a terminal connects to the workspace that
contains its working directory.

When one open workspace's root is inside another's, for example with a
workspace open on your home folder, a session you start yourself in the
inner workspace matches both, and Claude Code does not connect to either
by itself. Start it with `claude` from the command prompt instead, or
run `/ide` and pick Rune. Rune's tools in `/mcp` connect to the
workspace whose terminal started the session either way.

### Troubleshooting

- **The `rune` server failed in `/mcp`.** The session was not started in
  a Rune terminal of a workspace with the package installed. The error
  says to run `pkg install claude-code`. The rest of Claude Code works
  as usual.
- **Rune does not appear in `/ide`.** Check `claude --version`, start
  Claude Code from the workspace's own Rune terminal, and make sure the
  package is installed with `pkg current claude-code`.
- **Claude Code does not connect by itself.** The terminal was opened
  before the package was installed, or the workspace is nested in
  another open one (see [Several workspaces](#several-workspaces)).
  Start Claude Code with `claude` from the command prompt, or run `/ide`
  and pick Rune.
- **A status line asks you to update.** The package and the plugin are
  released together. Update the package with `pkg install claude-code`
  in Rune, or the plugin with `claude plugin update rune@unstablebuild`
  in a terminal, as the line says.

## Without the package and plugin

You can also connect Claude Code to Rune by hand with
[`runectl`](../develop/runectl.md), Rune's command-line companion. This
gives Claude Code Rune's language-server and syntax tools, notifications,
and skills, but not the IDE integration above: no shared selection, diff
review in Rune, diagnostics after edits, or symbols in the prompt. The
notification hooks below also work alongside the package and plugin.

### Install runectl

Install it from Rune's [console](../learn/console.md):

```
pkg install runectl
```

Start Claude Code inside the Rune terminal for the workspace you want it
to use. Its commands, hooks, and stdio MCP server inherit Rune's connection
environment and target that workspace automatically. Rune may ask you to
authorize access when a capability is first used. A Claude Code session
started outside Rune does not inherit this connection.

### Notify Rune when Claude is done

Claude Code supports [hooks](https://code.claude.com/docs/en/hooks):
shell commands it runs at lifecycle points such as finishing a turn or
asking for permission. You can wire those hooks to `runectl notify`,
which posts a Rune notification. These hooks are optional; they add
explicit notifications rather than enabling the automatic status above:

```
runectl notify <level> <message>
```

The level is one of `error`, `warn`, `info`, or `success`.

The reason this is more than a toast: when the notification targets a
workspace that is **not** currently in focus, Rune highlights that
workspace's tab with the level's color. So if you keep several
workspaces open and let Claude work in the background, the tab of the
workspace that finished (or needs permission) lights up, and you know
where to look without hunting through tabs.

Merge this into your project's `.claude/settings.json` (or your global
`~/.claude/settings.json`), keeping any existing hooks:

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "runectl notify success 'Claude finished'"
          }
        ]
      }
    ],
    "PermissionRequest": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "jq -r '.tool_name // \"tool\"' | { IFS= read -r msg; runectl notify warn \"claude needs permission: $msg\"; }"
          }
        ]
      }
    ]
  }
}
```

- The **Stop** hook fires when Claude finishes a turn. It posts a
  `success` notification, so the originating workspace's tab is
  highlighted in green when you are looking elsewhere.
- The **PermissionRequest** hook fires when Claude wants to run a tool
  that needs your approval. A `warn` notification flags the workspace so
  you can switch over and answer.

The permission hook uses `jq` to read the tool name from the hook's JSON
payload on stdin, so install `jq` if you use that example. Adjust the
levels and wording to taste.

### Use Rune as a Claude Code MCP server

`runectl` can also act as an [MCP](https://modelcontextprotocol.io)
server, exposing Rune's syntax-tree and language-server tooling to any
MCP client. Claude Code is an MCP client, so you can give it Rune's tools
by declaring `runectl mcp` as a server.

Add a `.mcp.json` at your project root, or merge the `rune` entry into an
existing `mcpServers` object:

```json
{
  "mcpServers": {
    "rune": {
      "type": "stdio",
      "command": "runectl",
      "args": ["mcp"]
    }
  }
}
```

Start Claude Code from that project's Rune terminal, approve the
project MCP server when prompted, and run `/mcp` to check its connection.
`runectl mcp` starts a stdio MCP server bound to the Rune workspace.
Through it Claude Code gets:

- **Syntax tools** for tree-sitter searches and structural queries
  (`syntax_search`, `syntax_search_node`, `syntax_query`,
  `syntax_query_node`).
- **Language-server tools** backed by the same LSP servers Rune uses,
  covering definitions, references, implementations, hovers, completion,
  rename, code actions, diagnostics, symbols, call and type hierarchy,
  and more (`lsp_definition`, `lsp_references`, `lsp_diagnostics`,
  `lsp_rename`, and the rest of the `lsp_*` family).

These let Claude Code navigate and reason about your code with
language-aware precision instead of plain text search.

The available language features depend on the language servers configured
in Rune. This connection exposes MCP tools; it does not install a Claude
Code LSP plugin or add symbol completion to Claude Code's prompt.

:::note Refactoring previews

The MCP `lsp_rename` tool reports affected files and edit counts without
applying the rename. The formatting tools return proposed text edits
without applying them. To apply a rename or formatting through `runectl`,
use its `lsp rename` or `lsp formatting` CLI commands instead; those also
offer `--dry-run` for previewing changes.

:::

### Install Rune's skills into Claude Code

Rune Agent ships a set of [Agent
Skills](https://code.claude.com/docs/en/skills) that teach a
harness to use Rune's semantic tooling through `runectl`. They are plain
`SKILL.md` files in the same format Claude Code reads, so you can install
them into Claude Code directly and get the same code-navigation muscle
Rune Agent has.

The skills are distributed with the **rune-agent** package. Install it
from Rune's [console](../learn/console.md) with `pkg install rune-agent` (or
`console pkg install rune-agent` from the command prompt). The package
bundles a `skills/` directory containing:

| Skill | What it teaches |
| --- | --- |
| `code-navigation` | Jump to definitions, find references and implementations, resolve types (`runectl lsp`). |
| `code-diagnostics` | Read compiler and linter diagnostics for a file or the workspace (`runectl lsp diagnostics`). |
| `code-search` | Structural tree-sitter search and queries (`runectl syntax`). |
| `code-structure` | Outline a file's symbols and structure. |
| `code-understanding` | Inspect types, signatures, and docs for a symbol. |
| `code-refactoring` | Rename symbols and apply language-server code actions. |

Every skill calls `runectl`, so it depends on
[`runectl`](../develop/runectl.md) being installed (`pkg install
runectl`). Together they give Claude Code the same grep-free, semantic
workflow Rune Agent uses.

#### Make them discoverable

Claude Code discovers skills from `.claude/skills/` in your project and
`~/.claude/skills/` in your home directory. The installed package exposes
a stable, unversioned path at
`~/.local/share/rune/lib/rune-agent/`, so copy (or
symlink) the `code-*` skills from there into one of Claude Code's
directories:

```bash
# Per project, for everyone who clones the repo:
mkdir -p .claude/skills
cp -R ~/.local/share/rune/lib/rune-agent/skills/code-* .claude/skills/

# Or globally, for all your projects:
mkdir -p ~/.claude/skills
cp -R ~/.local/share/rune/lib/rune-agent/skills/code-* ~/.claude/skills/
```

`~/.local/share/rune/lib/rune-agent/` always points at the installed
version, so this works without hardcoding a version number. A symlink
(`ln -s`) instead of a copy keeps the skills updated as you upgrade the
package. The `code-*` pattern picks up exactly the six skills above; the
directory also holds Rune Agent's own `explore` and `plan` sub-agents,
which Claude Code does not use.

Once the files are in place, Claude Code picks the skills up
automatically and invokes them when a task calls for code navigation,
diagnostics, or structural search.

### Steer the harness with `CLAUDE.md`

Claude Code reads a `CLAUDE.md` from your project the way Rune Agent
reads [`AGENTS.md`](./intro.md#project-instructions). Use it to tell Claude
Code which Rune capabilities are available.

Pick the snippet that matches your setup. If you installed the Rune skills,
prefer the skills-first version. If you only connected the Rune MCP server, use
the direct MCP tools version instead.

#### Skills-first example

Use this when you installed the `code-*` skills into Claude Code:

```md
## Rune skills

Rune code skills are available through `runectl`. Prefer them over text-based
search when they apply.

- `code-navigation`: jump to definitions, find references, locate declarations,
  resolve type definitions, and find implementations. Prefer this over grepping
  for where a symbol is defined or used.
- `code-structure`: list symbols in a file or search for functions, types, and
  methods across the workspace. Prefer this over reading an entire file just to
  understand its structure.
- `code-search`: run structural tree-sitter searches. Use this for AST patterns
  such as composite literals, function calls, or declarations. It is more precise
  than regex for structural code patterns.
- `code-understanding`: get type information, documentation, and function
  signatures without reading source files end to end.
- `code-diagnostics`: get compiler and linter diagnostics from the language
  server. Prefer this before running full build commands just to discover local
  file errors.
- `code-refactoring`: rename symbols, discover code actions, and format files.
  Prefer this over manual find-and-replace or ad hoc formatting commands.
```

#### Direct MCP tools example

Use this when you configured `runectl mcp` but did not install the skills:

```md
## Rune MCP tools

When the Rune MCP server is connected as `rune`, prefer its language-server and
syntax tools over plain text search. They use Rune's semantic tooling for the
current workspace.

### Code navigation

- `lsp_definition`: jump to where a symbol is defined.
- `lsp_references`: find usages of a symbol across the workspace.
- `lsp_declaration`: find a symbol's declaration.
- `lsp_type_definition`: find the type behind a value.
- `lsp_implementation`: find concrete implementations of an interface.

Use these instead of grepping for function names, callers, or implementors.

### Code structure

- `lsp_document_symbols`: list functions, types, and variables in one file.
- `lsp_workspace_symbols`: search for symbols by name across the workspace.
- `syntax_search_node`: find functions, methods, types, variables, references,
  or namespaces workspace-wide.
- `syntax_query_node`: do the same in a single known file.

Use these instead of reading whole files just to understand their structure.

### Structural search

- `syntax_search`: run a tree-sitter query across workspace files.
- `syntax_query`: run a tree-sitter query in a single known file.

Use these for structural patterns such as function calls, composite literals, or
declarations. They are more precise than regex search for code structure.

### Understanding code

- `lsp_documentation`: get type information and documentation for a symbol.
- `lsp_signature_help`: get function parameter names and types.
- `lsp_completion`: discover available methods and fields at a position.

Use these before reading large source regions only to identify a type or
signature.

### Diagnostics and refactoring

- `lsp_workspace_diagnostics`: get workspace diagnostics.
- `lsp_diagnostics`: get diagnostics for one file.
- `lsp_rename`: preview affected files and edit counts for a rename; does not apply it.
- `lsp_prepare_rename`: check whether a rename is valid.
- `lsp_code_actions`: discover quick fixes and refactorings.
- `lsp_formatting`: get proposed formatting edits for a file; does not apply them.
- `lsp_range_formatting`: get proposed formatting edits for a range; does not apply them.

Prefer these over manual find-and-replace, ad hoc formatting commands, or full
build commands when a focused language-server result is enough.
```

## Use Claude Code skills in Rune Agent

Rune Agent reads skills in the same `SKILL.md` format as Claude Code, the
[Agent Skills](https://agentskills.io/specification) format, so most Claude
Code skills work in Rune Agent unchanged. Put a skill in one of Rune's
[skill directories](./skills.md#skill-directories), such as
`~/.agents/skills`, or add Claude Code's directory with
`agent skills add-dir`.

A few differences apply to every skill:

- **`name` and `description` are required.** Claude Code falls back to the
  directory name and the first line of the body. Rune skips a skill
  without them.
- **Claude Code's own fields are ignored**, such as `context`, `agent`,
  `argument-hint`, `hooks` and `disable-model-invocation`. In particular,
  Rune Agent may load a skill that sets `disable-model-invocation: true`
  on its own.
- **`allowed-tools` and `disallowed-tools` do nothing in an ordinary
  skill.** In Claude Code they pre-approve and remove tools while the
  skill is active. Rune Agent ignores them, so the skill loads whatever
  they hold, and its commands go through Rune's command permission prompt
  as usual.

### Sub-agents

Claude Code runs a skill with `context: fork` in a subagent of the type
its `agent` field names, and gives it the skill's body as its task. Rune
Agent ignores `context`, so it loads such a skill into the conversation
like any other.

A Rune [sub-agent](./sub-agents.md) is a skill with `type: agent`. Its body
is the sub-agent's system prompt, and its task comes from whoever runs it:
the text after `/<name>`, or the prompt the agent writes. That makes it
the counterpart of a Claude Code subagent file in `.claude/agents/`. To
port one:

1. Move it to `<skill directory>/<name>/SKILL.md`.
2. Add `type: agent`.
3. Rename `tools` to `allowed-tools` and `disallowedTools` to
   `disallowed-tools`, and port their entries as described below.
4. Set `model` to a model that `agent models` lists, or remove it to use
   the model of the conversation that spawns the sub-agent.

### Tool rules

In a sub-agent, `allowed-tools` limits the sub-agent to the tools it
allows and approves nothing, like the `tools` field of a Claude Code
subagent. Adding `type: agent` to a Claude Code skill therefore turns its
`allowed-tools` from approvals into limits, so check every entry.

| Claude Code entry | In Rune Agent |
| --- | --- |
| `Read`, `Grep`, `Glob`, `Edit` | Rune Agent's own tool names, which `agent tools` lists, such as `read_file`, `search_content` and `find_files`. An entry that names no tool allows nothing. |
| `Bash`, `Skill`, `Agent` | The same. These names are case-insensitive. |
| `Bash(git add *)`, `Bash(git add:*)` | The same. |
| `Bash(*)` | `bash` |
| `Bash(ls*)`, `Bash(* --version)` | Not accepted: write the program's name in full, as in `bash(ls *)`. |
| `Bash(${CLAUDE_SKILL_DIR}/scripts/render.sh *)` | `bash($render-chart)`, with the skill's name, which allows the programs in its `scripts/` directory. See [Skills as tools](./sub-agents.md#skills-as-tools). |
| `Skill(deploy *)` | `skill(deploy)`. Rune patterns match skill names, as in `skill(deploy-*)`. |
| `Agent(worker, researcher)`, `Task(worker)` | One entry per type: `agent(worker) agent(researcher)`. Claude Code ignores such a list in a subagent file; Rune Agent enforces it. |
| `mcp__github__*` | `github_*` |
| `Read(./.env)`, `WebFetch(domain:example.com)` | Not accepted: only `bash`, `skill` and `agent` take a pattern. |

Rune does not load a sub-agent with an entry it does not accept, and
logs a warning that names the entry.

Claude Code matches the text of a command, while Rune checks every program
a command runs, as [How commands are
checked](./sub-agents.md#how-commands-are-checked) describes. Two more
differences follow:

- **A `*` in the middle of a pattern matches a single argument.** In
  Claude Code it matches any text. `bash(git log * main)` matches
  `git log -5 main` but not `git log --oneline -5 main`. An allowed
  pattern is therefore stricter in Rune and a disallowed one looser:
  `bash(git push * --force)` does not reject
  `git push origin main --force`. Disallow a command with a trailing `*`
  instead, as in `bash(git push *)`.
- **A disallowed pattern keeps the shell.** In a Claude Code subagent,
  `disallowedTools: Bash(git push *)` removes the shell. In Rune Agent, it
  only rejects matching commands. Write `bash` to remove the shell.
