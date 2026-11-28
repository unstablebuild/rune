---
sidebar_position: 4
description: How Rune keeps a terminal running after you close its tab, and how to get back to it.
---

# Detached terminals

Closing the tab of a terminal that is still running something does not have
to end it. Rune asks whether to detach the terminal or terminate it. A
detached terminal keeps running its program, Rune keeps reading its output so
it never stalls, and the [status inbox](./program-status.md#the-status-inbox)
tells you when it is done.

## Closing a terminal's tab

When you close the tab of a terminal, with `tabclose`, `tabcloseall`,
`tabcloseinactive`, or the tab's close icon, Rune looks at what the terminal
is doing:

- **A shell waiting at its prompt**, or a terminal whose program exited, is
  closed without asking.
- **A terminal running a program** in the foreground of its shell, such as a
  build, a server, or an `ssh` session, or showing a full-screen program,
  asks what to do with it:
  - **Cancel** (`c`, `<enter>`, or `<esc>`) keeps the tab open.
  - **Detach** (`d`) closes the tab and keeps the terminal running.
  - **Terminate** (`t`) closes the tab and ends the terminal and its program.
- **When Rune cannot tell** what the terminal runs, it asks too. This happens
  on a Windows host, or one running an older Rune, whose shell does not mark
  its prompts with OSC 133.

A terminal that another tab still shows, on this machine or on another one,
is closed without asking: the other tabs keep it. `tabcloseall` and
`tabcloseinactive` close every other tab at once and ask a single question
about the terminals running programs; cancelling keeps only those tabs.

Rune tells a running program from the process group that holds the terminal,
and from the OSC 133 marks that zsh, fish, and bash 4.4 or later put around
every prompt and command in Rune's terminals. The marks matter when the shell
runs without job control, as it does after `set +m` or `unsetopt monitor`,
which some plugin managers leave set: every command then runs in the shell's
own process group. A shell without job control that does not mark its
commands, such as macOS's bash 3.2, looks idle while it runs one.

Closing a terminal's tab from the status inbox ends the terminal, asking first
when it is running a program: a detached terminal would only be listed in the
inbox again.

`terminaldetach` and `terminalclose` skip the question: `terminaldetach`
detaches the terminal in the current window even when its shell is idle, and
`terminalclose` ends it.

A few closes detach a terminal that may be running something without asking:

- A terminal outside a tab, opened with `terminalnew`, whose window shows
  something else, for example after `tabnext`.
- A tab an extension closes.

## Getting back to a detached terminal

A detached terminal running a program is listed in the status inbox
(`programstatus`) as `terminal`, `working`, with a live preview of its screen.

- `<enter>` shows it again in a new tab of the current window.
- `<delete>` ends it, after asking.

When the program finishes and the shell is back at its prompt, the record
turns `done` and Rune notifies you, wherever you are. The shell stays, so you
can come back to read the output or keep working in it. If the shell itself
exits while detached, the terminal goes away along with its records.

`terminalattach` shows a detached terminal without the inbox. Without an
argument it shows the first detached terminal. Press `<tab>` after the command
to pick one: every terminal of the workspace is listed, detached ones first,
by the start of its id, how many tabs show it, what it runs, and its title.

| Command | What it does |
| --- | --- |
| `terminaldetach` | Close the terminal's tab and keep the terminal running. |
| `terminalattach [<terminal-id>]` | Show a terminal in a new tab of the current window. |
| `terminalclose [<terminal-id>]` | End a terminal and close every tab showing it. Asks first when it is running a program or is shown in another tab. |

## One terminal in several tabs

`terminalattach` on a terminal that another tab already shows opens a second
view of the same terminal. Every view shows the same screen and scrollback,
and what you type in any of them reaches the same program.

A program draws for exactly one size, so a terminal has a single size however
many views show it: the size of the view you focused last. The other views
show its bottom-left corner, cutting the top rows and the right columns that
do not fit, or padding the rest with the background. Focus a view to give the
terminal its size.

## Terminals end with Rune

A terminal of a local workspace, or of an `ssh://` one, lives as long as the
Rune that runs it. Quitting Rune ends every such terminal, detached ones
included, so `quit` warns you when a detached terminal is still running a
program. `forcequit!` and `writeforcequit!` do not ask.

On the next start, the output of a detached terminal comes back in a hidden
tab, next to a fresh shell, the same way Rune restores the terminals you left
open.

## Terminals on another machine

The terminals of a `rune://` or `tailscale://` workspace run on the machine
that serves it, and belong to the Rune serving there: the `rune --headless`
node, or the Rune a user runs on that machine. What you see is a live copy of
each terminal, fed over the mesh.

- **They survive the connection.** When the link drops, your laptop sleeps,
  or you switch networks, the program keeps running on the other machine and
  its terminal keeps reading its output. The tab keeps showing what it had,
  and catches up with the terminal's screen once the machine is reachable
  again. A terminal whose output outruns a slow link is caught up with the
  same way, in a jump, rather than left behind.
- **They survive Rune.** Quitting Rune leaves them running, so `quit` does not
  warn about them. On the next start, the tabs you left open attach to the
  same terminals again, with their programs still running; a terminal the
  machine no longer has comes back as a fresh shell showing the output you
  left.
- **They end with the machine.** Restarting the Rune serving the workspace
  ends its terminals, as does `terminalclose`.

A detached terminal on the other machine is listed in the status inbox and by
`terminalattach` as any other, and reported `working` and `done` the same way.
Another machine attached to the same terminal counts as a view of it:
`terminalattach` completion counts that machine's tab among the tabs showing
the terminal, and closing your tab leaves the terminal to that machine.

An `ssh://` workspace's terminals are not served this way: their shells run
on the remote host, but their terminals live in your Rune and end with the
connection.
