---
sidebar_position: 4
description: How Rune keeps a terminal running after you close its tab, and how to get back to it.
---

# Detached terminals

Closing the tab of a terminal that is still running something does not end
it. Rune detaches the terminal instead: the program keeps running, Rune keeps
reading its output so it never stalls, and the [status
inbox](./program-status.md#the-status-inbox) tells you when it is done.

## When closing a tab detaches

When you close a tab, a split, or a window that shows a terminal, Rune looks
at what the terminal is doing:

- **A shell waiting at its prompt**, or a terminal whose program exited, is
  closed, as it always was.
- **A terminal running a program** in the foreground of its shell, such as a
  build, a server, or an `ssh` session, or showing a full-screen program, is
  detached.
- **When Rune cannot tell**, it detaches. This happens on a Windows host, or
  one running an older Rune, whose shell does not mark its prompts with OSC
  133.

Rune tells a running program from the process group that holds the terminal,
and from the OSC 133 marks that zsh, fish, and bash 4.4 or later put around
every prompt and command in Rune's terminals. The marks matter when the shell
runs without job control, as it does after `set +m` or `unsetopt monitor`,
which some plugin managers leave set: every command then runs in the shell's
own process group. A shell without job control that does not mark its
commands, such as macOS's bash 3.2, looks idle while it runs one.

`terminaldetach` detaches the terminal in the current window even when its
shell is idle, and `terminalclose` ends it even when it is running something.

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
by the start of its id, its state, and its title.

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

A terminal lives as long as the Rune that runs it. Quitting Rune ends every
terminal, detached ones included, so `quit` warns you when a detached terminal
is still running a program. `forcequit!` and `writeforcequit!` do not ask.

On the next start, the output of a detached terminal comes back in a hidden
tab, next to a fresh shell, the same way Rune restores the terminals you left
open.
