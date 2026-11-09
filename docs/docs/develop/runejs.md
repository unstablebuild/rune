---
sidebar_position: 14
---

# rune.js

rune.js lets you customize Rune with JavaScript that reloads every time
you save it. A script can register commands and REPL commands, open
panels, floating windows, tabs and status bars, and react to editor
events, without building an [extension](./extensions.md) or restarting
Rune. Agents use it the same way: write a script, check it, look at
it, all from a Rune terminal.

rune.js is itself one extension, `runejs`, built on the Go
[SDK](./sdk/index.md). It runs scripts with
[goja](https://github.com/dop251/goja), a JavaScript engine written in
Go, and gives them the whole extension API.

## Install

Install the `runejs` package from the [Rune console](../learn/console.md):

```
pkg install runejs
```

It registers the extension and puts the `runejs` CLI on your `PATH` in
Rune's terminals. On start the extension writes a script guide,
`AGENTS.md`, and the API typings, `rune.d.ts`, into the scripts folder,
and installs the `runejs` skill for rune-agent in
`~/.rune/skills/runejs/SKILL.md`.

## Scripts

Each script is a folder in the scripts folder, `~/.rune/runejs` by
default, with a `main.js`:

```js
// ~/.rune/runejs/hello/main.js
const panel = {
  key: "hello",
  draw(w) {
    const text = "hello from rune.js";
    for (let x = 0; x < text.length && x < w.width; x++) {
      w.setCell(x, 0, text[x], { fg: "green" });
    }
  },
};

exports.activate = async (ws, config) => {
  const wm = ws.windowManager();
  await ws.commands().registerCommand(
    { name: "hello", summary: "Open the hello panel" },
    (cmd) => wm.split(rune.orientation.right, cmd.window, panel),
  );
  // Keep the panel open across reloads.
  if (rune.isOpen(panel.key)) await wm.split(rune.orientation.right, null, panel);
};
```

Scripts are CommonJS modules: `require("./lib.js")` loads other files of
the same folder. `activate` gets `ws`, the extension API, and the map at
`extensions.runejs.config.scripts.<name>` of your config. An optional
`deactivate` runs before a reload.

Every method of the API returns a Promise, except the accessors on `ws`
itself such as `ws.windowManager()`. The methods of the handlers a
script implements, such as `draw` and `handle`, are synchronous. The
`rune.d.ts` file in the scripts folder declares the whole API, and
`AGENTS.md` next to it is the script guide.

## Reloads

Saving any file of a script reloads it. Registrations outlive a reload:
each command, REPL command, resource opener, window, tab and bar keeps
its registration with Rune, and the reload swaps the new code in behind
it. So key bindings keep working and keyed windows stay where they are.

- A command the new code no longer registers answers that it is not
  defined by the current scripts.
- Two scripts can't register the same command.
- Windows, tabs and bars with a `key` survive the reload if `activate`
  opens them again; `rune.isOpen(key)` tells it which ones were open.
  The rest close.
- If the new code fails to load, the previous version keeps running and
  `runejs status` shows the error.

## The runejs CLI

`runejs` talks to the extension of the Rune it runs in:

| Command | What it does |
| --- | --- |
| `runejs status` | Each script's state, last error with its JS stack, and console output. |
| `runejs ls` | Every command, window, tab and bar scripts registered. |
| `runejs check <name\|dir>` | Loads a script without installing anything, draws each view it opens once, and exits nonzero on any error. |
| `runejs shot <key>` | Draws a window, tab or bar as text. `--width`, `--height` and `--images DIR` are optional. |
| `runejs reload [name]` | Reloads one script, or all of them. |

Add `--json` for machine-readable output. If the extension gets stuck,
`extensions restart runejs` in the Rune console restarts it.

## Trust

Scripts run with every permission an extension can have: they can read and write files,
run programs (with your approval), and call language models. Only put scripts you trust
in the scripts folder.

## See also

- [Extensions](./extensions.md): how Rune runs extensions such as `runejs`.
- [SDK](./sdk/index.md): the Go API rune.js gives scripts.
- [Packages](./packages.md): how `runejs` is installed.
