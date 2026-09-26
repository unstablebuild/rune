# basics.star — the first-run tutorial.
#
# Walks the user through the three things they need to know to be
# productive in Rune: where to put work in the empty workspace,
# how to open a workspace (`:workspaceopen`), and how to start editing
# files inside that workspace (`:edit`), plus the layout model
# (the meta/alt/shift system) and the windows and tabs that hold
# that content.
#
# The DSL is interpreted by ide/idetutorial/starlarktutorial. The
# entry function runs on its own Starlark goroutine; each blocking
# builtin (wait_command, wait_event, confirm) is one screen of the
# tutorial tile that stays up until its milestone is met, and returns
# a real value so authors can branch, loop, and compose helpers with
# regular Starlark control flow.

ck = command_key()
mode = editor_mode()
# Super belongs to the desktop on Linux, so the Linux presets hold Rune's
# layout on <alt> and <ctrl-alt> (<alt-shift> in Emacs mode, whose editor
# owns the Alt chords) and step tabs with the bracket keys.
linux = os() == "linux"
# Helix is modal too: every surface has NORMAL and INSERT modes, the
# prompt sits on the same key, and motion is on the home row. Only the
# picker keys differ, so copy about modality branches on this and copy
# about keys branches on the mode itself.
modal_mode = mode == "vim" or mode == "helix"
# The config file moves with the data directory (`rune -d`), so copy
# that names it has to ask the host instead of assuming ~/.rune.
config_file = config_path()
config_file_ref = ("(`" + config_file + "`)") if config_file else ""

# Buffer motion and layout direction are separate systems. The file explorer
# uses each editor's native movement, while layout commands use HJKL in vim
# and helix mode, IJKL in standard mode, and PNBF in Emacs mode.
if mode == "vim":
    dir_phrase = "the home row, `h` `j` `k` `l`"
    completer_pick_phrase = "`<ctrl-j>` / `<ctrl-k>` (or `<up>` / `<down>`)"
    completer_move_phrase = ("press `<ctrl-j>` to move down the list and " +
                             "`<ctrl-k>` to move up (or `<down>` / `<up>`)")
elif mode == "helix":
    # Helix's own pickers walk their list with <ctrl-n> / <ctrl-p>, and
    # Rune's completers answer to the same pair.
    dir_phrase = "the home row, `h` `j` `k` `l`"
    completer_pick_phrase = "`<ctrl-n>` / `<ctrl-p>` (or `<up>` / `<down>`)"
    completer_move_phrase = ("press `<ctrl-n>` to move down the list and " +
                             "`<ctrl-p>` to move up (or `<down>` / `<up>`)")
elif mode == "emacs":
    dir_phrase = "the motion keys `<ctrl-p>` / `<ctrl-n>` or the arrow keys"
    completer_pick_phrase = "`<ctrl-p>` / `<ctrl-n>` (or `<up>` / `<down>`)"
    completer_move_phrase = ("press `<ctrl-n>` to move down the list and " +
                             "`<ctrl-p>` to move up (or `<down>` / `<up>`)")
else:
    dir_phrase = "the arrow keys"
    completer_pick_phrase = "the arrow keys `<up>` / `<down>`"
    completer_move_phrase = ("press `<down>` to move down the list and " +
                             "`<up>` to move up")

def completer_steps(what):
    # Sub-steps of an auto-completer step: typing and moving the
    # selection are alternatives, not an order to follow.
    return """\
   - If you know the """ + what + """, start typing it: the list narrows
     as you type.

   - `<tab>` selects whatever sits at the top of the list, so narrowing
     until what you want is first is all it takes.

   - To select something further down, """ + completer_move_phrase + """,
     then press `<tab>` or `<enter>`."""

def keyhint(cmd, *args):
    k = key_for(cmd, *args)
    return (" Default key: `" + k + "`.") if k else ""

def keypress(cmd, *args):
    # The user's real bound key for cmd, phrased as a keypress
    # instruction. Falls back to the command-prompt wording when the
    # command is unbound, so copy adapts to mode and custom rebinds.
    k = key_for(cmd, *args)
    if k:
        return "press `" + k + "`"
    return ("open the command prompt (`" + ck + "`) and run `" +
            command_line(cmd, args) + "`")

def command_line(cmd, args):
    return cmd + ((" " + " ".join(args)) if len(args) else "")

def keylabel(cmd, *args):
    k = key_for(cmd, *args)
    return "`" + (k if k else command_line(cmd, args)) + "`"

workspace_slot_key_row = " ".join([
    keylabel("workspacefocus", "1"),
    keylabel("workspacefocus", "2"),
    keylabel("workspacefocus", "3"),
    keylabel("workspacefocus", "4"),
    keylabel("workspacefocus", "5"),
    keylabel("workspacefocus", "6"),
    keylabel("workspacefocus", "7"),
    keylabel("workspacefocus", "8"),
    keylabel("workspacefocus", "9"),
])
def args_match(got, want):
    if len(got) != len(want):
        return False
    for i in range(len(want)):
        if got[i] != want[i]:
            return False
    return True

def wait_expected_command(title, command, expected_args, text):
    # A successfully dispatched command has already taken effect, so keep the
    # lesson armed and ask for the intended direction rather than rejecting it.
    #
    # These steps come in sequences that ask for one direction and then
    # another; the copy describes the whole sequence and the notification
    # says which half is still due.
    for _ in range(1000):
        result = wait_command(
            title   = title,
            command = command_line(command, expected_args),
            text    = text,
        )
        if args_match(result.args, expected_args):
            return
        notify(
            level   = info,
            message = ("That ran `" + command_line(command, result.args) +
                       "`. Now run `" + command_line(command, expected_args) + "`."),
        )

focus_key_row = " | ".join([
    keylabel("windowfocus", "up"),
    keylabel("windowfocus", "left"),
    keylabel("windowfocus", "down"),
    keylabel("windowfocus", "right"),
])
move_key_row = " | ".join([
    keylabel("windowmove", "up"),
    keylabel("windowmove", "left"),
    keylabel("windowmove", "down"),
    keylabel("windowmove", "right"),
])
resize_key_row = " | ".join([
    keylabel("windowresize", "increase", "height"),
    keylabel("windowresize", "decrease", "width"),
    keylabel("windowresize", "decrease", "height"),
    keylabel("windowresize", "increase", "width"),
])
# Emacs has no bare `windownew`; its split keys are direction-qualified.
# Use the rightward one so every mode ends up with the same layout.
split_window_args = ["right"] if mode == "emacs" else []

if linux:
    hjkl_layout_list = """\
- Hold `<alt>` with `h` `j` `k` `l` to focus a window in that direction.
- Hold `<ctrl-alt>` with `[` or `]` to focus the previous or next tab.
- Hold `<ctrl-shift-alt>` to move the content instead of focus it:
  - `<ctrl-shift-alt>` + `h` `j` `k` `l` moves the focused window's content.
  - `<ctrl-shift-alt>` + `[` or `]` moves the current tab left or right in the tab list.
- `<ctrl-alt>` + `h` `j` `k` `l` resizes the focused window.
"""
    emacs_layer = "`<alt-shift>`"
    emacs_layer_name = "`<alt-shift>` layer"
    emacs_move_add = "`<ctrl>`"
    emacs_move_layer = "`<ctrl-shift-alt>`"
    standard_resize_add = "add `<ctrl>` as well"
else:
    hjkl_layout_list = """\
- Hold `<meta>` with `h` `j` `k` `l` to focus a window in that direction.
- Hold `<alt>` with `h` or `l` to focus the previous or next tab.
- Add `<shift>` to move the content instead of focus it:
  - `<shift-meta>` + `h` `j` `k` `l` moves the focused window's content.
  - `<shift-alt>` + `h` or `l` moves the current tab left or right in the tab list.
"""
    emacs_layer = "`<meta>`"
    emacs_layer_name = "host Meta layer"
    emacs_move_add = "`<shift>`"
    emacs_move_layer = "`<shift-meta>`"
    standard_resize_add = "add `<meta>`"

if mode == "vim":
    layout_pattern_md = """\
## HJKL controls the layout

Vim's keyboard-first design keeps navigation under your fingers. Repeated
actions become muscle memory, so you spend less time searching for interface
controls and can keep your attention on the work.

Rune carries that same HJKL language into layout management: `H` points left,
`J` down, `K` up, and `L` right.

""" + hjkl_layout_list
elif mode == "helix":
    layout_pattern_md = """\
## HJKL controls the layout

Helix's keyboard-first design keeps navigation under your fingers. Repeated
actions become muscle memory, so you spend less time searching for interface
controls and can keep your attention on the work.

Rune carries that same HJKL language into layout management: `H` points left,
`J` down, `K` up, and `L` right.

""" + hjkl_layout_list
elif mode == "emacs":
    layout_pattern_md = """\
## Emacs directions control the layout

Rune keeps `<ctrl-p>` / `<ctrl-n>` and `<ctrl-b>` / `<ctrl-f>` available for
editing. Rather than teach a second direction map, Rune changes the target:
hold """ + emacs_layer + """ with the same PNBF directions to focus windows, then add """ + emacs_move_add + """
to move window content instead. Reusing that muscle memory keeps repeated
layout actions fast, and the """ + emacs_layer_name + """ stays reachable from terminals.

- Hold """ + emacs_layer + """ and press P/N/B/F to focus a window in that direction.
- Press """ + keylabel("tabprevious") + """ / """ + keylabel("tabnext") + """ to focus the previous or next tab.
- Add """ + emacs_move_add + """ to move the content instead of focus it:
  - """ + emacs_move_layer + """ + P/N/B/F moves the focused window's content.
  - """ + keylabel("tabmove", "left") + """ / """ + keylabel("tabmove", "right") + """ moves the current tab left or right in the tab list.
- Manage windows:
  - """ + keylabel("windownew", "down") + """ / """ + keylabel("windownew", "right") + """ splits below or right.
  - """ + keylabel("windowclose") + """ closes a window, and """ + keylabel("windowcloseall") + """ closes the others.
  - """ + keylabel("windowtogglemaximize") + """ toggles maximization.
"""
else:
    layout_pattern_md = """\
## Alt drives the layout

Vim made generations of programmers extraordinarily productive by keeping
navigation under their fingers. Repeated actions become muscle memory,
reducing menu hunting and the mental fatigue of switching attention between
code and interface controls.

Keyboard-driven does not have to mean learning an entirely new way to edit.
Rune brings that advantage to a familiar, non-modal editor by treating IJKL
as a second set of arrow keys:

```text
    I
  J K L
```

`I` points up, `J` left, `K` down, and `L` right. So hold `<alt>` and press IJKL to focus
a window. Add `<shift>` to move its content, or """ + standard_resize_add + """ to resize it.

The pattern is Alt plus the target: IJKL affects windows, brackets affect tabs,
and adding `<shift>` moves content instead of focus.
"""

# The workspace-open step branches on the host OS. On macOS the File
# menu's Open Project… item drives the native open panel, so the copy
# points there first; the command prompt stays documented as the
# keyboard alternative. wait_command("workspaceopen") advances on
# either path, so only the wording changes.
if os() == "darwin":
    open_workspace_section = """\
## Opening a project

1. Open the **File** menu in the macOS menu bar and choose
   **Open Project…**.
2. Pick a project directory in the panel and click **Open**.

Prefer the keyboard? Press `""" + ck + """`, type `workspaceopen`, and use
the auto-completer to pick a workspace: move through the list with
""" + completer_pick_phrase + """. Or type the path yourself."""
else:
    open_workspace_section = """\
## Opening a workspace

1. Press `""" + ck + """` to open the command prompt.

2. Type `workspaceopen` and use the auto-completer
   to **pick a workspace** from the list:

""" + completer_steps("path") + """

3. Press `<enter>` to open it."""

welcome_md = """\
This is the **home workspace**: a scratch workspace rooted at `~/` that
Rune shows when no project workspace is open at the current slot.
Use it for files outside any project, quick terminals,
or to keep notes between sessions.

## Switching workspaces

Rune has **nine workspace slots**. Their current bindings are:

""" + workspace_slot_key_row + """

**Press one to jump to that slot.**

You'll see the current workspace number change at the bottom left of the screen.
As you add more workspaces, you'll see them appear here.

Every empty slot shows this same home workspace; a slot only gets a workspace attached when
you open one inside it. So slot 1 may be the project you're working on while slots 2-9 are
still the home workspace, ready for whatever you need.

""" + open_workspace_section + """
"""

edit_md = """\
You're in a real workspace now 🎉 To open a file:

1. Press `""" + ck + """` to open the command prompt.

2. Type `edit` followed by a filename.

3. Use the auto-completer to pick the file you want:

""" + completer_steps("file name") + """

4. Press `<enter>`.
"""

layout_md = """\
Rune is a full tiling window manager: you split the screen into
**windows**, fill each one with **tabs** (files, terminals, agents),
and group whole projects into **workspaces**. The editor is
just one kind of content among many. 

Every layout action is a command you can type at the prompt; the default keys are just
shortcuts, and they follow a directional pattern.

""" + layout_pattern_md + """
"""

directional_layout_md = """\
## Your current layout keys

| Action | Up | Left | Down | Right |
| --- | --- | --- | --- | --- |
| Focus | """ + focus_key_row + """ |
| Move | """ + move_key_row + """ |
| Resize | """ + resize_key_row + """ |

The table shows your current configuration, based on the preset you chose earlier.

**Play with them and get comfortable.** They all work while this lesson
is up.
"""

split_window_md = """\
A **window** is a tile on screen, and right now this workspace has just
one.

Split the focused window in two: """ + keypress("windownew", *split_window_args) + """.
The new window lands to the right.
"""

terminal_md = """\
The new window is empty, and windows hold any kind of content, not just
files. Fill this one with a terminal.

Open a terminal here: """ + keypress("terminalneworsplit") + """.
"""

modal_surfaces_md = """\
You picked **""" + mode + """** editor mode, and in """ + mode + """ mode every input surface
is modal, not just the editor. This includes the terminal, Rune's
console, and the file explorer 🚀

The cursor shape tells you which mode a surface is in: a block cursor
means NORMAL mode, a bar cursor means INSERT mode.

So when a terminal or console is focused and you want to open the command
prompt (`""" + ck + """`), first switch back to NORMAL mode with
`<esc>`.
"""

split_horizontal_md = """\
Splits have a direction, and so far every one has landed to the right.
`windowdefaultsplit` aims the next one.

Send it **below** the focused window instead.

Flip the split direction: """ + keypress("windowdefaultsplit", "h") + """.
"""

terminal_split_md = """\
The same key opens a terminal in a new split when the window already
has content. Do it again and watch the split land along the direction
you just set.

Open another terminal split: """ + keypress("terminalneworsplit") + """.
"""

focus_window_md = """\
The screen holds multiple windows now. Directional layout commands move focus
across the splits, so you can hop between them without the mouse.

- `windowfocus up` focuses the window above.""" + keyhint("windowfocus", "up") + """
- `windowfocus left` focuses the window to the left.""" + keyhint("windowfocus", "left") + """

Do both, in order:

1. Focus the window above: """ + keypress("windowfocus", "up") + """.

2. Then the window on the left: """ + keypress("windowfocus", "left") + """.
"""

move_window_md = """\
`<shift>` turns "go to" into "move". Add it to the focus direction and the
focused window's content swaps with its neighbor.

- `windowmove right` moves the focused window to the right.""" + keyhint("windowmove", "right") + """
- `windowmove left` moves it back to the left.""" + keyhint("windowmove", "left") + """

Do both, in order:

1. Move the editor to the right: """ + keypress("windowmove", "right") + """.

2. Then move it back to the left: """ + keypress("windowmove", "left") + """.
"""

resize_direction_md = ("""\
Window resizing keeps the IJKL directions: `I` makes the window taller, `J`
narrower, `K` shorter, and `L` wider.
""" if mode == "standard" else (("""\
Emacs mode keeps resize on `<ctrl-shift>` arrows instead of taking more editing
letters: up makes the window taller, left narrower, down shorter, and right
wider. The GNU `<ctrl-x>^` / `<ctrl-x>{` / `<ctrl-x>-` / `<ctrl-x>}` chords
resize too.
""" if linux else """\
Emacs mode keeps resize on host-Meta arrows instead of taking more editing
letters: up makes the window taller, left narrower, down shorter, and right
wider. These work from terminals as well as editors.
""") if mode == "emacs" else """\
Window resizing uses matching arrow directions: up makes the window taller,
left narrower, down shorter, and right wider.
"""))

resize_window_md = resize_direction_md + """

- `windowresize increase width` makes the focused window wider.""" + keyhint("windowresize", "increase", "width") + """
- `windowresize decrease width` makes it narrower.""" + keyhint("windowresize", "decrease", "width") + """

Do both, in order:

1. Make the window wider: """ + keypress("windowresize", "increase", "width") + """.

2. Then make it narrower again: """ + keypress("windowresize", "decrease", "width") + """.
"""

fullscreen_window_md = """\
When you want to focus on one window, `windowtogglemaximize` grows it
to fill the whole editor area. Run it again, or focus another window,
to restore the layout.""" + keyhint("windowtogglemaximize") + """

Toggle fullscreen now: """ + keypress("windowtogglemaximize") + """.
"""

close_window_md = """\
`windowclose` closes the focused split and hands focus back to the
window you came from. It will not close your last window: a workspace
always keeps at least one.""" + keyhint("windowclose") + """

Close any window now: """ + keypress("windowclose") + """. Focus lands on
the window you came from.
"""

emacs_close_others_md = """\
You used """ + keylabel("windowclose") + """ to close one window. It mirrors the familiar
browser pattern where """ + keylabel("tabclose") + """ closes a tab and adding `<shift>` closes
the whole window. """ + keylabel("windowcloseall") + """ keeps the focused window and closes
every other split.

Keep only this window: """ + keypress("windowcloseall") + """.
"""

tabs_intro_md = ("""\
A window shows one **tab** at a time: a file, a terminal, task output or agent. Emacs
mode keeps tab lifecycle on Rune's """ + emacs_layer_name + """: """ + keylabel("tabnew") + """ starts a
new tab and """ + keylabel("tabclose") + """ closes the current one.
""" if mode == "emacs" else ("""\
A window shows one **tab** at a time: a file, a terminal, task output. Helix's
`<space>` leader menu is here too: `<space>e` toggles the file explorer, the
way `file_explorer` does in Helix.
""" if mode == "helix" else """\
A window shows one **tab** at a time: a file, a terminal, task output.
"""))

# A focused terminal in INSERT mode answers <shift-tab> itself, so the
# binding never reaches Rune from there.
shift_tab_note = ("""
> If the window in focus is a terminal, it takes `<shift-tab>` before
> Rune sees it: press `<esc>` to go back to NORMAL mode first.
""" if modal_mode and "shift-tab" in key_for("fexplorer") else "")

tabs_md = tabs_intro_md + """
You already have one open. Let's add another from the file explorer.

Open the file explorer: """ + keypress("fexplorer") + """.
""" + shift_tab_note

tab_open_file_md = """\
The explorer is the workspace tree, and it is a live buffer: edit a
name to rename, add a line to create, delete a line to remove, then
save the buffer via `write` to apply. For now, just open a file.

1. Move to a file with """ + dir_phrase + """.

2. Press `<enter>`.

It opens as a new tab in the window you came from.
"""

tab_close_explorer_md = """\
`fexplorer` is a toggle: the same key that opened the explorer closes
it again. With your file open, you no longer need the tree taking up
space.

Toggle the explorer closed: """ + keypress("fexplorer") + """.
"""

tab_switch_intro_md = ("""\
That window now holds two tabs. Emacs muscle memory works here too:
""" + keylabel("tabprevious") + """ moves to the previous tab and """ + keylabel("tabnext") + """ moves to the next.
Both wrap around.
""" if mode == "emacs" else """\
That window now holds two tabs. `tabnext` / `tabprevious` cycle through them
and wrap around. Each preset has a horizontal tab pair; adding `<shift>` to
that pair reorders the current tab instead.
""")

tab_switch_md = tab_switch_intro_md + """

Do both, in order:

1. Switch to the next tab: """ + keypress("tabnext") + """.

2. Then back to the previous one: """ + keypress("tabprevious") + """.
"""

tab_move_intro_md = ("""\
For tab placement, """ + keylabel("tabmove", "left") + """ moves the current tab left and
""" + keylabel("tabmove", "right") + """ moves it right.
""" if mode == "emacs" else """\
Now use the same horizontal pair with `<shift>` to change the tab's position
instead of switching tabs.
""")

tab_move_md = tab_move_intro_md + """

- `tabmove left` moves it one slot left.""" + keyhint("tabmove", "left") + """
- `tabmove right` moves the current tab one slot right.""" + keyhint("tabmove", "right") + """

Do both, in order:

1. Move the current tab left: """ + keypress("tabmove", "left") + """.

2. Then move it back right: """ + keypress("tabmove", "right") + """.
"""

close_tab_md = """\
`tabclose` closes the focused tab; the next tab in the list takes its
place.""" + keyhint("tabclose") + """

To close the current tab, """ + keypress("tabclose") + """.
"""

terminals_md = """\
Rune is also a terminal multiplexer 👾: terminals live in windows and tabs, just
like files. For a program you only need to run once, skip the terminal and run
it straight from the command prompt.

## Running a quick, one-shot program ⚡

- `<cmd>!` runs a program in a floating window that shows its output, for
  example `<cmd>! git log`.
- `<cmd>!!` runs a program but hides its output. Use it when you only care
  whether it worked.

Let's run `git log`:

1. Press `""" + ck + """` to open the command prompt.

2. Type `! git log` and press `<enter>`.

Rune opens a floating window streaming its output.
"""

terminals_close_md = """\
The `git log` output is sitting in a floating window. It is a window,
not a tab, so close it with `windowclose`.

Close it now: """ + keypress("windowclose") + """.
"""

guicommands_md = """\
You have run a lot of commands by now: `windownew`, `terminalneworsplit`, `tabnext`,
`fexplorer`. They are all lowercase, no spaces, and read like `<category><action>`: the
category first (`window`, `terminal`, `tab`), then what you do to it (`new`, `close`,
`next`).

This is the Rune way: **form earns its place by serving function**. The naming
is what makes the fuzzy finder predictable and optimized for recall: every command in a
category shares a prefix, you can guess the sequence and let the finder rank it first.
`workspaceopen` resolves from typing `woso`, and `woso` fires in four keystrokes
instead of thirteen.

Let's try it with themes 💄 Themes control every color Rune draws with, and the `guitheme`
command switches the active one.

1. Press `""" + ck + """` to open the command prompt.
2. Type `guith` and press `<space>` to complete to `guitheme`.
3. Type a space, then use """ + completer_pick_phrase + """ to pick a
   different theme from the completer, and press `<enter>`.
"""

config_open_md = """\
Nice, the whole interface just re-themed at once, terminal colors and
all.

`guitheme` only changed this session. The file that decides what Rune
looks like when it starts """ + config_file_ref + """ is one keypress away.

Open it now: """ + keypress("config") + """.
"""

def config_edit_md(theme):
    return """\
`command.key_bindings` is active: every key this tutorial taught you
lives there. Everything else is commented out at Rune's own defaults,
so reading the file is how you find what is tunable.

1. Scroll to `gui.default_theme` and set it to `""" + theme + """`.

2. Save the file: """ + keypress("write") + """.
"""

config_done_md = """\
Rune reads the config once at startup, so `gui.default_theme` takes
effect the next time you launch. `guitheme` stays the quick way to
change the theme for the session you are in right now.
"""

cheatsheet_md = """\
There's a `cheatsheet` command that condenses all of this tutorial's
learnings and more into a single cheat sheet you can pull up any time.

Open it now: """ + keypress("cheatsheet") + """.
"""

def teach_edit():
    wait_command(
        title   = "Open a file",
        command = "edit",
        text    = edit_md,
    )


def teach_split_window():
    text = layout_md + "\n" + split_window_md
    if len(split_window_args):
        wait_expected_command(
            title         = "Split a window",
            command       = "windownew",
            expected_args = split_window_args,
            text          = text,
        )
    else:
        wait_command(
            title   = "Split a window",
            command = "windownew",
            text    = text,
        )


def teach_terminal():
    wait_command(
        title   = "Open a terminal",
        command = "terminalneworsplit",
        text    = terminal_md,
    )


def teach_split_horizontal():
    # A terminal is focused at this point, so modal users first need to
    # know how to reach the command prompt from it.
    text = split_horizontal_md
    if modal_mode:
        text = modal_surfaces_md + "\n" + split_horizontal_md
    wait_expected_command(
        title         = "Aim the next split",
        command       = "windowdefaultsplit",
        expected_args = ["h"],
        text          = text,
    )


def teach_terminal_split():
    wait_command(
        title   = "Open a terminal split",
        command = "terminalneworsplit",
        text    = terminal_split_md,
    )


def teach_focus_window():
    text = directional_layout_md + "\n" + focus_window_md
    wait_expected_command(
        title         = "Move between windows",
        command       = "windowfocus",
        expected_args = ["up"],
        text          = text,
    )
    wait_expected_command(
        title         = "Move between windows",
        command       = "windowfocus",
        expected_args = ["left"],
        text          = text,
    )


def teach_move_window():
    wait_expected_command(
        title         = "Move a window",
        command       = "windowmove",
        expected_args = ["right"],
        text          = move_window_md,
    )
    wait_expected_command(
        title         = "Move a window",
        command       = "windowmove",
        expected_args = ["left"],
        text          = move_window_md,
    )


def teach_resize_window():
    wait_expected_command(
        title         = "Resize a window",
        command       = "windowresize",
        expected_args = ["increase", "width"],
        text          = resize_window_md,
    )
    wait_expected_command(
        title         = "Resize a window",
        command       = "windowresize",
        expected_args = ["decrease", "width"],
        text          = resize_window_md,
    )


def teach_fullscreen_window():
    wait_command(
        title   = "Fullscreen a window",
        command = "windowtogglemaximize",
        text    = fullscreen_window_md,
    )


def teach_close_window():
    wait_command(
        title   = "Close a window",
        command = "windowclose",
        text    = close_window_md,
    )


def teach_emacs_close_others():
    if mode != "emacs":
        return
    wait_command(
        title   = "Keep one window",
        command = "windowcloseall",
        text    = emacs_close_others_md,
    )


def teach_tabs():
    wait_command(
        title   = "Open another tab",
        command = "fexplorer",
        text    = tabs_md,
    )

    wait_event(
        event = "open",
        title = "Open a file",
        text  = tab_open_file_md,
    )

    wait_command(
        title   = "Close the explorer",
        command = "fexplorer",
        text    = tab_close_explorer_md,
    )


def teach_switch_tabs():
    wait_command(
        title   = "Switch tabs",
        command = "tabnext",
        text    = tab_switch_md,
    )
    wait_command(
        title   = "Switch tabs",
        command = "tabprevious",
        text    = tab_switch_md,
    )


def teach_move_tabs():
    wait_expected_command(
        title         = "Reorder tabs",
        command       = "tabmove",
        expected_args = ["left"],
        text          = tab_move_md,
    )
    wait_expected_command(
        title         = "Reorder tabs",
        command       = "tabmove",
        expected_args = ["right"],
        text          = tab_move_md,
    )


def teach_close_tab():
    wait_command(
        title   = "Close a tab",
        command = "tabclose",
        text    = close_tab_md,
    )


def teach_terminals():
    wait_command(
        title   = "Run a program",
        command = "! git log",
        text    = terminals_md,
    )

    wait_command(
        title   = "Close the output window",
        command = "windowclose",
        text    = terminals_close_md,
    )


def teach_cheatsheet():
    wait_command(
        title   = "Your cheatsheet",
        command = "cheatsheet",
        text    = config_done_md + "\n" + cheatsheet_md,
    )


def teach_guicommands():
    theme = wait_command(
        title   = "Why commands look like that",
        command = "guitheme",
        text    = guicommands_md,
    )
    return theme.args[0] if len(theme.args) else "romero"


def teach_config(theme):
    wait_command(
        title   = "Your configuration",
        command = "config",
        text    = config_open_md,
    )

    # `config` opens whatever path the binary was launched with
    # (config.yaml, config.star, or a `-c` override), so match the URI
    # on a substring rather than a full path.
    wait_event(
        event = "flush",
        uri   = "config",
        title = "Make it stick",
        text  = config_edit_md(theme),
    )


def run():
    wait_command(
        title   = "Welcome 🔥",
        command = "workspaceopen",
        text    = welcome_md,
    )

    teach_edit()
    teach_split_window()
    teach_terminal()
    teach_split_horizontal()
    teach_terminal_split()
    teach_focus_window()
    teach_move_window()
    teach_resize_window()
    teach_fullscreen_window()
    teach_close_window()
    teach_emacs_close_others()
    teach_tabs()
    teach_switch_tabs()
    teach_move_tabs()
    teach_close_tab()
    teach_terminals()
    theme = teach_guicommands()
    teach_config(theme)
    teach_cheatsheet()


tutorial(id = "basics", title = "Rune basics", version = "72", entry = run)
