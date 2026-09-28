---
sidebar_position: 2
---

# Configuration

Rune Agent is installed as the `rune-agent` package, so its settings live
under `extensions.rune-agent.config` in your Rune config. Rune manages the
extension's entry and `path` for you; you normally only tweak the nested
`config` block. Every key is optional and falls back to a sensible
default.

```yaml
extensions:
  rune-agent:
    config:
      agents_file: AGENTS.md
      attribution:
        commit: 'Co-Authored-By: Rune Agent ({{provider}}/{{model}}) <agent@rune.build>'
      skills:
        - .rune/skills
        - ~/.rune/skills
        - .agents/skills
        - ~/.agents/skills
      editor_modal_start_insert: true
```

## Behavior

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `agents_file` | string | `AGENTS.md` | Name of the [project instructions](./intro.md#project-instructions) file to discover. The agent walks up from the workspace root looking for files with this name and injects their contents into the system prompt. Set it empty to disable discovery. |
| `attribution.commit` | string | `Co-Authored-By: Rune Agent ({{provider}}/{{model}}) <agent@rune.build>` | Attribution text Rune Agent is instructed to append to commits it creates. `{{provider}}` and `{{model}}` expand to the resolved model. Set it to an empty string to disable commit attribution. |
| `skills` | list | the four directories below | Ordered list of [skill directories](./skills.md#skill-directories) to search. Overrides the default set when present. |
| `max_line_bytes` | integer | `500` | Truncation limit, in bytes, for a single line of `read_file` output. |
| `max_tool_output_bytes` | integer | `40000` | Truncation limit, in bytes, for a single tool's output before it is trimmed in the transcript. |
| `auto_compact_ratio` | float | `0.85` | Fraction of the model's context window that must be filled before the agent automatically compacts the conversation. |
| `review_context_lines` | integer | `3` | How many surrounding lines of code to show around each hunk when you [review a chat's changes](./intro.md#reviewing-changes). Set it to `0` to show only the lines the agent actually changed. |
| `editor_modal_start_insert` | boolean | `true` | When the modal (vi-style) composer opens, start in insert mode so the first keystroke types instead of requiring `i`. Set to `false` to open in normal mode. Only affects the modal composer. |
| `audit_enabled` | boolean | `false` | Record a per-request LLM token audit log you can review with `chatlog` or `agent chats export --audit`. |
| `force_builtin_tools` | boolean | prompt | When set, decides up front whether bare `grep`-style shell commands are rejected in favor of the agent's built-in search tools. Left unset, the agent asks you the first time and remembers your choice. |

## Too many permission prompts?

Rune asks before Rune Agent runs workspace commands or uses other protected
workspace APIs. If those prompts are getting a bit overwhelming, you can have
Rune authorize workspace commands automatically:

```yaml
authorizer:
  auto_authorize_commands: true
```

`authorizer` is a top-level Rune setting, not part of
`extensions.rune-agent.config`. This setting applies to workspace commands from
other extensions and plugins too, so only enable it if you trust the code
installed in Rune. Existing saved denials still apply.

Rune separately grants non-command extension and plugin permissions
automatically by default. Set `authorizer.auto_authorize_extensions` to `false`
if you want Rune to prompt for those permissions too.

`auto_authorize_commands` only silences command prompts for extensions from a
**verified publisher** (and only when `authorizer.auto_authorize_verified` is
also on, which it is by default). Unverified extensions always prompt before
they run a command. See [Verified publishers](../develop/extensions.md#verified-publishers)
for the full trust model.

Models are configured host-side rather than in this block. Point the
`default`, `query`, `compact`, and `dream` aliases at real models with the
`models alias` command (see [Model aliases](./intro.md#model-aliases)), and
set the output token budget with `agent max_tokens`.

## Hooks

`hooks` wires shell commands or HTTP requests to fire at defined points in
the agent loop, letting you run scripts, post to a service, inject extra
context, or block an action. The configuration mirrors
[Claude Code's hook format](./claude-code.md#notify-rune-when-claude-is-done),
so the same block works in either tool.

### Events

A hook is registered against an **event**. These are the events Rune
Agent fires:

| Event | When it fires | Matched against |
| --- | --- | --- |
| `SessionStart` | A conversation starts. | the source (e.g. `startup`, `resume`) |
| `SessionEnd` | A conversation ends. | the end reason |
| `UserPromptSubmit` | You submit a prompt, before the model sees it. | (matches all) |
| `PostToolUse` | A tool call finishes. | the tool name (e.g. `read_file`, `bash`) |
| `Stop` | The agent finishes a turn. | (matches all) |
| `SubagentStop` | A spawned [sub-agent](./skills.md#sub-agents-and-planning) finishes. | (matches all) |
| `PreCompact` | Before a conversation is compacted. | the trigger (`auto` or `manual`) |
| `Notification` | The agent posts a notification. | the level (`error`, `warn`, `info`, `success`) |

### Shape

`hooks` maps each event name to a list of **groups**. A group pairs a
`matcher` with one or more hooks. The matcher decides which of that
event's occurrences the group applies to:

- `*` or omitted matches every occurrence.
- A plain name (or `|`-separated list like `read_file|bash`) matches those
  exact values.
- Anything else is treated as an [RE2](https://github.com/google/re2/wiki/Syntax)
  regular expression.

Each hook is either a `command` (a shell command) or an `http` request:

| Field | Applies to | Purpose |
| --- | --- | --- |
| `type` | both | `command` or `http`. Required. |
| `command` | `command` | The shell command to run. Required for `command`. |
| `shell` | `command` | Shell used to run `command`. Defaults to `bash`. |
| `url` | `http` | URL to `POST` the payload to. Required for `http`. |
| `headers` | `http` | Map of request headers. |
| `allowed_env_vars` | `http` | Env-var names allowed for `${VAR}` interpolation in header values. |
| `timeout` | both | Per-hook timeout, e.g. `10s` or a number of seconds. Defaults to `60s`. |

Every hook receives the event payload as JSON: command hooks read it on
stdin, HTTP hooks get it as the request body. The payload carries the
session ID, workspace path, event name, and event-specific fields such as
`tool_name`, `tool_input`, and `tool_response` for `PostToolUse`, or
`prompt` for `UserPromptSubmit`. Command hooks also get `RUNE_HOOK_EVENT`,
`RUNE_DIALOGUE_ID`, and `RUNE_PROJECT_DIR` in their environment.

### Reacting to the result

A hook can stay silent (exit `0`, empty output) or steer the agent:

- **Inject context.** For `UserPromptSubmit` and `SessionStart`, a
  command hook's plain-text stdout is added to the model's context. Any
  hook can also return JSON with
  `hookSpecificOutput.additionalContext` to do the same.
- **Block an action.** A command hook that exits with code `2` blocks the
  action, and its stderr becomes the reason shown to the agent.
  Equivalently, return JSON with `"decision": "block"` and a `"reason"`.
- Any other non-zero exit is logged as a non-blocking warning, so a
  failing hook never wedges the agent.

### Examples

Give every new conversation the current git branch and working-tree
status. On `SessionStart`, a command hook's stdout is folded into the
model's context, so the agent starts already knowing where the repo
stands:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        SessionStart:
          - hooks:
              - type: command
                command: git status --short --branch
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "SessionStart": [
                    {"hooks": [
                        {"type": "command",
                         "command": "git status --short --branch"},
                    ]},
                ],
            },
        },
    },
},
```

Run a formatter after every file write, matching only the `apply_patch`
tool:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        PostToolUse:
          - matcher: apply_patch
            hooks:
              - type: command
                command: make fmt
                timeout: 30s
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "PostToolUse": [
                    {"matcher": "apply_patch",
                     "hooks": [
                        {"type": "command", "command": "make fmt",
                         "timeout": "30s"},
                     ]},
                ],
            },
        },
    },
},
```

Add project context to every prompt, and block edits to a protected file.
The `UserPromptSubmit` hook prints text that is folded into the model's
context; the `PostToolUse` hook exits `2` to reject the tool call:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        UserPromptSubmit:
          - hooks:
              - type: command
                command: echo "Current sprint goal: ship the auth rewrite."
        PostToolUse:
          - matcher: apply_patch
            hooks:
              - type: command
                command: |
                  path=$(jq -r '.tool_input.path // empty')
                  case "$path" in
                    *config/secrets.yaml)
                      echo "Refusing to edit secrets.yaml" >&2
                      exit 2 ;;
                  esac
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "UserPromptSubmit": [
                    {"hooks": [
                        {"type": "command",
                         "command": 'echo "Current sprint goal: ship the auth rewrite."'},
                    ]},
                ],
                "PostToolUse": [
                    {"matcher": "apply_patch",
                     "hooks": [
                        {"type": "command", "command": '\n'.join([
                            'path=$(jq -r \'.tool_input.path // empty\')',
                            'case "$path" in',
                            '  *config/secrets.yaml)',
                            '    echo "Refusing to edit secrets.yaml" >&2',
                            '    exit 2 ;;',
                            'esac',
                        ])},
                     ]},
                ],
            },
        },
    },
},
```

Notify an external service over HTTP, interpolating a token from the
environment into a header:

```yaml tab
extensions:
  rune-agent:
    config:
      hooks:
        Stop:
          - hooks:
              - type: http
                url: https://hooks.internal/agent-done
                headers:
                  Authorization: "Bearer ${AGENT_HOOK_TOKEN}"
                allowed_env_vars:
                  - AGENT_HOOK_TOKEN
```

```python tab
"extensions": {
    "rune-agent": {
        "config": {
            "hooks": {
                "Stop": [
                    {"hooks": [
                        {"type": "http",
                         "url": "https://hooks.internal/agent-done",
                         "headers": {"Authorization": "Bearer ${AGENT_HOOK_TOKEN}"},
                         "allowed_env_vars": ["AGENT_HOOK_TOKEN"]},
                    ]},
                ],
            },
        },
    },
},
```

## Appearance

These keys style the chat tab and its composer. Each attribute value takes
an optional `fg` (foreground color), `bg` (background color), and `flags`
(such as `dim`, `italic`, or `bold`).

| Key | What it styles |
| --- | --- |
| `background_attr` | The chat tab background. |
| `user_msg_attr` | Your messages in the transcript. |
| `user_msg_background_attr` | The background behind your messages. |
| `assistant_msg_attr` | The agent's messages in the transcript. |
| `input_box_attr` | The composer's text content. |
| `input_box_placeholder_attr` | The composer's placeholder text. |
| `input_box_frame_attr` | The composer's border. |
| `input_box_frame_charset` | The character set used to draw the composer's border. |
| `input_background_color` | The composer's background color. |
| `completion_matched_text_attr` | The matched substring in `#` context completion results. |
| `completion_focus_element_attr` | The selected row in the `#` context completion list. |
| `completion_element_attr` | Unselected rows in the `#` context completion list. |
| `completion_radar_color` | The radar sweep drawn over the composer while `#` candidates load. |
| `inline_attachment_attr` | A file or symbol name inserted into the composer from `#` completion. |

The `completion_*_attr` keys default to the command prompt's styling so both
search overlays look the same. `completion_radar_color` defaults to a neutral
gray instead, so the sweep reads the same under every theme.

Accepting a `#` completion keeps the attachment chip above the composer and
also inserts its readable name into the sentence. `inline_attachment_attr`
styles that name while it remains linked to the attachment. Rune maps the
link to an internal, turn-local attachment reference when sending the message;
that internal reference is not durable across conversation compaction.

```yaml
extensions:
  rune-agent:
    config:
      background_attr:
        bg: default
      user_msg_attr:
        fg: default
        bg: default
        flags: dim
      user_msg_background_attr:
        bg: gray
      assistant_msg_attr:
        fg: default
        bg: default
        flags: italic
      input_box_placeholder_attr:
        fg: red
        bg: default
      completion_matched_text_attr:
        fg: blue
        bg: default
        flags: bold
      completion_focus_element_attr:
        fg: purple
        bg: default
      completion_element_attr:
        fg: default
        bg: default
        flags: dim
      completion_radar_color: dimgray
      inline_attachment_attr:
        fg: aqua
        bg: default
        flags: underline
```

## Status bar

Every chat reserves its bottom row for a status bar. It stays put while you
scroll, and it stays visible between turns: idle blanks the spinner and reads
`READY`, while the model, effort, context and cache remain. Reopening
a conversation fills the context gauge from its history, so the bar reflects
how much of the window is already in use before you send anything.

| Key | Type | Default | What it does |
| --- | --- | --- | --- |
| `status_bar.enabled` | boolean | `true` | Whether the chat reserves its bottom row for the bar. |
| `status_bar.layout` | string | see below | Template describing what the bar shows and where. |
| `status_bar.background_attr` | attribute | `bg: gray, fg: silver` | The bar's base attributes, matching the editor's status bar. `bg` fills the whole row, including the gaps between elements. `fg` is the foreground inherited by every layout component that does not name one of its own, so a theme can restyle the row from a single key. |
| `status_bar.status` | map | see below | How the `Spinner` and `Status` elements dress per turn state. The keys are the text `Status` draws: `IDLE`, `SENDING`, `REASONING`, `RECEIVING`, `EXECUTING`, `COMPACTING`, `ASKING`, and `ERROR`. Each takes an `attr` colouring both elements and an `animation` of `ch`, the spinner frames one character per frame, and `attr`, which overrides the status colours for the spinner alone so the icon can carry a colour of its own. Naming one status, or one of its keys, leaves the rest on their built-in values. `ASKING` is reported while the agent waits on an answer, and is the one status that outranks a running task's description. |
| `status_bar.gauge_empty_attr` | attribute | `fg: silver, bg: gray` | Styles the gauge's track. It sits flush with the bar, and its foreground doubles as the label color over the unfilled part. |
| `status_bar.context_gauge_fill_attrs` | list of attribute | green, yellow, red on black | The stops the context gauge's fill ramps through, left edge to right. |
| `status_bar.cache_gauge_fill_attrs` | list of attribute | red, yellow, green on black | The same for the cache gauge, listed backwards because a full cache is good news where a full context window is not. |
| `status_bar.gauge_start_rune` / `status_bar.gauge_end_rune` | string | `""` / `""` | Brackets around a gauge. Each takes one cell out of `gauge_width` rather than widening the field, and an empty string drops that bracket and gives its cell back to the track. Blank by default: the ramp already marks where the track starts and ends. |
| `status_bar.gauge_cap_attr` | attribute | `fg: green` | Styles the brackets. They sit on the bar rather than on the track, so leaving `bg` out inherits `background_attr`'s. |
| `status_bar.gauge_width` | integer | `18` | Cell width of each gauge. |
| `status_bar.shader` | string | none | Effect drawn over the bar while a turn runs. `blaze`, `burn`, `inferno`, `noise`, `shine` and `trippy` recolor only the text, leaving background colors and glyphs such as the `█▓▒░` fade as the layout drew them. `pulse` gently brightens the whole bar. Leave it unset or empty to keep the bar still. |
| `status_bar.shader_fps` | integer | `30` | Cadence the effect is redrawn at. Lower it to spend less time animating the bar. |
| `status_bar.shader_loop` | duration | `1200ms` | How long one visual loop of the effect lasts, written as a Go duration such as `2s` or `1200ms`. For `pulse` it is the length of one pulse. `blaze`, `inferno`, `noise` and `trippy` animate in real time and ignore this key; lowering `shader_fps` makes them choppier, not slower. |

### Layout

The layout is a template of elements written as `{{ .Name }}`. Everything
outside an element is literal text drawn as-is, so separators, padding, and
powerline glyphs all belong in the layout string. `{{ .ShiftRight }}` is the
alignment pivot: elements before it hug the left edge, elements after it hug
the right.

| Element | What it shows |
| --- | --- |
| `Spinner` | Activity spinner. Blank while the agent is idle. Takes its colors and its animation from `status`. |
| `Status` | What the turn is doing, or the running task's description when the agent is working through a plan. Reads `IDLE` between turns. Takes its colors from `status`, keyed by the turn's state rather than by the text shown, so a task description keeps the state's color. |
| `Conversation` | The name of the open chat, matching its tab label. |
| `Elapsed` | Time spent in the current turn. |
| `Model` | The model answering the conversation. |
| `Provider` | The provider serving that model. |
| `Effort` | The reasoning effort in use. When the conversation sets none, this names the level the provider applies instead — and reads `default` only for providers that publish no such level. |
| `MaxTokens` | The per-response output token budget. |
| `TokensSent` / `TokensReceived` | Input and output tokens for the turn. |
| `ContextTokens` / `ContextWindow` | Context occupancy and the model's total window. |
| `ContextPct` / `CachePct` | Context occupancy and prompt cache hit rate as percentages. |
| `Cache` | The prompt cache hit rate, as `87%`. Drops out until the conversation has sent something to report a rate on. |
| `ContextGauge` | Fixed-width context gauge: the numbers centered on top, bracketed by `gauge_start_rune` and `gauge_end_rune`, with a color boundary marking how full the window is. |
| `CacheGauge` | The same gauge for the prompt cache hit rate. |

A gauge marks its fill with color rather than with a texture, so the numbers
stay readable on both sides of the boundary.

The fill ramps through the stops in `_fill_attrs`, spread evenly across the
whole field rather than across the part the fill reaches — a cell keeps its
color as the gauge grows, so how far along the ramp the boundary sits is
itself a reading. Cells landing between two stops blend them, which is what
keeps every cell of a wide gauge a slightly different color; name more stops
to keep the blend closer to colors the theme defines. Reversing a gauge is a
matter of listing its stops backwards; there is no direction setting.

Any element accepts pipe operators to style it: `bg`, `fg`, `bold`, `italic`,
`underline`, `reverse`, and `dim`. `bg` and `fg` take a hex value or a W3C
color name. For `Spinner` and `Status`, `status` wins over `bg` and `fg`
written in the layout, which then only apply to a state the palette does not
cover.

Shade characters such as `█▓▒░` are drawn inverted against the bar background,
so placing them next to an element with its own `bg` fades that element out
into the row, and reversing them to `░▒▓█` fades it back in. The default
layout uses the first: the spinner and the status share a block that fades out
to the right. Give every element in such a block the same `bg`, or the ones without it
leave unpainted gaps. An element with nothing to show takes the literals
around it down with it, so a pill never fades into an empty name. Separate two
elements with a double space to start a new run; a single
space belongs to the element on its left.

The two gauges are fixed-width fields that already centre their label, so they
need less padding around them than the text elements do.

### Narrow terminals

When the row cannot fit everything, elements drop out in a fixed order rather
than the status text being cut short: the output budget first, then the
conversation name, the cache gauge, the cache percentage, the provider, the
effort, the token counts, the context numbers, the model, the context gauge,
and finally the elapsed time. The spinner and the status text are never
dropped; the status text clips to whatever room is left.

```yaml
extensions:
  rune-agent:
    config:
      status_bar:
        enabled: true
        layout: ' {{ .Spinner }} {{ .Status | bold }} █▓▒░  {{ .Model | fg "white" }}  {{ .Effort }}{{ .ShiftRight }}󰗂 {{ .Cache }}  {{ .Elapsed }}  {{ .Conversation | bold | fg "white" }}  {{ .ContextGauge }} '
        shader: pulse
        background_attr:
          bg: gray
          fg: silver
        status:
          IDLE:
            attr:
              fg: white
              bg: silver
              flags: bold
            animation:
              ch: ""
              attr:
                fg: yellow
          SENDING:
            attr:
              fg: black
              bg: teal
              flags: bold
            animation:
              ch: "⡘⡌⠆⢃⢡⠰"
              attr:
                fg: default
          REASONING:
            attr:
              fg: white
              bg: navy
              flags: bold
            animation:
              ch: "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
              attr:
                fg: default
          RECEIVING:
            attr:
              fg: black
              bg: magenta
              flags: bold
            animation:
              ch: "⢡⢃⠆⡌⡘⠰"
              attr:
                fg: default
          EXECUTING:
            attr:
              bg: red
              flags: bold
            animation:
              ch: "⢀⢀⣀⢄⢂⢀⣀⣠⣤⣦⣧⣶⣦⣧⣦⣤⣴⣼⣴⣶⣧⣦⣤⣴⣼⣴⣶⣧⣦⣤⣴⣼⣴⣶⣧⣦⣤⣄⣀⣀⡠⡠⠔⠊⠁⠁  "
              attr:
                fg: default
          COMPACTING:
            attr:
              bg: gray
              flags: bold
            animation:
              ch: "⣉⠶⠶⠒⠒⠒⠶⠶⣉"
              attr:
                fg: default
          ASKING:
            attr:
              fg: black
              bg: yellow
              flags: bold
            animation:
              ch: "⠁⠂⠄⡀⢀⠠⠐⠈"
              attr:
                fg: default
          ERROR:
            attr:
              bg: maroon
              flags: bold
            animation:
              ch: ""
              attr:
                fg: red
        gauge_empty_attr:
          fg: silver
          bg: gray
        gauge_start_rune: ""
        gauge_end_rune: ""
        gauge_cap_attr:
          fg: green
        context_gauge_fill_attrs:
          - fg: black
            bg: green
          - fg: black
            bg: yellow
          - fg: black
            bg: red
        cache_gauge_fill_attrs:
          - fg: black
            bg: red
          - fg: black
            bg: yellow
          - fg: black
            bg: green
        gauge_width: 18
```
