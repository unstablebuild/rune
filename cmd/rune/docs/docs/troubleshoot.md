---
sidebar_position: 4
---

# Troubleshoot

## A key binding is not firing

Before assuming the binding itself is misconfigured, take a step back and check
whether the keystroke is even reaching Rune. On macOS in particular, several useful
combinations are claimed by the operating system before any application sees them.
`<ctrl-space>`, for example, is bound by default to "Select the previous input source"
and never makes it through to Rune, even though Rune binds it by default.

### Solution 1: Disable the conflicting shortcut

#### 1. Disable the conflicting macOS shortcut

For `<ctrl-space>` specifically:

1. Open **System Settings → Keyboard → Keyboard Shortcuts…**
2. Select **Input Sources** in the left pane.
3. Uncheck **Select the previous input source** (and **Select next source in Input menu** if you also use `<ctrl-option-space>`).
4. Close the dialog.

The same procedure applies to any other combination that the OS is intercepting:
look under **Keyboard Shortcuts**, **Mission Control**, **Spotlight**, and
**Accessibility** for shortcuts that overlap with the binding you are trying to use.

On Linux, key events are dispatched by your desktop environment, window manager,
or compositor before they reach Rune. Consult the documentation for whichever
component governs input on your system and remove or remap the conflicting shortcut:

- **GNOME:** **Settings → Keyboard → View and Customize Shortcuts**.
- **KDE Plasma:** **System Settings → Shortcuts**, and **System Settings → Window Management → Window Behavior** for window-level bindings.
- **Xfce:** **Settings → Keyboard → Application Shortcuts** and **Window Manager → Keyboard**.
- **Tiling window managers** (i3, Sway, Hyprland, dwm, …): check the relevant config file (for example `~/.config/i3/config`, `~/.config/sway/config`, or `~/.config/hypr/hyprland.conf`) for a `bindsym`/`bind` line that claims the combination, and either remove it or rebind it.
- **Input method frameworks** (IBus, Fcitx, ibus-anthy, …) also intercept keys such as `<ctrl-space>` for input-source switching. Disable the trigger in the framework's settings if you do not need it.

#### 2. Confirm the keystroke reaches Rune with the `keydump` command

Once the OS-level shortcut is disabled, run the `keydump` command inside Rune to see exactly
which key events Rune receives. Press the binding you want to verify. Each keystroke that reaches Rune is printed
as it is decoded. If nothing appears, the event is still being swallowed before it
reaches Rune; revisit step 1 and check for window manager shortcuts that may
also be claiming the combination.

:::tip
Press `<ctrl-c>` followed by `<ctrl-d>` to exit `keydump` and return to the normal prompt.
:::

### Solution 2: Run your OS in Kiosk Mode

In Linux and Ubuntu, you can use [Gnome Kiosk](https://github.com/GNOME/gnome-kiosk)
out of the box. It runs a minimal display server, so keys like `<meta>` pass through
into Rune IDE instead of opening the start menu (and, `<alt+tab>` still tabs through OS windows).
Other OSes may have similar software to run them in Kiosk Mode as well.

Here is a step-by-step guide of how to set up Kiosk Mode for Ubuntu:

1. Install Gnome Kiosk and script session: `sudo apt install gnome-kiosk gnome-kiosk-script-session`
2. Edit `~/.local/bin/gnome-kiosk-script`, inserting commands to start Rune.
3. Log out, select a user, then select the Kiosk session in the gear icon at the bottom right, and login.
4. Rune IDE will start up, alongside any other apps you may have configured in "Startup Applications Preferences".

If you then need to open other applications, you may open them by entering their command line equivalent in Rune terminals.

## My remapped key is not honored in Rune

If you remapped a key at the OS level (a common example is making **CapsLock**
act as **Escape**) and Rune still does not respond to it, the keystroke is
usually reaching Rune under its *original* identity rather than the remapped
one.

Confirm this with the `keydump` command: press the key and look at what Rune
reports.

- If `keydump` shows the **remapped** key (for example, you press CapsLock and
  it prints `<esc>`), the remap is reaching Rune and any remaining problem is
  with the binding itself, not the remap.
- If `keydump` shows the **original** key (you press CapsLock and it prints
  `CapsLock`, or nothing at all), Rune is reading the physical key and never
  sees the OS-level substitution.

In the second case, do the remap inside Rune instead, with
[`gui.key_mapping`](./learn/key-mapping.md). It runs after Rune reads the
physical key, so it is not affected by whether the OS substitution reaches the
application:

```yaml tab
gui:
  key_mapping:
    "<capslock>": "<esc>"
```

```python tab
config["gui"]["key_mapping"] = {"<capslock>": "<esc>"}
```

Keys such as CapsLock, NumLock, ScrollLock, and Menu do nothing on their own in
Rune; `gui.key_mapping` is the only way to give them an effect. See
[Key remapping](./learn/key-mapping.md) for the full reference.

## Rune will not start on a machine without a display

Rune links its GPU renderer at build time, but it loads the X11 and OpenGL
client libraries only when it actually opens a window. A machine with no
graphical libraries installed at all can still run:

```bash
rune --tui        # the editor in the terminal
rune --headless   # a network node with no editor, see Network
```

If either of those fails with `error while loading shared libraries`, the
binary is an older release that still linked `libX11` eagerly; upgrade it.

`rune --gui` is the one mode that does need a display: it reports
`X11: Failed to load libX11` or `The DISPLAY environment variable is
missing` when there is none. Install your distribution's Mesa and X11
client library packages, or use `--tui` instead.

## The command prompt will not open in an external editor

When `editor.mode` is `exo`, Rune embeds a guest editor (such as `vim` or
`nvim`) and forwards your keystrokes to it. If the command prompt never opens,
the most likely cause is that the guest editor is swallowing the activation key
before Rune can act on it.

The classic case is leaving `command.key` at `:`. Inside `vim`, `:` enters the
editor's own command-line mode, so Rune never sees it and the Rune command
prompt does not open. `vim`, `nvim`, Helix, and Kakoune all bind `:` this way,
so a bare `:` is never a suitable command-prompt key when one of them is your
guest editor. There is no safe activation key that works across every guest
editor, so in `exo` mode you must pick one your guest editor will not capture.

The exo presets default `command.key` to `<shift-meta-p>` for exactly this
reason. If you have overridden it, choose a combination that carries a `meta`,
`ctrl`, or `alt` modifier, which guest editors rarely bind:

```yaml tab
command:
  key: "<shift-meta-p>"
```

```python tab
"command": {
    "key": "<shift-meta-p>",
},
```

`<ctrl-space>` is another common choice, but note that the OS often claims it
(see [A key binding is not firing](#a-key-binding-is-not-firing) above). If the
new key still does nothing, confirm it actually reaches Rune with the `keydump`
command; the guest editor or the OS may be claiming it.

## Inspect any process Rune starts

Rune tracks every process it starts. This includes language servers, debug
adapters, extensions, build tools, and other workspace helpers. The process
registry records how they were launched, their parent-child relationships,
their lifecycle, and their standard output and standard error destinations.

Use the console to find a process and inspect its output:

1. Run `process status` and note the process's PID. Use `process tree` when you
   need to identify a child from the command that launched it. Use
   `process audit` instead if the process has already exited.
2. Run `process info <pid>` to inspect its command, arguments, working
   directory, parent process, and lifecycle.
3. Run `process stdio <pid>`.

The report identifies the destination of each stream, followed by up to the
last 32 KiB of captured output. A stream connected directly to a file, socket,
or pseudoterminal is identified by that destination rather than duplicated
into the capture. For example, a language server's stdout normally appears as
`file+net lsp`; this is the socket Rune uses for LSP messages, not a log file.
Server logs appear under the captured stderr destination.

`process stdio` remains available after a process exits, so startup failures
can be inspected post-mortem. If a tee capture reaches its 4 MiB limit, the
report says that capture stopped. Rune removes the current session's capture
directory during a clean shutdown. Files left by a crash remain in the
operating system's temporary directory until its normal cleanup runs. In an SSH
workspace, these commands inspect processes on the remote workspace host.

This provides one troubleshooting path for tools that otherwise expose logs in
different ways. Use it for a language server that fails during initialization,
a debug adapter that exits before connecting, an extension helper that reports
an error, or a build tool whose output was launched in the background.

### Language-server logging

Go, Python, Rust, and Zig language servers log to standard error, where Rune
captures their output. Their language guides describe how to increase or reduce
verbosity while keeping the same `process stdio` inspection workflow:

- [Go logging](./languages/go.md#language-server-logs)
- [Python logging](./languages/python.md#language-server-logs)
- [Rust logging](./languages/rust.md#language-server-logs)
- [Zig logging](./languages/zig.md#language-server-logs)
