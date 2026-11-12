---
sidebar_position: 36
---

# ACP agents

Rune runs coding agents that speak the
[Agent Client Protocol](https://agentclientprotocol.com) (ACP), such as
Claude Code, Codex and Rune Agent itself, and chats with them in a tab.
ACP is the protocol Zed, JetBrains IDEs and neovim use for the same
purpose, so any agent written for one of them runs in Rune unchanged.

The agent does its own work: it reads files, edits them and runs
commands with its own tools, and asks you before it does something that
needs your approval. Rune shows the conversation, its tool calls and its
plan, and relays the questions.

## Configure agents

Add each agent under `acp.agents`, keyed by the name you will use to
start it. The shape matches Zed's `agent_servers`:

```yaml tab
acp:
  agents:
    claude:
      command: npx
      args: ["-y", "@agentclientprotocol/claude-agent-acp"]
    codex:
      command: npx
      args: ["-y", "@zed-industries/codex-acp"]
      env:
        RUST_LOG: warn
```

```python tab
config["acp"] = {
    "agents": {
        "claude": {
            "command": "npx",
            "args": ["-y", "@agentclientprotocol/claude-agent-acp"],
        },
        "codex": {
            "command": "npx",
            "args": ["-y", "@zed-industries/codex-acp"],
            "env": {"RUST_LOG": "warn"},
        },
    },
}
```

- `command` is the program to start. It is required.
- `args` are its arguments.
- `env` adds environment variables to the ones the workspace already
  has.

`$RUNE_DATADIR` in any of them expands to Rune's data directory, which is
how packages register the agents they ship. Installing the
**rune-agent** package registers `rune-agent`, so `acp new rune-agent`
works without any configuration. Installing the **claude-code** package
registers `claude`, which runs the Claude Code on your `PATH` with its
login and settings.

The two examples above need Node.js for `npx`, and each agent signs in the
way its own CLI does.

Claude Code shows no reasoning unless its `CLAUDE_CODE_THINKING_DISPLAY`
variable is `summarized`. The claude-code package sets it; add it to the
agent's `env` when you configure Claude Code yourself.

## Chat with an agent

Run these from the [command prompt](./command-prompt.md):

```
acp new claude
```

starts a session with the `claude` agent and opens it in a tab of the
focused window. Sessions get a random name, such as `brave-otter`, shown
in the tab. Give one yourself with `acp new claude review`.

```
acp open brave-otter
```

opens a running session in a tab, or focuses its tab when it is already
open. The tab replays the whole conversation, then follows along.
`acp open --new brave-otter` opens another tab on the same session, to
watch it from a second window, say. Every tab on a session shows the same
conversation, at its own size, and sends to it.

```
acp close brave-otter
```

ends the session, even with its tabs open. Closing the last tab of a
session ends it too, unless the agent is in the middle of a turn or waits
for your permission. The session then keeps running and shows in the
[status inbox](./terminal/program-status.md#the-status-inbox) under its
name, as working or blocked, and as done once the turn ends. Choose it
there, or run `acp open`, to bring its tab back; close it there to end it.
Press `<tab>` to complete agent and session names.

In the chat, type a message and press `<enter>` to send it. Attached files
are sent to the agent as links. While the agent works, `<ctrl-c>` stops
the turn. When the agent asks for permission, the chat shows its options;
dismissing the question rejects the request once.

The chat looks and edits like the Rune Agent chat. It reads the same
`extensions.rune-agent.config` keys: those under
[Appearance](../agent/config.md#appearance), the
[status bar](../agent/config.md#status-bar) and `editor_modal_start_insert`.
Its message box edits like your `editor.mode`. Reloading the config
restyles the status bar and the message box border of open chats, as it
does for Rune Agent's.

Each tool call shows the tool's name and its arguments, as Rune Agent's
chat does. ACP itself does not name tools, so the name comes from what
Rune Agent and Claude Code add to their tool calls. Other agents' calls
show their kind, such as `read` or `execute`, followed by the agent's
title for the call.

When the agent hands work to a subagent, as Claude does with its Agent
tool, the chat shows the subagent as a single tool call: what it was asked,
the tools it runs nested inside, and its report as the result. A
permission request from a subagent names the subagent that asks.

### Chat commands

Messages that start with `/` are commands:

| Command | What it does |
| --- | --- |
| `/model [<model>]` | Switches the session's model, or lists the models the agent offers. |
| `/effort [<level>]` | Switches the reasoning effort, or lists the levels. |
| `/mode [<mode>]` | Switches the agent's mode, or lists its modes. |
| `/close` | Ends the session, like `acp close`. |

Each of the first three works when the agent offers that setting. Any
other command, such as the agent's own `/review`, is sent to the agent as
your message; `/` completes the commands the agent advertises.

## How agents run

Each configured agent runs as **one process per workspace**, started the
first time you open a session with it and kept until the workspace closes.
All of the agent's sessions share that process, so a second `acp new`
starts no new process.

If the process exits, its sessions end and their tabs show the last lines
it wrote to its error output. The next `acp new` starts it again. Changes
to an agent's configuration apply the next time it starts.

## Use Rune from an agent

Agents start with the same variables as commands you run with `!` from the
command prompt: `RUNE_SOCKET`, `RUNE_TOKEN`, `RUNE_CERT`, `RUNE_DATADIR` and
`RUNE_INSTALLDIR`. With them, [`runectl`](../develop/runectl.md) and the
SDK connect to the workspace the agent runs in, so the agent and the tools
it runs can use Rune's language servers, syntax trees, notifications and
the rest of its APIs. Install runectl with `pkg install runectl`, and point
the agent at it the way you would any harness, for example with
[Rune's skills](../agent/claude-code.md#install-runes-skills-into-claude-code).

## Trust

Agents run unsandboxed, with your user's permissions, like any program you
run in a terminal. Only configure agents you trust.

Rune does not read or write files, or run commands, on an agent's behalf:
it offers agents none of ACP's file system or terminal capabilities.
When an agent calls back into Rune, it is authorized as a plugin: Rune asks
before it runs workspace commands, and grants its other access
automatically unless you set `authorizer.auto_authorize_extensions` to
`false` (see [Too many permission prompts?](../agent/config.md#too-many-permission-prompts)).

## For extension authors

### Run agents from an extension

Extensions get the same agents and sessions through `Workspace.ACP`, which
needs `extensionapi.PermissionACP`. Sessions an extension starts are
visible to `acp open`, and the other way around:

```go
svc := w.ACP(ctx)
s, err := svc.NewSession(ctx, acpapi.NewSessionRequest{Agent: "claude"})
if err != nil {
	return err
}
events := s.Events()
defer events.Close()
if err := s.Prompt(ctx, []acp.ContentBlock{acp.TextBlock("Summarize README.md")}); err != nil {
	return err
}
for {
	ev, ok := events.Next(ctx)
	if !ok {
		return events.Err()
	}
	switch {
	case ev.Update != nil && ev.Update.AgentMessageChunk != nil:
		// A piece of the agent's answer.
	case ev.Permission != nil:
		// Answer it with s.Respond, or leave it to the chat tab.
	case ev.Done != nil:
		return nil
	}
}
```

`acp` is `github.com/coder/acp-go-sdk`, and `acpapi` is
`github.com/unstablebuild/rune-go-sdk/api/acpapi`. A session is an
append-only log of events: every `Events` iterator starts from the first
event and then follows along live, so any number of viewers can watch the
same session. `acpapi.Apply` folds events into the session's current
state: its config options, commands, plan, usage and pending permissions.

A session's events also cover its subagents. `ev.Subagent` announces a
subagent or changes its state, and `ev.Message` and `ev.MessageChunk`
carry messages between sessions. `ev.Session` names the subagent session
an event came from, and is empty for the session itself, so an
`ev.Update` with a `Session` is a subagent's own activity. `Apply` tracks
subagents in `SessionState.Subagents`, and leaves the rest of the state to
the session's own updates.

### Write an agent

An agent is a program that speaks ACP on its standard input and output.
With the SDK, implement `acpapi.Agent` and serve it with `acpstdio`:

```go
func main() {
	err := acpstdio.Serve(context.Background(), newAgent(), os.Stdin, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
}
```

Standard output carries the protocol, so log to standard error.
`acpapi.Log` implements the event log a session needs: append updates to
it as the agent works, call `Ask` to request a permission and wait for the
answer, and `Done` to end the turn. Since the program speaks plain ACP, it
also runs in every other ACP client. `rune-agent acp` is built this way.

`acpstdio.Dial` is the other side: it starts talking to an agent over any
pair of streams and returns an `acpapi.Agent` for it.
