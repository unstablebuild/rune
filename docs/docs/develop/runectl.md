---
sidebar_position: 15
---

# runectl

`runectl` is Rune's command-line companion: a small CLI that runs as an
external process and communicates with a running Rune workspace as a
plugin. It is the fast path to integrating with Rune. Where a full
[extension](./sdk/index.md) is the right tool when you want to ship UI, register
commands, or run a long-lived process inside Rune, `runectl` lets a shell
script, a Makefile target, an editor hook, or a one-off command reach
into the workspace without writing any code.

Because it is just a CLI that reads and writes plain text, it composes
with the rest of your environment: pipe its output through `jq`, `grep`,
`fzf`, or anything else, and pipe other tools' output back into it.

## Install

`runectl` is distributed as a Rune package. Install it from the
[Rune console](../learn/console.md) with:

```
pkg install runectl
```

(or `console pkg install runectl` from the command prompt). Once installed,
`runectl` is on your `PATH` inside Rune's terminals.

To build it from source instead, run `make runectl` in a
[Rune checkout](./building.md#get-the-source); the binary is written to
`bin/runectl`.

## How it finds the workspace

Commands you run from a Rune terminal target the right workspace
automatically, with no flags or configuration. That is also why hooks and
scripts spawned by tools running inside Rune just work.

## What you can do

Run `runectl help` to see every command, or `runectl <command> --help`
for a specific one. The main groups:

| Command | What it does |
| --- | --- |
| `runectl notify <level> <message>` | Post a Rune notification (`error`, `warn`, `info`, `success`). |
| `runectl open <uri>` | Open a resource in the workspace. |
| `runectl uri <path>` | Resolve a filesystem path to a workspace URI. |
| `runectl datadir` | Print the extension data directory. |
| `runectl screenshot` | Take a GUI screenshot and print its absolute path. |
| `runectl exec -- <cmd> [args...]` | Run a command through the workspace executor. |
| `runectl editor ...` | Read and edit buffer text, cursors, and location lists. |
| `runectl wm ...` | Window-manager actions: focus, split, float, close, and [prompt](#prompts) the user. |
| `runectl lsp ...` | Language-server queries: definitions, references, diagnostics, rename, symbols, and more. |
| `runectl syntax ...` | Tree-sitter searches and structural queries across the workspace. |
| `runectl storage ...` | Create, read, update, and delete extension documents. |
| `runectl llm ...` | Talk to the workspace's model router. |
| `runectl search ...` | Search the workspace's files through its indexes, by regular expression or by meaning. |
| `runectl mcp` | Start an MCP server over stdio exposing Rune's syntax and LSP tools. |
| `runectl status <state> [message]` | Report program status to the terminal (OSC 7501): `idle`, `working`, `done`, `blocked`, `error`, or `clear`. |
| `runectl status wrap -- <cmd> [args...]` | Run any command and report its progress and outcome to the terminal, with no changes to the command. |

Most commands accept a `-F`/`--format` flag to choose the output format:
`table` (the default, human-readable), `json` (for piping into other
tools), or a Go template for custom shaping.

## Screenshots

```sh
image=$(runectl screenshot)
```

`runectl screenshot` saves Rune's rendered GUI to a PNG in
`$RUNE_DATADIR/screenshots/` and prints only its absolute path, followed by
a newline. It waits up to five seconds for a new complete image. Errors go
to stderr with a nonzero exit status and no path on stdout.

Before the capture, Rune reads its config file again and applies these
keys to every open workspace, as `themereload` does:

- `browser.window_manager`: `frame_attr`, `focus_frame_attr`,
  `frame_charset`, `focus_frame_charset`, `floating_frame_charset`,
  `focus_floating_frame_charset`, `focus_window_bar_charset`, `scroll_bar_attr`,
  `scroll_bar_charset`, `focus_scroll_bar_charset`, `scroll_bar_char`,
  `scroll_bar_hover_char`, `background_attr`, `focus_background_attr`,
  `background_graphics`, `focus_background_graphics`, `window_bar`,
  `window_bar_charset`, `close_icon` and `close_icon_attr`;
- `browser.frameunion_charset`;
- `browser`: `focus_tab_attr`, `non_focus_tab_attr`, `focus_tab_icon_attr`,
  `non_focus_tab_icon_attr`, `focus_tab_highlight_attr`, `hover_tab_attr`,
  `focus_tab_highlight_char`, `tab_images`,
  `focus_tab_highlight_graphics` and `tab_sounds`;
- `browser.prompt`: `text_attr`, `highlight_attr`, `hover_attr`,
  `clicked_attr`, `background_attr`, `field_attr`, `focus_field_attr`,
  `placeholder_attr` and `images`;
- `command.frame_charset`, `command.separator_charset` and
  `command.show_manual`;
- `editor.status_bar`: `layout`, `background_attr`,
  `foreground_error_attr` and `focus`;
- `gui`: `quick_menu`, `quick_menu_sounds`, `themes`, `default_theme`,
  `sound_volume` and [`app_icon`](../config.md#gui);
- `notifications`: `attr`, `background_attr`, `frame_charset`, `padding`,
  `progress_bar` and `progress_format`;
- `program_status`, every key;
- `terminal.bell`;
- `workspace`: `wallpaper_image`, `wallpaper_image_ascii`,
  `wallpaper_ascii`, `wallpaper`, `wallpaper_attr`,
  `wallpaper_background_attr`, `wallpaper_density_characters`, their
  `focus_` versions such as `focus_wallpaper_image`,
  `focus_tab_highlight_char`, `tab_images`,
  `focus_tab_highlight_graphics` and `tab_sounds`;
- `sounds`.

The capture cannot show `gui.app_icon`: the desktop draws the icon outside
Rune's output, so look for it in the taskbar, the window title or the Dock.

It also reads again every image these keys name, even a file overwritten
at the same path, so you can edit an image and see it in the next
screenshot. Config errors show up as notifications in the capture. If the
config file does not parse, none of its keys are applied.
This includes all images in the optional floating frame and focused header
charsets. Remove an optional key and capture again to restore its shared
fallback on existing floating windows; `{}` instead keeps an explicit
override with inherited slots. Set a graphics slot to `""` to clear its image.
Check both focused and unfocused floats and confirm tiled frames stay unchanged.
`gui.default_theme` and `gui.sound_volume` apply only when they differ
from the values Rune read last, so a theme or volume picked since then
stays. Prompts already open keep their style until reopened. Other keys
still take `workspacereload` or a restart. Notifications and the program
status inbox already open take the new style at once.

Rune also serves the new config to its extensions and, on macOS and
Linux, sends each running extension process `SIGUSR2`. When it signalled
any, it waits 300 ms before capturing so they can redraw; see [Reloading
configuration](sdk/go.md#reloading-configuration). Rune Agent uses this to
apply `extensions.rune-agent.config.status_bar` and
`extensions.rune-agent.config.input_box_frame_charset` to its open chats.
An extension slower than that shows its change in the next screenshot.

Screenshots require GUI mode. TUI sessions safely discard the request, and
`runectl` times out without creating an image. The capture contains Rune's
output, not native window borders or the desktop behind it. Requests sharing
a data directory should run sequentially: simultaneous requests can observe
the same new file.

## Prompts

```sh
choice=$(runectl wm prompt 'Deploy *main* to production?' Deploy Cancel)
```

`runectl wm prompt <message> <option>...` opens a dialog that asks the
markdown `<message>`, waits for the user to pick one of the options and
prints it. It fails if the user dismisses the dialog, or if one with the same
message is already open, and stopping the command closes the dialog.

| Flag | What it does |
| --- | --- |
| `-k`, `--keys` | Keys that pick each option in order, such as `yn` or `<enter><esc>`. |
| `-w`, `--max-width` | Size the message to its widest line, wrapping at this many cells. |
| `--field QUESTION[=ANSWER]` | Ask a question the user answers by typing, starting from `ANSWER`. |
| `--secret QUESTION[=ANSWER]` | The same, with the answer hidden as typed. |
| `--mandatory` | Keep the options from being picked while the answer to the `--field` or `--secret` before it is blank. |
| `--placeholder TEXT` | Show `TEXT` in the `--field` or `--secret` before it while its answer is empty. It is never the answer. |

Questions show in the order given, between the message and the options, and
the answers are printed a line each after the option:

```sh
runectl wm prompt 'Log in to *db*' OK Cancel \
  --field User=me --secret Password --mandatory -F json
```

```json
{"Answers":["me","hunter2"],"Index":0,"Option":"OK","success":true}
```

Tab and Shift-Tab, or Down and Up, move between the questions and the row of
options. Enter moves to the next question, and picks the selected option from
the last one. The `--keys` only pick an option from the row of options, so a
`y` typed into a question is just text. Esc and Ctrl+C dismiss the dialog.

A secret answer is hidden on screen but printed to stdout like any other, so
keep it out of your shell history and logs. A Rune too old for questions opens
the dialog without them, so `runectl` closes it at once and fails with an
unsupported operation error.

## Automating Rune with --exec

```sh
rune --exec 'edit main.go' --exec 'windownew right'
```

`--exec` (`-e`) runs a command line once Rune starts, as the command prompt
runs it when you type it and press Enter. Pass it more than once to run
several, in order. It runs Rune in GUI mode, as `--gui` does, and cannot be
combined with `--tui` or `--headless`. It needs a data directory that is
set up, with a `config.yaml` or `config.star`; otherwise Rune exits with an
error rather than run commands against the first-run setup.

For every command, Rune writes one line to standard output: `ok`,
`ok <path>` for `screenshot`, or `error: <message>`, with any line break in
the message written as `\n`. Rune reads the next command only once it has
replied. With only `--exec` commands, Rune stays open after the last one,
like `vim -c`.

`--exec -` reads command lines from standard input instead, one per line,
and skips empty ones. Other `--exec` flags run before or after it, in the
order given. When standard input ends, Rune exits without asking about
unsaved changes.

### Waiting for Rune

A command replies once it has finished, including the work it started:

- `echo` replies once the keys it sends, and whatever they set off, have
  been handled, so the next command sees their effect.
- After `edit` and the other commands that open files, the next command
  waits until those files have loaded, so its keys reach their editors.
- `write`, `write!`, `writeall` and `writeall!` reply once the files are
  saved, or with the error when a save fails.
- `workspaceready [command]` waits until the workspace being opened is
  ready, then runs the command, if any. `extensionready <id> [command]` does
  the same for an extension. Start a script with `workspaceready`.
- `screenshot` replies with the path of the PNG it saved in
  `$RUNE_DATADIR/screenshots/`. `screenshot --reload` first applies the
  config again, as described in [Screenshots](#screenshots).

When the workspace has a `.rune` config that the data directory has not
trusted yet, Rune asks whether to trust it, and `workspaceready` waits for
the answer. Answer with `echo` first; a `screenshot` shows the choices.

### Running without a window

```sh
rune --offscreen --geometry 1280x800@2 -d /tmp/rune-test -e -
```

`--offscreen` runs Rune with no window, Dock icon or menu bar, and draws
frames only when there is something to show, so it neither appears on
screen nor takes focus. It needs `--exec`, as nothing else could reach or
close it. It plays no sounds, opens no browser and keeps its own clipboard.
`--offscreen` is available on macOS and Linux. On Linux it needs no X or
Wayland server, but it needs Mesa's EGL, such as Debian's `libegl-mesa0`
package; without a GPU, Mesa draws in software.

`--geometry WxH@SCALE` sets the size in points and the device scale, so
screenshots do not depend on the displays attached: `1280x800@2`, the
default, captures 2560 by 1600 pixels. Without `--offscreen`,
`--geometry WxH` opens the window at that size instead of its last one, and
takes no scale. Each side can be at most 8192 pixels.

Give the instance its own data directory, as with `-d` above, so it has its
own config, history and screenshots and leaves yours alone. Copy your
config there to test it.

```console
$ mkdir -p /tmp/rune-test && printf 'editor:\n  mode: vim\n' > /tmp/rune-test/config.yaml
$ printf '%s\n' workspaceready 'edit main.go' 'echo yyp' write 'screenshot --reload' |
    rune --offscreen -d /tmp/rune-test -w ~/src/project -e -
ok
ok
ok
ok
ok /tmp/rune-test/screenshots/screenshot-20260101T120000.000000000Z-1.png
```

In Rune Agent, the `rune_gui` tool runs such an instance for the agent, so
it can check a change to Rune or to your config the way you would see it.

## Program status

`runectl status` speaks the [Program Status
Protocol](../learn/terminal/program-status.md)
(OSC 7501): a program tells the terminal what it is doing, and Rune shows
it on the tab, on the workspace bar, and as a notification when the tab
is not in focus. Every new message a program reports (`runectl status
<state> <message>`) comes through as a notification, even without a state
change. Programs that speak the protocol themselves need no help from
`runectl`. See the [Program Status
Protocol](../learn/terminal/program-status.md) guide for how Rune shows each state,
when a status clears, and how to configure it.

### Wrap any command

`runectl status wrap` gives any command a status, with no changes to the
command and no integration work. Put it in front of a build, a test run,
a deploy, or a download, then go do something else: the tab spins while
the command runs, and when it ends the tab turns green or red and Rune
notifies you, even if you are in another workspace.

```
runectl status wrap -- cargo build --release
```

The command runs exactly as it would without `wrap`: it keeps your
terminal, so prompts, colours, and progress bars work as usual, and
`Ctrl-C` and other signals reach it. `wrap` reports `working` when the
command starts and the outcome when it ends, then exits with the
command's own status, so scripts, `&&` chains, and CI steps behave the
same with or without it:

| When the command | `wrap` reports | and exits with |
| --- | --- | --- |
| exits 0 | `done` | 0 |
| exits with status N | `error`, `exit status N` | N |
| is killed by a signal, e.g. `Ctrl-C` | `error`, e.g. `signal: interrupt` | 128 + the signal number |
| cannot be started | `error`, with the reason | 1 |

`--app` names the program and `--msg` sets the message shown while it
runs and when it is done; it defaults to the command line:

```
runectl status wrap --app terraform --msg "apply production" -- terraform apply
```

**Make it the default.** An alias wraps a tool every time you run it from
your shell, with nothing else to remember:

```sh
alias make='runectl status wrap --app make -- make'
alias cargo='runectl status wrap --app cargo -- cargo'
```

**Hook it into a Makefile.** Wrap the long targets so everyone who runs
them gets a spinner while they run and a result when they finish:

```make
test:
	runectl status wrap --app make --msg "go test" -- go test ./...
```

**Hook it into git.** A `pre-push` hook can flag a slow check while it
runs and leave a `done` or `error` record behind for you to find:

```sh
#!/bin/sh
runectl status wrap --app git --msg "pre-push checks" -- make lint test
```

### Report status yourself

The first argument is the state, the optional second one a message:

```
runectl status working "Installing updates"
runectl status blocked --kind permission "Deploy to production?"
runectl status done "Upgraded 12 packages"
runectl status error "rsync failed"
runectl status idle
runectl status clear
```

`--app` names the program (`cargo`, `terraform`), `--title` labels the
record, `--progress 0-100` adds a percentage to `working` or `blocked`,
and `--id build/test` addresses one of several records so a tool can
report a tree of tasks. `clear` removes the addressed record and
everything beneath it, or every record when no `--id` is given.

**Hook it into an agent.** Agents that expose lifecycle hooks can report
when they stop to ask for permission, and when they are done, so you can
let them run in the background and come back when they need you:

```sh
# permission hook
runectl status blocked --kind permission --app my-agent "Allow running tests?"
# stop hook
runectl status done --app my-agent "Finished implementing the feature"
```

### See it in action

`demo` plays the examples from the specification so you can see how Rune
renders each state, with progress running from 0 to 100% while working
(`--progress-duration`, 8 seconds by default):

```
runectl status demo --scenario deploy
```

`runectl status query` checks whether the terminal supports the protocol,
exiting with status 1 when it does not.

## Composing with POSIX tools

The point of `runectl` is to get things out of Rune and into your
environment, and back. The examples below mix and match the command
groups to show the range.

**Notify on a long build, from outside Rune.** Kick off a build in a
plain terminal and have the workspace tab light up when it lands, even if
you have wandered off to another workspace:

```
make build && runectl notify success "build done" || runectl notify error "build failed"
```

**Code-review the buffer you are staring at.** Pull the live text of an
open editor with `editor print` and send it to the workspace's model
router (`llm message` takes the prompt as an argument), then read the
review back in your terminal:

```
runectl llm message gemini/gemini-3.5-flash "Review this Go file for bugs and edge cases: $(runectl editor print ./convert.go)"
```

**Grep the workspace without walking it.** `search regexp` answers from
the index Rune keeps of the workspace, so it returns as fast in a large
monorepo as in a small project. It prints each line as `grep -n` does,
and takes ripgrep's flags for case (`-i`), literal text (`-Q`), file
globs (`-g`), and matches per file (`-m`):

```
runectl search regexp -g '*.go' -m 1 'func \w+Retry'
```

Hidden, ignored, and excluded folders are skipped unless `--root` names
them or a folder that holds them, as in ripgrep:

```
runectl search regexp -r .github runs-on
```

**Find code by what it does.** `search semantic` takes every argument as the
query, so it needs no quoting, and ranks the workspace's files by meaning
as well as words. Each result prints like a grep match, `path:line:`,
followed by its score, how far it stands out by meaning (z), and the line
it opens at:

```
runectl search semantic --root internal --limit 5 where is the retry backoff computed
```

```
internal/retry/backoff.go:42:   3.14  2.50  func parseRetryBackoff(spec string) (time.Duration, error) {
internal/retry/policy.go:18:    1.20  0.00  first, factor, _ := parseRetryBackoff(p.Spec)
```

While the workspace is still being indexed, or when the query could only
be ranked by words, `runectl` says why on stderr, such as
`runectl: 1204 files left to index`, and stdout keeps only the results.

A Go template shapes the output. `{{.Text}}` is the line as written, so
this prints exactly what `grep -n` would. Run from Rune's command prompt,
it lists the results in the `searchtext` finder, ready to filter and open:

```
searchtext runectl search semantic -F '{{.Path}}:{{.Line}}:{{.Text}}' retry backoff
```

`--meaning=false` ranks by words alone, and `--prefix-last` also matches
the last word as a prefix, for search-as-you-type front ends.

**Turn diagnostics into a commit gate.** Count workspace errors and fail
a pre-commit hook if any remain:

```
errors=$(runectl lsp workspace-diagnostics --format json | jq '[.[] | select(.severity == "error")] | length')
[ "$errors" -eq 0 ] || { runectl notify error "$errors errors, commit blocked"; exit 1; }
```

**Ask the language server about a symbol by name.** The `lsp`
subcommands accept a bare symbol, so you do not need to hunt down a file,
line, and column. Read a type's hover documentation:

```
runectl lsp hover term.Writer
```

```go
type Writer interface { // size=16 (0x10)
	// Context returns the current context of the Writer.
	// This context can be used by tui.Components in combination
	// with term.Interrupter.Interrupt(context.Context) to disambiguate
	// regular calls to Draw from interrupt-driven calls to Draw.
	Context() context.Context
	// SetCell sets the contents of the given cell location.  If
	// the coordinates are out of range, then the operation is ignored.
	SetCell(Coordinates, Cell)
	// UnionAttributes computes the set union between a and b,
	// that is overrides a set of attributes at the given coordinates
	// that contain all the bit flags set in a, b or both, and uses the color
	// defined in b or if not set, uses the color in a.
	UnionAttributes(Coordinates, Attributes)
}
```

Locate where it is defined, list every reference, or find its
implementations the same way. Each prints the matching locations as
`<uri> <start-line>:<start-col>-<end-line>:<end-col>`, one per line:

```
runectl lsp definition term.Writer
runectl lsp references term.Writer
runectl lsp implementation term.Writer
```

Since those are just locations, pipe one into `runectl open` to open it
as a tab in the workspace:

```
runectl lsp definition term.Writer | head -n1 | cut -d' ' -f1 | xargs runectl open
```

**Drive the layout from a script.** Split the focused window and open a
file beside your code, using `wm focus` to target the active window:

```
runectl wm split "$(runectl wm focus)" ./docs/CHANGELOG.md
```

**Run a build in the workspace's environment, not your shell's.** Use the
workspace executor so the command sees the same tools and environment
Rune does:

```
runectl exec -- go test ./...
```

Because every command is line- and JSON-friendly, you can drop `runectl`
into the same pipelines you already use for everything else.
