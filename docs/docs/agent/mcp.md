---
sidebar_position: 9
---

# MCP servers

Rune Agent is an MCP (Model Context Protocol) client. Connect it to MCP
servers and their tools join the agent's own: the agent can search your
issue tracker, query a database, or call any other integration a server
offers, right from chat.

Rune connects to two kinds of servers:

- **Local servers** run as a command on the workspace host and talk to
  Rune over standard input and output.
- **Remote servers** run elsewhere and Rune reaches them over HTTP. Many
  hosted services offer one, and most ask you to sign in.

## Declaring servers

Servers come from two places:

- **The workspace.** An `.mcp.json` file at the workspace root declares
  servers for everyone who opens the project, so a team can check its
  integrations into the repository.
- **Your Rune config.** `extensions.rune-agent.config.mcpServers` declares
  servers for every workspace you open. It uses the same format as
  `.mcp.json`.

When both declare a server with the same name, the workspace's entry is
used.

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
    },
    "linear": {
      "type": "http",
      "url": "https://mcp.linear.app/mcp"
    }
  }
}
```

The same servers in your Rune config:

```yaml tab
extensions:
  rune-agent:
    config:
      mcpServers:
        linear:
          type: http
          url: https://mcp.linear.app/mcp
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "mcpServers": {
                "linear": {
                    "type": "http",
                    "url": "https://mcp.linear.app/mcp",
                },
            },
        },
    },
},
```

Rune connects to every server in the background when the agent starts, so
you can start chatting right away. A chat opened while servers are still
starting tells you which ones, and their tools join the conversation as
soon as they connect.

Chats also tell you about every server whose tools they cannot use: one
that needs you to [sign in](#signing-in), one you did not
[trust](#trusting-workspace-servers), and one that failed, along with the
command that fixes it. Open chats are told as soon as it happens, and new
chats when they open.

## Server settings

| Key | Applies to | What it does |
| --- | --- | --- |
| `type` | all | `stdio` for a local server, `http` for a remote server, or `sse` for a remote server that only speaks the older HTTP+SSE transport. Optional: a `command` means `stdio` and a `url` means `http`. `streamable-http` and `streamableHttp` are accepted as names for `http`. |
| `command`, `args` | local | The command that starts the server, and its arguments. It runs in the workspace root, on the workspace host. |
| `env` | local | Extra environment variables for the server. |
| `url` | remote | The server's address. It must use `https`, unless it points at this machine. |
| `headers` | remote | Headers sent with every request to the server, for servers that take an API key instead of a sign-in. |
| `headersHelper` | remote | A shell command that prints a JSON object of headers. Rune runs it before each connection and sends its headers over the ones in `headers`, for credentials that expire. |
| `timeout` | all | Longest time a single tool call may take, in milliseconds. Values under `1000` are ignored. |
| `oauth` | remote | Overrides for signing in, see [Signing in](#signing-in). |

Rune only sends `headers`, the output of `headersHelper` and your sign-in
to the server's own address. Requests that take the conversation to other
hosts, such as redirects, go out without them.

`headersHelper` runs on the workspace host, from the workspace root, and
asks for your approval like every other command the agent runs. It has
ten seconds to print its headers.

```json
{
  "mcpServers": {
    "internal": {
      "type": "http",
      "url": "https://mcp.internal.example.com/mcp",
      "headers": { "X-Team": "platform" },
      "headersHelper": "./scripts/mcp-token.sh"
    }
  }
}
```

:::note
The `sse` transport is deprecated in the MCP specification. Use it only
for servers that do not support `http` yet.
:::

### Environment variables

`command`, `args`, `env` values, `url`, `headers` values and
`oauth.clientSecret` can refer to environment variables, so secrets stay
out of `.mcp.json`:

- `${VAR}` is replaced with the value of `VAR`, or nothing when it is
  unset.
- `${VAR:-default}` is replaced with `default` when `VAR` is unset or
  empty.

Nothing else is expanded, and command substitution is an error. A server
that refers to a variable you have not set still connects, and `agent mcp`
lists the missing variable as a warning.

```json
{
  "mcpServers": {
    "search": {
      "url": "https://search.example.com/mcp",
      "headers": { "Authorization": "Bearer ${SEARCH_API_KEY}" }
    }
  }
}
```

Rune's own credentials, `RUNE_TOKEN`, `RUNE_CERT`, `RUNE_SOCKET`,
`IDE_TOKEN` and `IDE_CERT`, always expand to nothing, whatever default
the reference gives, and are never passed to local servers.

## Trusting workspace servers

A repository you clone decides what its `.mcp.json` runs and where it
connects, so Rune asks you before it connects to any server the workspace
declares. For a local server, the prompt shows:

- the command line that starts the server;
- the names of the environment variables it sets;
- the environment variables of yours it receives.

For a remote server, it shows:

- the server's address, with the host in bold;
- the names of the headers Rune would send;
- the environment variables of yours it receives;
- the `headersHelper` command, if there is one.

Choose **Trust** to connect and remember your answer for this workspace,
**Trust once** to connect until Rune exits, or **Don't connect**. Rune asks
again whenever the workspace changes the server's command, arguments,
environment, address, headers, `headersHelper` or `oauth` settings.

Trusting a local server does not replace Rune's
[command approvals](../develop/extensions.md#command-approvals): starting
it, and running a `headersHelper`, still goes through them. The approval
prompt names the MCP server the command starts.

Servers you declare in your own Rune config are trusted without asking.

A server you did not trust shows up in `agent mcp` as not trusted. To
review it again, run:

```
agent mcp trust <name>
```

## Signing in

Most hosted servers need you to sign in. Rune never opens your browser on
its own: a server that needs a sign-in shows a notification, and
`agent mcp` lists it as needing sign-in. When you are ready, run:

```
agent mcp login <name>
```

Rune opens the server's sign-in page in your browser, and prints its
address in case the browser does not open. Once you approve, the browser
returns to Rune, the server connects, and its tools join every open
chat. You have five minutes to finish.

If the browser cannot reach Rune after you approve, copy the address it
ends on and pass it to Rune:

```
agent mcp login <name> --callback <address>
```

Rune keeps your sign-in in the operating system's keychain, falling back
to Rune's local storage when no keychain is available, and renews it as
it expires, so you sign in once. To forget a sign-in, run:

```
agent mcp logout <name>
```

Rune also revokes the sign-in with the server when the server supports
it.

### Sign-in settings

Most servers need no settings: Rune identifies itself to the server's
authorization server, or registers itself where the authorization server
allows it. For servers that need a client registered ahead of time, set
`oauth`:

| Key | What it does |
| --- | --- |
| `oauth.clientId` | The client ID registered with the authorization server. |
| `oauth.clientSecret` | The client's secret, if it has one. Use an environment variable rather than writing it into `.mcp.json`. |
| `oauth.callbackPort` | The port of the address the browser returns to, `http://127.0.0.1:<port>/callback`, for clients registered with an exact address. Rune picks a free port otherwise. |
| `oauth.scopes` | The scopes to request, separated by spaces, in place of the ones the server asks for. |
| `oauth.authServerMetadataUrl` | Where to read the authorization server's metadata, for servers that do not publish it where the MCP specification says. |

```json
{
  "mcpServers": {
    "crm": {
      "type": "http",
      "url": "https://mcp.crm.example.com/mcp",
      "oauth": {
        "clientId": "rune-desktop",
        "clientSecret": "${CRM_CLIENT_SECRET}",
        "callbackPort": 8765
      }
    }
  }
}
```

## Tools

A server's tools are named `mcp__<server>__<tool>`, so they never take the
place of the agent's built-in tools, and two servers can offer tools with
the same name. Characters other than letters, digits, `_` and `-` become
`_`, and very long names are shortened.

Servers can add, change and remove tools while connected. Rune follows
along, and open chats pick up the change on their next turn.

Tools return text, images, and links to resources. Images reach the
model, so the agent can look at a screenshot or a chart a server
returns.

A tool may ask you for more information while it runs, such as a choice
or a value to fill in. Rune shows the question in the chat, one field at
a time, and you can answer or decline. When a server asks you to visit a
web page, Rune shows the address first and opens it only if you choose
**Open**.

## Staying connected

When a remote server drops the connection, Rune reconnects on its own,
trying five times with a growing delay before it reports the server as
disconnected. A server that rejects a sign-in Rune cannot renew is marked
as needing sign-in, with a notification. While a server is reconnecting
or waiting for a sign-in, its tools tell the agent so instead of failing
silently.

After fixing a problem, connect a server again with:

```
agent mcp reconnect <name>
```

`agent mcp reconnect all` reconnects every server that failed or needs
sign-in.

## Remote workspaces

Local servers run on the workspace host, next to your code. Remote servers
are reached from the machine running Rune, and so is the sign-in. In an
SSH workspace, a `localhost` address therefore names your own machine,
not the workspace host, and Rune warns you about it in a notification and
in `agent mcp`. To use a server that only listens on the workspace host,
declare it as a local server, or forward its port to your machine.

## Commands

| Command | What it does |
| --- | --- |
| `agent mcp` | Show every server's transport, address or command, server name and version, protocol version, status, tool and call counts, and warnings. Same as `agent mcp list`. |
| `agent mcp login <name>` | Sign in to a remote server through the browser. |
| `agent mcp login <name> --callback <address>` | Finish a sign-in whose redirect could not reach Rune. |
| `agent mcp logout <name>` | Forget the sign-in of a remote server and disconnect it. |
| `agent mcp reconnect <name>\|all` | Connect a server again, or every server that failed or needs sign-in. |
| `agent mcp trust <name>` | Review a server the workspace declares, and connect it if you trust it. |
