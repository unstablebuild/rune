# Workspace Notices

A workspace notice is a floating window that opens with the workspace and shows a short message you choose. It is useful for onboarding instructions ("read CONTRIBUTING.md before opening a PR"), in-flight warnings ("this branch is frozen"), or a quick welcome banner for teammates joining a repository for the first time.

The notice content can be inline text written directly in your config, or a file checked into the workspace so the message lives with the project. You can show it once per workspace, or every time the workspace opens.

## A first example

The shortest useful notice is a one-off welcome message:

```yaml tab
workspace:
  notice:
    literal: |
      # Welcome

      Run `make test` before opening a pull request.
    show: once
```

```python tab
"workspace": {
    "notice": {
        "literal": "# Welcome\n\nRun `make test` before opening a pull request.",
        "show":    "once",
    },
},
```

The next time you open this workspace, Rune renders that markdown in a centered floating window. Close it with `<esc>` (or whatever closes a floating window in your editor mode). Open the workspace again and the notice does not reappear; `show: once` remembers it.

Edit the message and reopen: the notice surfaces again, because the text changed. `once` deduplicates by content, not by occurrence.

## Configuration

The notice lives under `workspace.notice` and has three keys:

| Key | Value |
| --- | --- |
| `literal` | Inline message text. Always rendered as markdown. |
| `path` | Path to a file inside the workspace. A `.md` suffix is rendered as markdown; any other extension is rendered as plain text. |
| `show` | `once` (default) or `always`. `once` suppresses the notice on subsequent opens until the content changes; `always` shows it every time. |

If both `literal` and `path` are set, `literal` wins. If neither is set, the notice is silently disabled.

## Source the message from a file

For anything longer than a couple of lines, keep the message in a file under version control. The workspace's collaborators then edit the notice the same way they edit any other doc, and the rendered markdown reflects whatever lives at `HEAD`.

```yaml tab
workspace:
  notice:
    path: .rune/notice.md
    show: once
```

```python tab
"workspace": {
    "notice": {
        "path": ".rune/notice.md",
        "show": "once",
    },
},
```

Paths are resolved against the workspace root, so the same configuration works for local checkouts, remote SSH workspaces, and any other scheme Rune supports.

Plain-text notices work the same way: point `path` at a `.txt` file and Rune shows the file verbatim without markdown formatting:

```yaml tab
workspace:
  notice:
    path: .rune/notice.txt
    show: always
```

```python tab
"workspace": {
    "notice": {
        "path": ".rune/notice.txt",
        "show": "always",
    },
},
```

## Choosing `once` vs. `always`

`show: once` is the right default for onboarding text and one-time announcements. Rune fingerprints the rendered content and suppresses repeats: opening the same workspace twice in a row will not pop the same notice twice. Updating the message (edit the literal, change the file, switch from `path` to `literal`) bumps the fingerprint and the notice resurfaces once.

`show: always` is the right choice for persistent context that you actually want re-shown on every open, for example a "this branch is frozen, ship from `release/*` instead" warning that the team should not be allowed to dismiss permanently.

## Per-workspace overrides

Notices are workspace-scoped, so the most useful place to configure them is a workspace-local config file rather than your user-level config. That way the notice ships with the project and turns on for everyone who opens it and [trusts its config](../config.md#where-the-config-lives), instead of having to be re-pasted into each contributor's personal config.

## A few patterns

### Pin onboarding notes to the repo

```yaml tab
workspace:
  notice:
    path: docs/onboarding.md
    show: once
```

```python tab
"workspace": {
    "notice": {
        "path": "docs/onboarding.md",
        "show": "once",
    },
},
```

New contributors see the onboarding doc the first time they open the workspace; existing contributors see it once after each meaningful edit.

### Loudly mark a frozen branch

```yaml tab
workspace:
  notice:
    literal: |
      # Branch frozen

      Cut release branches from `main`. Do not push to this checkout.
    show: always
```

```python tab
"workspace": {
    "notice": {
        "literal": "# Branch frozen\n\nCut release branches from `main`. Do not push to this checkout.",
        "show":    "always",
    },
},
```

### Quick local reminder

```yaml tab
workspace:
  notice:
    literal: "Remember to start the dev server with `make dev`."
    show: once
```

```python tab
"workspace": {
    "notice": {
        "literal": "Remember to start the dev server with `make dev`.",
        "show":    "once",
    },
},
```
