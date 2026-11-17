---
sidebar_position: 2
description: How Rune shows what programs report through the Program Status Protocol (OSC 7501) on tabs, the workspace bar, notifications, and the status inbox.
---

# Program Status Protocol

The Program Status Protocol (OSC 7501) is a [novel terminal escape sequence
protocol](https://mitchellh.com/writing/program-status-osc7501) that lets a
TUI/CLI program tell the host terminal what it is doing: idle, working, waiting
on you, finished, or failed, and why. Rune understands it in every terminal, so a
build, a deploy, or a coding agent you left running in another tab or
workspace can tell you when it needs you, without you watching it.

## Try it

With [`runectl`](../../develop/runectl.md#program-status) installed (`pkg install
runectl` in the [console](../console.md)), play one of the examples from
the specification in a Rune terminal and watch the tab and the workspace bar:

```
runectl status demo --scenario deploy
```

To give any command a status without changing it, wrap it. The tab spins while
it runs, then turns green or red, and Rune notifies you if you have moved on:

```
runectl status wrap -- make test
```

Nothing about the protocol needs `runectl`. This shell function, from the
specification, writes the sequence directly:

```sh
status() {
  printf '\e]7501;state=%s:msg=%s\e\\' "$1" "$(printf '%s' "$2" | base64 | tr -d '\n')"
}
status working "Syncing photos"
rsync -a ~/Photos backup:/photos && status done "Photos synced" || status error "rsync failed"
```

Reports travel in the terminal's output, so they reach Rune from [SSH
workspaces](../ssh.md), from `rune://` workspaces on the
[network](../network.md), from inside containers, and through an `ssh`
session you start in a Rune terminal.

## What reports status

| Source | What it reports |
| --- | --- |
| [Claude Code](../../agent/claude-code.md#see-claudes-status-automatically) v2.1.295 or later | Working, waiting for your input, or done, with no setup. |
| [Rune Agent](../../agent/intro) | Working while the agent thinks and runs tools, with what it is doing as the title. Blocked when it asks you a question or for permission. Done with the first line of its reply, or failed with the reason. Sub-agents and skills are records beneath the chat. |
| [`runectl status`](../../develop/runectl.md#program-status) | Any state from a script, a hook, or your shell, and `runectl status wrap` for any command. |
| [Rune console](../console.md) commands | Each command is a record: working while it runs, with a percentage when it reports progress (package and model downloads do), then done or failed. Interrupting a command drops its record. |
| Rune's permission prompts | Blocked on permission, as app `rune`, until you answer the prompt. |
| [Extensions](#report-from-an-extension) | The status of the tabs they own, through the Go SDK. |
| Any OSC 7501 compatible terminal program| Whatever it reports. |

### Sounds

Each time a tab or terminal enters a state, Rune plays the
[sound](../../appearance/sounds.md) that `program_status.sounds` maps the state to:
`warning` for `blocked`, `error` for `error` and `success` for `done` by
default. Unlike notifications, sounds play whether or not you are looking at
the tab, and are not rate limited. Reporting the same state again, with a new
message or another `kind`, plays nothing.

Map a state to the name of any sound, or to an empty string for none.
`working` is also accepted. For example, to keep finished programs quiet and
tick when one starts working:

```yaml tab
program_status:
  sounds:
    done: ""
    working: info
```

```python tab
config["program_status"]["sounds"]["done"] = ""
config["program_status"]["sounds"]["working"] = "info"
```

A state mapped to a sound Rune does not know keeps its default, and Rune
reports it as a config error. Changes to `program_status.sounds` apply after a
restart, or on the next
[`runectl screenshot`](../../develop/runectl.md#screenshots).

### The status inbox

`programstatus` opens a floating window with a sidebar on the left and a live
preview of the selected program on the right. Everything in it updates while
it is open. The window sits at the top of the screen and takes at least 60% of
its width, growing and shrinking downwards as the preview changes size.

<video
  className="docs-figure"
  autoPlay
  loop
  muted
  playsInline
  preload="metadata"
  poster="https://assets.rune.build/videos/www-rune-1-3-programstatus-v2.jpg"
  aria-label="The programstatus inbox lists terminal programs and Rune Agent conversations across workspaces, with live previews, progress bars, and notifications as a program waits for input, runs, and completes.">
  <source src="https://assets.rune.build/videos/www-rune-1-3-programstatus-v2.webm#t=0.1" type="video/webm" />
  <source src="https://assets.rune.build/videos/www-rune-1-3-programstatus-v2.mp4#t=0.1" type="video/mp4" />
</video>

| Key | Action |
| --- | --- |
| `j` / `<down>` / `<ctrl-j>` / `<ctrl-n>` | Next program |
| `k` / `<up>` / `<ctrl-k>` / `<ctrl-p>` | Previous program |
| Click | Select a program |
| Mouse wheel over the sidebar | Scroll the programs |
| `<enter>` | Switch to the program's workspace and show it |
| `<delete>` / `<backspace>` | Close the program as `tabclose` would: a tab or window is closed, a task stopped, an ACP session ended, and the companion terminal hidden. A `working` or `blocked` program, or a file with unwritten changes, asks first |
| `<esc>` / `q` / `<ctrl-c>` | Close the inbox |

Tabs that never reported a status are not listed, and the inbox is not bound
to a key by default. Bind it like any other command under
`command.key_bindings`.

:::tip
Rune does not install shell integration. If your shell does not mark its
prompts with OSC 133, a program that dies without reporting again leaves its
`working` or `blocked` record until the terminal closes. Run
`runectl status clear` to remove it, or start the program under
`runectl status wrap`, which always reports how the command ended.
:::

## Report from an extension

Extensions report the status of the tabs they own through one of the [
SDKs](../../develop/sdk/go.md), without writing escape sequences. The model is the
same: `WindowManager.SetTabStatus` replaces the record a `browserapi.TabStatus`
addresses on the tab identified by its URI.

```go
err := wm.SetTabStatus(uri, browserapi.TabStatus{
	ID:       "index",
	State:    browserapi.TabWorking,
	Progress: 40,
	Title:    "Indexing",
})
```

- `State` is one of `TabIdle`, `TabWorking`, `TabDone`, `TabBlocked`,
  `TabError`, or `TabClear`, and `Kind` one of `TabKindNone`,
  `TabKindPermission`, `TabKindQuestion`, or `TabKindAuth`.
- Set `Progress` to `-1` when there is no percentage: its zero value means 0%.
- Rune fills in `app` with the extension's id.
- `Title` and `Message` follow the protocol's limits, and a report that breaks
  one, or carries a control character, fails with an `InvalidArgument` error.
- `TabDone`, `TabError` and `TabIdle` clear once you see the tab, and every
  record goes away when the tab closes.
- A report for a URI that no tab shows, such as that of a program that goes
  on working once its tab closed, is listed in the status inbox as a terminal
  that is not a tab is. Showing it from the inbox opens the URI again, in a
  tab, through the extension's resource opener.

Rune Agent reports its chats this way, including those that keep running
once their tab closes.

## Configuration

Program status is configured under `terminal.status`. The activity effects
that play over working tabs are configured separately, under
`animations.active_content_tab` and `animations.active_workspace_tab`; see
[Over working tabs](../../appearance/animations.md#over-working-tabs). The status inbox is
configured under [`program_status`](#configuring-the-inbox).

| Key | Default | Effect |
| --- | --- | --- |
| `enabled` | `true` | `false` drops the status icons and colors from tabs and the workspace bar, and raises no status notifications or [sounds](#sounds). The activity effects have their own `enabled` switch under `animations`. |
| `icons.permission`, `icons.question`, `icons.auth` | `?`, `?`, `@` | The icon of a blocked tab by `kind`. |
| `icons.done`, `icons.error` | a check mark, `x` | The icon of a finished or failed tab. |
| `spinner` | `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` | The frames a working tab's icon cycles through. |
| `spinner_fps` | `10` | Spinner frames per second. |
| `content_tab` | see below | How content tabs (files, terminals, agent chats) show their status. |
| `workspace_tab` | see below | How workspace tabs on the bar show the status of their workspace. |
| `notify` | `[blocked, error, done]` | The states that raise a notification when a tab out of sight enters them. `working` is also accepted. |
| `notify_interval` | `2s` | The minimum time between two state notifications for the same tab. |

`content_tab` and `workspace_tab` take the same keys. Colors left at `default`
keep the tab's own:

| Key | `content_tab` | `workspace_tab` | Effect |
| --- | --- | --- | --- |
| `progress` | `false` | `true` | Split a working label at the reported percentage. |
| `working_done_attr` | default | cyan | The completed part of a split label. |
| `blocked_attr`, `done_attr`, `error_attr` | default | yellow, green, red | The name in that state. |
| `idle_icon_attr`, `working_icon_attr` | gray, cyan | default | The icon of a tab that reported `idle`, or the spinner. |
| `blocked_icon_attr`, `done_icon_attr`, `error_icon_attr` | yellow, green, red | default | The icon in that state. |

For example, to be notified only when a program needs you or fails, and to
color the names of finished and failed content tabs:

```yaml tab
terminal:
  status:
    notify: [blocked, error]
    content_tab:
      done_attr: {fg: green}
      error_attr: {fg: red}
```

```python tab
config["terminal"]["status"]["notify"] = ["blocked", "error"]
config["terminal"]["status"]["content_tab"]["done_attr"] = attr(fg = "green")
config["terminal"]["status"]["content_tab"]["error_attr"] = attr(fg = "red")
```

### Configuring the inbox

The [status inbox](#the-status-inbox) is configured under the top-level
`program_status`. Colors left at `default` keep those of what they are drawn
over.

| Key | Default | Effect |
| --- | --- | --- |
| `selected_attr` | gray background | Styles the whole selected program. Its colors win over those of the program's name and state. |
| `hover_attr` | bold | Styles the whole program under the mouse, as `selected_attr` does. On the selected program, the selection's colors win. |
| `marker` | `▌` | The marker drawn down the left of the selected program. |
| `marker_attr` | default | Styles the marker. The colors it leaves at `default` are the foreground of `browser.focus_tab_highlight_attr` and the background of the selection. |
| `workspace_attr` | gray | The name of the workspace above each program's name. A workspace without a name takes no row. |
| `workspace_highlight_attr` | silver | The name of the workspace of the program selected or under the mouse, which gray would blend into the selection. Its colors, like `workspace_attr`'s, win over `selected_attr`'s and `hover_attr`'s. |
| `progress_bar` | `🬋🬋🬋🬋` | The four characters a progress bar is drawn with, in order: its left end, what is done, what is not, and its right end, as in the console's `╢░_╟`. Each must be one cell wide. |
| `progress_attr` | default | Beneath the whole bar. A background set here stays over the selection's. |
| `progress_done_attr` | green | What is done of the bar. |
| `progress_remaining_attr` | gray | What is not done of the bar. |
| `progress_remaining_selected_attr` | silver | What is not done of the selected program's bar, which the gray above would blend into the selection. |
| `progress_label_attr` | default | The percentage on the bar's right. |
| `more_below` | `\uf063` (Nerd Font arrow down) | The arrow drawn below the programs when there are more out of sight below. |
| `more_below_attr` | default | Styles that arrow. |
| `divider_attr` | gray | The line between the programs and the preview. |
| `divider_charset` | joins `🭽` and `🭼` in the GUI, `┬` and `┴` in a terminal | The characters joining that line to the inbox's frame: `top` and `bottom` in the frame's top and bottom rows, colored as the frame, and `vertical_top`, `vertical` and `vertical_bottom` down the line, colored by `divider_attr`. `vertical` defaults to the window frame's `verticalleft`, and `vertical_top` and `vertical_bottom` to `vertical`. Under a window bar, there is no top join. A `graphics` table names images for the GUI: `top` and `bottom` over the joins, `vertical` over each cell of the line, a `mode`, and an `outset` with `left`, `right` and `inward` in cells that the joins reach into. A missing join image keeps the frame's own image; `vertical` defaults to the frame's `verticalleft` image. |
| `placeholder_attr` | gray | The text shown when no program reported a status, or the selected one has nothing to preview. |
| `images` | none | Images the GUI draws under each program, with its text on top: `selected`, `hover` and `idle`, each with a `left`, `middle` and `right` image, and an `outset` with `top`, `bottom` and `sides` in cells. `selected` falls back to `hover`, and `hover` to `idle`. They stand in for the backgrounds of `selected_attr` and `hover_attr`. |
| `marker_graphics` | none | Images drawn down the marker of the selected program: `top`, `vertical` and `bottom`, and a `mode`. A top or bottom image left out is the vertical one. |
| `progress_bar_graphics` | none | Images drawn over the cells of a progress bar: `left` and `right` over its ends, `filled` over what is done and `empty` over the rest, and a `mode`. An end left out takes the image of the cell it stands in. |

For example, to draw progress bars as the console does, on a dark track, and
to highlight the program under the mouse:

```yaml tab
program_status:
  progress_bar: "╢░_╟"
  progress_attr: {bg: "#303030"}
  progress_remaining_selected_attr: {fg: gray}
  hover_attr: {bg: "#3a3a3a"}
```

```python tab
config["program_status"]["progress_bar"] = "╢░_╟"
config["program_status"]["progress_attr"] = attr(bg = "#303030")
config["program_status"]["progress_remaining_selected_attr"] = attr(fg = "gray")
config["program_status"]["hover_attr"] = attr(bg = "#3a3a3a")
```

## Troubleshooting

**Nothing shows when a program reports.** Run `runectl status query` in the
same terminal. If it reports the protocol unsupported while you are inside
tmux, screen, or another multiplexer, the multiplexer is answering instead of
Rune and does not forward the reports: run the program directly in a Rune
terminal. Otherwise, check that `terminal.status.enabled` is not `false`.

**A status never goes away.** A `working` or `blocked` record whose program
died needs the shell's next prompt to be marked with OSC 133, or a `clear`;
see [The status inbox](#the-status-inbox). A `done` or `error`
record stays until you see it: focus the tab or the window showing the
terminal, or choose it in the status inbox.

**Too many notifications.** Remove states from `terminal.status.notify`, or
raise `terminal.status.notify_interval`. Messages of the same state replace
one another rather than stack up.

**Too many sounds.** Map the states you do not want to hear to `""` in
`program_status.sounds`. Every command run in the IDE console reports `done`
or `error` when it ends, so each plays a sound.
