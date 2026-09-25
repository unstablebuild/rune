---
sidebar_position: 3
---

# Skills

Skills are focused capability bundles the agent can load on demand for a
particular kind of work, such as code navigation, understanding,
searching, refactoring, or reading diagnostics. The agent pulls in the
right skill when your request calls for it.

Skills are also available as **slash commands** in chat: typing
`/<skill>` (for example `/plan`, `/review`, or `/commit` when those
skills are installed) runs that skill directly.

## Skill directories

Skills are discovered from a configurable list of directories. By default
the agent looks in, in order:

| Directory | Scope |
| --- | --- |
| `.rune/skills` | this project |
| `~/.rune/skills` | all your projects |
| `.agents/skills` | this project |
| `~/.agents/skills` | all your projects |

The project-local directories let a team ship skills alongside the
repository, while the home directories hold skills you want everywhere.
Drop a skill into any of these and the agent picks it up.

You manage skills and their directories from the [`agent`
console command](./intro.md#the-agent-console-command):

| Command | What it does |
| --- | --- |
| `agent skills list` | List the discovered skills. |
| `agent skills show <name>` | Show a skill's full instructions. |
| `agent skills reload` | Re-scan tracked skill directories and report changes. |
| `agent skills list-dirs` | List the configured skill directories. |
| `agent skills add-dir <dir>` | Add a directory to the search list. |
| `agent skills remove-dir <dir>` | Remove a directory from the search list. |

## Sub-agents and planning

For larger tasks the agent can spawn **sub-agents** to work in parallel,
each scoped to a job. Two come built in:

- **explore**: a fast, read-only research agent for understanding a
  codebase without changing anything.
- **plan**: a read-only architect that designs an implementation plan.

This feeds into **plan mode**: the agent researches read-only, proposes a
step-by-step plan, and waits for your approval before making any changes.
If you ask for changes, it revises the plan and presents it again. Once
you accept, it executes and shows a live checklist as it goes.

### Adding your own sub-agents

`explore` and `plan` are just skills. A sub-agent is a skill whose
frontmatter sets `type: agent`, so you can add your own simply by dropping
a `SKILL.md` into one of your [skill directories](#skill-directories). The
agent picks it up automatically and can spawn it by name.

The frontmatter fields that define a sub-agent:

| Field | Purpose |
| --- | --- |
| `name` | The agent's ID, used to spawn it. |
| `description` | Tells the agent what the sub-agent is for and when to use it. |
| `type: agent` | Marks the skill as a spawnable sub-agent. |
| `allowed-tools` | The exact set of tools the sub-agent may use. Omitting a tool denies it, which is how `explore` and `plan` stay read-only. |
| `parent-context` | When `true`, the sub-agent receives the parent conversation's context. |

The body of the file is the sub-agent's system prompt: its instructions,
constraints, and style.

For example, a read-only sub-agent that reviews code for a specific
concern, placed at `.rune/skills/security-review/SKILL.md`:

```markdown
---
name: security-review
description: Read-only reviewer that audits code for security issues such as injection, unsafe input handling, and secrets in source. Reports findings; makes no changes.
type: agent
allowed-tools: read_file search_content find_files find_references search_symbols outline_file describe_symbol
---
You are a security code reviewer. You never modify files or run commands.

Audit the code in scope for:
- Injection (SQL, command, path) and unsafe handling of external input.
- Secrets or credentials committed to source.
- Missing authentication or authorization checks at trust boundaries.

Report each finding with its file, line, and a short explanation. If you
find nothing, say so. Be precise and avoid speculation.
```

Because its `allowed-tools` list contains only read and search tools, this
sub-agent cannot edit files or run commands, the same way the built-in
read-only agents are constrained. Once the file is in place, the main
agent can spawn it to run a focused review in parallel with other work.
