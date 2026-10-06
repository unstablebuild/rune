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
happily inside a Rune terminal like any other CLI, and with a little
configuration it works noticeably better: it can raise Rune
notifications, highlight the workspace that needs your attention, and
reach into Rune's own language and syntax tooling.

This guide is about pointing the harness you already use at Rune's
capabilities. None of it is required to run Claude Code in Rune; it just
makes the two fit together.

## How Claude Code reaches Rune

Everything below is glued together by [`runectl`](../develop/runectl.md),
Rune's command-line companion, which you install separately with
`pkg install runectl`. Any command Claude Code runs from a Rune terminal,
including its hooks, talks to the right workspace automatically with no
extra setup.

## Notify Rune when Claude is done

Claude Code supports [hooks](https://docs.claude.com/en/docs/claude-code/hooks):
shell commands it runs at lifecycle points such as finishing a turn or
asking for permission. You can wire those hooks to `runectl notify`,
which posts a Rune notification:

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

Put this in your project's `.claude/settings.json` (or your global
`~/.claude/settings.json`):

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "jq -r '.message // \"claude finished\"' | { IFS= read -r msg; runectl notify success \"$msg\"; }"
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

Each hook reads the hook's JSON payload from stdin with `jq` and passes a
message through to `runectl notify`. Adjust the levels and wording to
taste.

## Use Rune as a Claude Code MCP server

`runectl` can also act as an [MCP](https://modelcontextprotocol.io)
server, exposing Rune's syntax-tree and language-server tooling to any
MCP client. Claude Code is an MCP client, so you can give it Rune's tools
by declaring `runectl mcp` as a server.

Add a `.mcp.json` at your project root (Claude Code discovers it
automatically):

```json
{
  "mcpServers": {
    "rune": {
      "type": "stdio",
      "command": "runectl",
      "args": ["mcp"],
      "env": {}
    }
  }
}
```

That is the whole setup: `runectl mcp` starts a stdio MCP server bound to
the current workspace. Through it Claude Code gets:

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

## Install Rune's skills into Claude Code

Rune Agent ships a set of [Agent
Skills](https://docs.claude.com/en/docs/claude-code/skills) that teach a
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

### Make them discoverable

Claude Code discovers skills from `.claude/skills/` in your project and
`~/.claude/skills/` in your home directory. The installed package exposes
a stable, unversioned path at
`~/.local/share/rune/lib/rune-agent/`, so copy (or
symlink) the `code-*` skills from there into one of Claude Code's
directories:

```bash
# Per project, for everyone who clones the repo:
cp -R ~/.local/share/rune/lib/rune-agent/skills/code-* .claude/skills/

# Or globally, for all your projects:
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

## Steer the harness with `CLAUDE.md`

Claude Code reads a `CLAUDE.md` from your project the way Rune Agent
reads [`AGENTS.md`](./intro.md#project-instructions). Use it to tell Claude
Code which Rune capabilities are available.

Pick the snippet that matches your setup. If you installed the Rune skills,
prefer the skills-first version. If you only connected the Rune MCP server, use
the direct MCP tools version instead.

### Skills-first example

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

### Direct MCP tools example

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
- `lsp_rename`: rename a symbol across the workspace.
- `lsp_prepare_rename`: check whether a rename is valid.
- `lsp_code_actions`: discover quick fixes and refactorings.
- `lsp_formatting`: format a file with the language server.
- `lsp_range_formatting`: format a range with the language server.

Prefer these over manual find-and-replace, ad hoc formatting commands, or full
build commands when a focused language-server result is enough.
```
