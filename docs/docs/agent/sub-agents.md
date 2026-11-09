---
sidebar_position: 3.5
---

# Sub-agents

A sub-agent is a separate agent that Rune Agent starts for a single job. It
has its own instructions, its own conversation and, if you choose, a
smaller set of tools. When it finishes, it reports back to the agent that
started it. The built-in `explore` and `plan` sub-agents work this way:
both are read-only, so they can research freely without changing anything.

Build your own sub-agent for a recurring task that benefits from a narrow
focus. Examples are a reviewer that only reads code, a triage agent that
only talks to your issue tracker, or a release agent that only runs your
release CLI. Removing the tools a job does not need keeps the sub-agent on
task. It also limits what the sub-agent can do when it makes a mistake.

## Defining a sub-agent

A sub-agent is a [skill](./skills.md) whose frontmatter sets `type: agent`.
Place a `SKILL.md` in one of your
[skill directories](./skills.md#skill-directories). The agent can then
spawn it by name, and you can run it yourself as a slash command
(`/<name>`). Either way, the chat shows the sub-agent as a `skill` call
with its tool calls nested under it, and keeps the call and its reply in
the conversation, so later questions can refer to them. When you run it
yourself, its reply follows as the answer. The sub-agent's own
conversation is saved as `sub-agent-<name>-…`.

```markdown
---
name: changelog
description: Drafts a changelog entry from the commits since the last release tag. Use when preparing a release.
type: agent
allowed-tools: [read_file, "bash(git describe *)", "bash(git log *)"]
---
You write changelog entries. Find the last release tag with
`git describe --tags --abbrev=0`, read the commits since it with `git log`,
and group the user-visible changes under Added, Changed and Fixed.
```

The body of the file is the sub-agent's system prompt: its instructions,
constraints and style.

### Frontmatter reference

| Field | Purpose |
| --- | --- |
| `name` | The sub-agent's ID. The agent uses it to spawn the sub-agent, and it is also the slash command. Use lowercase letters, digits and hyphens. |
| `description` | What the sub-agent is for and when to use it. The main agent reads it to decide when to spawn the sub-agent. |
| `type: agent` | Marks the skill as a sub-agent. Without it, the skill is a set of instructions that the agent loads into its own conversation. |
| `model` | Optional. The model the sub-agent runs on when the agent spawns it, written as `provider/name` or as a model name that `agent models` lists. By default, it uses the model of the conversation that spawns it. |
| `allowed-tools` | Optional. The tools the sub-agent may use. Anything the list does not allow is unavailable. Omit it to give the sub-agent every tool. Leaving it empty gives it no tools. See [Tool rules](#tool-rules). |
| `disallowed-tools` | Optional. The tools, commands, skills and sub-agents the sub-agent may not use, even when `allowed-tools` allows them. |
| `parent-context` | When `true` and you run the sub-agent as a slash command, it starts with the conversation so far. Defaults to `false`, which keeps the sub-agent isolated. |

## Tool rules

Only sub-agents have tool rules. Rune ignores `allowed-tools` and
`disallowed-tools` in other skills, which harnesses such as Claude Code
read with a different meaning.

`allowed-tools` and `disallowed-tools` each take a list of entries. You can
write them as a YAML list, or as a single string with entries separated by
spaces or commas:

```yaml
allowed-tools: [read_file, search_content, "bash(git log *)"]
# The same list, as a string:
allowed-tools: read_file search_content bash(git log *)
```

Spaces, commas and quoted text inside parentheses belong to the entry.
Quote patterns in a YAML list, so that YAML does not read characters like
`*`, `:` and `,` as its own.

| Entry | In `allowed-tools` | In `disallowed-tools` |
| --- | --- | --- |
| `read_file` | Allows the tool. | Removes the tool. |
| `github_*` | Allows every tool whose name matches the glob. | Removes every matching tool. |
| `bash` | Allows the shell, with any command. | Removes the shell. |
| `bash(mycli a *)` | Allows the shell. Every program a command runs must match an allowed pattern. | Rejects commands that run a matching program. |
| `bash($acme-notes)` | Allows the shell, to run the scripts of the skill `acme-notes`. See [Skills as tools](#skills-as-tools). | Not accepted. |
| `skill` | Allows loading any skill. | Removes the `skill` tool. |
| `skill(acme-*)` | Allows loading only the matching skills. Skill names are case-sensitive. | Hides the matching skills. |
| `agent` | Allows spawning any sub-agent. | Removes the `agent` tool. |
| `agent(explore)` | Allows spawning only the matching sub-agent types, in any case. | Forbids the matching sub-agent types. |

Rules only take tools away. Without `allowed-tools`, a sub-agent gets every
tool except the ones that `disallowed-tools` removes. An `allowed-tools` field
with no entries, such as `allowed-tools: []` or `allowed-tools:` with
nothing after it, gives it no tools at all. A sub-agent never sees itself
among the skills and sub-agents it can use, so it cannot start itself.

The names `bash`, `skill` and `agent` are case-insensitive, and
`skill(*)` and `agent(*)` are the same as `skill` and `agent`. Other tool
names are case-sensitive. `bash` also covers the shell tools Rune Agent
offers some models in place of `bash`, so one rule applies on every model.

The sub-agent only lists the skills it may load, and only offers the
sub-agent types it may spawn. A sub-agent without the `skill` tool gets no
list of skills at all.

### Finding tool names

Run `agent tools` in the [console](../learn/console.md) to list the name of
every tool the agent can use. MCP server tools are named `<server>_<tool>`,
so `github_*` covers every tool of an MCP server named `github`.

## Command patterns

A `bash(...)` entry holds a command pattern: a program name followed by
argument patterns, written the way you would type the command.

| Pattern | Matches | Does not match |
| --- | --- | --- |
| `git status` | `git status` | `git status -s` |
| `git log *` | `git log`, `git log -1 --oneline` | `git push` |
| `mycli get issue-*` | `mycli get issue-42` | `mycli get user-1`, `mycli get issue-42 --json` |
| `mycli 'a*'` | `mycli 'a*'` | `mycli abc` |
| `./bin/mycli *` | `./bin/mycli sync` | `mycli sync`, `/repo/bin/mycli sync` |

- A `*` on its own at the end matches zero or more further arguments.
  Without it, the command must match the pattern argument for argument.
- Other unquoted `*`, `?` and `[...]` match within a single argument.
  Quoted text matches literally.
- A bare program name, such as `mycli`, only matches the program run by
  that name, which is looked up on the `PATH`. It does not match `./mycli`
  or `/usr/local/bin/mycli`.
- A name with a slash matches that path. A relative path, such as
  `./bin/mycli`, does not match after the command changes directory,
  because it then names a different file.
- A program name must name a file: a name starting with `~` or ending
  with `/` is an error. Write `/home/me/bin/mycli` rather than
  `~/bin/mycli`.
- For `git`, options before the subcommand are skipped, so `git commit *`
  matches `git -C repo commit -m "fix"`. `-c`, which can change what git
  runs, is not skipped.
- A pattern must be a single command of plain words. Variables, `$(...)`,
  redirections, `;`, `|` and brace expansions such as `{a,b}` are errors.
  Braces bash leaves alone, such as `{}`, match literally.

### How commands are checked

When `allowed-tools` allows the shell only through patterns, Rune works out
every program a command would start before the shell runs it. Each one must
match an allowed pattern:

- Every program in a pipeline (`|`), a list (`&&`, `||`, `;`) or a command
  substitution (`$(...)`) is checked. `mycli a | jq .` needs a pattern for
  `jq` too, and `echo $(curl example.com)` needs one for `curl`.
- Nested shells are followed. In `bash -c 'mycli a'`, Rune checks
  `mycli a`.
- Wrappers that only run another program, such as `timeout`, `env`,
  `nice`, `nohup` and `xargs`, need no pattern of their own. The program
  they run does. `timeout 5 mycli a` matches `mycli a *`.
- `find` needs its own pattern, and the commands of its `-exec` options
  are checked too.
- `cd`, variable assignments and builtins that run nothing, such as `echo`,
  `printf`, `test`, `pwd` and `read`, need no pattern.

Some commands are always rejected when commands are restricted:

- Commands whose programs Rune cannot tell before they run, such as
  `eval`, `source`, or a program name in a variable (`$CMD args`). This
  also applies when only `disallowed-tools` has command patterns.
- `sudo` and other programs that run commands as another user, unless a
  pattern allows them. Allowing `sudo *` allows every command as root.
- Redirections that write files, such as `> out.txt` or `>> log`. Writing
  to `/dev/null`, `/dev/stdout` or `/dev/stderr` is allowed, so
  `mycli a 2>/dev/null` runs.

Patterns in `disallowed-tools` reject a matching program anywhere in a
command, including inside wrappers and nested shells, and take precedence
over `allowed-tools`. To allow most commands but block a few, pair a plain
`bash` with deny patterns:

```yaml
allowed-tools: [read_file, bash]
disallowed-tools: ["bash(git push *)", "bash(git reset --hard *)"]
```

:::caution
Allowing an interpreter, such as `python *` or `node *`, allows arbitrary
code: once started, the interpreter can do anything. Allow your own CLIs
and scripts instead.
:::

## Skills as tools

A skill can ship programs in a `scripts/` directory next to its
`SKILL.md`. This lets you give a sub-agent a new tool with nothing more
than a skill that describes it and a script that implements it.

`bash($acme-notes)` lets a sub-agent run the programs in the `scripts/`
directory of the skill `acme-notes`, wherever that skill is installed. You
don't write the scripts' paths, and you don't need a pattern for each
script. The name can be a glob: `bash($acme-*)` covers the scripts of
every skill whose name starts with `acme-`.

Scripts are never allowed without such an entry, when commands are
restricted:

- `skill(acme-notes)` only lets the sub-agent load the skill's
  instructions. Pair it with `bash($acme-notes)` so the sub-agent learns
  how to use the scripts it may run.
- A sub-agent skill's own scripts need an entry with its own name, such as
  `bash($acme-triage)`.
- A sub-agent only runs the scripts that its own entries and those of
  every agent above it allow. A parent can keep the sub-agents it starts
  from running a skill's scripts by leaving out its `bash($...)` entry.

A script runs when the sub-agent calls it by its absolute path, such as
`/home/me/.agents/skills/acme-notes/scripts/summarize`. The shell tool
tells the sub-agent which script directories it may use, and loading the
skill also gives it the skill's directory. Give the script a shebang line
(such as `#!/usr/bin/env python3`) and make it executable. A command like
`python scripts/summarize.py` runs `python`, which needs a pattern of its
own.

A `bash($...)` entry gives the sub-agent the shell, restricted to the
scripts and to any command patterns you also list. Next to a plain `bash`
entry it changes nothing, since plain `bash` allows any command.

For example, a triage harness for a fictional `acme` issue tracker:

```text
.agents/skills/
├── acme-triage/
│   └── SKILL.md          # the sub-agent
└── acme-notes/
    ├── SKILL.md          # explains when and how to use the script
    └── scripts/
        └── summarize     # executable
```

```yaml
# .agents/skills/acme-triage/SKILL.md
---
name: acme-triage
description: Triages new Acme issues and drafts a summary for each. Use for issue triage.
type: agent
allowed-tools: [read_file, "skill(acme-*)", "bash($acme-notes)", "bash(acme issue *)", "bash(jq *)"]
disallowed-tools: ["bash(acme issue delete *)"]
---
```

`acme-triage` can read files, load `acme-notes` and any other `acme-`
skill, run the scripts of `acme-notes`, run `acme issue` commands other
than `acme issue delete`, and pipe their output through `jq`. It cannot
run the scripts of the other `acme-` skills it loads, edit files, write
files from the shell, or run any other program.

## Restricting sub-agents

`agent(...)` limits which sub-agent types a sub-agent may spawn. When the
`agent` tool is restricted, the sub-agent must name an allowed type every
time it spawns one. It cannot fall back to the default type.

Restrictions carry over to everything a sub-agent starts. A sub-agent it
spawns, or a sub-agent skill it runs, gets only what both its own rules
and the rules of every agent above it allow. A restricted sub-agent cannot
escape its rules by starting an unrestricted one.

## Restricting is not approving

Tool rules only decide what a sub-agent can try. They never approve
anything on your behalf. A command that a pattern allows still goes
through Rune's command permission prompt, as any command the agent runs
does.

To let a sub-agent work without interruptions, approve its commands once.
Choose **Always** in the prompt, and pick a scope that matches the pattern,
such as `mycli a *`. From then on, the sub-agent runs matching commands
without asking. See [Command approvals](../develop/extensions.md#command-approvals).

## Claude Code compatibility

Rune accepts some Claude Code entries as they are, such as
`Bash(git add:*)`, but not all of them, and some mean something else. See
[Claude Code › Use Claude Code skills in Rune
Agent](./claude-code.md#use-claude-code-skills-in-rune-agent) to port a
Claude Code skill or subagent.

## Troubleshooting

**A command is rejected.** The shell tool returns an error that names the
program at fault and lists the patterns the sub-agent may use:

```text
command rejected: "git push" matches no allowed pattern. Every program a command runs must match one of `git log *`, `jq *`.
```

The sub-agent sees this message and can choose a command that is allowed.
If it keeps hitting the same rejection, add a pattern for the program, or
tell the sub-agent in its instructions which commands to use.

**The sub-agent does not appear.** Rune does not load a sub-agent with an
entry it cannot parse, such as an unbalanced parenthesis, a pattern on a
tool other than `bash`, `skill` and `agent`, a command pattern with a
variable in it, a name with a space, slash or quote in it, or a
`bash($...)` entry in `disallowed-tools`. An unquoted comma inside
parentheses in a YAML list, as in `[bash(git commit -m 'a, b')]`, splits
the entry in two and fails the same way; quote the entry. The skill is
missing from `agent skills list`, and Rune logs a warning that names the
invalid entry.
