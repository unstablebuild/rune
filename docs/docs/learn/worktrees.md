---
sidebar_position: 32
---

# Worktrees

A [git worktree](https://git-scm.com/docs/git-worktree) is a second checkout
of the same repository on its own branch, in its own directory. Rune turns
worktrees into a first-class workflow: each worktree opens as its own
[workspace](./command-prompt.md), so you can have several branches checked out
at once, each in its own directory, and work on them side by side without
stashing or switching branches in place.

This makes worktrees the natural unit for **parallel work** on a repository.
Say you are mid-change on a feature branch when an urgent fix comes in: spin up
a worktree for the hotfix branch, fix and ship it there, and come back to your
feature branch exactly as you left it, no stash, no dirty tree. The same
isolation lets an agent churn through edits on its own branch in one worktree
while you keep working on yours in another, with no collisions. When a task
finishes or needs you, its workspace tab lights up so you know where to look.

## The worktree commands

Rune ships three commands for managing worktrees from the
[command prompt](./command-prompt.md):

| Command | What it does |
| --- | --- |
| `worktreenew <name>` | Create a new worktree on a new branch `<name>`, then open it as a workspace named `<name>`. |
| `worktreeopen <name>` | Open an existing worktree as a workspace. The command prompt's completer lets you fuzzy-search the list of worktrees for the current workspace's repository. |
| `worktreeremove <name>` | Remove a worktree. The command prompt's completer lets you fuzzy-search the same list of worktrees. |

A typical session: create a worktree for a feature, work in it, then remove it
when the branch is merged.

```
:worktreenew FEATURE-123
...
:worktreeremove FEATURE-123
```

`worktreenew` puts the checkout under Rune's data directory, in a path keyed to
the originating repository, so worktrees from different repos never clash and
nothing lands inside your project tree.

## Under the hood: just aliases

The worktree commands are not built into Rune's core. They are ordinary
**aliases** that compose existing commands, and they are a good showcase of how
far Rune's [alias and command-variable](./aliases.md) system goes. You
can read them, copy them, and bend them to your own workflow.

Here is `worktreenew`, the most involved of the three:

```yaml tab
command:
  aliases:
    worktreenew:
      - '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1'
      - '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)'
      - '!! ROOT_NAME=${ROOT##*/}'
      - '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1'
      - '!! git worktree add "$WORKTREE" -b $1'
      - 'workspaceopen $WORKTREE'
      - 'workspaceready workspacerename $1'
```

```python tab
"aliases": {
    "worktreenew": [
        '!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1',
        '!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)',
        '!! ROOT_NAME=${ROOT##*/}',
        '!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1',
        '!! git worktree add "$WORKTREE" -b $1',
        "workspaceopen $WORKTREE",
        "workspaceready workspacerename $1",
    ],
},
```

Every line is one step of a multi-step alias, run in order:

- The `!!` steps run a shell command and wait for it. The first four compute a
  worktree path: they find the repository root, derive a short hash of it so the
  directory name is unique per repository, and assemble
  `$RUNE_DATADIR/worktrees/<repo>-<hash>/<name>`. `$RUNE_DATADIR` and `$1` (the
  first alias argument) are [command variables](./aliases.md) Rune
  expands before each step runs, and the variables a step assigns are visible to
  the steps that follow.
- `git worktree add "$WORKTREE" -b $1` creates the worktree and its branch.
- `workspaceopen $WORKTREE` opens that directory as a new workspace.
- `workspaceready workspacerename $1` waits for the new workspace to finish
  loading, then renames its tab to the branch name. `workspaceready` exists for
  exactly this: it defers a command until the most recent `workspaceopen` is
  ready, instead of racing against it.

`worktreeopen` and `worktreeremove` follow the same pattern, and add a
`completer` so the prompt can suggest existing worktree names:

```yaml tab
command:
  aliases:
    worktreeremove:
      command:
        - "!! git worktree remove $1"
        - "shaderrun embers 600ms"
      completer: "! $SHELL -c 'git worktree list | tail -n +2 | cut -d\" \" -f1 | xargs -n1 basename'"
```

```python tab
"aliases": {
    "worktreeremove": {
        "command": [
            "!! git worktree remove $1",
            "shaderrun embers 600ms",
        ],
        "completer": "! $SHELL -c 'git worktree list | tail -n +2 | cut -d\" \" -f1 | xargs -n1 basename'",
    },
},
```

The completer is itself just a shell command whose output lines become the
suggestions. Because these are plain aliases, you can change where worktrees
live, add a step that runs your setup script, or open the agent automatically
in the new workspace. See [Command Variables](./aliases.md) for the
full alias and expansion model.

## Parallelizing work across worktrees

The reason worktrees pair so well with Rune is that each one is a separate
workspace, and Rune is a multi-workspace environment. Open several worktrees and
you can let different tasks run in each at the same time:

- Work on two unrelated branches at once: leave your half-finished feature
  exactly as it is in one worktree and review a teammate's branch in another,
  with no stashing and no switching branches in place.
- Run [Rune Agent](../agent/intro.md) or a third-party agent (Claude Code, Codex,
  your own harness) in its own worktree, on its own branch, so its edits stay
  isolated from the branch you are working on.
- Give each agent its own worktree and run several in parallel, one per task or
  per branch, then review and merge their branches one at a time.

Switch between worktree workspaces the same way you switch any workspace, and
the worktree's branch is exactly what `git` sees in that directory, so commits,
diffs, and status are all scoped to that branch.

## Knowing when a worktree needs you

When you run work in parallel, you are not watching every workspace at once. So
Rune surfaces activity from the workspaces you are **not** looking at.

When a notification is posted on a workspace that is not currently in focus,
Rune holds the notification open until you switch to it and counts it in a
superscript badge after that workspace's tab, in the color of the highest level
pending (`2 api²`). The tab of the workspace that finished a build, hit an
error, or needs your input shows the badge, and you can jump straight there
without hunting through tabs. Focusing the workspace clears it.

Programs that report their [status](./terminal/program-status.md) over
OSC 7501 (`runectl status`, agents, builds) also show on the workspace bar: the
tab's name takes the color of the most pressing status among the workspace's
terminals (yellow when blocked on you, red on error, green once done), so a
workspace that needs you reads at a glance even before any notification, and
`programstatus` lists every reported status across workspaces so you can jump
to the one that needs you. (Tune the colors under
`terminal.status.workspace_tab`.)

Rune Agent chats report their status this way: the chat tab spins while the
agent thinks and runs tools, its workspace tab turns green as the tasks it
planned complete (`terminal.status.workspace_tab.progress`; set
`terminal.status.content_tab.progress` to fill the chat tab too), turns
blocked when it asks you a question or for permission, and turns done with the
first line of its reply, or failed with the reason, when the turn ends. Rune's
own permission prompts, raised when an extension asks for something it was not
granted, show as blocked on the workspace bar until you answer them.

This works no matter where the notification comes from:

- **Internally**, from Rune itself: a `!!` command that finishes in a background
  worktree, an auto-save warning, or anything that posts a notification.
- **From an extension**, through the [Rune SDK](../develop/sdk/index.md): an extension
  can post a notification on its workspace, and if that workspace is in the
  background its tab is highlighted.
- **From outside Rune**, through [`runectl`](../develop/runectl.md): any process
  running in a workspace terminal can post a notification with
  `runectl notify <level> <message>`, where `<level>` is `error`, `warn`,
  `info`, or `success`. Because Rune sets the workspace's socket in the
  terminal's environment, `runectl` finds the right workspace automatically.

That last one is what makes worktrees and external agents click together. Wire
an agent's "I'm done" or "I need permission" hook to `runectl notify`, run it in
its own worktree, and its workspace tab lights up the moment it needs you, even
while you are working elsewhere. For a worked example with Claude Code, see the
[Claude Code guide](../agent/claude-code.md#notify-rune-when-claude-is-done).

## See also

- [Command Variables](./aliases.md): the alias and `$VAR` expansion
  model the worktree commands are built on.
- [Command Prompt](./command-prompt.md): how commands and aliases are
  dispatched.
- [Rune Agent](../agent/intro.md): Rune's built-in agent.
- [Claude Code](../agent/claude-code.md): wiring a third-party agent to Rune
  notifications with `runectl`.
- [runectl](../develop/runectl.md) and the [Rune SDK](../develop/sdk/index.md):
  posting notifications and driving Rune from outside.
