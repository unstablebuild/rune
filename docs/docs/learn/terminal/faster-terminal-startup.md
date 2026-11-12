---
sidebar_position: 3
---

# Faster terminal startup

Opening a terminal in Rune normally means allocating a fresh emulator,
spawning your shell, and waiting for the prompt to come back. On a
local workspace that is fast enough to feel instant. On a remote
workspace over SSH, or on a busy machine, the wait is noticeable,
especially when you open terminals many times a day.

Rune can keep a small pool of pre-initialized terminals ready in the
background so that the next terminal you open is already warmed up.
The next `terminal` (or any command that opens a terminal tab) hands
you a shell that has already started, instead of one that starts when
you press Enter.

## Enabling the pool

The pool is configured under `terminal.initial_reservoir` in
your Rune config. The value is the number of warm terminals Rune
keeps ready at any time. `0` (the default) disables the pool
entirely.

```yaml tab
terminal:
  # Keep two pre-initialized terminals ready in the background.
  # 0 disables the pool.
  initial_reservoir: 2
```

```python tab
"terminal": {
    # Keep two pre-initialized terminals ready in the background.
    # 0 disables the pool.
    "initial_reservoir": 2,
},
```

A value of `1` is usually enough to make the very next terminal you
open feel instant. A value of `2` covers the common case of opening
a terminal, then quickly opening a second one in another split.
Higher values rarely pay off in practice: the pool is only consulted
when you open a terminal that runs your default shell, and most
sessions do not open many of those back-to-back.

## What gets pooled

The pool only holds terminals that run your **default shell**, the
same shell Rune launches when you open a terminal with no explicit
command. Terminals that run a specific command (for example a
`!`-prefixed alias, an `exo` editor invocation, or anything you start
with `terminal <cmd>`) always spin up fresh, because their command
line does not match what the pool warmed up with.

The pool is warmed once, at startup, in the background. From that
point on the pool is replenished lazily: opening a terminal hands
you the next warm shell, and when the last warm shell is taken Rune
allocates exactly one more to keep at least one ready. The pool is
not automatically refilled back to its configured size after each
open, so a quick burst of terminal opens can outrun the warm-up and
fall back to allocating fresh shells.

Closing a default-shell terminal that is still in a clean state
returns it to the pool instead of discarding it, so terminals you
open and close repeatedly during a session benefit from the pool
too.

## Remote workspaces

The pool works the same way on SSH workspaces: warm shells are
started on the remote host so opening a remote terminal does not
wait for a fresh SSH session each time. If the SSH connection drops
and reconnects, Rune transparently discards the now-stale warm
shells and warms a new batch against the fresh transport. You do
not need to do anything on reconnect.

## Trade-offs

Each warm terminal in the pool is a real shell process and a real
emulator with its own scrollback buffer, so the pool is not free:

- **Memory.** Each pooled terminal carries an emulator with up to
  `terminal.max_lines` of scrollback capacity allocated.
- **Processes.** Each pooled terminal is a live shell process on the
  workspace host (local or remote). Anything in your shell's
  rc files (prompt setup, version managers, SSH agents) runs once
  per warm shell, in the background, before you ever see the tab.
- **Startup cost shifts, it does not vanish.** Rune still pays the
  full startup cost; it just pays it ahead of time, off the
  interactive path. If you open a terminal before the initial
  warm-up has finished, that open waits for the first warm shell to
  be ready rather than racing ahead with a from-scratch allocation,
  so on a cold start the first open is not necessarily faster than
  it would be with the pool disabled.

For most setups, `terminal.initial_reservoir = 1` or `2` is the
sweet spot: terminals feel instant, the memory cost is bounded, and
your shell's startup hooks run in the background instead of in front
of you.
