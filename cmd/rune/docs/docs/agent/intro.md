---
sidebar_position: 1
sidebar_label: Intro
---

# Rune Agent

Rune Agent is the AI coding assistant built into Rune. It lives in the
same workspace as your code, so it can read and edit files, search and
navigate the codebase, run commands, and carry a conversation, all
without leaving the editor.

The agent is distributed as the `rune-agent` package. Install it from
Rune's [console](../learn/console.md) with `pkg install rune-agent` (or run
`console pkg install rune-agent` from the command prompt).

This page introduces what the agent is and what it can do. More detailed
guides will follow in this section.

## Two ways to ask

The agent has two entry points, both [command prompt](../learn/command-prompt.md)
commands, depending on how much you need from it:

- **Quick question (`?`)**: a one-off question about the code you are
  looking at, answered in a floating window. See below.
- **Chat (`agent`)**: `agent` opens a conversation tab. This is the full
  experience: a back-and-forth dialogue where the agent can use tools,
  edit files, run code, and work through a task with you. You can pass a
  saved conversation ID to resume it, and an optional model to use for the
  session. Without an explicit model, chat uses the `default` alias (see
  [Models](#models)).

Type either at the command prompt. When the editor is in modal mode, the
chat input is itself a
[vim editor](../learn/vim-editor.md), so you get the same motions and
text objects you use everywhere else while composing messages.

### Quick questions with `?`

`? <message>` asks a one-off question and shows the answer in a floating
window, without opening a conversation tab. It is meant to be fast: each
`?` is its own throwaway session with no saved history.

What the model sees with `?`:

- **The files you have open.** The full contents of every open and focused
  file are put in context, along with their paths, so you can ask about
  the code in front of you without copying anything in.
- **Your project instructions.** Any `AGENTS.md` guidance for the
  workspace is applied, so answers respect your conventions.
- **The workspace root**, for orientation.

It does **not** receive your cursor position, your selection, or your
conversation history. So phrase the question to say what you mean ("what
does the retry loop in this file do?") rather than relying on where the
cursor is.

`?` can still use tools when it needs to, so it can read another file or
check a reference to answer well. But it is tuned to answer quickly and
avoid wandering the codebase. That makes it a good fit for:

- "What does this file (or function) do?"
- "Why might this be failing?"
- "How do I use this API / write this in Go?"
- Quick explanations and one-line fixes you want to read, not apply.

Reach for a full **chat** instead when you want the agent to make changes
across the project, work through a multi-step task, or keep context over a
back-and-forth.

By default `?` uses the same model as chat. If you prefer a faster or
cheaper model for quick questions, point the `query` alias at one (see
[Models](#models)); whenever that alias is set, `?` uses it instead.

## Skills

Skills are focused capability bundles the agent loads on demand, and
sub-agents let it spawn scoped, parallel workers such as `explore` and
`plan`. See [Skills](./skills.md) for how they are discovered, managed,
and authored.

## Memory

We are building a cutting-edge memory system for Rune Agent that lets it
carry durable learnings across conversations, such as architecture
decisions, project conventions, and prior fixes, and surface them when
they are relevant. It is in active development and will land in an
upcoming Rune Agent release.

## The `agent` console command

Alongside `?` and `agent`, Rune Agent registers an `agent` command in
Rune's [console](../learn/console.md) for inspecting and managing models,
conversations, skills, tools, and configuration. Run `agent help` to see
everything it offers. The most useful subcommands:

| Command | What it does |
| --- | --- |
| `agent models` | List available models with their context window sizes. |
| `agent model [<id>]` | Show the default model, or a conversation's model. |
| `agent effort [<level>]` | Show or set the default reasoning effort. |
| `agent max_tokens [<n>]` | Show or set the max output tokens. |
| `agent chats list` | List saved conversations. |
| `agent chats show <id>` | Show a conversation's history. |
| `agent chats export [--audit] <id>` | Export a conversation or its audit log. |
| `agent chats compact <id>` | Compact a conversation into a summarized copy. |
| `agent chats fork <id>` | Fork a conversation at a chosen message. |
| `agent chats clear <id>` | Clear a conversation (its contents are archived). |
| `agent skills ...` | Inspect and manage skills (see above). |
| `agent tools` | List the tools the agent can use. |
| `agent mcp` | Show MCP server status and tool stats. |
| `agent config` | Show the current configuration. |

## Focused chat commands

When you are focused on an open chat tab, use the Rune
[command prompt](../learn/command-prompt.md)'s chat commands to act on that
conversation:

| Command | What it does |
| --- | --- |
| `chatmodel [<model>]` | Show or switch the focused chat's model. |
| `chateffort [<level>]` | Show or set the focused chat's reasoning effort. |
| `chatmaxtokens [<n>]` | Show or set the focused chat's max output tokens. |
| `chatskill <skill> [args]` | Load a skill into the focused chat. |
| `chatclear` | Clear the focused chat and archive its previous contents. |
| `chatcompact` | Compact the focused chat into a summarized copy. |
| `chatfork` | Fork the focused chat at a selected message. |
| `chatreviewchanges` | Review everything the focused chat changed as one editable diff. |
| `chatexport [--audit]` | Export the focused chat or its audit log. |
| `chatlog` | Show the focused chat's LLM token audit log. |
| `chataddsymbol [<symbol>]` | Attach a named symbol, or the symbol under the editor cursor, to the last focused chat. |

These are command-prompt commands, not `agent` console command subcommands and not
chat-message slash commands. They are useful when you want completion, history,
or key bindings from Rune's normal command surface while operating on the chat
you are currently viewing.

## Attaching workspace context

Type `#` at a word boundary in the chat composer to search workspace files and
symbols. Keep typing to fuzzy-search the list, then press `<enter>` or `<tab>` to
attach the selected result. The typed `#query` is replaced with the result's
readable name, styled by `inline_attachment_attr`, so the message keeps a clear
inline reference to what was attached.
File discovery starts at the workspace. As the query becomes a path, Rune
reevaluates where to search, so relative directories, absolute paths, and home
paths such as `#~/Desktop/` list files from that location. Symbol results remain
in the same fuzzy list.

File attachments give the agent the file's contents. Symbol attachments include
the symbol's definition, references, and documentation. Attachments stay with
the conversation after you send the message. Click a sent file attachment to
open it in an editor tab, or click a sent symbol attachment to open its rendered
context. Before sending, click the `ˣ` on an attachment to remove it.

Use `chataddsymbol` when the code you want is already under the editor cursor.
The command resolves that exact file position, which distinguishes local
variables and other same-named symbols, then attaches a snapshot of its
definition, references, and documentation to the last chat that had focus. You
can open the command prompt from the editor without switching back to the chat.

Pass a name when there is no useful cursor position:

```
chataddsymbol pkg.Symbol
```

The optional symbol argument supports completion from symbols referenced in the
workspace. Named attachments use the same name-based lookup as symbols selected
through `#`.

## Reviewing changes

A long session can touch a lot of files, and reading the transcript back one
`apply_patch` call at a time is a poor way to judge the result.
`chatreviewchanges` collects the focused chat's surviving net changes into a
single diff and opens it in a floating window.

The diff is syntax highlighted and carries a few lines of surrounding code
around each hunk, read from the file as it stands now, so you can judge a change
against the code it lives in. Patches that failed are left out, because they
never reached your files. Use
[`review_context_lines`](./config.md#behavior) to widen or narrow how much
surrounding code you see.

Inverse edits made later in the chat cancel each other before the review is
shown. Every remaining change is checked against the file it touched, and
anything the workspace no longer shows is left out. Work the agent undid later,
or that you reverted yourself, stays out of the way of the changes that still
hold. Take that as corroboration rather than a verdict: a change is equally
invisible when later work rewrote the same lines, so a heavily reworked file can
drop out of a review even though the agent did change it. Nothing here consults
version control, and the review never folds the current state of your files into
the diff. When none of a conversation's changes survive, the review says so
instead of opening.

The window is a real editor buffer, so you can move through it with the motions
you use everywhere else and type straight into it. That is the point: write your
comments where the code they refer to is.

Close the window with `<ctrl-w>`. If your editor is not a
[vim editor](../learn/vim-editor.md), `<esc>` closes it too.

If you edited the diff, the result is attached to the composer as a **changes
review** and travels with your next message, so the agent reads your comments
next to the code that prompted them. Leave the diff untouched and nothing is
attached. Only the most recent review is kept, so reopening and editing again
replaces it, and clicking the attachment drops it.

## Models

The agent works with configurable model providers, including OpenAI,
Codex, Anthropic, Claude, Gemini, and local models. You can switch the
focused conversation's model or reasoning effort with the `chatmodel` and
`chateffort` [command prompt](../learn/command-prompt.md) commands.

### Model aliases

Which model the agent uses for each kind of work is controlled by named
**aliases**:

- `default`: the model new chats use when you do not pass one.
- `query`: the model `?` uses for quick questions.
- `compact`: the model used to compact (summarize) long conversations.
- `dream`: the model used for memory consolidation.

An alias is just a name that resolves to a real model, and it works
anywhere a model is accepted. You manage aliases with the `models alias`
command from the [Rune console](../learn/console.md):

| Command | What it does |
| --- | --- |
| `models alias` | List the aliases and the models they resolve to. |
| `models alias set <name> <provider/model>` | Point an alias at a model. |
| `models alias remove <name>` | Clear an alias. |

For example, to make chat use Codex's flagship model and route quick
questions, compaction, and memory consolidation to smaller, cheaper
models:

```
models alias set default codex/gpt-5.5
models alias set query codex/gpt-5.4-mini
models alias set compact codex/gpt-5.4-mini
models alias set dream codex/gpt-5.4-mini
```

The `models alias <name> <provider/model>` shorthand (without `set`) does
the same thing. Run `models` to see every available `provider/model`
name.

#### When an alias is unset

`default` is the one alias to set if you only set one. Any other alias
that you leave unset (`query`, `compact`, `dream`) falls back to
`default`, so setting `default` alone gives every kind of work a sensible
model. Set the others only when you want them to differ.

`default` itself, when unset, resolves to the **flagship model of the
provider you most recently signed in to**. So once you authenticate a
provider (see [Providers](./providers.md)), every alias works out of the
box, and signing in to a newer provider moves them over automatically.
Set an alias explicitly whenever you want to pin a specific model
instead.

## Connecting external tools (MCP)

Rune Agent is an MCP (Model Context Protocol) client. Declare external
MCP servers in an `.mcp.json` file and the agent connects to them,
discovers their tools, and makes them available in chat, extending what
it can do with third-party integrations.

## Project instructions

The agent reads `AGENTS.md` files from your project, walking up from the
workspace root, and folds those instructions into how it works. Use them
to teach it your conventions, so its output matches your project without
repeating yourself each session.

For the full list of `extensions.rune-agent.config` settings, including
hooks and appearance, see [Configuration](./config.md).
