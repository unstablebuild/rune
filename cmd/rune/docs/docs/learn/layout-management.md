---
sidebar_position: 5
---

# Layout Management

Rune is a full virtualized tiling window manager, inspired by
[i3](https://i3wm.org/). You split the screen into windows, fill them
with content from tabs, group projects into workspaces, and dock
background work as tasks. This guide builds that picture up one
primitive at a time, and along the way you will see why the preset
keybindings are the way they are. The bindings below follow the editor preset
selected at the top of the page.

On macOS, `<meta>` is the Command key. On Linux it is the Super key. See the
[key syntax](./key-syntax.md) reference for how modifiers are spelled.

## The big idea

Vim showed a generation of programmers how productive a small, spatial
keyboard language can be. Directions stay under your fingers, combinations
have consistent meanings, and repeated actions become muscle memory instead
of trips through menus and interface controls.

Rune's keybinding system brings that advantage to the entire workspace, not
just the text editor, and you do not have to use modal editing to get it. Each
preset starts with a directional vocabulary suited to its editor. The physical
keys differ, but the grammar stays the same.

The physical keys differ, but the grammar stays the same: direction plus
target. A window binding affects windows, a tab binding affects tabs, and
workspace digits select workspaces. Add `<shift>` to change "go to" into
"move": move window content instead of changing focus, reorder a tab instead
of switching tabs, or send content to a workspace instead of visiting it.

That composability is the important feature. Once the relationships are in
muscle memory, the same small set of ideas controls Rune's full virtualized
tiling environment. Every layout action is also a
[command](./command-prompt.md) you can type at the prompt or bind under
`command.key_bindings`, so you can keep the grammar while changing any
physical key.

## Windows

A **window** is a tile on screen. A fresh workspace has one window
filling the editor area; you split it to get more.

Split the current window in two with `windownew`. With no argument it
uses your default split orientation (see below); pass a direction to
force one:

```
windownew right
```

`terminalneworsplit` drops a terminal into the current window if it is empty,
or splits off a new window with a terminal if it is not. Run it with
<KeyBinding command="terminalneworsplit" />.

Once you have more than one window, use the directional keys to focus across
them:

| Action | Preset key |
| --- | --- |
| Focus window left | <KeyBinding command="windowfocus left" /> |
| Focus window right | <KeyBinding command="windowfocus right" /> |
| Focus window down | <KeyBinding command="windowfocus down" /> |
| Focus window up | <KeyBinding command="windowfocus up" /> |

Add `<shift>` and the same direction **moves the focused window's
content** in that direction, swapping it with its neighbor. This is
`windowmove`:

| Action | Preset key |
| --- | --- |
| Move window left | <KeyBinding command="windowmove left" /> |
| Move window right | <KeyBinding command="windowmove right" /> |
| Move window down | <KeyBinding command="windowmove down" /> |
| Move window up | <KeyBinding command="windowmove up" /> |

New splits land relative to the current window according to the
**default split orientation**. `windowdefaultsplit` toggles it, or sets
it explicitly: `horizontal` puts the next split below the current
window, `vertical` puts it to the right:

| Action | Preset key |
| --- | --- |
| Default to horizontal splits | <KeyBinding command="windowdefaultsplit h" /> |
| Default to vertical splits | <KeyBinding command="windowdefaultsplit v" /> |

Close the focused window with <KeyBinding command="windowclose" />. It fails if
only one window is left, since a workspace always has at least one. Close every
other window with <KeyBinding command="windowcloseall" />.

Toggle maximization of the focused window with
<KeyBinding command="windowtogglemaximize" />.

## Resizing windows

`windowresize` grows or shrinks the focused window. The grammar is
`windowresize (increase|decrease|max|min|reset) (width|height)`.

| Action | Preset key |
| --- | --- |
| Taller | <KeyBinding command="windowresize increase height" /> |
| Shorter | <KeyBinding command="windowresize decrease height" /> |
| Narrower | <KeyBinding command="windowresize decrease width" /> |
| Wider | <KeyBinding command="windowresize increase width" /> |
| Maximize both | <KeyBinding command={['windowresize max width', 'windowresize max height']} /> |
| Minimize both | <KeyBinding command={['windowresize min width', 'windowresize min height']} /> |
| Reset | <KeyBinding command="windowresize reset" /> |

These layers keep directional resize commands off native text movement and
selection key combinations.

`windowtogglemaximize` is the quick "blow this window up, then put it back"
toggle. It maximizes width and height, and a second press, or focusing another
window, resets the size.

## Content and file tabs

A window does not own a file; it **displays content**, and the content
it can show is a list of **tabs**. Open files, terminals, and task
output are all tabs. Switching tabs swaps what the focused window shows
without changing the window layout.

| Action | Preset key |
| --- | --- |
| New tab | <KeyBinding command="tabnew" /> |
| Next tab | <KeyBinding command="tabnext" /> |
| Previous tab | <KeyBinding command="tabprevious" /> |
| Focus tab 1 | <KeyBinding command="tabfocus 1" /> |
| Search tabs | <KeyBinding command="tabsearch" /> |
| Close tab | <KeyBinding command="tabclose" /> |

Following the `<shift>` = move rule, add `<shift>` to each preset's tab-switch
pair to reorder the current tab with `tabmove`:

| Action | Preset key |
| --- | --- |
| Move tab left | <KeyBinding command="tabmove left" /> |
| Move tab right | <KeyBinding command="tabmove right" /> |
| Move tab to slot 1 | <KeyBinding command="tabmove 1" /> |

`tabnext` and `tabprevious` wrap around the ends of the list.
`tabfocus N` jumps straight to a position. `tabmove` takes a direction
or an absolute slot number, exactly like the keybindings suggest.

## Splits versus tabs

Splits and tabs solve different problems. Split into a new **window**
when you want to see two things at once (a test beside its source, a
terminal under an editor). Open a new **tab** when you want another
thing available in the *same* window, one at a time.

Not every piece of content needs a tab, though. Some content is
**ephemeral**: it lives in the focused window but is never added to the
tab list, so it does not clutter the bar and disappears when you move
on. A quick terminal is the common case. `terminalnew` drops an
ephemeral terminal into the current window when you just need to run
one command and throw it away. When you want that terminal to stick
around, say you are flipping between a test file and the tests running
beside it, use `terminalnewtab` instead, which gives the terminal a
durable tab you can switch back to.

You can convert between them. `windowconverttab` takes the content of
the focused window and turns it into a tab, collapsing the split. Run it with
<KeyBinding command="echo {prompt}windowconverttab<space>" /> when the selected
preset provides a prompt-prefill binding:

```
windowconverttab <name> [<icon>]
```

If the focused window is a floating window, converting it to a tab
closes the floater.

## Floating windows

Some windows float above the tiling layout instead of taking a slot in
it. The canonical example is the [**file explorer**](./file-explorer.md):
it is a pre-minimized floating window docked on the left. `fexplorer`
(<KeyBinding command="fexplorer" />) un-minimizes and focuses it, and invoking the command
again minimizes it back.

The [location picker](./location-picker.md), fuzzy file and symbol
search, and the command prompt itself are floating surfaces too. They
appear on top of your windows, take focus while open, and get out of the
way when you are done, without ever disturbing the windows underneath.

## Workspaces

A **workspace** is a whole project: its own window layout, its own tabs,
its own root directory. Rune keeps up to nine workspaces in numbered
slots, with their tabs shown along the bottom of the screen.

| Action | Binding |
| --- | --- |
| Focus workspace 1 | <KeyBinding command="workspacefocus 1" /> |
| Move workspace to slot 1 | <KeyBinding command="workspacemove 1" /> |
| Search workspaces | <KeyBinding command="workspacesearch" /> |

`workspaceopen <uri>` opens another project in the first free slot (or
the current one if it is empty); if the URI has no scheme, `file://` is
assumed. `workspacefocus N` switches to a slot, and `workspacemove N`
reorders the workspace tabs the same way `tabmove` reorders content
tabs.

## Tasks

A [task](./tasks.md) is a long-running command (a build, a test run, a
dev server) that Rune keeps as a first-class window instead of hiding it
in another terminal. Tasks plug into the same layout primitives you have
just seen.

`tasknew` creates a task and **minimizes** it to the side of the screen,
where it shows as a small colored window: gray while running, green on a
clean exit, red on failure, yellow if you cancelled it. Press `<esc>` to
minimize a focused task back down; `taskfocus <name>` brings it back up,
and `taskclose <name>` stops it.

`tasknewtab` is the tab-shaped variant. It is exactly `tasknew` followed
by focusing the task and running `windowconverttab`, so the task lives
as a durable tab whose name and color track its status. Reach for it
when the task's *output* is the thing you want to watch (a server log, a
`tail -F`), and for `tasknew` when only the task's *status* matters.

See the [Tasks](./tasks.md) guide for the full command grammar, file
watchers, and examples.

## Layout aliases

If you tend to arrange a workspace the same few ways, capture each
arrangement as an [alias](./aliases.md) that tears the layout
down and rebuilds it. Because `windowcloseall` and `windownew` are
ordinary commands, a [multi-step alias](./aliases.md#multi-step-aliases)
can re-shuffle the workspace into one layout or another in a single
dispatch.

For example, a three-window layout for a wide auxiliary screen, and a
two-window layout for the laptop:

```yaml tab
command:
  aliases:
    layout-aux-screen:
      - windowcloseall
      - windownew right
      - windownew right
    layout-laptop:
      - windowcloseall
      - windownew right
```

```python tab
"aliases": {
    "layout-aux-screen": [
        "windowcloseall",
        "windownew right",
        "windownew right",
    ],
    "layout-laptop": [
        "windowcloseall",
        "windownew right",
    ],
},
```

`layout-aux-screen` closes every window, then splits twice for a total of
three windows side by side; `layout-laptop` leaves you with two. Run the
one that fits the screen you are on, and the workspace snaps into shape.

## Reference tables

These are the shipped preset assignments. Every layout binding lives in
`command.key_bindings`, so you can remap it (see [Rebinding](#rebinding)). Run
`cheatsheet` to see the bindings active in your configuration.

### Vim

| Command | Binding |
| --- | --- |
| `windowfocus left/right/down/up` | `<meta-h>` / `<meta-l>` / `<meta-j>` / `<meta-k>` |
| `windowmove left/right/down/up` | `<shift-meta-h>` / `<shift-meta-l>` / `<shift-meta-j>` / `<shift-meta-k>` |
| `windowresize` narrower/wider/shorter/taller | `<alt-meta-h>` / `<alt-meta-l>` / `<alt-meta-j>` / `<alt-meta-k>` |
| `windowresize` max / min / reset | `<shift-meta-+>` / `<shift-meta-->` / `<shift-meta-backspace>` |
| `windowtogglemaximize` | `<shift-meta-f>` |
| `windowdefaultsplit h/v` | `<ctrl-meta-h>` / `<ctrl-meta-v>` |
| `windownew` | `<meta-n>` |
| `windowclose` / `windowcloseall` | `<meta-w>` / `<shift-meta-w>` |
| `tabnew` | `<meta-t>` |
| `tabnext` / `tabprevious` | `<alt-l>` / `<alt-h>` |
| `tabfocus 1..9` | `<alt-1>` … `<alt-9>` |
| `tabmove left/right` | `<alt-shift-h>` / `<alt-shift-l>` |
| `tabmove 1..9` | `<alt-shift-1>` … `<alt-shift-9>` |
| `tabsearch` | `` <alt-`> `` |
| `tabclose` | `<alt-w>` |
| `windowconverttab` | `<alt-enter>` |
| `terminalneworsplit` | `<meta-enter>` |
| `workspacefocus 1..9` | `<meta-1>` … `<meta-9>` |
| `workspacemove 1..9` | `<shift-meta-1>` … `<shift-meta-9>` |
| `workspacesearch` | `` <meta-`> `` |

### Standard

| Command | Binding |
| --- | --- |
| `windowfocus left/right/down/up` | `<alt-j>` / `<alt-l>` / `<alt-k>` / `<alt-i>` |
| `windowmove left/right/down/up` | `<alt-shift-j>` / `<alt-shift-l>` / `<alt-shift-k>` / `<alt-shift-i>` |
| `windowresize` narrower/wider/shorter/taller | `<alt-meta-j>` / `<alt-meta-l>` / `<alt-meta-k>` / `<alt-meta-i>` |
| `windowresize` max / min / reset | `<shift-meta-+>` / `<shift-meta-->` / `<shift-meta-backspace>` |
| `windowtogglemaximize` | `<alt-m>` |
| `windowdefaultsplit h/v` | `<alt-h>` / `<alt-v>` |
| `windownew` | `<alt-n>` |
| `windowclose` / `windowcloseall` | `<alt-q>` / `<alt-shift-q>` |
| `windowconverttab` prompt | `<alt-shift-enter>` |
| `tabnew` | `<meta-t>` on macOS; `<ctrl-n>` on Linux |
| `tabnext` / `tabprevious` | `<alt-]>` / `<alt-[>` |
| `tabfocus 1..9` | `<alt-1>` … `<alt-9>` |
| `tabmove left/right` | `<alt-shift-[>` / `<alt-shift-]>` |
| `tabmove 1..9` | `<alt-shift-1>` … `<alt-shift-9>` |
| `tabsearch` | `` <alt-`> `` |
| `tabclose` | `<alt-w>` |
| `terminalneworsplit` | `<alt-enter>` / `<meta-enter>` |
| `workspacefocus 1..9` | `<meta-1>` … `<meta-9>` |
| `workspacemove 1..9` | `<shift-meta-1>` … `<shift-meta-9>` |
| `workspacesearch` | `` <meta-`> `` |

### Emacs

| Command | Binding |
| --- | --- |
| `windowfocus left/right/down/up` | `<meta-b>` / `<meta-f>` / `<meta-n>` / `<meta-p>` |
| `windowfocus other` | `<ctrl-x>o` |
| `windowmove left/right/down/up` | `<shift-meta-b>` / `<shift-meta-f>` / `<shift-meta-n>` / `<shift-meta-p>` |
| `windowresize` narrower/wider/shorter/taller | `<meta-left>` / `<meta-right>` / `<meta-down>` / `<meta-up>` |
| `windowresize` max / min / reset | `<shift-meta-+>` / `<shift-meta-->` / `<shift-meta-backspace>` |
| `windowclose` / `windowcloseall` | `<meta-k>` / `<shift-meta-k>`; `<ctrl-x>0` / `<ctrl-x>1` |
| `windownew down/right` | `<meta-d>` / `<meta-r>`; `<ctrl-x>2` / `<ctrl-x>3` |
| `windowtogglemaximize` | `<meta-m>` |
| `windowdefaultsplit h/v` | `<ctrl-meta-h>` / `<ctrl-meta-v>` |
| `tabnext` / `tabprevious` | `<meta-]>` / `<meta-[>`; `<ctrl-tab>` / `<ctrl-shift-tab>` |
| `tabmove left/right` | `<shift-meta-[>` / `<shift-meta-]>` |
| `tabnew` | `<meta-t>` |
| `tabclose` | `<meta-w>` |
| `terminalneworsplit` | `<meta-enter>` |
| `workspacefocus 1..9` | `<meta-1>` … `<meta-9>` |
| `workspacemove 1..9` | `<shift-meta-1>` … `<shift-meta-9>` |
| `workspacesearch` | `` <meta-`> `` |

## Rebinding

None of these bindings are fixed. They are entries in `command.key_bindings`,
and you can override any of them in your config. A command binding does not
take precedence over a key handled by the active editor, so choose an unused
key combination. For example, this places window focus on `<ctrl-alt>` plus the arrow
keys:

```yaml tab
command:
  key_bindings:
    "<ctrl-alt-left>": "windowfocus left"
    "<ctrl-alt-right>": "windowfocus right"
    "<ctrl-alt-down>": "windowfocus down"
    "<ctrl-alt-up>": "windowfocus up"
```

```python tab
for key, dir in [
    ("<ctrl-alt-left>", "left"),
    ("<ctrl-alt-right>", "right"),
    ("<ctrl-alt-down>", "down"),
    ("<ctrl-alt-up>", "up"),
]:
    config["command"]["key_bindings"][key] = "windowfocus " + dir
```

Set a binding to `""` to unbind it: the key you rebind a command *from* keeps
its shipped binding until you reset it explicitly, see [Move a binding to
another key](./command-prompt.md#move-a-binding-to-another-key). See
[Config](../config.md) for where the file lives and [key
syntax](./key-syntax.md) for the full grammar of key combinations.
